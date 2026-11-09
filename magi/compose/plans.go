package compose

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/model"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// Plans and to-dos (docs/plans.md). Each to-do is one run of the workflow
// "todo". A person (the owner, or another person in "who") does the work
// and marks it done. Magi sends the reminder, tells the person when the
// to-do is late, starts a to-do that waits for another one, and records
// an episode when it ends. A plan is a name that groups to-dos; the order
// of a plan is the "after" of each to-do.

const Todo = "todo"

const (
	// recheck is the time after which a blocked to-do checks its
	// predecessor again, in case the finish step did not reach it.
	recheck = 10 * time.Minute
	// nagEvery is the time between the notices of a late to-do.
	nagEvery = 24 * time.Hour
	// maxNags is the number of late notices. Then the to-do waits quietly.
	maxNags = 3
)

func todoDefinition() workflowapi.Definition {
	human := func(id, prompt string, next map[string]string, outcomes []string) workflowapi.Step {
		return workflowapi.Step{Id: id, Kind: "human", Prompt: ptr(prompt), Deciders: []string{"${who}"}, Outcomes: outcomes, Next: next}
	}
	task := func(id string, next map[string]string) workflowapi.Step {
		return workflowapi.Step{Id: id, Kind: "task", Queue: ptr("magi.todo." + id), Next: next, MaxAttempts: ptr(uint64(3))}
	}
	end := map[string]string{"done": "finish", "skip": "finish"}
	with := func(m map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for a, b := range m {
			out[a] = b
		}
		return out
	}
	blocked := human("blocked", "Waits for ${after_title}.", with(with(end, "ready", "gate"), "check", "gate"), []string{"ready", "check", "done", "skip"})
	blocked.WaitMs, blocked.TimeoutOutcome = ptr(uint64(recheck.Milliseconds())), ptr("check")
	early := human("early", "${title}", with(end, "remind", "remind"), []string{"done", "skip", "remind"})
	early.WaitUntil, early.TimeoutOutcome = ptr("${remind_ms}"), ptr("remind")
	do := human("do", "${title}", with(end, "overdue", "overdue"), []string{"done", "skip", "overdue"})
	do.WaitUntil, do.TimeoutOutcome = ptr("${due_ms}"), ptr("overdue")
	late := human("late", "${title} (late)", with(end, "overdue", "overdue"), []string{"done", "skip", "overdue"})
	late.WaitMs, late.TimeoutOutcome = ptr(uint64(nagEvery.Milliseconds())), ptr("overdue")
	return workflowapi.Definition{
		Name:        Todo,
		Description: ptr("A to-do of a person: an optional reminder, a due time, notices when it is late, and an order after another to-do."),
		Start:       "gate",
		Steps: []workflowapi.Step{
			task("gate", map[string]string{"ready": "do", "early": "early", "blocked": "blocked"}),
			blocked, early,
			task("remind", map[string]string{"sent": "do", "close": "finish"}),
			do,
			task("overdue", map[string]string{"sent": "late", "quiet": "waiting"}),
			late,
			human("waiting", "${title} (late)", end, []string{"done", "skip"}),
			task("finish", map[string]string{"done": ""}),
		},
	}
}

// todoGate sends a new to-do to its first wait.
func (s *System) todoGate(ctx context.Context, a workflowapi.Assignment, _ func(string)) (Result, error) {
	if after := a.Vars["after"]; after != "" {
		// A predecessor in its finish step is done: that step starts this one.
		if r, err := s.Workflow.GetRun(ctx, workflowapi.GetRunRequest{Id: after}); err == nil && !finished(r.Status) && r.CurrentStep != "finish" {
			return Result{Outcome: "blocked", Outputs: map[string]string{"after_title": r.Vars["title"]}}, nil
		}
	}
	// A reminder whose time is past goes at once; it is not lost.
	if _, ok := msVar(a.Vars["remind_ms"]); ok && a.Vars["reminded"] == "" {
		return Result{Outcome: "early"}, nil
	}
	return Result{Outcome: "ready"}, nil
}

