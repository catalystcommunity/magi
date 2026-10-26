// Package server serves the read-only protocol discovery boundary.
package server

import (
	"bytes"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sync/atomic"
	"time"

	transport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/magi"
	wire "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/protocol"
)

type Server struct {
	origin *url.URL
	ready  atomic.Bool
	slots  chan struct{}
}

func New(origin string) (*Server, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, protocol.ErrWire
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, protocol.ErrWire
	}
	s := &Server{origin: u, slots: make(chan struct{}, 32)}
	s.ready.Store(true)
	return s, nil
}
func (s *Server) Drain() { s.ready.Store(false) }
func Schema() wire.SchemaRef {
	digest, _ := hex.DecodeString(magi.DescriptorDigest)
	return wire.SchemaRef{Name: "opal", Version: magi.ProtocolVersion, Digest: digest}
}
func Description() wire.ServiceDescription {
	return wire.ServiceDescription{
		Protocol: magi.ProtocolVersion, Schema: Schema(), ImplementedOperations: []string{"Discovery/describe", "Discovery/read-schema"},
		Features: []string{"strict-cbor", "exact-signature-input", "read-only-discovery"}, MaxFrameBytes: protocol.MaxFrame,
		MaxCollectionItems: protocol.MaxCollection, MaxDepth: protocol.MaxDepth, RequestTimeoutMs: uint64(protocol.Timeout / time.Millisecond), AuthenticatedOperations: []string{},
	}
}
func appError(code int64, message string) transport.RpcResponse {
	return transport.NewRpcResponseOk("ServiceError", wire.EncodeServiceError(wire.ServiceError{Code: code, Message: message, Retryable: false}))
}

func Dispatch(req transport.RpcRequest) transport.RpcResponse {
	if req.Service != "Discovery" {
		return transport.NewRpcResponseTransportError(transport.StatusUnknownServiceOrOp, "Operation is not available")
	}
	switch req.Op {
	case "describe":
		v, e := protocol.Decode(req.Payload, wire.DecodeDescribeRequest, wire.EncodeDescribeRequest)
		if e != nil {
			return appError(1001, "Invalid request")
		}
		desc := Description()
		if !slices.Contains(v.Versions, magi.ProtocolVersion) {
			return appError(1002, "Protocol version is not supported")
		}
		for _, f := range v.RequiredFeatures {
			if !slices.Contains(desc.Features, f) {
				return appError(1002, "Required feature is not supported")
			}
		}
		return transport.NewRpcResponseOk("ServiceDescription", wire.EncodeServiceDescription(desc))
	case "read-schema":
		v, e := protocol.Decode(req.Payload, wire.DecodeSchemaRequest, wire.EncodeSchemaRequest)
		if e != nil {
			return appError(1001, "Invalid request")
		}
		if !reflect.DeepEqual(v.Schema, Schema()) {
			return appError(1002, "Schema is not supported")
		}
		return transport.NewRpcResponseOk("SchemaDocument", wire.EncodeSchemaDocument(wire.SchemaDocument{Schema: Schema(), Descriptor: bytes.Clone(magi.Descriptor)}))
	default:
		return transport.NewRpcResponseTransportError(transport.StatusUnknownServiceOrOp, "Operation is not available")
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	if s.origin.Scheme == "https" {
		w.Header().Set("Strict-Transport-Security", "max-age=86400")
	}
	if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/health/ready" && !s.ready.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.Host != s.origin.Host {
		http.Error(w, "Invalid host", 421)
		return
	}
	if !s.ready.Load() {
		http.Error(w, "Service is draining", 503)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "Busy", 503)
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(protocol.Timeout))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(protocol.Timeout))
	if r.URL.RawQuery != "" {
		http.Error(w, "Invalid request", 400)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, landing)
		return
	}
	if r.Method == "GET" && (r.URL.Path == "/schema/magi.csil-schema.cbor" || r.URL.Path == "/schema/opal.csil-schema.cbor") {
		w.Header().Set("Content-Type", "application/csil-schema+cbor")
		w.Write(magi.Descriptor)
		return
	}
	if r.URL.Path != "/csil/v1/rpc" {
		http.NotFound(w, r)
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(405)
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/cbor" || r.Header.Get("Content-Encoding") != "" {
		w.WriteHeader(415)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin.Scheme+"://"+s.origin.Host {
		w.WriteHeader(403)
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxFrame))
	if e != nil {
		http.Error(w, "Request body refused", 413)
		return
	}
	// The reference transport ignores unknown optional envelope fields. The Magi
	// profile instead requires the exact envelope shape before dispatch.
	req, e := protocol.Decode(raw, transport.DecodeRpcRequest, func(v transport.RpcRequest) []byte { b, _ := v.Encode(); return b })
	var response transport.RpcResponse
	if e != nil {
		response = transport.NewRpcResponseTransportError(transport.StatusMalformedEnvelope, "Invalid envelope")
	} else {
		response = Dispatch(req).WithID(req.ID)
	}
	result, e := response.Encode()
	if e != nil {
		http.Error(w, "Response failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.Write(result)
}

const landing = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Magi protocol lab</title><h1>Magi protocol lab</h1><p>The 0alpha1 contract is available. This service provides read-only CSIL-RPC discovery.</p><p>No login, task execution, secret resolution, or evidence storage is enabled.</p><p>Use POST /csil/v1/rpc with Discovery/describe or Discovery/read-schema.</p><p><a href="/schema/magi.csil-schema.cbor">Download the CSIL schema</a></p></html>`
