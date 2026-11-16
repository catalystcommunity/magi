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

// adminEnv serves MagiService over HTTP with the fake services of the admin
// tests. Later admin views reuse it: pass the fakes in services, keyed by
// service name, and the Config fields that name them stay empty.
func adminEnv(t *testing.T, cfg Config, services map[string]map[string]func(context.Context, []byte) ([]byte, error)) (*System, *magiapi.MagiClient, func(subject string, scopes ...string) context.Context) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok := func(sub, kind string, scopes ...string) string {
		s, err := iss.Issue(authn.Principal{Subject: sub, Kind: kind, Scopes: scopes}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	serve := func(name string, ops map[string]func(context.Context, []byte) ([]byte, error)) string {
		srv := &rpc.Server{Verifier: ver, Log: log}
		srv.Register(name, ops)
		h := httptest.NewServer(rpc.Mux(srv, nil))
		t.Cleanup(h.Close)
		return h.URL
	}
	// The scopes of the service token of magi (deploy/README.md).
	svcTok := tok("service:magi", authn.KindService,
		"conversation:act-as", "conversation:admin", "knowledge:act-as", "artifacts:act-as", "workflow:act-as", "workflow:worker",
		"execution:act-as", "source:act-as", "release:operate", "release:read", "speech:lexicon")
	for name, ops := range services {
		switch name {
		case "ArtifactService":
			cfg.Artifacts = serve(name, ops)
		case "ConversationService":
			cfg.Conversation = serve(name, ops)
		case "ExecutionService":
			cfg.Execution = serve(name, ops)
		case "KnowledgeService":
			cfg.Knowledge = serve(name, ops)
		case "SourceService":
			cfg.Source = serve(name, ops)
		case "SpeechService":
			cfg.Speech = serve(name, ops)
		case "WorkflowService":
			cfg.Workflow = serve(name, ops)
		default:
			t.Fatalf("adminEnv: unknown service %s", name)
		}
	}
	cfg.ServiceToken = svcTok
	sys := New(cfg, nil, log)
	srv := &rpc.Server{Verifier: ver, Log: log}
	srv.Register("MagiService", magiapi.MagiServiceOps(&Service{S: sys}))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	as := func(subject string, scopes ...string) context.Context {
		return rpc.WithToken(context.Background(), tok(subject, authn.KindUser, scopes...))
	}
	return sys, magiapi.NewMagiClient(rpc.NewClient(h.URL, nil)), as
}

// get-access tells a client if the caller is an operator: a plain person is
// not one, the scope and an admin subject are, and a call without a token
// does not pass the transport.
func TestGetAccess(t *testing.T) {
	_, mc, as := adminEnv(t, Config{Admins: []string{"user:admin@example.local"}}, nil)
	a, err := mc.GetAccess(as("user:a@example.local"), magiapi.GetAccessRequest{})
	if err != nil || a.Subject != "user:a@example.local" || a.Operator {
		t.Fatalf("plain person: %+v %v", a, err)
	}
	a, err = mc.GetAccess(as("user:b@example.local", ScopeOperate), magiapi.GetAccessRequest{})
	if err != nil || a.Subject != "user:b@example.local" || !a.Operator {
		t.Fatalf("scope: %+v %v", a, err)
	}
	a, err = mc.GetAccess(as("user:admin@example.local"), magiapi.GetAccessRequest{})
	if err != nil || a.Subject != "user:admin@example.local" || !a.Operator {
		t.Fatalf("admin subject: %+v %v", a, err)
	}
	if _, err := mc.GetAccess(context.Background(), magiapi.GetAccessRequest{}); rpc.CodeOf(err) != 401 {
		t.Fatalf("no token: %v", err)
	}
}
