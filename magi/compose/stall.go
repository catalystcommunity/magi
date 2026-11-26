package compose

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// The stall check (docs/decisions/0012) looks at each task whose
// apprentice works: a code change, and a question (an investigation). The apprentice writes a status line (a progress note
// of the step) each few tool calls. A task with no new line for too long,
// or whose lines repeat, is stopped, and the persona that started it gets
// the event. The guards of the apprentice find a loop from the inside;
// this check finds a process that hangs, and a loop that the guards miss.

// stallLimits are the settings of the stall check.
type stallLimits struct {
	// Every is the time between two looks (MAGI_STALL_EVERY, default 1m).
	Every time.Duration
	// After: a task with no status line for this time is stopped
	// (MAGI_STALL_AFTER, default 30m). The run variable stall_ms of a
	// task replaces it for that task.
	After time.Duration
	// Repeat: a task whose last Repeat status lines have the same
	// actions is stopped (MAGI_STALL_REPEAT, default 6). The run variable
	// stall_repeat replaces it.
	Repeat int
}

func (s *System) stallLimits() stallLimits {
	l := s.Cfg.Stall
	if l.Every <= 0 {
		l.Every = time.Minute
	}
	if l.After <= 0 {
		l.After = 30 * time.Minute
	}
	if l.Repeat <= 0 {
		l.Repeat = 6
	}
	return l
}

// stallSteps has the step that the check looks at for each kind of task:
// the step of the apprentice. The other steps are short, or they wait for
// a person.
var stallSteps = map[string]string{CodeChange: "work", Investigation: "investigate"}

// A question has a limit of 15 minutes in the apprentice (the runner sets
// it). The stall check finds the apprentice that hangs, so its limit for a
// question is a little above that. A person waits for the answer.
const questionStall = 20 * time.Minute

// questionWait: a question that no runner takes for this time is stopped
// (the runner is off, or it is busy for a long time). A code change can
// wait for a runner; a person who asked a question waits for an answer.
const questionWait = 15 * time.Minute

// actions gives the part of a status line that says what the apprentice
// did, with no counters ("step 12, 30 tool calls: ").
func actions(note string) string {
	if i := strings.Index(note, " tool calls: "); i >= 0 && strings.HasPrefix(note, "step ") {
		return note[i+len(" tool calls: "):]
	}
	return note
}

// stalled says why a run is stalled at now, or "". seen is the time when
// the check first saw the step with a worker: the start time of a step is
// the time when it went to the queue, and a step can wait there.
func stalled(run workflowapi.Run, lim stallLimits, seen, now time.Time) string {
	stallStep := stallSteps[run.Definition]
	if run.Status != "running" || stallStep == "" || run.CurrentStep != stallStep || len(run.History) == 0 {
		return ""
	}
	st := run.History[len(run.History)-1]
	if st.Step != stallStep || st.Status != "claimed" {
		// The step waits for a worker: no apprentice runs.
		return ""
	}
	after, repeat := lim.After, lim.Repeat
	if run.Definition == Investigation && after > questionStall {
		after = questionStall
	}
	if ms, err := strconv.ParseInt(run.Vars["stall_ms"], 10, 64); err == nil && ms > 0 {
		after = time.Duration(ms) * time.Millisecond
	}
	if n, err := strconv.Atoi(run.Vars["stall_repeat"]); err == nil && n > 1 {
		repeat = n
	}
	last := seen
	if n := len(st.Progress); n > 0 {
		if at := time.UnixMilli(int64(st.Progress[n-1].AtMs)); at.After(last) {
			last = at
		}
	}
	if quiet := now.Sub(last); quiet > after {
		return fmt.Sprintf("the apprentice wrote no status line for %s (the limit is %s)", quiet.Round(time.Second), after)
	}
	var lines []string
	for _, p := range st.Progress {
		if strings.HasPrefix(p.Note, "step ") {
			lines = append(lines, actions(p.Note))
		}
	}
	if len(lines) >= repeat {
		same := true
		for _, l := range lines[len(lines)-repeat:] {
			same = same && l == lines[len(lines)-1]
		}
		if same {
			return fmt.Sprintf("the last %d status lines of the apprentice have the same actions: %s", repeat, clip(lines[len(lines)-1], 200))
		}
	}
	return ""
}

