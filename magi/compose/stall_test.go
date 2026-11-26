package compose

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

func (f *eventWorkflow) Cancel(_ context.Context, r workflowapi.CancelRequest) (workflowapi.Run, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	run := f.byID[r.Id]
	run.Status, run.CurrentStep = "cancelled", ""
	run.Error = r.Reason
	return *run, nil
}

// working puts a task of the engineering persona in the fake workflow,
// with an apprentice that wrote the status lines.
func working(sys *System, wf *eventWorkflow, id string, at time.Time, lines ...string) *workflowapi.Run {
	a := taskStep(sys, id, 0)
	st := workflowapi.StepState{Step: "work", Attempt: 1, Status: "claimed", StartedMs: uint64(at.Add(-time.Hour).UnixMilli()), Progress: []workflowapi.Progress{}}
	for i, l := range lines {
		st.Progress = append(st.Progress, workflowapi.Progress{AtMs: uint64(at.Add(time.Duration(i) * time.Second).UnixMilli()), Note: l})
	}
	run := &workflowapi.Run{Id: id, Definition: CodeChange, Owner: turnOwner, Status: "running", CurrentStep: "work", Vars: a.Vars,
		History: []workflowapi.StepState{{Step: "prepare", Attempt: 1, Status: "done"}, st}}
	wf.w.mu.Lock()
	wf.byID[id] = run
	wf.ids = append(wf.ids, id)
	wf.w.mu.Unlock()
	return run
}

// A task that repeats one action stops within the configured time, with a
// reason that the persona can read, and the persona gets the event
// (docs/decisions/0012).
func TestStallCheckStopsATaskThatRepeats(t *testing.T) {
	sys, w, wf := eventEnv(t, nil)
	sys.Cfg.Stall.Repeat, sys.Cfg.Stall.After = 3, 10*time.Minute
	now := time.Now()
	var loop, fine []string
	for i := range 4 {
		loop = append(loop, fmt.Sprintf("step %d, %d tool calls: bash go test ./... (failed); bash go test ./... (failed)", 10+i, 20+2*i))
		fine = append(fine, fmt.Sprintf("step %d, %d tool calls: edit file%d.go; bash go test ./... (failed)", 10+i, 20+2*i, i))
	}
	looping := working(sys, wf, "run_loop", now, loop...)
	healthy := working(sys, wf, "run_fine", now, fine...)
	// The step waits for a runner: no apprentice works, so it is not late.
	queued := working(sys, wf, "run_queued", now.Add(-2*time.Hour))
	queued.History[1].Status = "pending"

	stopped := sys.checkStalls(context.Background(), now.Add(5*time.Second))
	if len(stopped) != 1 || stopped[0] != "run_loop" {
		t.Fatalf("stopped %v", stopped)
	}
	if looping.Status != "cancelled" || !strings.Contains(deref(looping.Error), "same actions") || !strings.Contains(deref(looping.Error), "go test") {
		t.Fatalf("the task that repeats: %s %q", looping.Status, deref(looping.Error))
	}
	if healthy.Status != "running" || queued.Status != "running" {
		t.Fatal("the check stopped a task that works or waits")
	}
	// The person gets the reason, and the persona gets the event.
	told := false
	for _, u := range w.utts {
		told = told || (u.AuthorKind == "persona" && strings.Contains(u.Text, "same actions"))
	}
	evs := wf.events()
	if !told || len(evs) != 1 || evs[0].Vars["event.source"] != "run_loop" {
		t.Fatalf("told %v, events %d", told, len(evs))
	}
}