// todoRemind sends the reminder. A reminder with no to-do ends then.
func (s *System) todoRemind(ctx context.Context, a workflowapi.Assignment, _ func(string)) (Result, error) {
	if err := s.todoNotice(ctx, a, "Reminder", a.Vars["title"]); err != nil {
		return Result{Outcome: "retry"}, nil
	}
	if a.Vars["kind"] == "reminder" {
		return Result{Outcome: "close", Outputs: map[string]string{"result": "reminded", "reminded": "1"}}, nil
	}
	return Result{Outcome: "sent", Outputs: map[string]string{"reminded": "1"}}, nil
}

// todoOverdue tells the person that a to-do is late, at most maxNags times.
func (s *System) todoOverdue(ctx context.Context, a workflowapi.Assignment, _ func(string)) (Result, error) {
	n, _ := strconv.Atoi(a.Vars["nags"])
	if n >= maxNags {
		return Result{Outcome: "quiet"}, nil
	}
	text := a.Vars["title"]
	if t, ok := msVar(a.Vars["due_ms"]); ok {
		text += " was due " + at(time.UnixMilli(int64(t)), time.Now(), s.zone()) + "."
	}
	if err := s.todoNotice(ctx, a, "Late", text); err != nil {
		return Result{Outcome: "retry"}, nil
	}
	return Result{Outcome: "sent", Outputs: map[string]string{"nags": strconv.Itoa(n + 1)}}, nil
}

// todoFinish starts the to-dos that wait for this one, and records an
// episode of it.
func (s *System) todoFinish(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	run, err := s.Workflow.GetRun(ctx, workflowapi.GetRunRequest{Id: a.Run})
	if err != nil {
		return Result{}, err
	}
	outcome := a.Vars["result"]
	for i := len(run.History) - 1; i >= 0 && outcome == ""; i-- {
		if o := deref(run.History[i].Outcome); o == "done" || o == "skip" {
			outcome = o
		}
	}
	next, err := s.Workflow.ListRuns(ctx, workflowapi.ListRunsRequest{Definition: ptr(Todo), Status: ptr("waiting"), Limit: ptr(uint64(500))})
	if err == nil {
		for _, r := range next.Runs {
			if r.Owner == a.Owner && r.Vars["after"] == a.Run && r.CurrentStep == "blocked" {
				if _, err := s.Workflow.Decide(ctx, workflowapi.DecideRequest{Run: r.Id, Step: "blocked", Outcome: "ready"}); err != nil {
					note("start " + r.Id + ": " + err.Error())
				}
			}
		}
	}
	if s.Knowledge != nil {
		ended := nowMs()
		ep := knowledgeapi.RecordEpisodeRequest{Kind: "task", Title: a.Vars["title"], Outcome: optional(outcome),
			StartedMs: ptr(run.CreatedMs), EndedMs: &ended, Owners: []string{a.Owner}, ActFor: &a.Owner,
			Refs: []knowledgeapi.Ref{{Kind: "workflow-run", Ref: a.Run}}}
		if p := a.Vars["plan"]; p != "" {
			ep.Summary = ptr("Part of the plan " + p + ".")
		}
		if _, err := s.Knowledge.RecordEpisode(ctx, ep); err != nil {
			note("episode: " + err.Error())
		}
	}
	return Result{Outcome: "done", Outputs: map[string]string{"result": outcome}}, nil
}

func (s *System) todoNotice(ctx context.Context, a workflowapi.Assignment, title, text string) error {
	if s.noticeHook != nil {
		return s.noticeHook(title+": "+clip(a.Vars["title"], 80), text)
	}
	_, err := s.sendNotice(ctx, noticeReq{owner: a.Owner, persona: firstNonEmpty(a.Vars["persona"], Persona), stream: a.Vars["stream"],
		title: title + ": " + clip(a.Vars["title"], 80), text: text, urgency: "normal"})
	return err
}

