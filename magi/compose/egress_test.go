package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/model"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/egressenv"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

const egressRulesJSON = `{
  "secrets": [
    {"placeholder": "magi-secret-gh-a", "secret": "gh-a", "hosts": ["api.example.local"], "path": "^/repos/", "place": "header:authorization", "owners": ["user:a@example.local"]},
    {"placeholder": "magi-secret-pypi", "secret": "pypi", "hosts": ["*.pypi.example.local"], "place": "header:authorization"}
  ],
  "rules": [
    {"hosts": ["api.example.local"], "methods": ["GET"], "verdict": "pass", "reason": "reads are fine"},
    {"hosts": ["*"], "methods": ["DELETE"], "verdict": "review"},
    {"hosts": ["evil.example.local"], "verdict": "block", "reason": "known bad"}
  ],
  "default": "model"
}`

func rulesFile(t *testing.T, text string) string {
	p := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func flowOf(owner, method, url string) magiapi.EgressFlow {
	host := strings.SplitN(strings.TrimPrefix(url, "https://"), "/", 2)[0]
	return magiapi.EgressFlow{Id: "flw_1", Environment: "env_1", Owner: owner, Direction: "out", Destination: host + ":443", Method: method, Url: url, Headers: []magiapi.EgressFlowHeader{}, Body: []byte{}}
}

func TestEgressRules(t *testing.T) {
	r, err := LoadEgressRules(rulesFile(t, egressRulesJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, url, want string }{
		{"GET", "https://api.example.local/repos/x", "pass"},
		{"POST", "https://api.example.local/repos/x", ""},
		{"DELETE", "https://other.example.local/x", "review"},
		{"GET", "https://evil.example.local/", "block"},
	} {
		got := ""
		if x := r.Match(flowOf("user:a@example.local", c.method, c.url)); x != nil {
			got = x.Verdict
		}
		if got != c.want {
			t.Errorf("%s %s: %q, want %q", c.method, c.url, got, c.want)
		}
	}
	if n := len(r.SecretRules(flowOf("user:a@example.local", "GET", "https://x/"))); n != 2 {
		t.Errorf("owner a: %d rules, want 2", n)
	}
	b := r.SecretRules(flowOf("user:b@example.local", "GET", "https://x/"))
	if len(b) != 1 || b[0].Secret != "pypi" {
		t.Errorf("owner b: %+v", b)
	}
	for _, bad := range []string{
		`{"default": "maybe"}`,
		`{"rules": [{"hosts": ["a"], "verdict": "allow"}]}`,
		`{"rules": [{"hosts": ["a"], "verdict": "pass", "path": "("}]}`,
		`{"secrets": [{"placeholder": "gh", "secret": "gh", "hosts": ["a"], "place": "any"}]}`,
		`{"unknown": 1}`,
	} {
		if _, err := LoadEgressRules(rulesFile(t, bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	if !hostMatch("*.a.b", "x.a.b") || hostMatch("*.a.b", "a.b") || hostMatch("a.b", "xa.b") {
		t.Error("host patterns")
	}
}

// The CBOR that the Rust proxy sends (magi.rs go_fixture): the plugin
// request with an extra key, and the plugin and flow keys of the host.
const egressFixture = "a4687175657374696f6e61716565787472610766706c7567696e6672657669657764666c6f77aa62696465666c775f316b656e7669726f6e6d656e7465656e765f61656f776e657274757365723a61406578616d706c652e6c6f63616c69646972656374696f6e636f75746b64657374696e6174696f6e756170692e6578616d706c652e6c6f63616c3a343433666d6574686f6464504f53546375726c781b68747470733a2f2f6170692e6578616d706c652e6c6f63616c2f786673746174757300676865616465727381a2646e616d65666163636570746576616c7565632a2f2a64626f6479426869"

func TestEgressDecodesProxyCBOR(t *testing.T) {
	b, _ := hex.DecodeString(egressFixture)
	r, err := magiapi.DecodeEgressEvaluateRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Plugin != "review" || deref(r.Question) != "q" || r.Flow.Environment != "env_a" || r.Flow.Owner != "user:a@example.local" ||
		r.Flow.Method != "POST" || string(r.Flow.Body) != "hi" || len(r.Flow.Headers) != 1 {
		t.Fatalf("%+v", r)
	}
}

// fakeModel answers with a verdict tool call, or with text when verdict is "".
type fakeModel struct {
	verdict string
	got     string
}

func (f *fakeModel) Chat(_ context.Context, msgs []model.Message, _ []model.Tool, _ int) (model.Message, error) {
	f.got = msgs[len(msgs)-1].Content
	if f.verdict == "" {
		return model.Message{Content: "I think it is fine."}, nil
	}
	if f.verdict == "error" {
		return model.Message{}, errors.New("model down")
	}
	m := model.Message{Served: "local"}
	c := model.ToolCall{}
	c.Function.Name = "verdict"
	c.Function.Arguments = `{"verdict":"` + f.verdict + `","reason":"looked at it"}`
	m.ToolCalls = []model.ToolCall{c}
	return m, nil
}

type fakeExecution struct{ executionapi.ExecutionService }

func (fakeExecution) Get(_ context.Context, r executionapi.GetRequest) (executionapi.Environment, error) {
	if r.Id != "env_1" {
		return executionapi.Environment{}, rpc.NotFound("no environment %s", r.Id)
	}
	return executionapi.Environment{Id: r.Id, Owner: "user:a@example.local", Spec: executionapi.EnvironmentSpec{Labels: map[string]string{"magi.run": "run_7"}}}, nil
}

// fakeWorkflow knows run_9, whose step "work" has the claim "c".
type fakeWorkflow struct{ workflowapi.WorkflowService }

func (fakeWorkflow) GetRun(_ context.Context, r workflowapi.GetRunRequest) (workflowapi.Run, error) {
	if r.Id != "run_9" {
		return workflowapi.Run{}, rpc.NotFound("no run %s", r.Id)
	}
	return workflowapi.Run{Id: r.Id, Owner: "user:c@example.local"}, nil
}

func (fakeWorkflow) Heartbeat(_ context.Context, r workflowapi.HeartbeatRequest) (workflowapi.Ack, error) {
	return workflowapi.Ack{Ok: r.Run == "run_9" && r.Claim == "c"}, nil
}

// fakeArtifacts keeps artifacts in memory; List returns them newest first.
type fakeArtifacts struct {
	artifactsapi.ArtifactService
	mu   sync.Mutex
	list []artifactsapi.Artifact
	body map[string][]byte
}

func (f *fakeArtifacts) Put(_ context.Context, r artifactsapi.PutRequest) (artifactsapi.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := sha256.Sum256(r.Body)
	a := artifactsapi.Artifact{Digest: "sha256:" + hex.EncodeToString(h[:]), MediaType: r.MediaType, Size: uint64(len(r.Body)),
		Owner: "service:magi", Labels: r.Labels, CreatedMs: uint64(time.Now().UnixMilli())}
	f.list = append(f.list, a)
	f.body[a.Digest] = r.Body
	return a, nil
}

func (f *fakeArtifacts) Get(_ context.Context, r artifactsapi.GetRequest) (artifactsapi.GetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.list {
		if a.Digest == r.Digest {
			return artifactsapi.GetResponse{Artifact: a, Body: f.body[a.Digest]}, nil
		}
	}
	return artifactsapi.GetResponse{}, rpc.NotFound("no artifact")
}

func (f *fakeArtifacts) List(_ context.Context, r artifactsapi.ListRequest) (artifactsapi.ListResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := artifactsapi.ListResponse{Artifacts: []artifactsapi.Artifact{}}
	for i := len(f.list) - 1; i >= 0; i-- {
		if a := f.list[i]; r.LabelKey == nil || a.Labels[*r.LabelKey] == deref(r.LabelValue) {
			out.Artifacts = append(out.Artifacts, a)
		}
	}
	return out, nil
}

func TestEgressTransport(t *testing.T) {
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
	execURL := serve("ExecutionService", executionapi.ExecutionServiceOps(fakeExecution{}))
	fa := &fakeArtifacts{body: map[string][]byte{}}
	artURL := serve("ArtifactService", artifactsapi.ArtifactServiceOps(fa))

	rules, err := LoadEgressRules(rulesFile(t, egressRulesJSON))
	if err != nil {
		t.Fatal(err)
	}
	svcTok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"execution:act-as", "artifacts:act-as"}}, time.Hour)
	wfURL := serve("WorkflowService", workflowapi.WorkflowServiceOps(fakeWorkflow{}))
	key := []byte("0123456789abcdef0123456789abcdef")
	sys := New(Config{Execution: execURL, Workflow: wfURL, Artifacts: artURL, EgressRules: rules, ServiceToken: svcTok, Admins: []string{"user:admin@example.local"},
		EgressProxy: &EgressProxy{Addr: "egress:3128", Key: key, CA: []byte("ca")}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fm := &fakeModel{verdict: "block"}
	sys.ReviewModel = fm
	srv := &rpc.Server{Verifier: ver, Log: sys.Log}
	srv.Register("MagiService", magiapi.MagiServiceOps(&Service{S: sys}))
	srv.Register("EgressAskService", magiapi.EgressAskServiceOps(&Egress{S: sys}))
	srv.Register("RunnerService", magiapi.RunnerServiceOps(&Runners{S: sys}))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	defer h.Close()
	ask := magiapi.NewEgressAskClient(rpc.NewClient(h.URL, nil))
	mc := magiapi.NewMagiClient(rpc.NewClient(h.URL, nil))

	proxyCtx := tok("service:egress", authn.KindService, ScopeEgress)
	if _, err := ask.Environment(tok("user:a@example.local", authn.KindUser), magiapi.EgressEnvironmentRequest{Environment: "env_1"}); err == nil {
		t.Fatal("a user must not call EgressAskService")
	}
	if _, err := ask.Environment(tok("service:other", authn.KindService), magiapi.EgressEnvironmentRequest{Environment: "env_1"}); err == nil {
		t.Fatal("a service without magi:egress must not call EgressAskService")
	}
	env, err := ask.Environment(proxyCtx, magiapi.EgressEnvironmentRequest{Environment: "env_1"})
	if err != nil || env.Owner != "user:a@example.local" || deref(env.Task) != "run_7" {
		t.Fatalf("environment: %+v %v", env, err)
	}
	if _, err := ask.Environment(proxyCtx, magiapi.EgressEnvironmentRequest{Environment: "env_x"}); err == nil {
		t.Fatal("unknown environment")
	}
	// A runner sandbox: the proxy user is the run, and Magi gives its
	// credential only to the runner that holds a step of the run.
	runnerCtx := tok("runner:r1", authn.KindService, ScopeRunner)
	rc := magiapi.NewRunnerClient(rpc.NewClient(h.URL, nil))
	acc, err := rc.EgressAccess(runnerCtx, magiapi.RunnerStep{Run: "run_9", Step: "prepare", Claim: "c"})
	if err != nil || acc.Proxy == nil || acc.Proxy.Environment != "run_9" || acc.Proxy.Credential != egressenv.Credential(key, "run_9") || string(acc.Proxy.Ca) != "ca" {
		t.Fatalf("egress access: %+v %v", acc, err)
	}
	if _, err := rc.EgressAccess(runnerCtx, magiapi.RunnerStep{Run: "run_9", Step: "prepare", Claim: "wrong"}); err == nil {
		t.Fatal("egress access without the claim")
	}
	if _, err := rc.EgressAccess(tok("user:a@example.local", authn.KindUser), magiapi.RunnerStep{Run: "run_9", Step: "prepare", Claim: "c"}); err == nil {
		t.Fatal("egress access for a user")
	}
	env, err = ask.Environment(proxyCtx, magiapi.EgressEnvironmentRequest{Environment: "run_9"})
	if err != nil || env.Owner != "user:c@example.local" || deref(env.Task) != "run_9" {
		t.Fatalf("runner environment: %+v %v", env, err)
	}

	// The model calls of Magi itself: a service user, which passes with no
	// review (a review would call the model again).
	env, err = ask.Environment(proxyCtx, magiapi.EgressEnvironmentRequest{Environment: ServiceUser("magi")})
	if err != nil || env.Owner != "service:magi" || env.Task != nil {
		t.Fatalf("service environment: %+v %v", env, err)
	}
	sf := flowOf("service:magi", "POST", "https://models.example.local/v1/chat/completions")
	sf.Environment = ServiceUser("magi")
	if v, err := ask.Evaluate(proxyCtx, magiapi.EgressEvaluateRequest{Plugin: "magi-review", Flow: sf}); err != nil || v.Verdict != "pass" || fm.got != "" {
		t.Fatalf("service flow: %+v %v (model got %q)", v, err, fm.got)
	}

	sr, err := ask.SecretRules(proxyCtx, magiapi.EgressSecretRulesRequest{Plugin: "placeholders", Flow: flowOf("user:b@example.local", "GET", "https://x/")})
	if err != nil || len(sr.Rules) != 1 || sr.Rules[0].Placeholder != "magi-secret-pypi" {
		t.Fatalf("secret rules: %+v %v", sr, err)
	}

	eval := func(method, url string) magiapi.EgressVerdict {
		v, err := ask.Evaluate(proxyCtx, magiapi.EgressEvaluateRequest{Plugin: "magi-review", Flow: flowOf("user:a@example.local", method, url)})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := eval("GET", "https://api.example.local/repos/x"); v.Verdict != "pass" || v.By != "rules" {
		t.Fatalf("rule: %+v", v)
	}
	if v := eval("POST", "https://upload.example.local/x"); v.Verdict != "block" || v.By != "model local" {
		t.Fatalf("model: %+v", v)
	}
	if !strings.Contains(fm.got, "Task run: run_7") || !strings.Contains(fm.got, "POST https://upload.example.local/x") {
		t.Fatalf("the model did not get the flow and the task: %s", fm.got)
	}
	fm.verdict = ""
	if v := eval("POST", "https://upload.example.local/x"); v.Verdict != "review" {
		t.Fatalf("no tool call: %+v", v)
	}
	fm.verdict = "error"
	if v := eval("POST", "https://upload.example.local/x"); v.Verdict != "review" {
		t.Fatalf("model error: %+v", v)
	}
	fm.verdict = "block"

	// Reviews: the proxy reports blocked flows; only admins list and decide.
	report := func(kind, method, url string, body string) string {
		f := flowOf("user:a@example.local", method, url)
		f.Body = []byte(body)
		if kind == "destination" {
			f.Destination = strings.TrimPrefix(url, "https://")
		}
		r, err := ask.Report(proxyCtx, magiapi.EgressReportRequest{Kind: kind, Flow: f, Reason: "r", By: "magi-review"})
		if err != nil {
			t.Fatal(err)
		}
		return r.Review
	}
	if _, err := ask.Report(tok("user:a@example.local", authn.KindUser), magiapi.EgressReportRequest{Kind: "request", Flow: flowOf("x", "GET", "https://a/")}); err == nil {
		t.Fatal("a user reported")
	}
	id := report("request", "POST", "https://upload.example.local/x?q=1", "data")
	if again := report("request", "POST", "https://upload.example.local/x?q=2", "data2"); again != id {
		t.Fatalf("same host, method, and path: %s and %s", id, again)
	}
	dest := report("destination", "CONNECT", "https://new.example.local:443", "")
	a := tok("user:a@example.local", authn.KindUser)
	if _, err := mc.ListEgressReviews(a, magiapi.ListEgressReviewsRequest{}); err == nil {
		t.Fatal("a non-admin listed reviews")
	}
	admin := tok("user:admin@example.local", authn.KindUser)
	open, err := mc.ListEgressReviews(admin, magiapi.ListEgressReviewsRequest{})
	if err != nil || len(open.Reviews) != 2 || open.Reviews[1].Id != id || open.Reviews[1].Count != 1 || string(open.Reviews[1].Body) != "data" {
		t.Fatalf("open reviews (one report each minute): %+v %v", open, err)
	}
	allowed := func(d string) bool {
		r, err := ask.Destination(proxyCtx, magiapi.EgressDestinationRequest{Environment: "env_1", Destination: d})
		if err != nil {
			t.Fatal(err)
		}
		return r.Allowed
	}
	if allowed("new.example.local:443") {
		t.Fatal("destination allowed before a decision")
	}
	if _, err := mc.DecideEgressReview(a, magiapi.DecideEgressReviewRequest{Id: dest, Decision: "allow-host"}); err == nil {
		t.Fatal("a non-admin decided")
	}
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: dest, Decision: "allow-path"}); err == nil {
		t.Fatal("allow-path for a destination")
	}
	if d, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: dest, Decision: "allow-host"}); err != nil || deref(d.Rule) != "permit the destination new.example.local:443" {
		t.Fatalf("decide destination: %+v %v", d, err)
	}
	if !allowed("new.example.local:443") || allowed("new.example.local:8443") {
		t.Fatal("destination rule")
	}
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: id, Decision: "allow-layer4"}); err == nil {
		t.Fatal("allow-layer4 for a request review")
	}
	if d, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: dest, Decision: "allow-layer4"}); err != nil ||
		deref(d.Rule) != "permit the destination new.example.local:443 at layer 4 (not inspected)" {
		t.Fatalf("decide layer 4: %+v %v", d, err)
	}
	if r, err := ask.Destination(proxyCtx, magiapi.EgressDestinationRequest{Environment: "env_1", Destination: "new.example.local:443"}); err != nil ||
		!r.Allowed || r.Layer4 == nil || !*r.Layer4 {
		t.Fatalf("layer 4 answer: %+v %v", r, err)
	}
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: dest, Decision: "allow-host"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := ask.Destination(proxyCtx, magiapi.EgressDestinationRequest{Environment: "env_1", Destination: "new.example.local:443"}); r.Layer4 != nil {
		t.Fatalf("allow-host must replace layer 4: %+v", r)
	}
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: id, Decision: "allow-path", Note: ptr("uploads are fine")}); err != nil {
		t.Fatal(err)
	}
	if v := eval("POST", "https://upload.example.local/x?z=1"); v.Verdict != "pass" || v.By != "review user:admin@example.local" {
		t.Fatalf("decided path: %+v", v)
	}
	if v := eval("POST", "https://upload.example.local/y"); v.By == "review user:admin@example.local" {
		t.Fatalf("another path: %+v", v)
	}
	// A new decision replaces the rule: dismiss removes it.
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: id, Decision: "dismiss"}); err != nil {
		t.Fatal(err)
	}
	if v := eval("POST", "https://upload.example.local/x"); v.By == "review user:admin@example.local" {
		t.Fatalf("the rule stayed after dismiss: %+v", v)
	}
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: "egr_none", Decision: "block"}); err == nil {
		t.Fatal("decided an unknown review")
	}
	// block: a rule with no new review. dismiss: no rule, a new review later.
	del := report("request", "DELETE", "https://api.example.local/repos/x", "")
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: del, Decision: "block"}); err != nil {
		t.Fatal(err)
	}
	if v := eval("DELETE", "https://api.example.local/repos/x"); v.Verdict != "block" {
		t.Fatalf("blocked: %+v", v)
	}
	sys.egressReviews.reported = map[string]time.Time{}
	report("request", "DELETE", "https://api.example.local/repos/x", "")
	put := report("request", "PUT", "https://api.example.local/repos/x", "")
	if _, err := mc.DecideEgressReview(admin, magiapi.DecideEgressReviewRequest{Id: put, Decision: "dismiss"}); err != nil {
		t.Fatal(err)
	}
	if v := eval("PUT", "https://api.example.local/repos/x"); v.By == "review user:admin@example.local" {
		t.Fatalf("dismiss made a rule: %+v", v)
	}
	sys.egressReviews.reported = map[string]time.Time{}
	report("request", "PUT", "https://api.example.local/repos/x", "")
	open, _ = mc.ListEgressReviews(admin, magiapi.ListEgressReviewsRequest{})
	if len(open.Reviews) != 1 || open.Reviews[0].Id != put {
		t.Fatalf("open after decisions: %+v", open.Reviews)
	}
	done, _ := mc.ListEgressReviews(admin, magiapi.ListEgressReviewsRequest{State: ptr("decided")})
	if len(done.Reviews) != 4 || deref(done.Reviews[len(done.Reviews)-1].Decision) == "" {
		t.Fatalf("decided reviews: %+v", done.Reviews)
	}
}

