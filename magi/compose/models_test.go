package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/model"
	inferenceapi "github.com/catalystcommunity/magi/primitives/inference/api"
	modelsapi "github.com/catalystcommunity/magi/primitives/models/api"
)

// fakeCatalog has task defaults and gives one candidate for each select:
// the model with the name of the selector, or "default-<task>".
type fakeCatalog struct {
	modelsapi.ModelService
	base     string
	bases    []string // more candidates after the first (decision)
	cal      map[string]float64
	attrs    map[string]string // of the first candidate
	tasks    map[string]modelsapi.Selector
	down     bool
	mu       sync.Mutex
	selects  []modelsapi.SelectRequest
	getTasks int
}

func (f *fakeCatalog) GetTask(_ context.Context, r modelsapi.GetTaskRequest) (modelsapi.TaskDefault, error) {
	f.mu.Lock()
	f.getTasks++
	f.mu.Unlock()
	if f.down {
		return modelsapi.TaskDefault{}, rpc.Unavailable("down")
	}
	s, ok := f.tasks[r.Task]
	if !ok {
		return modelsapi.TaskDefault{}, rpc.NotFound("task %s", r.Task)
	}
	return modelsapi.TaskDefault{Task: r.Task, Selector: s}, nil
}

func (f *fakeCatalog) Select(_ context.Context, r modelsapi.SelectRequest) (modelsapi.SelectResponse, error) {
	f.mu.Lock()
	f.selects = append(f.selects, r)
	f.mu.Unlock()
	name := "default-" + deref(r.Task)
	if r.Selector != nil && r.Selector.Name != nil {
		name = *r.Selector.Name
	}
	m := modelsapi.Model{Name: name, Endpoint: modelsapi.Endpoint{BaseUrl: f.base, Model: name, KeyRef: ptr("inference")}, Enabled: true, Calibration: f.cal, Attributes: f.attrs}
	out := []modelsapi.Candidate{{Model: m, Reason: "test"}}
	for i, b := range f.bases {
		out = append(out, modelsapi.Candidate{Model: modelsapi.Model{Name: fmt.Sprintf("more-%d", i), Endpoint: modelsapi.Endpoint{BaseUrl: b, Model: "m", KeyRef: ptr("inference")}, Enabled: true, Calibration: f.cal}})
	}
	return modelsapi.SelectResponse{Candidates: out}, nil
}

