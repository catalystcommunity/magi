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

// hostModel is a decision model: the probability of "yes" for each host.
type hostModel struct {
	yes   float64
	own   float64 // the limit of the model in the catalog
	err   error
	calls int
	got   string
}

func (f *hostModel) decide(_ context.Context, state string) (float64, string, float64, error) {
	f.calls++
	f.got = state
	return f.yes, "local", f.own, f.err
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
	rules, err := LoadEgressRules(rulesFile(t, `{"default": "review", "auto": {"sources": true, "hosts": ["*.mirror.example.org"], "model": true, "limit": 0.9}}`))
	if err != nil {
		t.Fatal(err)
	}
	svcTok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"execution:act-as", "artifacts:act-as"}}, time.Hour)
	sys := New(Config{Execution: serve("ExecutionService", executionapi.ExecutionServiceOps(fakeExecution{})),
		Workflow:    serve("WorkflowService", workflowapi.WorkflowServiceOps(autoWorkflow{})),
		Artifacts:   serve("ArtifactService", artifactsapi.ArtifactServiceOps(&fakeArtifacts{body: map[string][]byte{}})),
		EgressRules: rules, ServiceToken: svcTok, Admins: []string{"user:admin@example.local"}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fm := &hostModel{}
	sys.hostDecide = fm.decide
	pop, err := readPopularity(strings.NewReader("1,example.org\n7,docs.example.org\n3,google.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	sys.Popularity = pop
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
	fm.yes = 1
	if allowed("env_1", "10.0.0.7:443") || allowed("env_1", "db.prod.internal:443") || allowed("env_1", "pypi.org:22") || fm.calls != 0 {
		t.Fatalf("a private destination was permitted or went to the model (%d calls)", fm.calls)
	}
	// An environment with no task does not go to the model.
	if allowed("env_none", "yaml.example.org:443") || fm.calls != 0 {
		t.Fatalf("no task: %d calls", fm.calls)
	}
	// A name that looks like a known name does not go to the model.
	for _, d := range []string{"pypl.org:443", "pypi.org.cdn.example.org:443", "registry-npmjs.org:443", "gooogle.com:443", "crates-io.com:443"} {
		if allowed("env_1", d) || fm.calls != 0 {
			t.Fatalf("%s: permitted, or the model was asked (%d calls)", d, fm.calls)
		}
	}

	// A popular site that takes the content of anyone does not go to the model.
	for _, d := range []string{"docs.google.com:443", "www.googletagmanager.com:443", "godbolt.org:443"} {
		if allowed("env_1", d) || fm.calls != 0 {
			t.Fatalf("%s: permitted, or the model was asked (%d calls)", d, fm.calls)
		}
	}

	// The model: only a probability of the limit or more permits.
	fm.yes = 0.89
	if allowed("env_1", "yaml.example.org:443") || fm.calls != 1 {
		t.Fatalf("a probability below the limit permitted (%d calls)", fm.calls)
	}
	if !strings.Contains(fm.got, "\nPopularity: the domain example.org has rank 1 in a public list of the 1,000,000 most visited domains.\n") {
		t.Fatalf("the popularity line: %s", fm.got)
	}
	if !strings.Contains(fm.got, "Host: yaml.example.org\n") || !strings.Contains(fm.got, "Task: Add a parser") || strings.Contains(fm.got, "Use the yaml package") {
		t.Fatalf("the model must get the host and the title of the task only: %s", fm.got)
	}
	// The same task does not ask again for the same host.
	fm.yes = 1
	if allowed("env_1", "yaml.example.org:443") || fm.calls != 1 {
		t.Fatalf("asked the model again (%d calls)", fm.calls)
	}
	fm.err = io.ErrUnexpectedEOF
	if allowed("env_1", "err.example.org:443") {
		t.Fatal("permitted when the model failed")
	}
	fm.err, fm.yes = nil, 0.9
	calls := fm.calls
	if !allowed("env_1", "docs.example.org:443") || fm.calls != calls+1 {
		t.Fatal("the probability of the limit did not permit")
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
	if !strings.Contains(deref(docs.Note), "the decision model local gives 0.900 that the host is a known public source (limit 0.90)") ||
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

func TestPopularityOfTheBinary(t *testing.T) {
	p, err := (&System{}).popularity()
	if err != nil || p.Len() < 900000 {
		t.Fatal(err)
	}
	if d, r := p.rank("docs.python.org"); d != "python.org" || r == 0 {
		t.Fatalf("docs.python.org: %s %d", d, r)
	}
	if _, r := p.rank("pyth0n-mirror-downloads.example"); r != 0 {
		t.Fatal("an invented host has a rank")
	}
}

func TestPopularity(t *testing.T) {
	p, err := readPopularity(strings.NewReader("2,example.org\n1,pages.example.org\n9,example.co.uk\n\n"))
	if err != nil || p.Len() != 3 {
		t.Fatal(p, err)
	}
	for host, want := range map[string]string{
		"example.org":         "the domain example.org has rank 2 ",
		"A.Pages.Example.org": "the domain pages.example.org has rank 1 ",
		"x.example.co.uk":     "the domain example.co.uk has rank 9 ",
		"examp1e.org":         "no domain of this host is in a public list",
		"org":                 "no domain of this host is in a public list",
	} {
		if got := p.line(host); !strings.Contains(got, want) || !strings.HasPrefix(got, "Popularity: ") {
			t.Errorf("%s: %s", host, got)
		}
	}
	for _, bad := range []string{"", "example.org\n", "x,example.org\n", "0,example.org\n"} {
		if _, err := readPopularity(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
