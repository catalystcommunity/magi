package eventing

import (
	"bytes"
	"context"
	"errors"
	transport "github.com/catalystcommunity/csilgen/transports/go"
	schema "github.com/catalystcommunity/magi/generated/events"
	w "github.com/catalystcommunity/magi/generated/events/go"
	p "github.com/catalystcommunity/magi/protocol"
	"slices"
	"time"
)

type Service struct {
	Store *Store
	Keys  KeyResolver
}

var operations = []string{"Events/publish", "Events/subscribe", "Events/pull", "Events/acknowledge", "Events/read-publication"}

func failure(e error) transport.RpcResponse {
	code := int64(1500)
	message := "Service is unavailable"
	retry := true
	switch {
	case errors.Is(e, ErrCapacity):
		code = 1429
		message = "Event storage capacity reached"
	case errors.Is(e, ErrRecovery):
		code = 1503
		message = "Storage reconciliation is required"
	case errors.Is(e, ErrDenied):
		code = 1403
		message = "Operation denied"
		retry = false
	case errors.Is(e, ErrInvalid), errors.Is(e, p.ErrWire):
		code = 1400
		message = "Invalid request"
		retry = false
	case errors.Is(e, ErrConflict):
		code = 1409
		message = "Identifier conflicts with retained content"
		retry = false
	case errors.Is(e, ErrNotFound):
		code = 1404
		message = "Record is not available"
		retry = false
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
			features := []string{"strict-cbor", "linkkeys-application-proof", "durable-pull", "local-transaction-helpers"}
			if !slices.Contains(v.Versions, "0alpha1") {
				return failure(ErrInvalid)
			}
			for _, f := range v.RequiredFeatures {
				if !slices.Contains(features, f) {
					return failure(ErrInvalid)
				}
			}
			desc := w.ServiceDescription{Protocol: "0alpha1", Schema: Schema(), ImplementedOperations: append([]string{"Discovery/describe", "Discovery/read-schema"}, operations...), AuthenticatedOperations: slices.Clone(operations), Features: features, MaxFrameBytes: p.MaxFrame, MaxCollectionItems: p.MaxCollection, MaxDepth: p.MaxDepth, RequestTimeoutMs: uint64(p.Timeout / time.Millisecond)}
			return transport.NewRpcResponseOk("ServiceDescription", w.EncodeServiceDescription(desc))
		case "read-schema":
			v, e := p.Decode(req.Payload, w.DecodeSchemaRequest, w.EncodeSchemaRequest)
			if e != nil {
				return failure(e)
			}
			expected := Schema()
			if v.Schema.Name != expected.Name || v.Schema.Version != expected.Version || !bytes.Equal(v.Schema.Digest, expected.Digest) {
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
	input, e := authenticate(ctx, s.Keys, s.Store.Application, req.Op, packet)
	if e != nil {
		return failure(e)
	}
	var raw []byte
	var variant string
	switch req.Op {
	case "publish":
		variant = "PublicationReceipt"
		raw, e = s.Store.publish(ctx, input.Signer, input.Body, w.EncodeSignedRecord(packet))
	case "subscribe":
		var v w.EventSubscription
		v, e = p.Decode(input.Body, w.DecodeEventSubscription, w.EncodeEventSubscription)
		if e == nil {
			raw, e = s.Store.Subscribe(ctx, input.Signer, v)
		}
		variant = "SubscriptionReceipt"
	case "pull":
		var v w.PullEvents
		v, e = p.Decode(input.Body, w.DecodePullEvents, w.EncodePullEvents)
		if e == nil {
			raw, e = s.Store.Pull(ctx, input.Signer, v)
		}
		variant = "DeliveryBatch"
	case "acknowledge":
		var v w.AcknowledgeDelivery
		v, e = p.Decode(input.Body, w.DecodeAcknowledgeDelivery, w.EncodeAcknowledgeDelivery)
		if e == nil {
			raw, e = s.Store.acknowledge(ctx, input.Signer, v, w.EncodeSignedRecord(packet))
		}
		variant = "DeliveryReceipt"
	case "read-publication":
		var v w.ReadPublication
		v, e = p.Decode(input.Body, w.DecodeReadPublication, w.EncodeReadPublication)
		if e == nil {
			raw, e = s.Store.ReadPublication(ctx, input.Signer, v)
		}
		variant = "PublicationReceipt"
	}
	if e != nil {
		return failure(e)
	}
	return transport.NewRpcResponseOk(variant, raw)
}
func response[T any](name string, decode func([]byte) (T, error), encode func(T) []byte) p.ResponseType {
	return p.ResponseType{Variant: name, Validate: func(b []byte) error { _, e := p.Decode(b, decode, encode); return e }}
}
func NewHTTPTransport(endpoint string) (*p.HTTPTransport, error) {
	return p.NewComponentTransport(endpoint, map[string]p.ResponseType{
		"Discovery/describe":      response("ServiceDescription", w.DecodeServiceDescription, w.EncodeServiceDescription),
		"Discovery/read-schema":   response("SchemaDocument", w.DecodeSchemaDocument, w.EncodeSchemaDocument),
		"Events/publish":          response("PublicationReceipt", w.DecodePublicationReceipt, w.EncodePublicationReceipt),
		"Events/subscribe":        response("SubscriptionReceipt", w.DecodeSubscriptionReceipt, w.EncodeSubscriptionReceipt),
		"Events/pull":             response("DeliveryBatch", w.DecodeDeliveryBatch, w.EncodeDeliveryBatch),
		"Events/acknowledge":      response("DeliveryReceipt", w.DecodeDeliveryReceipt, w.EncodeDeliveryReceipt),
		"Events/read-publication": response("PublicationReceipt", w.DecodePublicationReceipt, w.EncodePublicationReceipt),
	})
}
