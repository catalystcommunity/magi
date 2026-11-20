package compose

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/magi/model"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// testDispatch is the dispatch of work that owner starts through persona
// with no egress rules.
func testDispatch(owner, persona string) string {
	return (&System{}).newDispatch(owner, persona, "", "").encode()
}

const grantRules = `{"secrets": [
  {"placeholder": "magi-secret-github", "secret": "gh", "hosts": ["api.example.local"], "place": "header:authorization", "personas": ["persona:engineering"], "env": "GH_TOKEN"}
], "rules": [], "default": "block"}`

const twoGrantRules = `{"secrets": [
  {"placeholder": "magi-secret-github", "secret": "gh", "hosts": ["api.example.local"], "place": "header:authorization", "personas": ["persona:engineering"], "env": "GH_TOKEN"},
  {"placeholder": "magi-secret-pypi", "secret": "pypi", "hosts": ["pypi.example.local"], "place": "header:authorization", "env": "PYPI_TOKEN"}
], "rules": [], "default": "block"}`

func loadRules(t *testing.T, text string) *EgressRules {
	t.Helper()
	r, err := LoadEgressRules(rulesFile(t, text))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRestoreNarrowsTheDispatch(t *testing.T) {
	const owner = "user:a@example.local"
	s := &System{EgressRules: loadRules(t, grantRules)}
	d := s.newDispatch(owner, Engineering, "str_1", "utt_1")
	if !slices.Equal(d.Grants, []string{"magi-secret-github"}) || !slices.Contains(d.Tools, "start_code_change") {
		t.Fatalf("dispatch %+v", d)
	}
	vars := map[string]string{dispatchVar: d.encode(), "persona": Engineering}

	if _, err := s.restore(owner, map[string]string{}); !errors.Is(err, errNoDispatch) {
		t.Fatalf("no dispatch: %v", err)
	}
	if _, err := s.restore(owner, map[string]string{dispatchVar: "{"}); err == nil {
		t.Fatal("a dispatch that cannot be read must not run")
	}
	if _, err := s.restore("user:b@example.local", vars); err == nil {
		t.Fatal("the dispatch of a different principal must not run")
	}
	if _, err := s.restore(owner, map[string]string{dispatchVar: d.encode(), "persona": Assistant}); err == nil {
		t.Fatal("the dispatch of a different persona must not run")
	}
	au, err := s.restore(owner, vars)
	if err != nil || !au.has("magi-secret-github") || au.Principal != owner {
		t.Fatalf("restore: %+v %v", au, err)
	}

	// A tool that the persona does not have now is gone. A tool that the
	// dispatch did not have does not come.
	d2 := d
	d2.Tools = []string{"start_code_change", "launch_rocket"}
	au, err = s.restore(owner, map[string]string{dispatchVar: d2.encode()})
	if err != nil || !slices.Equal(au.Tools, []string{"start_code_change"}) {
		t.Fatalf("tools: %+v %v", au.Tools, err)
	}

	// The policy gives one more grant now: work that started before does
	// not get it.
	s.EgressRules = loadRules(t, twoGrantRules)
	au, err = s.restore(owner, vars)
	if err != nil || au.has("magi-secret-pypi") || !au.has("magi-secret-github") {
		t.Fatalf("a later grant: %+v %v", au.Grants, err)
	}
	if env := s.taskEnv(owner, vars, map[string]string{}); len(env) != 1 || env["GH_TOKEN"] != "magi-secret-github" {
		t.Fatalf("task env: %v", env)
	}

	// The grant is revoked: the work does not continue.
	s.EgressRules = loadRules(t, `{"secrets": [], "rules": [], "default": "block"}`)
	if _, err := s.restore(owner, vars); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked grant: %v", err)
	}
	if env := s.taskEnv(owner, vars, map[string]string{}); len(env) != 0 {
		t.Fatalf("task env after the grant was revoked: %v", env)
	}
	// The policy cannot be read.
	s.EgressRules = nil
	if _, err := s.restore(owner, vars); err == nil {
		t.Fatal("grants with no policy must not run")
	}
}

