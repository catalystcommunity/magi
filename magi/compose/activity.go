package compose

// The working signal: a persona turn notes each step (persona.go), and
// clients read the newest note while they wait for the reply.

import (
	"context"
	"strings"

	"github.com/catalystcommunity/magi/lib/rpc"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// activityOf says what a tool call does, in a few plain words.
func activityOf(tool string, args map[string]string) string {
	words := map[string]string{
		"recall": "looking in the notes", "remember": "taking a note", "change_knowledge": "changing a note",
		"search_conversation": "searching the conversation", "web_search": "searching the web", "read_page": "reading a web page",
		"calendar_events": "checking the calendar", "find_events": "searching the calendar", "create_event": "putting it on the calendar",
		"change_event": "changing the event", "cancel_event": "removing the event", "free_busy": "checking free time",
		"longhouse_projects": "reading the Longhouse projects", "longhouse_tasks": "reading the Longhouse tasks",
		"add_longhouse_task": "adding a Longhouse task", "update_longhouse_task": "changing a Longhouse task", "add_longhouse_project": "making a Longhouse project",
		"list_todos": "checking the to-do list", "add_todo": "adding a to-do", "set_reminder": "setting a reminder",
		"finish_todo": "changing a to-do", "change_todo": "changing a to-do",
		"list_tasks": "checking the tasks", "task_status": "reading the task", "task_diff": "reading the change",
		"start_code_change": "starting a task", "send_notice": "sending a notice", "connect_account": "making a link",
	}
	w, ok := words[tool]
	if !ok {
		w = "working"
	}
	if t := strings.TrimSpace(firstNonEmpty(args["text"], args["project"])); t != "" && (tool == "web_search" || tool == "recall" || tool == "find_events" || tool == "longhouse_tasks") {
		w += ": " + clip(t, 60)
	}
	return w
}

func (m *Service) Activity(ctx context.Context, req magiapi.ActivityRequest) (magiapi.Activity, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.Activity{}, err
	}
	s := m.S
	runs, err := s.Workflow.ListRuns(AsCaller(ctx), workflowapi.ListRunsRequest{Status: ptr("running"), Definition: ptr(PersonaTurn), Limit: ptr(uint64(20))})
	if err != nil {
		return magiapi.Activity{}, err
	}
	var best *workflowapi.Run
	for i, r := range runs.Runs {
		if r.Owner != caller.Subject {
			continue
		}
		if p := deref(req.Persona); p != "" && personaOr(&p) != personaOr(ptr(r.Vars["persona"])) {
			continue
		}
		if best == nil || r.CreatedMs > best.CreatedMs {
			best = &runs.Runs[i]
		}
	}
	if best == nil {
		return magiapi.Activity{Working: false}, nil
	}
	out := magiapi.Activity{Working: true, Persona: ptr(personaOr(ptr(best.Vars["persona"]))), Message: ptr(best.Vars["utterance"]),
		StartedMs: ptr(best.CreatedMs), UpdatedMs: ptr(best.UpdatedMs), Text: ptr("waiting for its turn")}
	if n := len(best.History); n > 0 {
		h := best.History[n-1]
		if h.Status == "claimed" {
			out.Text = ptr("reading the message")
		}
		if k := len(h.Progress); k > 0 {
			out.Text, out.UpdatedMs = ptr(h.Progress[k-1].Note), ptr(h.Progress[k-1].AtMs)
		}
	}
	return out, nil
}
