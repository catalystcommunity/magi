package server

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/workflow/api"
	"github.com/catalystcommunity/magi/primitives/workflow/store"
)

// Run and step statuses.
const (
	RunRunning   = "running"
	RunWaiting   = "waiting"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"

	StepPending   = "pending"
	StepClaimed   = "claimed"
	StepWaiting   = "waiting"
	StepDone      = "done"
	StepFailed    = "failed"
	StepCancelled = "cancelled"

	DefaultMaxAttempts = 3
)

// Validate checks a definition.
func Validate(d api.Definition) error {
	if d.Name == "" || len(d.Steps) == 0 {
		return rpc.Invalid("name and steps are required")
	}
	ids := map[string]bool{}
	for _, s := range d.Steps {
		if s.Id == "" || ids[s.Id] {
			return rpc.Invalid("step id %q is empty or not unique", s.Id)
		}
		ids[s.Id] = true
		switch s.Kind {
		case "task":
			if s.Queue == nil || *s.Queue == "" {
				return rpc.Invalid("task step %q needs a queue", s.Id)
			}
		case "human":
		default:
			return rpc.Invalid("step %q: kind must be task or human", s.Id)
		}
	}
	if !ids[d.Start] {
		return rpc.Invalid("start step %q does not exist", d.Start)
	}
	for _, s := range d.Steps {
		for out, target := range s.Next {
			if target != "" && !ids[target] {
				return rpc.Invalid("step %q: outcome %q goes to unknown step %q", s.Id, out, target)
			}
		}
	}
	return nil
}

func findStep(d *api.Definition, id string) *api.Step {
	for i := range d.Steps {
		if d.Steps[i].Id == id {
			return &d.Steps[i]
		}
	}
	return nil
}

func maxAttempts(s *api.Step) uint64 {
	if s.MaxAttempts == nil || *s.MaxAttempts == 0 {
		return DefaultMaxAttempts
	}
	return *s.MaxAttempts
}

func current(r *api.Run) *api.StepState {
	if len(r.History) == 0 {
		return nil
	}
	return &r.History[len(r.History)-1]
}

func finished(status string) bool {
	return status == RunSucceeded || status == RunFailed || status == RunCancelled
}

func ptr[T any](v T) *T { return &v }

// enter makes stepID the current step. It returns the task row to create for
// a task step. An empty stepID finishes the run as succeeded.
func enter(r *api.Run, d *api.Definition, stepID string, attempt uint64, now uint64) *store.Task {
	if stepID == "" {
		r.Status, r.CurrentStep = RunSucceeded, ""
		return nil
	}
	s := findStep(d, stepID)
	st := api.StepState{Step: stepID, Attempt: attempt, Progress: []api.Progress{}, StartedMs: now}
	r.CurrentStep = stepID
	if s.Kind == "human" {
		st.Status, r.Status = StepWaiting, RunWaiting
		r.History = append(r.History, st)
		return nil
	}
	st.Status, r.Status = StepPending, RunRunning
	r.History = append(r.History, st)
	idx := len(r.History) - 1
	return &store.Task{Key: r.Id + "/" + strconv.Itoa(idx), Run: r.Id, Index: idx, Step: stepID, Queue: *s.Queue, State: store.TaskSubmit}
}

// fail ends the run as failed.
func fail(r *api.Run, msg string) {
	r.Status, r.CurrentStep, r.Error = RunFailed, "", &msg
}

// retry offers the current task step again, or fails the run when the step
// has used its attempts.
func retry(r *api.Run, d *api.Definition, reason string, now uint64) *store.Task {
	cur := current(r)
	s := findStep(d, cur.Step)
	if cur.Attempt >= maxAttempts(s) {
		fail(r, fmt.Sprintf("step %s: %s after %d attempts", s.Id, reason, cur.Attempt))
		return nil
	}
	return enter(r, d, s.Id, cur.Attempt+1, now)
}

// advance applies outcome to the ended current step and moves the run on.
func advance(r *api.Run, d *api.Definition, outcome, errText string, now uint64) *store.Task {
	cur := current(r)
	s := findStep(d, cur.Step)
	target, mapped := s.Next[outcome]
	switch {
	case mapped:
		return enter(r, d, target, 1, now)
	case outcome == "retry" && s.Kind == "task":
		return retry(r, d, "retry requested", now)
	case outcome == "fail":
		msg := "step " + s.Id + " failed"
		if errText != "" {
			msg += ": " + errText
		}
		fail(r, msg)
		return nil
	}
	return enter(r, d, "", 0, now)
}

// endStep closes the current step state.
func endStep(st *api.StepState, status, outcome string, now uint64) {
	st.Status, st.EndedMs = status, &now
	if outcome != "" {
		st.Outcome = &outcome
	}
}

func outcomeAllowed(s *api.Step, outcome string) bool {
	return len(s.Outcomes) == 0 || slices.Contains(s.Outcomes, outcome)
}