// stopWorkflow records the runs that Magi cancels.
type stopWorkflow struct {
	workflowapi.WorkflowService
	mu        sync.Mutex
	cancelled []workflowapi.CancelRequest
	completes int
	runs      map[string]workflowapi.Run
}

func (w *stopWorkflow) Cancel(_ context.Context, r workflowapi.CancelRequest) (workflowapi.Run, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cancelled = append(w.cancelled, r)
	run := w.runs[r.Id]
	run.Id, run.Status = r.Id, "cancelled"
	return run, nil
}

func (w *stopWorkflow) Complete(context.Context, workflowapi.CompleteRequest) (workflowapi.Run, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.completes++
	return workflowapi.Run{}, nil
}

func (w *stopWorkflow) Heartbeat(context.Context, workflowapi.HeartbeatRequest) (workflowapi.Ack, error) {
	return workflowapi.Ack{Ok: true}, nil
}

func (w *stopWorkflow) GetRun(_ context.Context, r workflowapi.GetRunRequest) (workflowapi.Run, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, ok := w.runs[r.Id]
	if !ok {
		return run, rpc.NotFound("no run %s", r.Id)
	}
	return run, nil
}

// noEnvironments is an execution service with no environment: the
// environment of a flow is then the sandbox of a runner, by its run.
type noEnvironments struct{ executionapi.ExecutionService }

func (noEnvironments) Get(_ context.Context, r executionapi.GetRequest) (executionapi.Environment, error) {
	return executionapi.Environment{}, rpc.NotFound("no environment %s", r.Id)
}

func stopEnv(t *testing.T, cfg Config) (*System, *stopWorkflow) {
	wf := &stopWorkflow{runs: map[string]workflowapi.Run{}}
	sys, _, _ := adminEnv(t, cfg, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"WorkflowService":  safeOps(workflowapi.WorkflowServiceOps(wf)),
		"ExecutionService": safeOps(executionapi.ExecutionServiceOps(noEnvironments{})),
	})
	return sys, wf
}

// Work with no dispatch does not run, and there is no fallback identity
// (docs/decisions/0007).
func TestWorkWithNoDispatchDoesNotRun(t *testing.T) {
	sys, wf := stopEnv(t, Config{})
	ran := false
	h := func(context.Context, workflowapi.Assignment, func(string)) (Result, error) {
		ran = true
		return Result{Outcome: "done"}, nil
	}
	for _, vars := range []map[string]string{
		{"persona": Engineering},
		{"persona": Engineering, dispatchVar: testDispatch("user:b@example.local", Engineering)},
	} {
		sys.run(sys.AsService(context.Background()), workflowapi.Assignment{Run: "run_1", Step: "work", Claim: "c1", Owner: turnOwner, Vars: vars}, h)
	}
	if ran {
		t.Fatal("work with no dispatch of its owner ran")
	}
	if len(wf.cancelled) != 2 || wf.completes != 0 || !strings.Contains(deref(wf.cancelled[0].Reason), "no dispatch") {
		t.Fatalf("cancelled %+v, completes %d", wf.cancelled, wf.completes)
	}
}

// A grant is revoked while a task runs: the next step of the task is
// refused, for a worker of Magi and for a runner (docs/decisions/0007).
func TestRevokedGrantStopsTheNextStep(t *testing.T) {
	sys, wf := stopEnv(t, Config{EgressRules: loadRules(t, grantRules)})
	vars := map[string]string{"persona": Engineering, "title": "Fix it",
		dispatchVar: sys.newDispatch(turnOwner, Engineering, "", "").encode()}
	a := workflowapi.Assignment{Run: "run_1", Step: "check", Claim: "c1", Owner: turnOwner, Vars: vars}
	steps := 0
	h := func(context.Context, workflowapi.Assignment, func(string)) (Result, error) {
		steps++
		return Result{Outcome: "done"}, nil
	}
	sctx := sys.AsService(context.Background())
	sys.run(sctx, a, h)
	if steps != 1 || len(wf.cancelled) != 0 || wf.completes != 1 {
		t.Fatalf("with the grant: steps %d, cancelled %d, completes %d", steps, len(wf.cancelled), wf.completes)
	}

	sys.EgressRules = loadRules(t, `{"secrets": [], "rules": [], "default": "block"}`)
	a.Step, a.Claim = "pr-merge", "c2"
	sys.run(sctx, a, h)
	if steps != 1 {
		t.Fatal("the step ran after the grant was revoked")
	}
	if len(wf.cancelled) != 1 || !strings.Contains(deref(wf.cancelled[0].Reason), "revoked") {
		t.Fatalf("cancelled %+v", wf.cancelled)
	}
	// The check of a runner claim is the same function.
	if sys.permitted(sctx, a) || len(wf.cancelled) != 2 {
		t.Fatal("a runner got a step after the grant was revoked")
	}
}

