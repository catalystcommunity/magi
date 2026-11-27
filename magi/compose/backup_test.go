package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	modelsapi "github.com/catalystcommunity/magi/primitives/models/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// backupEnv is a system whose model catalog has the model of the work
// task ("usual") and, with a name in backup, a default for the backup task.
func backupEnv(t *testing.T, backup string) (*System, *turnWorld) {
	t.Helper()
	sys, w, _ := eventEnv(t, nil)
	cat := &proxyCatalog{bases: map[string]string{"usual": "http://usual.invalid/v1", "careful": "http://careful.invalid/v1"},
		tasks:  map[string]modelsapi.Selector{"step." + CodeChange + ".work": {Name: ptr("usual")}},
		byTask: map[string][]string{"step." + CodeChange + ".work": {"usual"}}}
	if backup != "" {
		cat.tasks[BackupTask] = modelsapi.Selector{Name: ptr(backup)}
		cat.byTask[BackupTask] = []string{backup}
	}
	sys.Router = catalogRouter(t, cat)
	return sys, w
}

// catalogRouter gives a model router on a catalog that answers through the
// real HTTP transport.
func catalogRouter(t *testing.T, cat *proxyCatalog) *ModelRouter {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, Log: quiet}
	srv.Register("ModelService", modelsapi.ModelServiceOps(cat))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	tok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService}, time.Hour)
	return &ModelRouter{Catalog: modelsapi.NewModelClient(rpc.NewClient(h.URL, rpc.StaticToken(tok))), Token: tok,
		Keys: map[string]string{"inference": "k"}, Log: quiet}
}