// planTools are the tools of plans and to-dos.
func planTools(obj func(map[string]any, ...string) map[string]any, str func(string) map[string]any) []model.Tool {
	when := "Optional. A local time as YYYY-MM-DD HH:MM (24 hours), or a date as YYYY-MM-DD. Work it out from the time of today at the top."
	return []model.Tool{
		{Name: "add_todo", Description: "Add a to-do for the person (or a step of a plan). Magi reminds the person, and tells them when it is late.", Parameters: obj(map[string]any{
			"title":  str("What to do, short, in the words of the person."),
			"due":    str("When it must be done. " + when),
			"remind": str("When to remind the person. " + when),
			"plan":   str("Optional. The name of the plan that it is a step of."),
			"after":  str("Optional. The id of a to-do that must be done first, or \"previous\" for the to-do that you added before in this turn."),
		}, "title")},
		{Name: "set_reminder", Description: "Remind the person of something at a time. It is not a to-do: it ends when Magi sends it.", Parameters: obj(map[string]any{
			"text": str("What to remind the person of."),
			"at":   str("When. A local time as YYYY-MM-DD HH:MM (24 hours). Work it out from the time of today at the top."),
		}, "text", "at")},
		{Name: "list_todos", Description: "List the person's open to-dos and reminders (with the steps of plans), or the ones of one plan.", Parameters: obj(map[string]any{
			"plan":          str("Optional. Only the to-dos of this plan."),
			"include_ended": map[string]any{"type": "boolean", "description": "Also to-dos that ended in the last 7 days."},
		})},
		{Name: "finish_todo", Description: "Mark a to-do done, skip it, or cancel it (also a reminder).", Parameters: obj(map[string]any{
			"id":      str("The to-do id (tdo_...), from list_todos."),
			"outcome": map[string]any{"type": "string", "enum": []string{"done", "skip", "cancel"}},
		}, "id", "outcome")},
		{Name: "change_todo", Description: "Change the title, the due time, or the reminder of an open to-do. Give only what changes.", Parameters: obj(map[string]any{
			"id":     str("The to-do id."),
			"title":  str("Optional. The new title."),
			"due":    str("Optional. The new due time, or \"none\". " + when),
			"remind": str("Optional. The new reminder time, or \"none\". " + when),
		}, "id")},
	}
}

var planToolNames = []string{"add_todo", "set_reminder", "list_todos", "finish_todo", "change_todo"}

