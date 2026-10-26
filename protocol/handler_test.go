package protocol

import (
	"bytes"
	"context"
	"errors"
	transport "github.com/catalystcommunity/csilgen/transports/go"
	"net/http/httptest"
	"testing"
)

func TestComponentHandlerControls(t *testing.T) {
	calls := 0
	h, e := NewRPCHandler("https://events.example.test", func(ctx context.Context, req transport.RpcRequest) transport.RpcResponse {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Error("no request deadline")
		}
		return transport.NewRpcResponseOk("Empty", []byte{0xa0})
	}, func(context.Context) error { return errors.New("store unavailable") })
	if e != nil {
		t.Fatal(e)
	}
	raw, e := transport.NewRpcRequest("Fixture", "read", []byte{0xa0}).WithID(7).Encode()
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, method, path, host, origin, media string
		body                                    []byte
		status                                  int
	}{
		{"valid", "POST", "/csil/v1/rpc", "events.example.test", "", "application/cbor", raw, 200},
		{"host", "POST", "/csil/v1/rpc", "elsewhere.test", "", "application/cbor", raw, 421},
		{"origin", "POST", "/csil/v1/rpc", "events.example.test", "https://foreign.test", "application/cbor", raw, 403},
		{"method", "GET", "/csil/v1/rpc", "events.example.test", "", "application/cbor", nil, 405},
		{"query", "POST", "/csil/v1/rpc?x=y", "events.example.test", "", "application/cbor", raw, 400},
		{"media", "POST", "/csil/v1/rpc", "events.example.test", "", "application/json", raw, 415},
		{"limit", "POST", "/csil/v1/rpc", "events.example.test", "", "application/cbor", make([]byte, MaxFrame+1), 413},
		{"liveness", "GET", "/health/live", "localhost", "", "", nil, 204},
		{"readiness", "GET", "/health/ready", "localhost", "", "", nil, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.media)
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			if out.Code != tc.status {
				t.Fatalf("status %d", out.Code)
			}
			if out.Header().Get("Strict-Transport-Security") == "" {
				t.Fatal("no HSTS")
			}
		})
	}
	if calls != 1 {
		t.Fatal("rejected request reached dispatcher")
	}
	h.Drain()
	r := httptest.NewRequest("POST", "/csil/v1/rpc", bytes.NewReader(raw))
	r.Host = "events.example.test"
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 503 {
		t.Fatal("drain")
	}
}