func TestModelRouter(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: "service"}, time.Hour)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The model endpoint answers with the model name of the request, and
	// needs the inference key.
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer inference-key" {
			http.Error(w, `{"error":{"message":"no key"}}`, 401)
			return
		}
		var in struct{ Model string }
		json.NewDecoder(r.Body).Decode(&in)
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"hi from %s"}}]}`, in.Model, in.Model)
	}))
	t.Cleanup(llm.Close)
	cat := &fakeCatalog{base: llm.URL, tasks: map[string]modelsapi.Selector{
		"persona-turn":       {All: []string{"chat"}},
		"persona.archimedes": {All: []string{"tools"}},
		"note-taker":         {All: []string{"notetaker"}},
	}}
	srv := &rpc.Server{Verifier: ver, Log: log}
	srv.Register("ModelService", modelsapi.ModelServiceOps(cat))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)

	static := model.Chain{{BaseURL: llm.URL, Model: "static", APIKey: "inference-key", HTTP: &http.Client{}}}
	r := &ModelRouter{Catalog: modelsapi.NewModelClient(rpc.NewClient(h.URL, nil)), Token: tok,
		Keys: map[string]string{"inference": "inference-key"}, Log: log}
	persona := r.For("persona-turn", static)
	librarian := r.For("librarian", static)
	ctx := context.Background()
	served := func(c model.Chatter, ctx context.Context) string {
		t.Helper()
		m, err := c.Chat(ctx, []model.Message{{Role: "user", Content: "hi"}}, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		return m.Served
	}

	if got := served(persona, ctx); got != "default-persona-turn" {
		t.Errorf("task default: %s", got)
	}
	if got := served(librarian, ctx); got != "static" {
		t.Errorf("no default: %s", got)
	}
	pctx := WithModelTask(ctx, personaTask("persona:archimedes"))
	if got := served(persona, pctx); got != "default-persona.archimedes" {
		t.Errorf("persona default: %s", got)
	}
	// A person names a model for one request: strict.
	octx := modelVars(pctx, map[string]string{"model": `{"name":"glm-5.3"}`, "model_strict": "true"})
	if got := served(persona, octx); got != "glm-5.3" {
		t.Errorf("override: %s", got)
	}
	cat.mu.Lock()
	last := cat.selects[len(cat.selects)-1]
	cat.mu.Unlock()
	if deref(last.Task) != "persona.archimedes" || last.Strict == nil || !*last.Strict || *last.Selector.Name != "glm-5.3" {
		t.Errorf("select request: %+v", last)
	}
	// The note taker in the same turn: its own task, not the override.
	if got := served(librarian, backgroundModel(octx, "note-taker")); got != "default-note-taker" {
		t.Errorf("background: %s", got)
	}
	// A task with no default in the background falls to the task of the
	// model, then to the static chain; not to the persona.
	if got := served(librarian, backgroundModel(octx, "search-judge")); got != "static" {
		t.Errorf("background without default: %s", got)
	}
	// The cache: the same call does not ask the catalog again.
	cat.mu.Lock()
	n := cat.getTasks
	cat.mu.Unlock()
	served(persona, ctx)
	cat.mu.Lock()
	if cat.getTasks != n {
		t.Errorf("cache: %d task reads, want %d", cat.getTasks, n)
	}
	cat.mu.Unlock()
	// A catalog that is down: the static chain.
	r2 := &ModelRouter{Catalog: r.Catalog, Token: tok, Keys: r.Keys, Log: log}
	cat.down = true
	if got := served(r2.For("persona-turn", static), ctx); got != "static" {
		t.Errorf("catalog down: %s", got)
	}
	// A nil static model stays nil (the part is off).
	if r.For("x", nil) != nil {
		t.Error("nil static")
	}
}

// fakeDecider is an inference host with decide.
type fakeDecider struct {
	inferenceapi.InferenceService
	down bool
	yes  float64 // 0 is 0.9
	got  inferenceapi.DecideRequest
}

func (f *fakeDecider) Decide(ctx context.Context, r inferenceapi.DecideRequest) (inferenceapi.DecideResponse, error) {
	if f.down {
		return inferenceapi.DecideResponse{}, rpc.Unavailable("host_busy")
	}
	if p, ok := authn.From(ctx); !ok || !slices.Contains(p.Scopes, "inference:use") {
		return inferenceapi.DecideResponse{}, rpc.Denied("inference:use is required")
	}
	f.got = r
	var out []inferenceapi.Answer
	yes := f.yes
	if yes == 0 {
		yes = 0.9
	}
	for _, q := range r.Questions {
		out = append(out, inferenceapi.Answer{Id: q.Id, Probs: []float64{yes, 1 - yes}, Answer: "yes", Confidence: max(yes, 1-yes)})
	}
	return inferenceapi.DecideResponse{Answers: out, Model: deref(r.Model), Ms: 3}, nil
}

func TestDecision(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok := func(sub string, scopes ...string) string {
		s, _ := iss.Issue(authn.Principal{Subject: sub, Kind: "service", Scopes: scopes}, time.Hour)
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
	busy, ok := &fakeDecider{down: true}, &fakeDecider{}
	busyURL := serve("InferenceService", inferenceapi.InferenceServiceOps(busy))
	okURL := serve("InferenceService", inferenceapi.InferenceServiceOps(ok))
	cat := &fakeCatalog{base: busyURL + "/v1", bases: []string{okURL + "/v1"}, cal: map[string]float64{"record": 1.5},
		tasks: map[string]modelsapi.Selector{"decide": {All: []string{"decide"}}}}
	catURL := serve("ModelService", modelsapi.ModelServiceOps(cat))
	sys := &System{Log: log}
	sys.Router = &ModelRouter{Catalog: modelsapi.NewModelClient(rpc.NewClient(catURL, nil)), Token: tok("service:magi"),
		Keys: map[string]string{"inference": tok("service:magi", "inference:use")}, Log: log}
	qs := []magiapi.DecisionQuestion{{Id: "record", Type: "noul", Question: "Anything to keep?"}, {Id: "asks", Type: "noul", Question: "A question?"}}
	res, err := sys.Decide(context.Background(), DecisionTask, nil, false, "state", qs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "more-0" || len(res.Answers) != 2 || !res.Answers[0].Calibrated || res.Answers[1].Calibrated {
		t.Fatalf("decision: %+v", res)
	}
	if ok.got.Questions[0].Temperature == nil || *ok.got.Questions[0].Temperature != 1.5 || ok.got.Questions[1].Temperature != nil {
		t.Fatalf("temperatures: %+v", ok.got.Questions)
	}
	if _, err := sys.Decide(context.Background(), "no-such-task", nil, false, "state", qs); rpc.CodeOf(err) != 503 {
		t.Fatalf("no model: %v", err)
	}

	// The host question: with no task "decide.host" in the catalog, the
	// models of "decide" answer, and they have no limit of their own.
	hq := magiapi.DecisionQuestion{Id: hostQuestionID, Type: "noul", Question: hostQuestion}
	yes, name, limit, err := sys.hostDecision(context.Background(), "state", hq)
	if err != nil || yes != 0.9 || name != "more-0" || limit != 0 {
		t.Fatalf("host decision with the task decide: %v %s %v %v", yes, name, limit, err)
	}
	// With the task, its first model answers, with the limit of the catalog.
	head := &fakeDecider{yes: 0.96}
	cat.mu.Lock()
	cat.base, cat.attrs = serve("InferenceService", inferenceapi.InferenceServiceOps(head))+"/v1", map[string]string{autoLimitAttribute: "0.95"}
	cat.tasks[HostDecisionTask] = modelsapi.Selector{Any: []string{"host-head", "decide"}}
	cat.mu.Unlock()
	// The router keeps the task of a catalog for some time.
	sys.Router.mu.Lock()
	sys.Router.tasks = nil
	sys.Router.mu.Unlock()
	yes, name, limit, err = sys.hostDecision(context.Background(), "state", hq)
	if err != nil || yes != 0.96 || name != "default-"+HostDecisionTask || limit != 0.95 {
		t.Fatalf("host decision with a head: %v %s %v %v", yes, name, limit, err)
	}
	if len(head.got.Questions) != 1 || head.got.Questions[0].Question != hostQuestion {
		t.Fatalf("the head got %+v", head.got.Questions)
	}
	// The head is down: the next model answers, with no limit of its own.
	head.down = true
	if yes, name, limit, err = sys.hostDecision(context.Background(), "state", hq); err != nil || yes != 0.9 || name != "more-0" || limit != 0 {
		t.Fatalf("fallback: %v %s %v %v", yes, name, limit, err)
	}
	for _, c := range []struct{ setting, model, want float64 }{{0, 0, autoLimit}, {0, 0.95, 0.95}, {0.9, 0.95, 0.9}, {0.9, 0, 0.9}} {
		if got := pickLimit(c.setting, c.model); got != c.want {
			t.Errorf("pickLimit(%v, %v) = %v, want %v", c.setting, c.model, got, c.want)
		}
	}

	// The note gate: off, yes, and no.
	if _, on := sys.noteGate(context.Background(), "Ana", "", "Hi"); on {
		t.Fatal("gate with NoteGate 0")
	}
	sys.Cfg.NoteGate = 0.15
	// A turn has the service token of Magi in its context; the host gets
	// the inference key.
	turn := rpc.WithToken(context.Background(), tok("service:magi"))
	if g, on := sys.noteGate(turn, "Ana", "", "My sister Bea moved to Oslo."); !on || g.Skip || g.Model != "more-0" {
		t.Fatalf("gate yes: %+v %v", g, on)
	}
	if !strings.Contains(ok.got.State, "Message from Ana to the assistant:\nMy sister Bea moved to Oslo.") || ok.got.Questions[0].Question != recordQuestion {
		t.Fatalf("gate request: %+v", ok.got)
	}
	ok.yes = 0.05
	if g, on := sys.noteGate(context.Background(), "Ana", "", "What time is it?"); !on || !g.Skip {
		t.Fatalf("gate no: %+v %v", g, on)
	}
}
