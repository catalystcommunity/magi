package compose

import (
	"context"
	"encoding/json"
	"fmt"

	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// BackupTask is the catalog task of the backup model of a code change. Its
// default names a model that is slower and more careful than the model of
// the task step.code-change.work. With no default, a code change has no
// backup round.
const BackupTask = "step." + CodeChange + ".backup"

// backupStatuses are the apprentice statuses that a different model can
// repair: a guard ended a run that went wrong. A gone endpoint or gone
// money is not here: the chain of the work task has the next endpoint.
// "escalated" is not here: the apprentice asks for a person.
var backupStatuses = map[string]bool{"loop": true, "no_progress": true, "stalled": true, "supervisor_stop": true}

// backup is the step after a work that the model of the run could not
// finish. The work step gave "blocked" (the apprentice stopped), or the
// check step gave "give-up" (the checks failed too many times) or
// "guard-stop" (a loop or a stall left a change that fails the checks).
//
// One time for each run, it starts a work round with the backup model: the
// run then names that model for its work and for its reviews, and the
// checks get new attempts. The work of the first model stays in the
// environment, with the output of its last failed check. Code decides
// this, with no model call.
//
// With no backup round, it tells the person what the steps before it did
// not tell: the task stopped, or the task waits for a decision.
func (s *System) backup(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	gaveUp := a.Vars["task.result"] == "gave-up"
	if name := s.backupModel(ctx, a, gaveUp); name != "" {
		m, err := json.Marshal(map[string]string{"name": name, "review_model": name})
		if err != nil {
			return Result{}, err
		}
		why := "The apprentice stopped before it finished (" + a.Vars["apprentice.status"] + ")"
		if gaveUp {
			why = fmt.Sprintf("The checks failed %s times", a.Vars["check.attempts"])
		}
		note("A work round with the backup model " + name + " starts.")
		// A notice, not a post: a task that no conversation started has no
		// stream, and its owner must also know why the task takes longer.
		// "low": the persona does not get an event for it.
		s.report(ctx, a, "low", "Backup model", why+". I started one more work round with the backup model "+name+
			". It is slower than the usual model: the round can take two hours.")
		return Result{Outcome: "work", Outputs: map[string]string{
			"model": string(m), "backup.model": name,
			// The backup model gets its own check attempts, and it is not a
			// blocked round: the comment of a "blocked" decision is not its
			// review comment.
			"check.attempts": "0", "task.result": "", "apprentice.blocked": "", "apprentice.guard_stop": "",
		}}, nil
	}
	switch {
	case gaveUp:
		s.report(ctx, a, "normal", "Task stopped", fmt.Sprintf("The checks failed %s times. I stopped the task. The last output is in artifact %s.",
			a.Vars["check.attempts"], a.Vars["check.output"]))
		return Result{Outcome: "give-up"}, nil
	case a.Vars["apprentice.guard_stop"] == "true":
		s.report(ctx, a, "normal", "Task blocked", "The apprentice stopped before it finished, and the checks failed on the change that it left. The work is kept. Resume it, run the checks again, or reject it.")
	default:
		// Without this report, a blocked task waits and nobody knows.
		s.report(ctx, a, "normal", "Task blocked", fmt.Sprintf("The apprentice stopped (%s) after %s steps. The work stays in the environment. Resume it, run the checks on it, or reject it.",
			a.Vars["apprentice.status"], a.Vars["apprentice.steps"]))
	}
	return Result{Outcome: "blocked"}, nil
}

// backupModel gives the catalog name of the backup model for the run of
// a, or "" when the run gets no backup round.
func (s *System) backupModel(ctx context.Context, a workflowapi.Assignment, gaveUp bool) string {
	switch {
	case a.Vars["backup.model"] != "":
		// One backup round for each run.
		return ""
	case a.Vars["model"] != "" || a.Vars["base"] != "":
		// An operator named the model of the run, or the run measures a
		// model (an eval run): a different model is a different run.
		return ""
	case !gaveUp && a.Vars["apprentice.guard_stop"] != "true" && !backupStatuses[a.Vars["apprentice.status"]]:
		return ""
	case s.Router == nil:
		return ""
	}
	ms, err := s.Router.Models(ctx, BackupTask, nil, false)
	if err != nil {
		s.Log.Warn("backup model", "run", a.Run, "err", err)
		return ""
	}
	if len(ms) == 0 {
		return ""
	}
	// The same model again is no backup.
	if first, err := s.Router.Models(ctx, "step."+CodeChange+".work", nil, false); err == nil && len(first) > 0 && first[0].Name == ms[0].Name {
		return ""
	}
	return ms[0].Name
}
