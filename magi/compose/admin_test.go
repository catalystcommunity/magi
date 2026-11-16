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
	modelsapi "github.com/catalystcommunity/magi/primitives/models/api"
)

// adminEnv serves MagiService over HTTP with the fake services of the admin
// tests. Later admin views reuse it: pass the fakes in services, keyed by
// service name, and the Config URL field of each named service that is empty
// gets the address of its test server.
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
	// Each service name maps to the Config URL field of that service. The
	// harness sets a field only when it is empty, so a test can point one
	// service at its own server.
	urls := map[string]*string{
		"ArtifactService":     &cfg.Artifacts,
		"ConversationService": &cfg.Conversation,
		"ExecutionService":    &cfg.Execution,
		"KnowledgeService":    &cfg.Knowledge,
		"SourceService":       &cfg.Source,
		"SpeechService":       &cfg.Speech,
		"WorkflowService":     &cfg.Workflow,
		"ModelService":        &cfg.Models,
		"ReleaseService":      &cfg.Release,
		"EgressService":       &cfg.Egress,
		"IdentityService":     &cfg.Identity,
	}
	for name, ops := range services {
		field, ok := urls[name]
		if !ok {
			t.Fatalf("adminEnv: unknown service %s", name)
		}
		if *field == "" {
			*field = serve(name, ops)
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

// The harness serves each fake service on its own server, and it does not
// replace a URL that the caller set.
func TestAdminEnv(t *testing.T) {
	// list_models answers, so the test proves that the harness wires the
	// fake into the client, not only that it fills a URL field.
	models := map[string]func(context.Context, []byte) ([]byte, error){
		"list_models": func(context.Context, []byte) ([]byte, error) {
			return modelsapi.EncodeListModelsResponse(modelsapi.ListModelsResponse{Models: []modelsapi.Model{{Name: "m1"}}}), nil
		},
	}
	ops := map[string]func(context.Context, []byte) ([]byte, error){}
	preset := "http://preset.example.local"
	sys, _, as := adminEnv(t, Config{Conversation: preset}, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"ConversationService": ops, "ModelService": models, "ReleaseService": ops,
		"EgressService": ops, "IdentityService": ops,
	})
	if sys.Cfg.Conversation != preset {
		t.Fatalf("the harness replaced the URL of the conversation service: %s", sys.Cfg.Conversation)
	}
	for _, c := range []struct{ name, url string }{
		{"release", sys.Cfg.Release},
		{"identity", sys.Cfg.Identity},
		{"egress", sys.Cfg.Egress},
	} {
		if c.url == "" {
			t.Fatalf("the harness did not serve the %s service", c.name)
		}
	}
	if sys.Release == nil || sys.Identity == nil {
		t.Fatal("the harness did not turn on the release and identity clients")
	}
	r, err := sys.Models.ListModels(as("user:a@example.local"), modelsapi.ListModelsRequest{})
	if err != nil || len(r.Models) != 1 || r.Models[0].Name != "m1" {
		t.Fatalf("models: %+v %v", r, err)
	}
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
