package workfloweditor

import (
	"bytes"
	"net/http/httptest"
	"testing"

	w "github.com/catalystcommunity/magi/generated/go"
)

func TestEditorBoundary(t *testing.T) {
	h, e := NewHandler("http://127.0.0.1:8092", "../.build/workflows")
	if e != nil {
		t.Fatal(e)
	}
	empty := w.EncodeWorkflowDefinition(w.WorkflowDefinition{})
	for _, c := range []struct {
		name, method, path, host, origin string
		body                             []byte
		code                             int
	}{
		{"page", "GET", "/", "127.0.0.1:8092", "", nil, 200},
		{"model", "GET", "/model.mjs", "127.0.0.1:8092", "", nil, 200},
		{"host", "GET", "/", "attacker.test", "", nil, 421},
		{"origin", "POST", "/validate", "127.0.0.1:8092", "http://attacker.test", empty, 403},
		{"semantic-errors", "POST", "/validate", "127.0.0.1:8092", "", empty, 200},
		{"trailing-data", "POST", "/validate", "127.0.0.1:8092", "", append(empty, 0), 400},
		{"no-execution", "POST", "/start", "127.0.0.1:8092", "", empty, 404},
		{"no-file-access", "GET", "/go.mod", "127.0.0.1:8092", "", nil, 404},
		{"bounded", "POST", "/validate", "127.0.0.1:8092", "", make([]byte, 32769), 413},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "http://"+c.host+c.path, bytes.NewReader(c.body))
			r.Header.Set("Content-Type", "application/cbor")
			r.Header.Set("Origin", c.origin)
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			if out.Code != c.code {
				t.Fatal(out.Code, out.Body.String())
			}
			if out.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("no CSP")
			}
			if c.name == "semantic-errors" {
				v, e := w.DecodeWorkflowValidation(out.Body.Bytes())
				if e != nil || v.Valid {
					t.Fatal(v, e)
				}
			}
		})
	}
	if _, e = NewHandler("http://0.0.0.0:8092", "../.build/workflows"); e == nil {
		t.Fatal("nonloopback editor allowed")
	}
}
