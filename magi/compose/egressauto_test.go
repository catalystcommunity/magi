package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/model"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

func TestAutoHost(t *testing.T) {
	for dest, want := range map[string]bool{
		"pypi.org:443":                   true,
		"deb.debian.org:80":              true,
		"PyPI.org.:443":                  true,
		"pypi.org:8443":                  false, // not the HTTP or HTTPS port
		"pypi.org":                       false,
		"10.0.0.7:443":                   false, // an address
		"[::1]:443":                      false,
		"registry:443":                   false, // one label
		"db.prod.internal:443":           false,
		"magi.svc.cluster.local:443":     false,
		"xn--pypi-9ua.org:443":           false, // punycode can look like a different name
		"pypi_.org:443":                  false,
		"a..org:443":                     false,
		"host.example.org:443":           true,
		"metadata.google.internal.:443":  false,
		"169.254.169.254:80":             false,
		"something.onion:443":            false,
		"registry.npmjs.org.evil.io:443": true, // a public name: the lists and the model decide it
	} {
		if _, ok := autoHost(dest); ok != want {
			t.Errorf("autoHost(%q) = %v, want %v", dest, ok, want)
		}
	}
	for host, want := range map[string]bool{
		"pypl.org": true, "rnicrosoft.com": true, "golang-proxy.org": true, "githubusercontent.com.raw-cdn.net": true,
		"files.pythonhosted.org.cdn-downloads.net": true, "npmjs.help": true,
		"docs.github.com": false, "learn.microsoft.com": false, "en.wikipedia.org": false, "docs.rs": false,
		"yaml.example.org": false, "www.python.org": false, "docs.docker.com": false, "example.co.uk": false,
	} {
		if got := lookAlike(host); got != want {
			t.Errorf("lookAlike(%q) = %v, want %v", host, got, want)
		}
	}
	if anyHost(sourceHosts, "registry.npmjs.org.evil.io") {
		t.Error("a longer name is not a source host")
	}
}

// autoModel answers for a host and counts the calls.
type autoModel struct {
	args  string
	err   error
	calls int
	got   string
}

func (f *autoModel) Chat(_ context.Context, msgs []model.Message, _ []model.Tool, _ int) (model.Message, error) {
	f.calls++
	f.got = msgs[len(msgs)-1].Content
	if f.err != nil {
		return model.Message{}, f.err
	}
	m := model.Message{Served: "local"}
	if f.args != "" {
		c := model.ToolCall{}
		c.Function.Name, c.Function.Arguments = "verdict", f.args
		m.ToolCalls = []model.ToolCall{c}
	}
	return m, nil
}

type autoWorkflow struct{ workflowapi.WorkflowService }

func (autoWorkflow) GetRun(_ context.Context, r workflowapi.GetRunRequest) (workflowapi.Run, error) {
	if r.Id != "run_7" {
		return workflowapi.Run{}, rpc.NotFound("no run %s", r.Id)
	}
	return workflowapi.Run{Id: r.Id, Owner: "user:a@example.local", Vars: map[string]string{"title": "Add a parser", "instructions": "Use the yaml package."}}, nil
}