// backupStep runs the backup step of a task with the vars of the steps
// before it, and gives the result and the texts that the person got.
func backupStep(t *testing.T, sys *System, w *turnWorld, vars map[string]string) (Result, string) {
	t.Helper()
	a := taskStep(sys, "run_b", 0)
	a.Step = "backup"
	for k, v := range vars {
		a.Vars[k] = v
	}
	w.mu.Lock()
	before := len(w.utts)
	w.mu.Unlock()
	res, err := sys.backup(sys.AsService(context.Background()), a, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var told []string
	for _, u := range w.utts[before:] {
		told = append(told, u.Text)
	}
	return res, strings.Join(told, "\n")
}

var gaveUp = map[string]string{"task.result": "gave-up", "check.attempts": "3", "check.output": "sha256:out", "check_output": "FAIL TestX"}

// The checks failed three times with the usual model: one work round with
// the backup model starts, with new check attempts, and the task does not
// stop.
func TestBackupRoundAfterGiveUp(t *testing.T) {
	sys, w := backupEnv(t, "careful")
	res, told := backupStep(t, sys, w, gaveUp)
	if res.Outcome != "work" {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	o := res.Outputs
	if o["model"] != `{"name":"careful","review_model":"careful"}` || o["backup.model"] != "careful" {
		t.Fatalf("model of the round: %v", o)
	}
	if v, ok := o["check.attempts"]; !ok || v != "0" {
		t.Fatalf("the backup model did not get its own check attempts: %v", o)
	}
	if v, ok := o["task.result"]; !ok || v != "" {
		t.Fatalf("the result of the task still says that it stopped: %v", o)
	}
	// The output of the failed check stays for the new round.
	if _, ok := o["check_output"]; ok {
		t.Fatalf("the step changed the check output: %v", o)
	}
	if !strings.Contains(told, "backup model careful") || !strings.Contains(told, "The checks failed 3 times") || strings.Contains(told, "I stopped the task") {
		t.Fatalf("told = %q", told)
	}
}

// A run gets one backup round. After it, the task stops, and the person
// gets the notice that the check step did not send.
func TestBackupRoundOnlyOnce(t *testing.T) {
	sys, w := backupEnv(t, "careful")
	vars := map[string]string{"backup.model": "careful", "model": `{"name":"careful","review_model":"careful"}`}
	for k, v := range gaveUp {
		vars[k] = v
	}
	res, told := backupStep(t, sys, w, vars)
	if res.Outcome != "give-up" || len(res.Outputs) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(told, "The checks failed 3 times. I stopped the task. The last output is in artifact sha256:out.") {
		t.Fatalf("told = %q", told)
	}
}

// With no default for the backup task, the task stops as before.
func TestNoBackupModel(t *testing.T) {
	sys, w := backupEnv(t, "")
	res, told := backupStep(t, sys, w, gaveUp)
	if res.Outcome != "give-up" || !strings.Contains(told, "I stopped the task") {
		t.Fatalf("result = %+v, told = %q", res, told)
	}
	// The same model again is no backup.
	sys, w = backupEnv(t, "usual")
	if res, _ := backupStep(t, sys, w, gaveUp); res.Outcome != "give-up" {
		t.Fatalf("backup model = work model: %+v", res)
	}
	// No router: an installation with no model catalog.
	sys.Router = nil
	if res, _ := backupStep(t, sys, w, gaveUp); res.Outcome != "give-up" {
		t.Fatalf("no router: %+v", res)
	}
}

// A guard stopped the usual model (a loop, a stall): the backup model gets
// the work. A gone endpoint and a question to a person go to the person.
func TestBackupRoundAfterAGuardStop(t *testing.T) {
	sys, w := backupEnv(t, "careful")
	for _, status := range []string{"loop", "no_progress", "stalled", "supervisor_stop"} {
		res, told := backupStep(t, sys, w, map[string]string{"apprentice.blocked": "true", "apprentice.status": status, "apprentice.steps": "71"})
		if res.Outcome != "work" || res.Outputs["backup.model"] != "careful" || !strings.Contains(told, "("+status+")") || strings.Contains(told, "Resume it") {
			t.Fatalf("%s: result = %+v, told = %q", status, res, told)
		}
		if v, ok := res.Outputs["apprentice.blocked"]; !ok || v != "" {
			t.Fatalf("%s: the round is still a blocked round: %v", status, res.Outputs)
		}
	}
	for _, status := range []string{"blocked_provider", "model_unavailable", "escalated", "preflight"} {
		res, told := backupStep(t, sys, w, map[string]string{"apprentice.blocked": "true", "apprentice.status": status, "apprentice.steps": "4"})
		want := "The apprentice stopped (" + status + ") after 4 steps. The work stays in the environment. Resume it, run the checks on it, or reject it."
		if res.Outcome != "blocked" || len(res.Outputs) != 0 || !strings.Contains(told, want) {
			t.Fatalf("%s: result = %+v, told = %q", status, res, told)
		}
	}
}

// A loop or a stall left a change, and the checks failed on it (the outcome
// "guard-stop" of the check step).
func TestBackupRoundAfterAFailedCheckOfAGuardStop(t *testing.T) {
	vars := map[string]string{"apprentice.blocked": "true", "apprentice.guard_stop": "true", "apprentice.status": "loop", "check.attempts": "1"}
	sys, w := backupEnv(t, "careful")
	res, _ := backupStep(t, sys, w, vars)
	if v, ok := res.Outputs["apprentice.guard_stop"]; res.Outcome != "work" || !ok || v != "" {
		t.Fatalf("result = %+v", res)
	}
	sys, w = backupEnv(t, "")
	res, told := backupStep(t, sys, w, vars)
	if res.Outcome != "blocked" || !strings.Contains(told, "the checks failed on the change that it left") {
		t.Fatalf("no backup model: result = %+v, told = %q", res, told)
	}
}

// An operator named the model of the run, or the run measures a model (an
// eval run with a base): no different model does its work.
func TestNoBackupRoundForANamedModelOrAnEvalRun(t *testing.T) {
	sys, w := backupEnv(t, "careful")
	for _, more := range []map[string]string{{"model": `{"name":"usual"}`}, {"base": "1a2b3c4"}} {
		vars := map[string]string{}
		for k, v := range gaveUp {
			vars[k] = v
		}
		for k, v := range more {
			vars[k] = v
		}
		if res, _ := backupStep(t, sys, w, vars); res.Outcome != "give-up" {
			t.Fatalf("%v: result = %+v", more, res)
		}
	}
}

// The way of a run through the workflow service: the usual model stops in
// a loop, the backup step starts a round with the backup model, the checks
// of that round fail three times, and then the task stops.
func TestBackupRoundInTheWorkflow(t *testing.T) {
	s := turnSystem(t)
	runner := fmt.Sprintf("bk%d", time.Now().UnixNano())
	s.Router = catalogRouter(t, &proxyCatalog{bases: map[string]string{"usual": "http://usual.invalid/v1", "careful": "http://careful.invalid/v1"},
		tasks:  map[string]modelsapi.Selector{"step." + CodeChange + ".work": {Name: ptr("usual")}, BackupTask: {Name: ptr("careful")}},
		byTask: map[string][]string{"step." + CodeChange + ".work": {"usual"}, BackupTask: {"careful"}}})
	ctx := s.AsService(context.Background())
	person := fmt.Sprintf("user:bk-%d@example.local", time.Now().UnixNano())
	st, err := s.Conversation.OpenStream(ctx, conversationapi.OpenStreamRequest{Persona: Engineering, Person: &person})
	if err != nil {
		t.Fatal(err)
	}
	// The task has no stream: a person started it with the CLI. The notices
	// still go to the stream of the person with the persona.
	run, err := s.Workflow.Start(ctx, workflowapi.StartRequest{Definition: CodeChange, Owner: &person, Vars: map[string]string{
		"title": "Fix the worker", "instructions": "Fix it.", "runner": runner, "publisher": runner, "persona": Engineering}})
	if err != nil {
		t.Fatal(err)
	}
	// backupNow claims the backup step and runs it as the worker of Magi does.
	backupNow := func() string {
		t.Helper()
		res, err := s.Workflow.Claim(ctx, workflowapi.ClaimRequest{Queue: "magi.backup", Worker: "test", WaitMs: ptr(uint64(5000)), LeaseMs: ptr(uint64(60000))})
		if err != nil || res.Assignment == nil || res.Assignment.Run != run.Id {
			t.Fatalf("claim the backup step: %+v, %v", res.Assignment, err)
		}
		a := *res.Assignment
		out, err := s.backup(ctx, a, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Workflow.Complete(ctx, workflowapi.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: out.Outcome, Outputs: out.Outputs}); err != nil {
			t.Fatal(err)
		}
		return out.Outcome
	}
	q := func(step string) string { return "runner." + runner + "." + step }
	invStep(t, s, q("prepare"), "done", map[string]string{"environment": run.Id, "branch": "task/" + run.Id})
	invStep(t, s, q("work"), "blocked", map[string]string{"apprentice.status": "loop", "apprentice.blocked": "true", "apprentice.steps": "71"})
	if got := backupNow(); got != "work" {
		t.Fatalf("backup step after a loop = %s", got)
	}
	// The next work round is for the backup model, with its own reviews.
	res, err := s.Workflow.Claim(ctx, workflowapi.ClaimRequest{Queue: q("work"), Worker: "test", WaitMs: ptr(uint64(5000)), LeaseMs: ptr(uint64(60000))})
	if err != nil || res.Assignment == nil {
		t.Fatalf("claim the backup work round: %v", err)
	}
	a := *res.Assignment
	if a.Vars["model"] != `{"name":"careful","review_model":"careful"}` || a.Vars["backup.model"] != "careful" || a.Vars["apprentice.blocked"] != "" {
		t.Fatalf("vars of the backup round: model %q, backup.model %q, blocked %q", a.Vars["model"], a.Vars["backup.model"], a.Vars["apprentice.blocked"])
	}
	if _, err := s.Workflow.Complete(ctx, workflowapi.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "done"}); err != nil {
		t.Fatal(err)
	}
	invStep(t, s, q("check"), "give-up", map[string]string{"task.result": "gave-up", "check.attempts": "3", "check.output": "sha256:out"})
	if got := backupNow(); got != "give-up" {
		t.Fatalf("backup step after the backup round = %s", got)
	}
	invStep(t, s, q("cleanup"), "done", map[string]string{"task.result": "gave-up"})
	got, err := s.Workflow.GetRun(ctx, workflowapi.GetRunRequest{Id: run.Id})
	if err != nil || got.Status != "succeeded" || got.Vars["task.result"] != "gave-up" {
		t.Fatalf("run = %s %v, %v", got.Status, got.Vars["task.result"], err)
	}
	// The person got one message for the backup round, and one notice when
	// the task stopped.
	us, err := s.Conversation.ListUtterances(ctx, conversationapi.ListUtterancesRequest{Stream: st.Id})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, u := range us.Utterances {
		texts = append(texts, u.Text)
	}
	all := strings.Join(texts, "\n")
	if strings.Count(all, "backup model careful") != 1 || strings.Count(all, "I stopped the task") != 1 || strings.Contains(all, "Task blocked") {
		t.Fatalf("messages to the person: %q", texts)
	}
}
