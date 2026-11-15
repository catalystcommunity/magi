package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// startTaskSystem starts a System with a MagiService on the real HTTP
// transport. The workflow service records the starts.
func startTaskSystem(t *testing.T) (*System, *startWorkflow, *magiapi.MagiClient, *authn.Issuer) {
	t.Helper()
	s, wf := startSystem(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("MagiService", magiapi.MagiServiceOps(&Service{S: s}))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	return s, wf, magiapi.NewMagiClient(rpc.NewClient(h.URL, nil)), iss
}

// startTaskCtx is the context of one caller.
func startTaskCtx(t *testing.T, iss *authn.Issuer, sub, kind string, scopes ...string) context.Context {
	t.Helper()
	tok, err := iss.Issue(authn.Principal{Subject: sub, Kind: kind, Scopes: scopes}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return rpc.WithToken(context.Background(), tok)
}

// The model field of StartTask needs an operator; an operator sets the var
// "model" of the run.
func TestStartTaskModel(t *testing.T) {
	_, wf, mc, iss := startTaskSystem(t)
	model := `{"name": "glm-5.3-coder", "max_tokens": 8000}`
	user := startTaskCtx(t, iss, "user:a@example.local", authn.KindUser)
	if _, err := mc.StartTask(user, magiapi.StartTaskRequest{Kind: CodeChange, Instructions: "A change.",
		Model: ptr(model)}); rpc.CodeOf(err) != 403 {
		t.Fatalf("a user without the operator scope: %v", err)
	}
	op := startTaskCtx(t, iss, "user:op@example.local", authn.KindUser, ScopeOperate)
	if _, err := mc.StartTask(op, magiapi.StartTaskRequest{Kind: CodeChange, Instructions: "A change.",
		Model: ptr(model)}); err != nil {
		t.Fatal(err)
	}
	if got := wf.last().Vars["model"]; got != model {
		t.Fatalf("model var = %q, want %q", got, model)
	}
	// Without a model, the var stays out.
	if _, err := mc.StartTask(op, magiapi.StartTaskRequest{Kind: CodeChange, Instructions: "Another change."}); err != nil {
		t.Fatal(err)
	}
	if v, ok := wf.last().Vars["model"]; ok {
		t.Fatalf("model var = %q, want none", v)
	}
}

// The base field of StartTask needs an operator, must be a commit, and an
// operator sets the var "base" of the run.
func TestStartTaskBase(t *testing.T) {
	_, wf, mc, iss := startTaskSystem(t)
	base := "0123456789abcdef"
	user := startTaskCtx(t, iss, "user:a@example.local", authn.KindUser)
	if _, err := mc.StartTask(user, magiapi.StartTaskRequest{Kind: CodeChange, Instructions: "A change.",
		Base: ptr(base)}); rpc.CodeOf(err) != 403 {
		t.Fatalf("a user without the operator scope: %v", err)
	}
	op := startTaskCtx(t, iss, "user:op@example.local", authn.KindUser, ScopeOperate)
	// A value that is not hex of 7 to 40 characters is invalid.
	for _, bad := range []string{"01234", "0123456789abcdeZ", "0123456789abcdef0123456789abcdef0123456789abcdef"} {
		if _, err := mc.StartTask(op, magiapi.StartTaskRequest{Kind: CodeChange, Instructions: "A change.",
			Base: ptr(bad)}); rpc.CodeOf(err) != 400 {
			t.Fatalf("base %q: %v, want invalid", bad, err)
		}
	}
	if _, err := mc.StartTask(op, magiapi.StartTaskRequest{Kind: CodeChange, Instructions: "A change.",
		Base: ptr(base)}); err != nil {
		t.Fatal(err)
	}
	if got := wf.last().Vars["base"]; got != base {
		t.Fatalf("base var = %q, want %q", got, base)
	}
	// Without a base, the var stays out.
	if _, err := mc.StartTask(op, magiapi.StartTaskRequest{Kind: CodeChange, Instructions: "Another change."}); err != nil {
		t.Fatal(err)
	}
	if v, ok := wf.last().Vars["base"]; ok {
		t.Fatalf("base var = %q, want none", v)
	}
}
