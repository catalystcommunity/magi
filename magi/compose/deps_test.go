package compose

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCheckDeps: an answer that this build cannot decode (here an empty
// CBOR map, as from a server of an older contract) and a server that does
// not answer are failures. The handler gives 503 and no error text.
func TestCheckDeps(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cbor")
		w.Write([]byte{0xa0})
	}))
	defer old.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	cfg := Config{Artifacts: old.URL, Conversation: old.URL, Execution: old.URL, Knowledge: old.URL,
		Source: old.URL, Speech: old.URL, Workflow: down.URL, ServiceToken: "t"}
	sys := New(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := map[string]error{}
	for _, c := range sys.CheckDeps(context.Background()) {
		got[c.Name] = c.Err
	}
	if len(got) != 7 {
		t.Fatalf("checks: %v", got)
	}
	if got["workflow"] == nil {
		t.Error("workflow: a server that does not answer passed")
	}
	if e := got["knowledge"]; e == nil || !strings.Contains(e.Error(), "missing") {
		t.Errorf("knowledge: want a decode error for a missing field, got %v", e)
	}

	rec := httptest.NewRecorder()
	sys.DepsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz/deps", nil))
	body := rec.Body.String()
	if rec.Code != 503 || !strings.Contains(body, "workflow fail") || strings.Contains(body, "missing") || strings.Contains(body, "refused") {
		t.Errorf("handler: %d %q", rec.Code, body)
	}
}
