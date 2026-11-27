// Probes of case long2. Target: magi/compose
package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
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
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	inferenceapi "github.com/catalystcommunity/magi/primitives/inference/api"
	modelsapi "github.com/catalystcommunity/magi/primitives/models/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// probeCatalog is a model catalog: the models of each task in their
// sequence. A task that is not in it has no default (404), as in the real
// service.
type probeCatalog struct {
	modelsapi.ModelService
	tasks map[string][]modelsapi.Model
}

func (f *probeCatalog) GetTask(_ context.Context, r modelsapi.GetTaskRequest) (modelsapi.TaskDefault, error) {
	if _, ok := f.tasks[r.Task]; !ok {
		return modelsapi.TaskDefault{}, rpc.NotFound("task %s", r.Task)
	}
	return modelsapi.TaskDefault{Task: r.Task, Selector: modelsapi.Selector{All: []string{r.Task}}}, nil
}

func (f *probeCatalog) Select(_ context.Context, r modelsapi.SelectRequest) (modelsapi.SelectResponse, error) {
	out := modelsapi.SelectResponse{Candidates: []modelsapi.Candidate{}}
	if r.Task != nil {
		for _, m := range f.tasks[*r.Task] {
			out.Candidates = append(out.Candidates, modelsapi.Candidate{Model: m, Reason: "probe"})
		}
	}
	return out, nil
}

// probeDecider is an inference host with decide: one probability of "yes"
// for each question.
type probeDecider struct {
	inferenceapi.InferenceService
	yes  float64
	down bool
	// empty: the host answers with no probability.
	empty bool
	mu    sync.Mutex
	got   []inferenceapi.DecideRequest
}

func (f *probeDecider) Decide(_ context.Context, r inferenceapi.DecideRequest) (inferenceapi.DecideResponse, error) {
	if f.down {
		return inferenceapi.DecideResponse{}, rpc.Unavailable("host_busy: the host is busy")
	}
	f.mu.Lock()
	f.got = append(f.got, r)
	f.mu.Unlock()
	out := inferenceapi.DecideResponse{Answers: []inferenceapi.Answer{}, Model: deref(r.Model), Ms: 3}
	for _, q := range r.Questions {
		a := inferenceapi.Answer{Id: q.Id, Probs: []float64{f.yes, 1 - f.yes}, Answer: "yes", Confidence: f.yes}
		if f.yes < 0.5 {
			a.Answer, a.Confidence = "no", 1-f.yes
		}
		if f.empty {
			a.Probs = []float64{}
		}
		out.Answers = append(out.Answers, a)
	}
	return out, nil
}

func (f *probeDecider) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// probeRun is the workflow service with the run of the environment env_1
// (fakeExecution of the tests of the base).
type probeRun struct{ workflowapi.WorkflowService }

func (probeRun) GetRun(_ context.Context, r workflowapi.GetRunRequest) (workflowapi.Run, error) {
	if r.Id != "run_7" {
		return workflowapi.Run{}, rpc.NotFound("no run %s", r.Id)
	}
	return workflowapi.Run{Id: r.Id, Owner: "user:a@example.local", Vars: map[string]string{"title": "Add a parser"}}, nil
}

// probeEgress is a Magi with the automatic destination decision of the
// model, all services on the real HTTP transport.
type probeEgress struct {
	t      *testing.T
	ask    *magiapi.EgressAskClient
	magi   *magiapi.MagiClient
	proxy  context.Context
	admin  context.Context
	models map[string]*probeDecider
	n      int
}

// probeModel is one model of a task in the catalog of a probe.
type probeModel struct {
	name  string
	yes   float64
	down  bool
	empty bool
	limit string // the attribute egress-auto-limit; "" is no attribute
}