// planTool runs a plan tool. ok is false for other tools.
func (s *System) planTool(ctx context.Context, a workflowapi.Assignment, as conversationLens, utt conversationapi.Utterance, ts *turnState, name string, args map[string]string) (string, bool) {
	if !slices.Contains(planToolNames, name) {
		return "", false
	}
	loc, now := s.zone(), time.Now()
	switch name {
	case "add_todo", "set_reminder":
		title := strings.TrimSpace(firstNonEmpty(args["title"], args["text"]))
		if title == "" {
			return "error: give a title", true
		}
		vars := map[string]string{"title": clip(title, 200), "who": a.Owner, "persona": as.persona, "stream": utt.Stream, "utterance": utt.Id}
		if name == "set_reminder" {
			vars["kind"] = "reminder"
			args = map[string]string{"remind": args["at"]}
			if strings.TrimSpace(args["remind"]) == "" {
				return "error: give the time of the reminder", true
			}
		}
		for _, k := range []string{"due", "remind"} {
			if v := strings.TrimSpace(args[k]); v != "" && v != "none" {
				t, err := parseWhen(v, k, loc, now)
				if err != nil {
					return "error: " + k + ": " + err.Error(), true
				}
				vars[k+"_ms"] = strconv.FormatInt(t.UnixMilli(), 10)
			}
		}
		if p := strings.TrimSpace(args["plan"]); p != "" {
			vars["plan"] = clip(p, 120)
		}
		if after := strings.TrimSpace(args["after"]); after != "" {
			if after == "previous" {
				after = ts.lastTodo
			}
			if _, err := s.ownTodo(ctx, a.Owner, after); err != nil {
				return "error: after: " + err.Error(), true
			}
			vars["after"] = after
		}
		// One to-do for each utterance and title, also when the model
		// calls the tool twice.
		run, err := s.Workflow.Start(ctx, workflowapi.StartRequest{Definition: Todo, Vars: vars, Owner: &a.Owner, Key: ptr(utt.Id + "/" + strings.ToLower(title))})
		if err != nil {
			return "error: " + err.Error(), true
		}
		ts.lastTodo = run.Id
		what := "To-do"
		if name == "set_reminder" {
			what = "Reminder"
		}
		return fmt.Sprintf("%s added: %s", what, describeTodo(run, nil, loc, now)), true
	case "list_todos":
		runs, err := s.todos(ctx, a.Owner, args["include_ended"] == "true")
		if err != nil {
			return "error: " + err.Error(), true
		}
		titles := map[string]string{}
		for _, r := range runs {
			titles[r.Id] = r.Vars["title"]
		}
		var b strings.Builder
		for _, r := range runs {
			if p := strings.TrimSpace(args["plan"]); p != "" && !strings.EqualFold(r.Vars["plan"], p) {
				continue
			}
			b.WriteString("- " + describeTodo(r, titles, loc, now) + "\n")
		}
		if b.Len() == 0 {
			return "No to-dos.", true
		}
		return b.String(), true
	case "finish_todo":
		out := args["outcome"]
		run, err := s.finishTodo(ctx, a.Owner, args["id"], out, displayName(a)+" through "+as.persona)
		if err != nil {
			var re *rpc.Error
			if errors.As(err, &re) {
				return "error: " + re.Message, true
			}
			return "error: " + err.Error(), true
		}
		word := map[string]string{"done": "done", "skip": "skipped", "cancel": "cancelled"}[out]
		return fmt.Sprintf("Marked the to-do %s %q %s.", run.Id, run.Vars["title"], word), true
	case "change_todo":
		run, err := s.ownTodo(ctx, a.Owner, args["id"])
		if err != nil {
			return "error: " + err.Error(), true
		}
		if finished(run.Status) {
			return fmt.Sprintf("error: the to-do %s has ended; add a new one", run.Id), true
		}
		vars := map[string]string{}
		for k, v := range run.Vars {
			if k == "title" || k == "who" || k == "persona" || k == "stream" || k == "plan" || k == "after" || k == "kind" || k == "due_ms" || k == "remind_ms" || k == "reminded" {
				vars[k] = v
			}
		}
		vars["utterance"], vars["replaces"] = utt.Id, run.Id
		if t := strings.TrimSpace(args["title"]); t != "" {
			vars["title"] = clip(t, 200)
		}
		for _, k := range []string{"due", "remind"} {
			switch v := strings.TrimSpace(args[k]); v {
			case "":
			case "none":
				delete(vars, k+"_ms")
			default:
				if k == "remind" {
					delete(vars, "reminded") // a new reminder time sends it again
				}
				t, err := parseWhen(v, k, loc, now)
				if err != nil {
					return "error: " + k + ": " + err.Error(), true
				}
				vars[k+"_ms"] = strconv.FormatInt(t.UnixMilli(), 10)
			}
		}
		// A run keeps its variables: the change is a new run, and the old
		// one ends. To-dos that wait for the old one wait for the new one.
		nr, err := s.Workflow.Start(ctx, workflowapi.StartRequest{Definition: Todo, Vars: vars, Owner: &a.Owner, Key: ptr(utt.Id + "/change/" + run.Id)})
		if err != nil {
			return "error: " + err.Error(), true
		}
		if _, err := s.Workflow.Cancel(ctx, workflowapi.CancelRequest{Id: run.Id, Reason: ptr("replaced by " + nr.Id)}); err != nil {
			s.Log.Warn("change todo: cancel old", "run", run.Id, "err", err)
		}
		s.moveDependents(ctx, a.Owner, run.Id, nr.Id)
		return fmt.Sprintf("Changed the to-do. It is now %s", describeTodo(nr, nil, loc, now)), true
	}
	return "", false
}

// moveDependents starts again, after the new to-do, the to-dos that waited
// for the old one.
func (s *System) moveDependents(ctx context.Context, owner, old, nu string) {
	runs, err := s.todos(ctx, owner, false)
	if err != nil {
		return
	}
	for _, r := range runs {
		if r.Vars["after"] != old {
			continue
		}
		vars := maps.Clone(r.Vars)
		vars["after"], vars["replaces"] = nu, r.Id
		delete(vars, "after_title")
		if _, err := s.Workflow.Start(ctx, workflowapi.StartRequest{Definition: Todo, Vars: vars, Owner: &owner, Key: ptr("after/" + nu + "/" + r.Id)}); err == nil {
			s.Workflow.Cancel(ctx, workflowapi.CancelRequest{Id: r.Id, Reason: ptr("its predecessor changed to " + nu)})
		}
	}
}

