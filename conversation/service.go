package conversation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	transport "github.com/catalystcommunity/csilgen/transports/go"
	schema "github.com/catalystcommunity/magi/generated/conversations"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
	p "github.com/catalystcommunity/magi/protocol"
	"slices"
	"time"
)

type Service struct {
	Store *Store
	Keys  identity.KeyResolver
}

func Schema() w.SchemaRef {
	digest, _ := hex.DecodeString(schema.Digest)
	return w.SchemaRef{Name: "catalyst.conversations", Version: "0alpha1", Digest: digest}
}
func Sign(op string, body []byte, actor, recipient w.ApplicationRef, keyID string, key ed25519.PrivateKey, at time.Time) (w.SignedRecord, error) {
	return p.Sign(p.SignatureScope{Context: "catalyst.conversations/" + op, Schema: Schema(), Signer: actor, Recipient: recipient}, body, keyID, at, key)
}

var operations = []string{"Conversations/append", "Conversations/read", "Conversations/history"}

func failure(e error) transport.RpcResponse {
	code, message, retry := int64(1500), "Conversation storage unavailable", true
	switch {
	case errors.Is(e, ErrDenied), errors.Is(e, identity.ErrDenied):
		code, message, retry = 1403, "Operation denied", false
	case errors.Is(e, ErrInvalid), errors.Is(e, p.ErrWire):
		code, message, retry = 1400, "Invalid record", false
	case errors.Is(e, ErrConflict):
		code, message, retry = 1409, "Record content conflicts", false
	case errors.Is(e, ErrCapacity):
		code, message, retry = 1429, "Record storage capacity reached", false
	}
	return transport.NewRpcResponseOk("ServiceError", w.EncodeServiceError(w.ServiceError{Code: code, Message: message, Retryable: retry}))
}
func (s *Service) Dispatch(ctx context.Context, req transport.RpcRequest) transport.RpcResponse {
	if req.Service == "Discovery" {
		switch req.Op {
		case "describe":
			v, e := p.Decode(req.Payload, w.DecodeDescribeRequest, w.EncodeDescribeRequest)
			if e != nil {
				return failure(e)
			}
			features := []string{"strict-cbor", "linkkeys-application-proof", "immutable-source-records", "multi-session-links", "public-test-data-only"}
			if !slices.Contains(v.Versions, "0alpha1") {
				return failure(ErrInvalid)
			}
			for _, f := range v.RequiredFeatures {
				if !slices.Contains(features, f) {
					return failure(ErrInvalid)
				}
			}
			return transport.NewRpcResponseOk("ServiceDescription", w.EncodeServiceDescription(w.ServiceDescription{Protocol: "0alpha1", Schema: Schema(), ImplementedOperations: append([]string{"Discovery/describe", "Discovery/read-schema"}, operations...), AuthenticatedOperations: slices.Clone(operations), Features: features, MaxFrameBytes: p.MaxFrame, MaxCollectionItems: p.MaxCollection, MaxDepth: p.MaxDepth, RequestTimeoutMs: uint64(p.Timeout / time.Millisecond)}))
		case "read-schema":
			v, e := p.Decode(req.Payload, w.DecodeSchemaRequest, w.EncodeSchemaRequest)
			expected := Schema()
			if e != nil || !bytes.Equal(w.EncodeSchemaRef(v.Schema), w.EncodeSchemaRef(expected)) {
				return failure(ErrInvalid)
			}
			return transport.NewRpcResponseOk("SchemaDocument", w.EncodeSchemaDocument(w.SchemaDocument{Schema: expected, Descriptor: bytes.Clone(schema.Descriptor)}))
		}
	}
	if !slices.Contains(operations, req.Service+"/"+req.Op) {
		return transport.NewRpcResponseTransportError(transport.StatusUnknownServiceOrOp, "Operation is not available")
	}
	if s.Store == nil {
		return failure(ErrDenied)
	}
	packet, e := p.Decode(req.Payload, w.DecodeSignedRecord, w.EncodeSignedRecord)
	if e != nil {
		return failure(e)
	}
	input, e := identity.Authenticate(ctx, s.Keys, s.Store.Application, "catalyst.conversations/"+req.Op, Schema(), packet)
	if e != nil {
		return failure(e)
	}
	switch req.Op {
	case "append":
		v, e := s.Store.Append(ctx, input.Signer, input.Body, w.EncodeSignedRecord(packet))
		if e != nil {
			return failure(e)
		}
		return transport.NewRpcResponseOk("ConversationReceipt", w.EncodeConversationReceipt(v))
	case "read":
		query, e := p.Decode(input.Body, w.DecodeConversationRead, w.EncodeConversationRead)
		if e != nil {
			return failure(e)
		}
		v, e := s.Store.Read(ctx, input.Signer, query)
		if e != nil {
			return failure(e)
		}
		return transport.NewRpcResponseOk("ConversationStored", w.EncodeConversationStored(v))
	case "history":
		query, e := p.Decode(input.Body, w.DecodeConversationHistory, w.EncodeConversationHistory)
		if e != nil {
			return failure(e)
		}
		v, e := s.Store.History(ctx, input.Signer, query)
		if e != nil {
			return failure(e)
		}
		return transport.NewRpcResponseOk("ConversationPage", w.EncodeConversationPage(v))
	}
	return failure(ErrInvalid)
}
func response[T any](name string, decode func([]byte) (T, error), encode func(T) []byte) p.ResponseType {
	return p.ResponseType{Variant: name, Validate: func(b []byte) error { _, e := p.Decode(b, decode, encode); return e }}
}
func NewHTTPTransport(endpoint string) (*p.HTTPTransport, error) {
	return p.NewComponentTransport(endpoint, map[string]p.ResponseType{
		"Discovery/describe":    response("ServiceDescription", w.DecodeServiceDescription, w.EncodeServiceDescription),
		"Discovery/read-schema": response("SchemaDocument", w.DecodeSchemaDocument, w.EncodeSchemaDocument),
		"Conversations/append":  response("ConversationReceipt", w.DecodeConversationReceipt, w.EncodeConversationReceipt),
		"Conversations/read":    response("ConversationStored", w.DecodeConversationStored, w.EncodeConversationStored),
		"Conversations/history": response("ConversationPage", w.DecodeConversationPage, w.EncodeConversationPage),
	})
}