// probeSystem starts Magi with the "auto" setting of the egress rules and
// the models of each task.
func probeSystem(t *testing.T, auto string, tasks map[string][]probeModel) *probeEgress {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	token := func(sub, kind string, scopes ...string) string {
		s, err := iss.Issue(authn.Principal{Subject: sub, Kind: kind, Scopes: scopes}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	serve := func(name string, ops map[string]func(context.Context, []byte) ([]byte, error)) string {
		srv := &rpc.Server{Verifier: ver, Log: log}
		srv.Register(name, ops)
		h := httptest.NewServer(rpc.Mux(srv, nil))
		t.Cleanup(h.Close)
		return h.URL
	}
	p := &probeEgress{t: t, models: map[string]*probeDecider{}}
	cat := &probeCatalog{tasks: map[string][]modelsapi.Model{}}
	for task, ms := range tasks {
		cat.tasks[task] = []modelsapi.Model{}
		for _, m := range ms {
			d := &probeDecider{yes: m.yes, down: m.down, empty: m.empty}
			p.models[m.name] = d
			attrs := map[string]string{"base": "probe"}
			if m.limit != "" {
				attrs["egress-auto-limit"] = m.limit
			}
			cat.tasks[task] = append(cat.tasks[task], modelsapi.Model{Name: m.name, Description: "A model of the probe.",
				Endpoint: modelsapi.Endpoint{BaseUrl: serve("InferenceService", inferenceapi.InferenceServiceOps(d)) + "/v1",
					Model: m.name, KeyRef: ptr("inference")},
				Tags: []string{task}, Attributes: attrs, Scores: map[string]float64{}, Calibration: map[string]float64{}, Enabled: true})
		}
	}
	rulesPath := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(rulesPath, []byte(`{"default": "review", "auto": `+auto+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := LoadEgressRules(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := token("service:magi", authn.KindService, "execution:act-as", "artifacts:act-as")
	sys := New(Config{Execution: serve("ExecutionService", executionapi.ExecutionServiceOps(fakeExecution{})),
		Workflow:    serve("WorkflowService", workflowapi.WorkflowServiceOps(probeRun{})),
		Artifacts:   serve("ArtifactService", artifactsapi.ArtifactServiceOps(&fakeArtifacts{body: map[string][]byte{}})),
		EgressRules: rules, ServiceToken: svc, Admins: []string{"user:admin@example.local"}}, nil, log)
	sys.Router = &ModelRouter{Catalog: modelsapi.NewModelClient(rpc.NewClient(serve("ModelService", modelsapi.ModelServiceOps(cat)), nil)),
		Token: svc, Keys: map[string]string{"inference": token("service:magi", authn.KindService, "inference:use")}, Log: log}
	pop, err := readPopularity(strings.NewReader("1,example.org\n"))
	if err != nil {
		t.Fatal(err)
	}
	sys.Popularity = pop
	srv := &rpc.Server{Verifier: ver, Log: log}
	srv.Register("MagiService", magiapi.MagiServiceOps(&Service{S: sys}))
	srv.Register("EgressAskService", magiapi.EgressAskServiceOps(&Egress{S: sys}))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	p.ask = magiapi.NewEgressAskClient(rpc.NewClient(h.URL, nil))
	p.magi = magiapi.NewMagiClient(rpc.NewClient(h.URL, nil))
	p.proxy = rpc.WithToken(context.Background(), token("service:egress", authn.KindService, ScopeEgress))
	p.admin = rpc.WithToken(context.Background(), token("user:admin@example.local", authn.KindUser))
	return p
}

// allowed asks about a new host of the task environment, as the egress
// proxy does. Each call has its own host: Magi does not ask the model two
// times about one host of one task.
func (p *probeEgress) allowed() (bool, string) {
	p.t.Helper()
	p.n++
	dest := fmt.Sprintf("h%d.example.org:443", p.n)
	r, err := p.ask.Destination(p.proxy, magiapi.EgressDestinationRequest{Environment: "env_1", Destination: dest})
	if err != nil {
		p.t.Fatal(err)
	}
	return r.Allowed, dest
}

// note gives the note of the automatic decision about a destination.
func (p *probeEgress) note(dest string) string {
	p.t.Helper()
	done, err := p.magi.ListEgressReviews(p.admin, magiapi.ListEgressReviewsRequest{State: ptr("decided")})
	if err != nil {
		p.t.Fatal(err)
	}
	for _, v := range done.Reviews {
		if v.Destination == dest {
			return deref(v.Note)
		}
	}
	p.t.Fatalf("no decided review for %s", dest)
	return ""
}

const probeAuto = `{"model": true}`

// The question about a new host goes to the models of the task "decide.host" of the model catalog.
func TestProbeHostQuestionGoesToTheTaskDecideHost(t *testing.T) {
	p := probeSystem(t, probeAuto, map[string][]probeModel{
		"decide.host": {{name: "host-head", yes: 0.99}},
		"decide":      {{name: "adapter", yes: 0.01}},
	})
	ok, _ := p.allowed()
	head, adapter := p.models["host-head"], p.models["adapter"]
	if !ok || head.calls() != 1 || adapter.calls() != 0 {
		t.Fatalf("allowed %v; calls: the model of decide.host %d, the model of decide %d", ok, head.calls(), adapter.calls())
	}
	got := head.got[0]
	if len(got.Questions) != 1 || got.Questions[0].Type != "noul" || got.Questions[0].Question != hostQuestion || got.Questions[0].Id != hostQuestionID {
		t.Errorf("the question of the model: %+v", got.Questions)
	}
	if !strings.Contains(got.State, "Host: h1.example.org\n") || !strings.Contains(got.State, "Task: Add a parser") {
		t.Errorf("the state of the question: %s", got.State)
	}
}

// When the catalog has no task "decide.host", the models of the task "decide" answer, as before.
func TestProbeNoTaskDecideHostUsesTheTaskDecide(t *testing.T) {
	p := probeSystem(t, probeAuto, map[string][]probeModel{"decide": {{name: "adapter", yes: 0.99}}})
	if ok, _ := p.allowed(); !ok || p.models["adapter"].calls() != 1 {
		t.Errorf("allowed %v, calls of the model of decide %d", ok, p.models["adapter"].calls())
	}
}

// When the task "decide.host" has no model, the models of the task "decide" answer.
func TestProbeTaskDecideHostWithNoModelUsesTheTaskDecide(t *testing.T) {
	p := probeSystem(t, probeAuto, map[string][]probeModel{"decide.host": {}, "decide": {{name: "adapter", yes: 0.99}}})
	if ok, _ := p.allowed(); !ok || p.models["adapter"].calls() != 1 {
		t.Errorf("allowed %v, calls of the model of decide %d", ok, p.models["adapter"].calls())
	}
}

// Magi asks the models of the task in their sequence. The first model that answers decides.
func TestProbeTheNextModelOfTheTaskAnswers(t *testing.T) {
	p := probeSystem(t, probeAuto, map[string][]probeModel{
		"decide.host": {{name: "host-head", down: true}, {name: "host-second", yes: 0.99}, {name: "host-third", yes: 0.01}},
		"decide":      {{name: "adapter", yes: 0.01}},
	})
	ok, dest := p.allowed()
	if !ok || p.models["host-second"].calls() != 1 || p.models["host-third"].calls() != 0 || p.models["adapter"].calls() != 0 {
		t.Fatalf("allowed %v; calls: second %d, third %d, the model of decide %d", ok, p.models["host-second"].calls(),
			p.models["host-third"].calls(), p.models["adapter"].calls())
	}
	if n := p.note(dest); !strings.Contains(n, "the decision model host-second gives 0.990") {
		t.Errorf("the note of the decision: %s", n)
	}
}

// A model that fails, or that gives no probability, does not answer.
func TestProbeAModelWithNoProbabilityDoesNotAnswer(t *testing.T) {
	p := probeSystem(t, probeAuto, map[string][]probeModel{
		"decide.host": {{name: "host-head", yes: 0.99, empty: true}, {name: "host-second", yes: 0.99}},
	})
	ok, dest := p.allowed()
	if !ok || p.models["host-head"].calls() != 1 || p.models["host-second"].calls() != 1 {
		t.Fatalf("allowed %v; calls: first %d, second %d", ok, p.models["host-head"].calls(), p.models["host-second"].calls())
	}
	if n := p.note(dest); !strings.Contains(n, "the decision model host-second gives 0.990") {
		t.Errorf("the note of the decision: %s", n)
	}
}

// With no limit in the setting, the limit is the attribute "egress-auto-limit" of the model that answered.
func TestProbeTheLimitOfTheModel(t *testing.T) {
	p := probeSystem(t, probeAuto, map[string][]probeModel{"decide.host": {{name: "host-head", yes: 0.96, limit: "0.95"}}})
	ok, dest := p.allowed()
	if !ok {
		t.Fatal("0.96 from a model with the limit 0.95 did not permit the host")
	}
	if n := p.note(dest); !strings.Contains(n, "the decision model host-head gives 0.960 that the host is a known public source (limit 0.95)") {
		t.Errorf("the note of the decision: %s", n)
	}
	low := probeSystem(t, probeAuto, map[string][]probeModel{"decide.host": {{name: "host-head", yes: 0.94, limit: "0.95"}}})
	if ok, _ := low.allowed(); ok || low.models["host-head"].calls() != 1 {
		t.Error("0.94 from a model with the limit 0.95 permitted the host")
	}
}

// A model with no such attribute has the limit 0.97.
func TestProbeTheDefaultLimit(t *testing.T) {
	low := probeSystem(t, probeAuto, map[string][]probeModel{"decide.host": {{name: "host-head", yes: 0.96}}})
	if ok, _ := low.allowed(); ok || low.models["host-head"].calls() != 1 {
		t.Errorf("0.96 from a model with no limit: allowed %v, calls %d", ok, low.models["host-head"].calls())
	}
	p := probeSystem(t, probeAuto, map[string][]probeModel{"decide.host": {{name: "host-head", yes: 0.97}}})
	ok, dest := p.allowed()
	if !ok {
		t.Fatal("0.97 from a model with no limit did not permit the host")
	}
	if n := p.note(dest); !strings.Contains(n, "(limit 0.97)") {
		t.Errorf("the note of the decision: %s", n)
	}
}

// The limit of the "auto" setting comes first.
func TestProbeTheLimitOfTheSettingComesFirst(t *testing.T) {
	high := probeSystem(t, `{"model": true, "limit": 0.99}`, map[string][]probeModel{"decide.host": {{name: "host-head", yes: 0.96, limit: "0.95"}}})
	if ok, _ := high.allowed(); ok || high.models["host-head"].calls() != 1 {
		t.Errorf("the setting 0.99 and a model with the limit 0.95: 0.96 gave allowed %v, calls %d", ok, high.models["host-head"].calls())
	}
	low := probeSystem(t, `{"model": true, "limit": 0.9}`, map[string][]probeModel{"decide.host": {{name: "host-head", yes: 0.91, limit: "0.95"}}})
	ok, dest := low.allowed()
	if !ok {
		t.Fatal("the setting 0.9 and a model with the limit 0.95: 0.91 did not permit the host")
	}
	if n := low.note(dest); !strings.Contains(n, "(limit 0.90)") {
		t.Errorf("the note of the decision: %s", n)
	}
}

// The limit is of the model that answered, not of a model that did not answer.
func TestProbeTheLimitIsOfTheModelThatAnswered(t *testing.T) {
	p := probeSystem(t, probeAuto, map[string][]probeModel{
		"decide.host": {{name: "host-head", down: true, limit: "0.95"}, {name: "adapter", yes: 0.96}},
	})
	if ok, _ := p.allowed(); ok || p.models["adapter"].calls() != 1 {
		t.Errorf("0.96 from a model with no limit, after a model with the limit 0.95 that did not answer: allowed %v, calls %d",
			ok, p.models["adapter"].calls())
	}
	q := probeSystem(t, probeAuto, map[string][]probeModel{
		"decide.host": {{name: "host-head", down: true}, {name: "second-head", yes: 0.96, limit: "0.95"}},
	})
	if ok, _ := q.allowed(); !ok {
		t.Error("0.96 from a model with the limit 0.95, after a model with no limit that did not answer, did not permit the host")
	}
}

// An attribute value that is not a number above 0 and at most 1 counts as no attribute.
func TestProbeABadLimitAttribute(t *testing.T) {
	for _, c := range []struct {
		limit string
		yes   float64
		want  bool
	}{{"high", 0.96, false}, {"high", 0.98, true}, {"1.5", 0.98, true}, {"0", 0.5, false}, {"-0.2", 0.5, false}, {"0", 0.98, true}} {
		p := probeSystem(t, probeAuto, map[string][]probeModel{"decide.host": {{name: "host-head", yes: c.yes, limit: c.limit}}})
		if ok, _ := p.allowed(); ok != c.want || p.models["host-head"].calls() != 1 {
			t.Errorf("the attribute %q and the probability %v: allowed %v, want %v (calls %d)", c.limit, c.yes, ok, c.want, p.models["host-head"].calls())
		}
	}
}
