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
// apprentice works. The apprentice writes a status line (a progress note
// of the step) each few tool calls. A task with no new line for too long,
// or whose lines repeat, is stopped, and the persona that started it gets
// the event. The guards of the apprentice find a loop from the inside;
// this check finds a process that hangs, and a loop that the guards miss.

// stallLimits are the settings of the stall check.
type stallLimits struct {
	// Every is the time between two looks (MAGI_STALL_EVERY, default 1m).
	Every time.Duration
	// After: a task with no status line for this time is stopped
	// (MAGI_STALL_AFTER, default 20m). The run variable stall_ms of a
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
		l.After = 20 * time.Minute
	}
	if l.Repeat <= 0 {
		l.Repeat = 6
	}
	return l
}

// stallStep is the step that the check looks at: the work of the
// apprentice. The other steps are short, or they wait for a person.
const stallStep = "work"

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
	if run.Status != "running" || run.CurrentStep != stallStep || len(run.History) == 0 {
		return ""
	}
	st := run.History[len(run.History)-1]
	if st.Step != stallStep || st.Status != "running" {
		// The step waits for a worker: no apprentice runs.
		return ""
	}
	after, repeat := lim.After, lim.Repeat
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

// checkStalls stops each task that is stalled at now, and gives their
// ids. The owner gets a notice, and the persona that started the task
// gets the event (events.go).
func (s *System) checkStalls(ctx context.Context, now time.Time) []string {
	ctx = s.AsService(ctx)
	res, err := s.Workflow.ListRuns(ctx, workflowapi.ListRunsRequest{Definition: ptr(CodeChange), Status: ptr("running"), Limit: ptr(uint64(200))})
	if err != nil {
		s.Log.Warn("stall check: list", "err", err)
		return nil
	}
	lim := s.stallLimits()
	var stopped []string
	s.stallMu.Lock()
	defer s.stallMu.Unlock()
	if s.stallSeen == nil {
		s.stallSeen = map[string]time.Time{}
	}
	working := map[string]bool{}
	for _, run := range res.Runs {
		if run.CurrentStep != stallStep || len(run.History) == 0 || run.History[len(run.History)-1].Status != "running" {
			continue
		}
		// One time for each attempt of the step.
		key := fmt.Sprintf("%s/%d/%d", run.Id, len(run.History), run.History[len(run.History)-1].Attempt)
		working[key] = true
		if _, ok := s.stallSeen[key]; !ok {
			s.stallSeen[key] = now
		}
		why := stalled(run, lim, s.stallSeen[key], now)
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
		s.report(ctx, workflowapi.Assignment{Run: got.Id, Owner: got.Owner, Vars: got.Vars}, "normal", "Task stopped",
			"I stopped the task: "+why+". The branch stays for reference.")
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