func TestEgressProxyTransport(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	var got string
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Proxy-Authorization") + " " + r.URL.String()
	}))
	defer px.Close()
	tl := httptest.NewTLSServer(http.NotFoundHandler())
	defer tl.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tl.Certificate().Raw})
	if _, err := (&EgressProxy{Addr: "x", Key: key, CA: []byte("no")}).Transport("svc_magi"); err == nil {
		t.Fatal("a CA that is not PEM")
	}
	tr, err := (&EgressProxy{Addr: strings.TrimPrefix(px.URL, "http://"), Key: key, CA: ca}).Transport(ServiceUser("magi"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&http.Client{Transport: tr}).Get("http://models.example.local:8095/v1/models"); err != nil {
		t.Fatal(err)
	}
	cred := base64.StdEncoding.EncodeToString([]byte("svc_magi:" + egressenv.Credential(key, "svc_magi")))
	if got != "Basic "+cred+" http://models.example.local:8095/v1/models" {
		t.Fatalf("proxy got %q", got)
	}
}

// A secret rule with personas reaches only the tasks of those personas.
func TestEgressSecretPersonas(t *testing.T) {
	r, err := LoadEgressRules(rulesFile(t, `{"secrets": [
	  {"placeholder": "magi-secret-github", "secret": "github", "hosts": ["api.github.com"], "place": "header:authorization", "personas": ["persona:engineering"]},
	  {"placeholder": "magi-secret-pypi", "secret": "pypi", "hosts": ["pypi.org"], "place": "header:authorization"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	f := flowOf("user:a@example.local", "GET", "https://api.github.com/")
	for persona, want := range map[string]int{"persona:engineering": 2, "persona:assistant": 1, "persona:magi": 1, "": 1} {
		asked := 0
		got := r.secretRules(f, func() string { asked++; return persona })
		if len(got) != want {
			t.Errorf("%q: %d rules, want %d", persona, len(got), want)
		}
		if asked != 1 {
			t.Errorf("%q: persona asked %d times", persona, asked)
		}
	}
}

func TestEgressTaskEnv(t *testing.T) {
	r, err := LoadEgressRules(rulesFile(t, `{"secrets": [
	  {"placeholder": "magi-secret-github", "secret": "github", "hosts": ["api.github.com"], "place": "header:authorization", "personas": ["persona:engineering"], "env": "GH_TOKEN"},
	  {"placeholder": "magi-secret-pypi", "secret": "pypi", "hosts": ["pypi.org"], "place": "header:authorization", "owners": ["user:b@example.local"], "env": "PYPI_TOKEN"},
	  {"placeholder": "magi-secret-x", "secret": "x", "hosts": ["x.example.local"], "place": "any"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.TaskEnv("user:a@example.local", "persona:engineering"); len(got) != 1 || got["GH_TOKEN"] != "magi-secret-github" {
		t.Errorf("engineering: %v", got)
	}
	if got := r.TaskEnv("user:a@example.local", "persona:assistant"); len(got) != 0 {
		t.Errorf("assistant: %v", got)
	}
	if got := r.TaskEnv("user:b@example.local", "persona:assistant"); len(got) != 1 || got["PYPI_TOKEN"] == "" {
		t.Errorf("owner b: %v", got)
	}
	if got := (*EgressRules)(nil).TaskEnv("user:a@example.local", "persona:engineering"); len(got) != 0 {
		t.Errorf("nil rules: %v", got)
	}
	if _, err := LoadEgressRules(rulesFile(t, `{"secrets": [{"placeholder": "magi-secret-a", "secret": "a", "hosts": ["h"], "place": "any", "env": "a b"}]}`)); err == nil {
		t.Error("a bad env name was accepted")
	}
}