// todos gives the person's to-do runs that are open, and with ended also
// the ones that ended in the last 7 days, oldest first.
func (s *System) todos(ctx context.Context, owner string, ended bool) ([]workflowapi.Run, error) {
	var out []workflowapi.Run
	statuses := []string{"waiting", "running"}
	if ended {
		statuses = append(statuses, "succeeded", "cancelled", "failed")
	}
	week := nowMs() - uint64((7 * 24 * time.Hour).Milliseconds())
	for _, st := range statuses {
		res, err := s.Workflow.ListRuns(ctx, workflowapi.ListRunsRequest{Definition: ptr(Todo), Status: ptr(st), Limit: ptr(uint64(500))})
		if err != nil {
			return nil, err
		}
		for _, r := range res.Runs {
			if r.Owner != owner || finished(r.Status) && r.UpdatedMs < week {
				continue
			}
			// A replaced to-do is not an ended one.
			if r.Status == "cancelled" && strings.Contains(deref(r.Error), ": replaced by ") {
				continue
			}
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(x, y workflowapi.Run) int { return int(int64(x.CreatedMs) - int64(y.CreatedMs)) })
	return out, nil
}

func (s *System) ownTodo(ctx context.Context, owner, id string) (workflowapi.Run, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return workflowapi.Run{}, fmt.Errorf("give a to-do id from list_todos")
	}
	r, err := s.Workflow.GetRun(ctx, workflowapi.GetRunRequest{Id: id})
	if err != nil || r.Owner != owner || r.Definition != Todo {
		return workflowapi.Run{}, fmt.Errorf("no to-do %s; use list_todos", id)
	}
	return r, nil
}

// describeTodo is one line of a to-do for the model.
func describeTodo(r workflowapi.Run, titles map[string]string, loc *time.Location, now time.Time) string {
	v := r.Vars
	what := "to-do"
	if v["kind"] == "reminder" {
		what = "reminder"
	}
	line := fmt.Sprintf("[%s] %s %q", r.Id, what, v["title"])
	if p := v["plan"]; p != "" {
		line += " (plan " + p + ")"
	}
	var parts []string
	if t, ok := msVar(v["remind_ms"]); ok && v["kind"] == "reminder" {
		parts = append(parts, "at "+at(time.UnixMilli(int64(t)), now, loc))
	} else if ok {
		parts = append(parts, "reminder "+at(time.UnixMilli(int64(t)), now, loc))
	}
	if t, ok := msVar(v["due_ms"]); ok {
		parts = append(parts, "due "+at(time.UnixMilli(int64(t)), now, loc))
	}
	if after := v["after"]; after != "" {
		t := firstNonEmpty(titles[after], v["after_title"], after)
		parts = append(parts, "after "+t)
	}
	parts = append(parts, todoState(r, titles, now, loc))
	return line + ": " + strings.Join(parts, "; ")
}

// todoState says where a to-do is, in words.
func todoState(r workflowapi.Run, _ map[string]string, _ time.Time, _ *time.Location) string {
	switch st := stateOf(r); st {
	case "waiting":
		return "waits for the to-do before it"
	case "failed":
		return "failed: " + deref(r.Error)
	default:
		return st
	}
}

// stateOf is the state of a to-do: open, waiting, late, done, skipped,
// sent, cancelled, or failed (contracts/magi.csil, Todo).
func stateOf(r workflowapi.Run) string {
	switch r.Status {
	case "succeeded":
		switch r.Vars["result"] {
		case "skip":
			return "skipped"
		case "reminded":
			return "sent"
		}
		return "done"
	case "cancelled", "failed":
		return r.Status
	}
	switch r.CurrentStep {
	case "blocked", "gate":
		if r.Vars["after"] != "" {
			return "waiting"
		}
	case "late", "waiting", "overdue":
		return "late"
	}
	return "open"
}

// todoOf is the API record of a to-do.
func todoOf(r workflowapi.Run) magiapi.Todo {
	t := magiapi.Todo{Id: r.Id, Kind: "todo", Title: r.Vars["title"], State: stateOf(r), Plan: optional(r.Vars["plan"]),
		After: optional(r.Vars["after"]), CreatedMs: r.CreatedMs, UpdatedMs: r.UpdatedMs}
	if r.Vars["kind"] == "reminder" {
		t.Kind = "reminder"
	}
	if v, ok := msVar(r.Vars["due_ms"]); ok {
		t.DueMs = &v
	}
	if v, ok := msVar(r.Vars["remind_ms"]); ok {
		t.RemindMs = &v
	}
	return t
}