// unanswered says why a question waits too long for a runner at now, or
// "". The steps on a runner before the answer are prepare and investigate.
func unanswered(run workflowapi.Run, now time.Time) string {
	if run.Definition != Investigation || run.Status != "running" || len(run.History) == 0 {
		return ""
	}
	st := run.History[len(run.History)-1]
	if (st.Step != "prepare" && st.Step != "investigate") || st.Status == "claimed" || st.StartedMs == 0 {
		return ""
	}
	if wait := now.Sub(time.UnixMilli(int64(st.StartedMs))); wait > questionWait {
		return fmt.Sprintf("no runner took the question for %s (runner %s)", wait.Round(time.Minute), firstNonEmpty(run.Vars["runner"], "of Magi"))
	}
	return ""
}

// checkStalls stops each task that is stalled at now, and gives their
// ids. The owner gets a notice, and the persona that started the task
// gets the event (events.go).
func (s *System) checkStalls(ctx context.Context, now time.Time) []string {
	ctx = s.AsService(ctx)
	var runs []workflowapi.Run
	for def := range stallSteps {
		res, err := s.Workflow.ListRuns(ctx, workflowapi.ListRunsRequest{Definition: ptr(def), Status: ptr("running"), Limit: ptr(uint64(200))})
		if err != nil {
			s.Log.Warn("stall check: list", "definition", def, "err", err)
			continue
		}
		runs = append(runs, res.Runs...)
	}
	lim := s.stallLimits()
	var stopped []string
	s.stallMu.Lock()
	defer s.stallMu.Unlock()
	if s.stallSeen == nil {
		s.stallSeen = map[string]time.Time{}
	}
	working := map[string]bool{}
	for _, run := range runs {
		why := unanswered(run, now)
		if why == "" {
			if run.CurrentStep != stallSteps[run.Definition] || len(run.History) == 0 || run.History[len(run.History)-1].Status != "claimed" {
				continue
			}
			// One time for each attempt of the step.
			key := fmt.Sprintf("%s/%d/%d", run.Id, len(run.History), run.History[len(run.History)-1].Attempt)
			working[key] = true
			if _, ok := s.stallSeen[key]; !ok {
				s.stallSeen[key] = now
			}
			why = stalled(run, lim, s.stallSeen[key], now)
		}
		if why == "" {
			continue
		}
		reason := "Magi stopped the task: " + why
		got, err := s.Workflow.Cancel(ctx, workflowapi.CancelRequest{Id: run.Id, Reason: &reason})
		if err != nil {
			s.Log.Warn("stall check: cancel", "run", run.Id, "err", err)
			continue
		}
		s.Log.Warn("task stalled", "run", run.Id, "why", why)
		// A runner removes its own environments when it sees that the run
		// ended, and it stops the apprentice when the claim is lost.
		if id := got.Vars["environment"]; id != "" && got.Vars["runner"] == "" && s.Execution != nil {
			if _, err := s.Execution.Destroy(ctx, executionapi.DestroyRequest{Id: id}); err != nil {
				s.Log.Warn("destroy environment", "env", id, "err", err)
			}
		}
		if run.Definition == Investigation {
			// A question has no branch and no review: the person only
			// waits for the answer, so they must know that none comes.
			s.report(ctx, workflowapi.Assignment{Run: got.Id, Owner: got.Owner, Vars: got.Vars}, "normal", "No answer",
				"I stopped the question \""+clip(got.Vars["title"], 100)+"\": "+why+". There is no answer. Ask again if you still want to know.")
		} else {
			s.report(ctx, workflowapi.Assignment{Run: got.Id, Owner: got.Owner, Vars: got.Vars}, "normal", "Task stopped",
				"I stopped the task: "+why+". The branch stays for reference.")
		}
		stopped = append(stopped, run.Id)
	}
	for key := range s.stallSeen {
		if !working[key] {
			delete(s.stallSeen, key)
		}
	}
	return stopped
}

// StallCheck runs the stall check until ctx ends.
func (s *System) StallCheck(ctx context.Context) {
	t := time.NewTicker(s.stallLimits().Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.checkStalls(ctx, now)
		}
	}
}