// A task whose apprentice writes no status line stops after the
// configured time. The time starts when the check first sees the work,
// and a task can have its own time.
func TestStallCheckStopsAQuietTask(t *testing.T) {
	sys, _, wf := eventEnv(t, nil)
	sys.Cfg.Stall.After = 10 * time.Minute
	now := time.Now()
	quiet := working(sys, wf, "run_quiet", now)
	tight := working(sys, wf, "run_tight", now)
	tight.Vars["stall_ms"] = "60000"
	talks := working(sys, wf, "run_talks", now)

	if got := sys.checkStalls(context.Background(), now); len(got) != 0 {
		t.Fatalf("stopped at the first look: %v", got)
	}
	// Two minutes: only the task with the tight time is late.
	if got := sys.checkStalls(context.Background(), now.Add(2*time.Minute)); len(got) != 1 || got[0] != "run_tight" {
		t.Fatalf("after 2 minutes: %v", got)
	}
	talks.History[1].Progress = append(talks.History[1].Progress, workflowapi.Progress{AtMs: uint64(now.Add(9 * time.Minute).UnixMilli()), Note: "step 3, 6 tool calls: edit a.go"})
	got := sys.checkStalls(context.Background(), now.Add(11*time.Minute))
	if len(got) != 1 || got[0] != "run_quiet" || !strings.Contains(deref(quiet.Error), "no status line") {
		t.Fatalf("after 11 minutes: %v %q", got, deref(quiet.Error))
	}
	if talks.Status != "running" {
		t.Fatal("the check stopped a task that wrote a status line")
	}
}

// A question to an apprentice has the same check, with a shorter time: a
// person waits for the answer. A question that no runner takes is stopped
// too, and the person gets the reason in each case.
func TestStallCheckStopsAQuestion(t *testing.T) {
	sys, w, wf := eventEnv(t, nil)
	now := time.Now()
	question := func(id, step, status string, started, line time.Time) *workflowapi.Run {
		run := working(sys, wf, id, line, "step 3, 6 tool calls: read watch.rs")
		run.Definition, run.CurrentStep = Investigation, step
		run.Vars["title"], run.Vars["mode"] = "Restart limits", QuestionMode
		run.History[1].Step, run.History[1].Status, run.History[1].StartedMs = step, status, uint64(started.UnixMilli())
		return run
	}
	// The apprentice wrote its last line 25 minutes ago: more than the
	// limit of a question, less than the limit of a code change.
	quiet := question("run_q_quiet", "investigate", "claimed", now.Add(-40*time.Minute), now.Add(-25*time.Minute))
	fine := question("run_q_fine", "investigate", "claimed", now.Add(-10*time.Minute), now.Add(-2*time.Minute))
	// No runner took these steps.
	waits := question("run_q_waits", "investigate", "pending", now.Add(-20*time.Minute), now.Add(-20*time.Minute))
	fresh := question("run_q_fresh", "prepare", "pending", now.Add(-5*time.Minute), now.Add(-5*time.Minute))
	change := working(sys, wf, "run_change", now.Add(-25*time.Minute), "step 3, 6 tool calls: edit a.go")

	// The first look starts the clock of the steps that work.
	if stopped := sys.checkStalls(context.Background(), now.Add(-21*time.Minute)); len(stopped) != 0 {
		t.Fatalf("the first look stopped %v", stopped)
	}
	stopped := sys.checkStalls(context.Background(), now)
	if len(stopped) != 2 || quiet.Status != "cancelled" || waits.Status != "cancelled" {
		t.Fatalf("stopped %v: quiet %s, waits %s", stopped, quiet.Status, waits.Status)
	}
	if fine.Status != "running" || fresh.Status != "running" || change.Status != "running" {
		t.Fatalf("stopped a task that works or just started: %s %s %s", fine.Status, fresh.Status, change.Status)
	}
	if !strings.Contains(deref(quiet.Error), "no status line") || !strings.Contains(deref(waits.Error), "no runner took the question") {
		t.Fatalf("reasons: %q, %q", deref(quiet.Error), deref(waits.Error))
	}
	// The person gets each reason (as a message and as a notice).
	for _, why := range []string{"no status line", "no runner took the question"} {
		told := false
		for _, u := range w.utts {
			told = told || (u.AuthorKind == "persona" && strings.Contains(u.Text, "I stopped the question \"Restart limits\"") &&
				strings.Contains(u.Text, why) && strings.Contains(u.Text, "There is no answer"))
		}
		if !told {
			t.Fatalf("the person did not get the reason %q", why)
		}
	}
}
