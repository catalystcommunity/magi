package server

import (
	"bytes"
	"context"
	transport "github.com/catalystcommunity/csilgen/transports/go"
	wire "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPBoundary(t *testing.T) {
	h, e := New("https://magi.catalystsquad.local")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := transport.NewRpcRequest("Discovery", "describe", wire.EncodeDescribeRequest(wire.DescribeRequest{Versions: []string{"0alpha1"}})).WithID(23).Encode()
	for _, tc := range []struct {
		name, method, path, host, origin, media string
		body                                    []byte
		status                                  int
	}{
		{"describe", "POST", "/csil/v1/rpc", "magi.catalystsquad.local", "", "application/cbor", raw, 200},
		{"origin", "POST", "/csil/v1/rpc", "magi.catalystsquad.local", "https://other.test", "application/cbor", raw, 403},
		{"host", "POST", "/csil/v1/rpc", "other.test", "", "application/cbor", raw, 421},
		{"size", "POST", "/csil/v1/rpc", "magi.catalystsquad.local", "", "application/cbor", bytes.Repeat([]byte{0}, protocol.MaxFrame+1), 413},
		{"media", "POST", "/csil/v1/rpc", "magi.catalystsquad.local", "", "application/json", raw, 415},
		{"method", "GET", "/csil/v1/rpc", "magi.catalystsquad.local", "", "application/cbor", nil, 405},
		{"route", "GET", "/auth/callback", "magi.catalystsquad.local", "", "", nil, 404},
		{"query", "POST", "/csil/v1/rpc?token=sensitive", "magi.catalystsquad.local", "", "application/cbor", raw, 400},
		{"landing", "GET", "/", "magi.catalystsquad.local", "", "", nil, 200},
		{"schema", "GET", "/schema/opal.csil-schema.cbor", "magi.catalystsquad.local", "", "", nil, 200},
		{"schema-renamed", "GET", "/schema/magi.csil-schema.cbor", "magi.catalystsquad.local", "", "", nil, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.media)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code)
			}
			if strings.Contains(w.Body.String(), "sensitive") {
				t.Fatal("input leaked")
			}
			if tc.name == "describe" {
				v, e := transport.DecodeRpcResponse(w.Body.Bytes())
				if e != nil || v.ID == nil || *v.ID != 23 || v.Variant == nil || *v.Variant != "ServiceDescription" {
					t.Fatal("response", e)
				}
			}
		})
	}
	h.Drain()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
	if w.Code != 503 {
		t.Fatal("readiness during drain")
	}
}
func TestGeneratedClient(t *testing.T) {
	// Install the listener first so the explicit origin includes its selected port.
	srv := httptest.NewUnstartedServer(nil)
	origin := "http://" + srv.Listener.Addr().String()
	h, e := New(origin)
	if e != nil {
		t.Fatal(e)
	}
	srv.Config.Handler = h
	srv.Start()
	defer srv.Close()
	carrier, e := protocol.NewHTTPTransport(origin + "/csil/v1/rpc")
	if e != nil {
		t.Fatal(e)
	}
	client := wire.NewDiscoveryClient(carrier)
	d, e := client.Describe(context.Background(), wire.DescribeRequest{Versions: []string{"0alpha1"}})
	if e != nil {
		t.Fatal(e)
	}
	if len(d.ImplementedOperations) != 2 {
		t.Fatal("false capabilities")
	}
	if _, e := client.ReadSchema(context.Background(), wire.SchemaRequest{Schema: d.Schema}); e != nil {
		t.Fatal(e)
	}
	for _, q := range []wire.DescribeRequest{{Versions: []string{"old"}}, {Versions: []string{"0alpha1"}, RequiredFeatures: []string{"execute"}}} {
		if _, e := client.Describe(context.Background(), q); e == nil {
			t.Fatal("unsupported requirements accepted")
		}
	}
}
func TestDispatchRefusesMutation(t *testing.T) {
	for _, service := range []string{"Commands", "Federation", "Events", "Environment", "Execution", "Agent", "Evidence", "Knowledge"} {
		t.Run(service, func(t *testing.T) {
			r := Dispatch(transport.NewRpcRequest(service, "submit", []byte{0xa0}))
			if r.Status != transport.StatusUnknownServiceOrOp {
				t.Fatal("unimplemented operation admitted")
			}
		})
	}
}
func TestResponseChecks(t *testing.T) {
	for _, mode := range []string{"wrong_id", "wrong_variant", "unknown_field", "redirect", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					w.Header().Set("Location", "http://other.test")
					w.WriteHeader(302)
					return
				}
				w.Header().Set("Content-Type", "application/cbor")
				if mode == "oversize" {
					io.WriteString(w, strings.Repeat("x", protocol.MaxFrame+1))
					return
				}
				id := uint64(1)
				if mode == "wrong_id" {
					id = 2
				}
				variant := "ServiceDescription"
				if mode == "wrong_variant" {
					variant = "Receipt"
				}
				payload := wire.EncodeServiceDescription(Description())
				if mode == "unknown_field" {
					payload[0]++
					payload = append(payload, 0x61, 'x', 0xf5)
				}
				b, _ := transport.NewRpcResponseOk(variant, payload).WithID(&id).Encode()
				w.Write(b)
			}))
			defer srv.Close()
			c, _ := protocol.NewHTTPTransport(srv.URL)
			_, e := wire.NewDiscoveryClient(c).Describe(context.Background(), wire.DescribeRequest{Versions: []string{"0alpha1"}})
			if e == nil {
				t.Fatal("accepted response")
			}
		})
	}
}
