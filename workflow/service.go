package workflow

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	transport "github.com/catalystcommunity/csilgen/transports/go"
	w "github.com/catalystcommunity/magi/generated/go"
	schema "github.com/catalystcommunity/magi/generated/workflows"
	"github.com/catalystcommunity/magi/identity"
	p "github.com/catalystcommunity/magi/protocol"
)

func Schema() w.SchemaRef {
	digest, _ := hex.DecodeString(schema.Digest)
	return w.SchemaRef{Name: "catalyst.workflows", Version: "0alpha1", Digest: digest}
}
func Sign(op string, body []byte, signer, recipient w.ApplicationRef, keyID string, key ed25519.PrivateKey, at time.Time) (w.SignedRecord, error) {
	if !validApplication(signer) || !validApplication(recipient) {
		return w.SignedRecord{}, ErrInvalid
	}
	return p.Sign(p.SignatureScope{Context: "catalyst.workflows/" + op, Schema: Schema(), Signer: signer, Recipient: recipient}, body, keyID, at, key)
}

type Service struct {
	Store *Store
	Keys  identity.KeyResolver
}

func (s *Service) authenticate(ctx context.Context, op string, packet w.SignedRecord) (w.SignatureInput, error) {
	packet.Input = bytes.Clone(packet.Input)
	packet.Signature = bytes.Clone(packet.Signature)
	v, e := p.Decode(packet.Input, w.DecodeSignatureInput, w.EncodeSignatureInput)
	at := time.Now()
	expected := Schema()
	if e != nil || s.Keys == nil || s.Store == nil || !validApplication(v.Signer) || v.Recipient != s.Store.Application || v.Context != "catalyst.workflows/"+op || v.Schema.Name != expected.Name || v.Schema.Version != expected.Version || !bytes.Equal(v.Schema.Digest, expected.Digest) || v.SignedAt.After(at.Add(5*time.Second)) || at.Sub(v.SignedAt) > time.Minute {
		return v, ErrDenied
	}
	key, e := s.Keys.Resolve(ctx, v.Signer, v.KeyId)
	if e != nil {
		return v, e
	}
	at = time.Now()
	if key.CreatedAt.After(at) || v.SignedAt.Before(key.CreatedAt) || !v.SignedAt.Before(key.ExpiresAt) || !at.Before(key.ExpiresAt) || at.Sub(v.SignedAt) > time.Minute {
		return v, ErrDenied
	}
	v, _, e = p.Verify(packet, key.Public, p.SignatureScope{Context: "catalyst.workflows/" + op, Schema: expected, Signer: v.Signer, Recipient: s.Store.Application})
	if e != nil {
		return v, ErrDenied
	}
	return v, nil
}

var workflowOperations = []string{"Workflows/validate", "Workflows/save-draft", "Workflows/read-draft", "Workflows/publish", "Workflows/read-definition", "Workflows/start", "Workflows/read-run", "Workflows/complete-human", "Workflows/read-call"}

