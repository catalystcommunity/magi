package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// startWorkflow records the start requests that it gets.
type startWorkflow struct {
	workflowapi.WorkflowService
	mu    sync.Mutex
	start []workflowapi.StartRequest
}

func (f *startWorkflow) Start(_ context.Context, r workflowapi.StartRequest) (workflowapi.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.start = append(f.start, r)
	return workflowapi.Run{Id: "run_1", Definition: CodeChange, Vars: r.Vars}, nil
}

func (f *startWorkflow) last() workflowapi.StartRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.start[len(f.start)-1]
}

// startSystem starts a System with the lesson entities in the knowledge
// service and a workflow service that records starts, both on the real HTTP
// transport. The repository of Magi is "magi".
func startSystem(t *testing.T, ents ...knowledgeapi.Entity) (*System, *startWorkflow) {
	t.Helper()
	wf := &startWorkflow{}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	tok, err := iss.Issue(authn.Principal{Subject: lessonOwner, Kind: authn.KindService,
		Scopes: []string{"knowledge:act-as", "workflow:admin"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// One server holds both services, so one token reaches each.
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("KnowledgeService", knowledgeapi.KnowledgeServiceOps(&fakeKnowledge{ents: ents}))
	srv.Register("WorkflowService", workflowapi.WorkflowServiceOps(wf))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	var buf strings.Builder
	s := &System{Log: slog.New(slog.NewJSONHandler(&buf, nil)), Cfg: Config{ServiceToken: tok, Repo: "magi"},
		Knowledge: knowledgeapi.NewKnowledgeClient(rpc.NewClient(h.URL, rpc.StaticToken(tok))),
		Workflow:  workflowapi.NewWorkflowClient(rpc.NewClient(h.URL, rpc.StaticToken(tok)))}
	return s, wf
}

// startCodeChange puts the lessons that relate to the task in the var
// "lessons" of the run.
func TestStartCodeChangeSetsLessons(t *testing.T) {
	s, wf := startSystem(t,
		lessonEntity("rl-0001", "Keep the state of one run in the run method", "primitives/apprentice", "Set the flags at the start of the run method."),
		lessonEntity("rl-0002", "No helper for one expression", "all code", "Write the expression where the code uses it."))
	if _, err := s.startCodeChange(context.Background(), "user:a@example.local", origin{}, "Fix the worker",
		"Change primitives/apprentice so that the worker keeps its state in one place.", "magi", ""); err != nil {
		t.Fatal(err)
	}
	vars := wf.last().Vars
	want := "- rl-0001 Keep the state of one run in the run method: Set the flags at the start of the run method.\n" +
		"- rl-0002 No helper for one expression: Write the expression where the code uses it."
	if vars["lessons"] != want {
		t.Fatalf("lessons = %q, want\n%s", vars["lessons"], want)
	}
	if vars["repo"] != "magi" || vars["title"] != "Fix the worker" {
		t.Fatalf("vars = %v", vars)
	}
}

// Without a lesson that relates to the task, the var stays out.
func TestStartCodeChangeWithoutLessons(t *testing.T) {
	s, wf := startSystem(t, lessonEntity("rl-0001", "A diff for review shows new files", "primitives/speech", "Run git add first."))
	if _, err := s.startCodeChange(context.Background(), "user:a@example.local", origin{}, "A title",
		"An unrelated task.", "magi", ""); err != nil {
		t.Fatal(err)
	}
	if v, ok := wf.last().Vars["lessons"]; ok {
		t.Fatalf("lessons = %q, want none", v)
	}
}

// The lessons are cut at 6000 characters, so that the first message of the
// apprentice stays small.
func TestStartCodeChangeCutsLongLessons(t *testing.T) {
	s, wf := startSystem(t,
		lessonEntity("rl-0001", strings.Repeat("a", 3000), "all code", strings.Repeat("b", 3000)),
		lessonEntity("rl-0002", strings.Repeat("c", 3000), "all code", strings.Repeat("d", 3000)))
	if _, err := s.startCodeChange(context.Background(), "user:a@example.local", origin{}, "A title",
		"An unrelated task.", "magi", ""); err != nil {
		t.Fatal(err)
	}
	if got := len(wf.last().Vars["lessons"]); got != 6000 {
		t.Fatalf("lessons = %d characters, want 6000", got)
	}
}
