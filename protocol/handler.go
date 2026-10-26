package protocol

import (
	"context"
	transport "github.com/catalystcommunity/csilgen/transports/go"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// RPCHandler supplies carrier controls without an application data model.
type RPCHandler struct {
	origin   *url.URL
	dispatch func(context.Context, transport.RpcRequest) transport.RpcResponse
	health   func(context.Context) error
	ready    atomic.Bool
	slots    chan struct{}
}

func NewRPCHandler(origin string, dispatch func(context.Context, transport.RpcRequest) transport.RpcResponse, health func(context.Context) error) (*RPCHandler, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1"))) || dispatch == nil {
		return nil, ErrWire
	}
	h := &RPCHandler{origin: u, dispatch: dispatch, health: health, slots: make(chan struct{}, 32)}
	h.ready.Store(true)
	return h, nil
}
func (h *RPCHandler) Drain() { h.ready.Store(false) }
func (h *RPCHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	if h.origin.Scheme == "https" {
		w.Header().Set("Strict-Transport-Security", "max-age=86400")
	}
	if r.Method == "GET" && (r.URL.Path == "/health/live" || r.URL.Path == "/health/ready") {
		if r.URL.Path == "/health/ready" {
			if !h.ready.Load() {
				w.WriteHeader(503)
				return
			}
			if h.health != nil {
				ctx, cancel := context.WithTimeout(r.Context(), time.Second)
				defer cancel()
				if h.health(ctx) != nil {
					w.WriteHeader(503)
					return
				}
			}
		}
		w.WriteHeader(204)
		return
	}
	if r.Host != h.origin.Host {
		w.WriteHeader(421)
		return
	}
	if !h.ready.Load() {
		w.WriteHeader(503)
		return
	}
	if r.URL.Path != "/csil/v1/rpc" {
		w.WriteHeader(404)
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(405)
		return
	}
	if r.URL.RawQuery != "" {
		w.WriteHeader(400)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != h.origin.Scheme+"://"+h.origin.Host {
		w.WriteHeader(403)
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/cbor" || r.Header.Get("Content-Encoding") != "" {
		w.WriteHeader(415)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.WriteHeader(503)
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(Timeout))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(Timeout))
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxFrame))
	if e != nil {
		w.WriteHeader(413)
		return
	}
	req, e := Decode(raw, transport.DecodeRpcRequest, func(v transport.RpcRequest) []byte { b, _ := v.Encode(); return b })
	var response transport.RpcResponse
	if e != nil {
		response = transport.NewRpcResponseTransportError(transport.StatusMalformedEnvelope, "Invalid envelope")
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), Timeout)
		defer cancel()
		response = h.dispatch(ctx, req).WithID(req.ID)
	}
	result, e := response.Encode()
	if e != nil || len(result) > MaxFrame {
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	_, _ = w.Write(result)
}