// The egress proxy puts in a secret only for the flow of a task whose
// dispatch has the grant.
func TestSecretRulesFollowTheDispatch(t *testing.T) {
	sys, wf := stopEnv(t, Config{EgressRules: loadRules(t, grantRules), EgressProxy: &EgressProxy{Addr: "egress:3128", Key: []byte("0123456789abcdef0123456789abcdef")}})
	with := sys.newDispatch(turnOwner, Engineering, "", "").encode()
	wf.runs["run_with"] = workflowapi.Run{Id: "run_with", Owner: turnOwner, Vars: map[string]string{"persona": Engineering, dispatchVar: with}}
	// This task started when no rule gave the grant.
	wf.runs["run_before"] = workflowapi.Run{Id: "run_before", Owner: turnOwner, Vars: map[string]string{"persona": Engineering, dispatchVar: testDispatch(turnOwner, Engineering)}}
	wf.runs["run_none"] = workflowapi.Run{Id: "run_none", Owner: turnOwner, Vars: map[string]string{"persona": Engineering}}
	e := &Egress{S: sys}
	rules := func(run string) int {
		f := flowOf(turnOwner, "GET", "https://api.example.local/x")
		f.Environment = run
		return len(e.secretRules(context.Background(), f))
	}
	if n := rules("run_with"); n != 1 {
		t.Fatalf("a task with the grant got %d secret rules", n)
	}
	if n := rules("run_before"); n != 0 {
		t.Fatalf("a task that started before the grant got %d secret rules", n)
	}
	if n := rules("run_none"); n != 0 {
		t.Fatalf("a task with no dispatch got %d secret rules", n)
	}
}

// A turn has only the tools of its dispatch, and the work that it starts
// gets a dispatch for the same principal.
func TestTurnUsesAndPassesItsDispatch(t *testing.T) {
	m := &scriptModel{}
	m.reply = func(n int, _ []model.Message) (model.Message, error) {
		if n == 1 {
			return toolCall("add_todo", addMilk), nil
		}
		return model.Message{Role: "assistant", Content: "Done."}, nil
	}
	sys, w, a := turnEnv(t, "Put milk on my list.", m)
	if _, err := run(sys, context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if w.todoStarts() != 1 {
		t.Fatalf("to-do starts: %d", w.todoStarts())
	}
	var got string
	for _, st := range w.starts {
		if st.Definition == Todo {
			got = st.Vars[dispatchVar]
		}
	}
	au, err := sys.restore(turnOwner, map[string]string{dispatchVar: got})
	if err != nil || au.Persona != Assistant || au.Cause != "utt_1" || au.Stream != "str_1" {
		t.Fatalf("dispatch of the to-do: %q %+v %v", got, au, err)
	}

	// The dispatch of this turn does not have add_todo.
	m2 := &scriptModel{reply: m.reply}
	sys, w, a = turnEnv(t, "Put milk on my list.", m2)
	d := sys.newDispatch(turnOwner, Assistant, "str_1", "utt_1")
	d.Tools = slices.DeleteFunc(d.Tools, func(n string) bool { return n == "add_todo" })
	a.Vars[dispatchVar] = d.encode()
	if _, err := run(sys, context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if w.todoStarts() != 0 {
		t.Fatal("a turn used a tool that its dispatch does not have")
	}
}