func workflowFailure(e error) transport.RpcResponse {
	code, message, retry := int64(1500), "Service is unavailable", true
	switch {
	case errors.Is(e, ErrDenied), errors.Is(e, identity.ErrDenied):
		code, message, retry = 1403, "Operation denied", false
	case errors.Is(e, ErrInvalid), errors.Is(e, p.ErrWire):
		code, message, retry = 1400, "Invalid request", false
	case errors.Is(e, ErrConflict), errors.Is(e, ErrStale):
		code, message, retry = 1409, "Record conflicts with retained content", false
	case errors.Is(e, ErrNotFound):
		code, message, retry = 1404, "Record is not available", false
	case errors.Is(e, ErrReconcile):
		code, message, retry = 1503, "Reconciliation is required", false
	}
	return transport.NewRpcResponseOk("ServiceError", w.EncodeServiceError(w.ServiceError{Code: code, Message: message, Retryable: retry}))
}
func (s *Service) Dispatch(ctx context.Context, req transport.RpcRequest) transport.RpcResponse {
	if req.Service == "Discovery" {
		switch req.Op {
		case "describe":
			v, e := p.Decode(req.Payload, w.DecodeDescribeRequest, w.EncodeDescribeRequest)
			if e != nil {
				return workflowFailure(e)
			}
			features := []string{"strict-cbor", "linkkeys-application-proof", "immutable-definitions", "typed-acyclic-workflows", "direct-human-response", "call-evidence"}
			if !slices.Contains(v.Versions, "0alpha1") {
				return workflowFailure(ErrInvalid)
			}
			for _, f := range v.RequiredFeatures {
				if !slices.Contains(features, f) {
					return workflowFailure(ErrInvalid)
				}
			}
			d := w.ServiceDescription{Protocol: "0alpha1", Schema: Schema(), ImplementedOperations: append([]string{"Discovery/describe", "Discovery/read-schema"}, workflowOperations...), AuthenticatedOperations: slices.Clone(workflowOperations), Features: features, MaxFrameBytes: p.MaxFrame, MaxCollectionItems: p.MaxCollection, MaxDepth: p.MaxDepth, RequestTimeoutMs: uint64(p.Timeout / time.Millisecond)}
			return transport.NewRpcResponseOk("ServiceDescription", w.EncodeServiceDescription(d))
		case "read-schema":
			v, e := p.Decode(req.Payload, w.DecodeSchemaRequest, w.EncodeSchemaRequest)
			if e != nil {
				return workflowFailure(e)
			}
			expected := Schema()
			if v.Schema.Name != expected.Name || v.Schema.Version != expected.Version || !bytes.Equal(v.Schema.Digest, expected.Digest) {
				return workflowFailure(ErrInvalid)
			}
			return transport.NewRpcResponseOk("SchemaDocument", w.EncodeSchemaDocument(w.SchemaDocument{Schema: expected, Descriptor: bytes.Clone(schema.Descriptor)}))
		}
	}
	if !slices.Contains(workflowOperations, req.Service+"/"+req.Op) {
		return transport.NewRpcResponseTransportError(transport.StatusUnknownServiceOrOp, "Operation is not available")
	}
	packet, e := p.Decode(req.Payload, w.DecodeSignedRecord, w.EncodeSignedRecord)
	if e != nil {
		return workflowFailure(e)
	}
	input, e := s.authenticate(ctx, req.Op, packet)
	if e != nil {
		return workflowFailure(e)
	}
	if len(input.Body) > MaxDefinitionBytes {
		return workflowFailure(ErrInvalid)
	}
	var raw []byte
	var variant string
	switch req.Op {
	case "validate", "publish":
		var d w.WorkflowDefinition
		d, e = p.Decode(input.Body, w.DecodeWorkflowDefinition, w.EncodeWorkflowDefinition)
		if e == nil {
			if req.Op == "validate" {
				tx, err := s.Store.Journal.Pool.Begin(ctx)
				e = err
				if e == nil {
					e = s.Store.authorize(ctx, tx, input.Signer, d.Owner, "write")
					tx.Rollback(ctx)
				}
				if e == nil {
					raw = w.EncodeWorkflowValidation(ValidateDefinition(d))
				}
				variant = "WorkflowValidation"
			} else {
				var ref w.WorkflowDefinitionRef
				ref, e = s.Store.Publish(ctx, input.Signer, d, w.EncodeSignedRecord(packet))
				raw = w.EncodeWorkflowDefinitionRef(ref)
				variant = "WorkflowDefinitionRef"
			}
		}
	case "save-draft":
		var r w.SaveWorkflowDraft
		r, e = p.Decode(input.Body, w.DecodeSaveWorkflowDraft, w.EncodeSaveWorkflowDraft)
		if e == nil {
			var v w.WorkflowDraft
			v, e = s.Store.SaveDraft(ctx, input.Signer, r)
			raw = w.EncodeWorkflowDraft(v)
		}
		variant = "WorkflowDraft"
	case "read-draft":
		var r w.ReadWorkflowDraft
		r, e = p.Decode(input.Body, w.DecodeReadWorkflowDraft, w.EncodeReadWorkflowDraft)
		if e == nil {
			var v w.WorkflowDraft
			v, e = s.Store.ReadDraft(ctx, input.Signer, r)
			raw = w.EncodeWorkflowDraft(v)
		}
		variant = "WorkflowDraft"
	case "read-definition":
		var r w.ReadWorkflowDefinition
		r, e = p.Decode(input.Body, w.DecodeReadWorkflowDefinition, w.EncodeReadWorkflowDefinition)
		if e == nil {
			var v w.WorkflowDefinition
			v, e = s.Store.ReadDefinition(ctx, input.Signer, r.Definition)
			raw = w.EncodeWorkflowDefinition(v)
		}
		variant = "WorkflowDefinition"
	case "start":
		var r w.StartWorkflow
		r, e = p.Decode(input.Body, w.DecodeStartWorkflow, w.EncodeStartWorkflow)
		if e == nil {
			var v w.WorkflowRun
			v, e = s.Store.Start(ctx, input.Signer, r, w.EncodeSignedRecord(packet))
			raw = w.EncodeWorkflowRun(v)
		}
		variant = "WorkflowRun"
	case "read-run":
		var r w.ReadWorkflowRun
		r, e = p.Decode(input.Body, w.DecodeReadWorkflowRun, w.EncodeReadWorkflowRun)
		if e == nil {
			var v w.WorkflowRun
			v, e = s.Store.ReadRun(ctx, input.Signer, r)
			raw = w.EncodeWorkflowRun(v)
		}
		variant = "WorkflowRun"
	case "read-call":
		var r w.ReadWorkflowCall
		r, e = p.Decode(input.Body, w.DecodeReadWorkflowCall, w.EncodeReadWorkflowCall)
		if e == nil {
			var v w.WorkflowCallEvidence
			v, e = s.Store.ReadCall(ctx, input.Signer, r)
			raw = w.EncodeWorkflowCallEvidence(v)
		}
		variant = "WorkflowCallEvidence"
	case "complete-human":
		var r w.CompleteWorkflowHuman
		r, e = p.Decode(input.Body, w.DecodeCompleteWorkflowHuman, w.EncodeCompleteWorkflowHuman)
		if e == nil {
			var v w.WorkflowHumanReceipt
			v, e = s.Store.CompleteHuman(ctx, input.Signer, r, w.EncodeSignedRecord(packet))
			raw = w.EncodeWorkflowHumanReceipt(v)
		}
		variant = "WorkflowHumanReceipt"
	}
	if e != nil {
		return workflowFailure(e)
	}
	if len(raw) > p.MaxFrame || p.CheckFrame(raw) != nil {
		return workflowFailure(ErrInvalid)
	}
	return transport.NewRpcResponseOk(variant, raw)
}
func NewHTTPTransport(endpoint string) (*p.HTTPTransport, error) {
	shape := func(name string, check func([]byte) error) p.ResponseType {
		return p.ResponseType{Variant: name, Validate: check}
	}
	return p.NewComponentTransport(endpoint, map[string]p.ResponseType{
		"Discovery/describe": shape("ServiceDescription", func(b []byte) error {
			_, e := p.Decode(b, w.DecodeServiceDescription, w.EncodeServiceDescription)
			return e
		}),
		"Discovery/read-schema": shape("SchemaDocument", func(b []byte) error { _, e := p.Decode(b, w.DecodeSchemaDocument, w.EncodeSchemaDocument); return e }),
		"Workflows/validate": shape("WorkflowValidation", func(b []byte) error {
			_, e := p.Decode(b, w.DecodeWorkflowValidation, w.EncodeWorkflowValidation)
			return e
		}),
		"Workflows/save-draft": shape("WorkflowDraft", func(b []byte) error { _, e := p.Decode(b, w.DecodeWorkflowDraft, w.EncodeWorkflowDraft); return e }),
		"Workflows/read-draft": shape("WorkflowDraft", func(b []byte) error { _, e := p.Decode(b, w.DecodeWorkflowDraft, w.EncodeWorkflowDraft); return e }),
		"Workflows/publish": shape("WorkflowDefinitionRef", func(b []byte) error {
			_, e := p.Decode(b, w.DecodeWorkflowDefinitionRef, w.EncodeWorkflowDefinitionRef)
			return e
		}),
		"Workflows/read-definition": shape("WorkflowDefinition", func(b []byte) error {
			_, e := p.Decode(b, w.DecodeWorkflowDefinition, w.EncodeWorkflowDefinition)
			return e
		}),
		"Workflows/start":    shape("WorkflowRun", func(b []byte) error { _, e := p.Decode(b, w.DecodeWorkflowRun, w.EncodeWorkflowRun); return e }),
		"Workflows/read-run": shape("WorkflowRun", func(b []byte) error { _, e := p.Decode(b, w.DecodeWorkflowRun, w.EncodeWorkflowRun); return e }),
		"Workflows/read-call": shape("WorkflowCallEvidence", func(b []byte) error {
			_, e := p.Decode(b, w.DecodeWorkflowCallEvidence, w.EncodeWorkflowCallEvidence)
			return e
		}),
		"Workflows/complete-human": shape("WorkflowHumanReceipt", func(b []byte) error {
			_, e := p.Decode(b, w.DecodeWorkflowHumanReceipt, w.EncodeWorkflowHumanReceipt)
			return e
		}),
	})
}