// finishTodo marks a to-do of owner done or skipped, or cancels it. by
// says who did it, for the record of the run.
func (s *System) finishTodo(ctx context.Context, owner, id, outcome, by string) (workflowapi.Run, error) {
	run, err := s.ownTodo(ctx, owner, id)
	if err != nil {
		return run, rpc.NotFound("%v", err)
	}
	if finished(run.Status) {
		return run, rpc.Conflict("the to-do %s has ended already (%s)", run.Id, stateOf(run))
	}
	switch outcome {
	case "cancel":
		return s.Workflow.Cancel(ctx, workflowapi.CancelRequest{Id: run.Id, Reason: ptr("cancelled by " + by)})
	case "done", "skip":
		if run.Status != "waiting" {
			return run, rpc.Conflict("the to-do is between steps; try again in a few seconds")
		}
		return s.Workflow.Decide(ctx, workflowapi.DecideRequest{Run: run.Id, Step: run.CurrentStep, Outcome: outcome, Comment: ptr(outcome + " by " + by)})
	}
	return run, rpc.Invalid("outcome must be done, skip, or cancel")
}

// ListTodos gives the caller's to-dos.
func (m *Service) ListTodos(ctx context.Context, req magiapi.ListTodosRequest) (magiapi.ListTodosResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.ListTodosResponse{}, err
	}
	runs, err := m.S.todos(m.S.AsService(ctx), caller.Subject, deref(req.IncludeEnded))
	if err != nil {
		return magiapi.ListTodosResponse{}, err
	}
	out := magiapi.ListTodosResponse{Todos: []magiapi.Todo{}}
	for _, r := range runs {
		if p := deref(req.Plan); p != "" && !strings.EqualFold(r.Vars["plan"], p) {
			continue
		}
		out.Todos = append(out.Todos, todoOf(r))
	}
	return out, nil
}

// FinishTodo marks a to-do of the caller done or skipped, or cancels it.
func (m *Service) FinishTodo(ctx context.Context, req magiapi.FinishTodoRequest) (magiapi.Todo, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.Todo{}, err
	}
	r, err := m.S.finishTodo(m.S.AsService(ctx), caller.Subject, req.Id, req.Outcome, caller.Subject+" through the API")
	if err != nil {
		return magiapi.Todo{}, err
	}
	if r, err = m.S.Workflow.GetRun(m.S.AsService(ctx), workflowapi.GetRunRequest{Id: r.Id}); err != nil {
		return magiapi.Todo{}, err
	}
	return todoOf(r), nil
}

// parseWhen reads a time that the model gives, in the zone of the
// household. A date alone is 09:00 for a reminder and 17:00 for a due time.
func parseWhen(v, kind string, loc *time.Location, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "."))
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return checkFuture(t, now)
	}
	for _, f := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(f, v, loc); err == nil {
			return checkFuture(t, now)
		}
	}
	if d, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		h := 17
		if kind == "remind" {
			h = 9
		}
		return checkFuture(d.Add(time.Duration(h)*time.Hour), now)
	}
	return time.Time{}, fmt.Errorf("%q is not YYYY-MM-DD HH:MM", v)
}

func checkFuture(t, now time.Time) (time.Time, error) {
	if t.Before(now.Add(-time.Minute)) {
		return t, fmt.Errorf("%s is in the past; the time now is %s", t.Format("2006-01-02 15:04"), now.In(t.Location()).Format("2006-01-02 15:04"))
	}
	return t, nil
}

// at says a time of a to-do: "today 17:00", "tomorrow 09:00", "Tuesday
// 10 November 17:00".
func at(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	y1, m1, d1 := t.Date()
	day := func(d time.Time) bool { y, m, dd := d.Date(); return y == y1 && m == m1 && dd == d1 }
	switch {
	case day(now):
		return "today " + t.Format("15:04")
	case day(now.AddDate(0, 0, 1)):
		return "tomorrow " + t.Format("15:04")
	case day(now.AddDate(0, 0, -1)):
		return "yesterday " + t.Format("15:04")
	case y1 != now.Year():
		return t.Format("Monday 2 January 2006 15:04")
	}
	return t.Format("Monday 2 January 15:04")
}

func msVar(v string) (uint64, bool) {
	t, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	return t, err == nil && t > 0
}

func finished(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

// zone is the time zone of the household (MAGI_TIME_ZONE).
func (s *System) zone() *time.Location {
	if s.Cfg.TimeZone != nil {
		return s.Cfg.TimeZone
	}
	return time.UTC
}