// Magi permits a destination with no person only for a public software
// source, a host of the installation list, or a host that the model knows
// with high confidence. Each automatic decision is a review that an admin
// sees and can decide again.
func TestEgressAutoDestination(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok := func(sub, kind string, scopes ...string) context.Context {
		s, err := iss.Issue(authn.Principal{Subject: sub, Kind: kind, Scopes: scopes}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return rpc.WithToken(context.Background(), s)
	}
	serve := func(name string, ops map[string]func(context.Context, []byte) ([]byte, error)) string {
		srv := &rpc.Server{Verifier: ver, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		srv.Register(name, ops)
		h := httptest.NewServer(rpc.Mux(srv, nil))
		t.Cleanup(h.Close)
		return h.URL
	}
	rules, err := LoadEgressRules(rulesFile(t, `{"default": "review", "auto": {"sources": true, "hosts": ["*.mirror.example.org"], "model": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	svcTok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"execution:act-as", "artifacts:act-as"}}, time.Hour)
	sys := New(Config{Execution: serve("ExecutionService", executionapi.ExecutionServiceOps(fakeExecution{})),
		Workflow:    serve("WorkflowService", workflowapi.WorkflowServiceOps(autoWorkflow{})),
		Artifacts:   serve("ArtifactService", artifactsapi.ArtifactServiceOps(&fakeArtifacts{body: map[string][]byte{}})),
		EgressRules: rules, ServiceToken: svcTok, Admins: []string{"user:admin@example.local"}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fm := &autoModel{}
	sys.ReviewModel = fm
	srv := &rpc.Server{Verifier: ver, Log: sys.Log}
	srv.Register("MagiService", magiapi.MagiServiceOps(&Service{S: sys}))
	srv.Register("EgressAskService", magiapi.EgressAskServiceOps(&Egress{S: sys}))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	defer h.Close()
	ask := magiapi.NewEgressAskClient(rpc.NewClient(h.URL, nil))
	mc := magiapi.NewMagiClient(rpc.NewClient(h.URL, nil))
	proxyCtx := tok("service:egress", authn.KindService, ScopeEgress)
	admin := tok("user:admin@example.local", authn.KindUser)
	allowed := func(env, d string) bool {
		t.Helper()
		r, err := ask.Destination(proxyCtx, magiapi.EgressDestinationRequest{Environment: env, Destination: d})
		if err != nil {
			t.Fatal(err)
		}
		return r.Allowed
	}

	// The lists need no model.
	if !allowed("env_1", "pypi.org:443") || !allowed("env_1", "eu.mirror.example.org:443") || fm.calls != 0 {
		t.Fatalf("the lists must permit with no model call (%d calls)", fm.calls)
	}
	// A private name and an address never get an automatic decision.
	fm.args = `{"what":"a package index","official_host":"yaml.example.org","verdict":"allow","confidence":"high"}`
	if allowed("env_1", "10.0.0.7:443") || allowed("env_1", "db.prod.internal:443") || allowed("env_1", "pypi.org:22") || fm.calls != 0 {
		t.Fatalf("a private destination was permitted or went to the model (%d calls)", fm.calls)
	}
	// An environment with no task does not go to the model.
	if allowed("env_none", "yaml.example.org:443") || fm.calls != 0 {
		t.Fatalf("no task: %d calls", fm.calls)
	}

	// The model: only "allow" with high confidence permits.
	fm.args = `{"what":"a package index","official_host":"yaml.example.org","verdict":"allow","confidence":"medium"}`
	if allowed("env_1", "yaml.example.org:443") || fm.calls != 1 {
		t.Fatalf("medium confidence permitted (%d calls)", fm.calls)
	}
	if !strings.Contains(fm.got, "Host: yaml.example.org") || !strings.Contains(fm.got, "Task: Add a parser") {
		t.Fatalf("the model did not get the host and the task: %s", fm.got)
	}
	// The same task does not ask again for the same host.
	fm.args = `{"what":"a package index","official_host":"yaml.example.org","verdict":"allow","confidence":"high"}`
	if allowed("env_1", "yaml.example.org:443") || fm.calls != 1 {
		t.Fatalf("asked the model again (%d calls)", fm.calls)
	}
	for _, args := range []string{`{"what":"unknown","verdict":"ask","confidence":"high"}`, ``, `not json`} {
		fm.args = args
		if allowed("env_1", "a"+strings.Repeat("b", fm.calls)+".example.org:443") {
			t.Fatalf("permitted for the answer %q", args)
		}
	}
	// The model names a different official host: the host is not that site.
	fm.args = `{"what":"the yaml project","official_host":"yaml.example.org","verdict":"allow","confidence":"high"}`
	if allowed("env_1", "yarnl.example.org:443") {
		t.Fatal("permitted a host that is not the official host")
	}
	// A name that looks like a known name does not go to the model.
	n := fm.calls
	for _, d := range []string{"pypl.org:443", "pypi.org.cdn.example.org:443", "registry-npmjs.org:443", "gooogle.com:443", "crates-io.com:443"} {
		if allowed("env_1", d) || fm.calls != n {
			t.Fatalf("%s: permitted, or the model was asked (%d calls)", d, fm.calls-n)
		}
	}
	fm.args, fm.err = "", io.ErrUnexpectedEOF
	if allowed("env_1", "err.example.org:443") {
		t.Fatal("permitted when the model failed")
	}
	fm.err, fm.args = nil, `{"what":"the documentation of the yaml project","official_host":"example.org","verdict":"allow","confidence":"high"}`
	calls := fm.calls
	if !allowed("env_1", "docs.example.org:443") || fm.calls != calls+1 {
		t.Fatal("high confidence did not permit")
	}
	// The decision is a rule: the next ask needs no model.
	if !allowed("env_1", "docs.example.org:443") || fm.calls != calls+1 {
		t.Fatal("the decision is not a rule")
	}

	// An admin sees each automatic decision and can decide it again.
	done, err := mc.ListEgressReviews(admin, magiapi.ListEgressReviewsRequest{State: ptr("decided")})
	if err != nil || len(done.Reviews) != 3 {
		t.Fatalf("decided reviews: %+v %v", done, err)
	}
	var docs magiapi.EgressReview
	for _, v := range done.Reviews {
		if deref(v.DecidedBy) != autoBy || deref(v.Decision) != "allow-host" || v.By != autoBy {
			t.Fatalf("review: %+v", v)
		}
		if v.Destination == "docs.example.org:443" {
			docs = v
		}
	}
	if !strings.Contains(deref(docs.Note), "the model local knows the host with high confidence: the documentation of the yaml project") ||
		docs.Owner != "user:a@example.local" {
		t.Fatalf("the review of the model decision: %+v", docs)
	}
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: docs.Id, Decision: "block"}); err != nil {
		t.Fatal(err)
	}
	if allowed("env_1", "docs.example.org:443") || fm.calls != calls+1 {
		t.Fatal("the block of the admin must stay, with no new model call")
	}

	// No "auto" in the rules: no automatic decision.
	sys.EgressRules = &EgressRules{Default: "review"}
	if allowed("env_1", "crates.io:443") {
		t.Fatal("permitted with no auto rules")
	}
}
