package compose

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
	workflowserver "github.com/catalystcommunity/magi/primitives/workflow/server"
	workflowstore "github.com/catalystcommunity/magi/primitives/workflow/store"
)

func TestParseWhen(t *testing.T) {
	loc, _ := time.LoadLocation("America/Denver")
	now := time.Date(2026, 11, 8, 14, 0, 0, 0, loc)
	for in, want := range map[string]string{
		"2026-11-09 09:00":          "2026-11-09 09:00 MST",
		"2026-11-09T18:30":          "2026-11-09 18:30 MST",
		"2026-11-09":                "2026-11-09 17:00 MST",
		"2026-11-09T16:00:00Z":      "2026-11-09 09:00 MST",
		"2026-11-09T09:00:00-07:00": "2026-11-09 09:00 MST",
	} {
		got, err := parseWhen(in, "due", loc, now)
		if err != nil || got.In(loc).Format("2006-01-02 15:04 MST") != want {
			t.Errorf("%s: %v %v, want %s", in, got.In(loc), err, want)
		}
	}
	if got, _ := parseWhen("2026-11-09", "remind", loc, now); got.Hour() != 9 {
		t.Errorf("date reminder at %v", got)
	}
	for _, bad := range []string{"tomorrow", "2026-11-08 08:00", "9am"} {
		if _, err := parseWhen(bad, "due", loc, now); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if got := at(time.Date(2026, 11, 9, 9, 0, 0, 0, loc), now, loc); got != "tomorrow 09:00" {
		t.Error(got)
	}
	if got := at(time.Date(2026, 11, 17, 9, 0, 0, 0, loc), now, loc); got != "Tuesday 17 November 09:00" {
		t.Error(got)
	}
}

// planSystem is a System with knowledge and a workflow service (memory
// queue, sweep every 50 ms) and the to-do workers.
func planSystem(t *testing.T) (*System, *[]string) {
	s := graphSystem(t)
	ctx, stop := context.WithCancel(context.Background())
	st, err := workflowstore.Open(ctx, testDatabase(t, "compose_wf"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	svc := &workflowserver.Service{Store: st, Queue: &workflowserver.MemQueue{}, ClaimPoll: 20 * time.Millisecond, WatchPoll: 20 * time.Millisecond}
	rs := &rpc.Server{Verifier: testVerifier}
	rs.Register("WorkflowService", workflowapi.WorkflowServiceOps(svc))
	hs := httptest.NewServer(rpc.Mux(rs, nil))
	t.Cleanup(hs.Close)
	t.Cleanup(stop) // first: the workers stop before the server closes
	s.Workflow = workflowapi.NewWorkflowClient(rpc.NewClient(hs.URL, nil))
	go func() {
		for ctx.Err() == nil {
			svc.SweepOnce(ctx)
			time.Sleep(50 * time.Millisecond)
		}
	}()
	var mu sync.Mutex
	var notices []string
	s.noticeHook = func(title, text string) error {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, title+" | "+text)
		return nil
	}
	s.Cfg.TimeZone = time.UTC
	if _, err := s.Workflow.PutDefinition(s.AsService(ctx), todoDefinition()); err != nil {
		t.Fatal(err)
	}
	for q, h := range map[string]Handler{"magi.todo.gate": s.todoGate, "magi.todo.remind": s.todoRemind, "magi.todo.overdue": s.todoOverdue, "magi.todo.finish": s.todoFinish} {
		go s.Work(ctx, q, h)
	}
	return s, &notices
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func TestTodoLifecycle(t *testing.T) {
	s, notices := planSystem(t)
	ctx := s.AsService(context.Background())
	a := workflowapi.Assignment{Owner: graphOwner, Vars: map[string]string{"person_name": "Kim"}}
	as := conversationLens{persona: Assistant}
	ts := &turnState{}
	tool := func(id, name string, args map[string]string) string {
		out, ok := s.planTool(ctx, a, as, conversationapi.Utterance{Id: id, Stream: "str_x"}, ts, name, args)
		if !ok {
			t.Fatalf("%s not a plan tool", name)
		}
		return out
	}
	rfc := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
	run := func(id string) workflowapi.Run {
		r, err := s.Workflow.GetRun(ctx, workflowapi.GetRunRequest{Id: id})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	idOf := func(out string) string {
		i := strings.Index(out, "[")
		j := strings.Index(out, "]")
		if i < 0 || j < i {
			t.Fatalf("no id in %q", out)
		}
		return out[i+1 : j]
	}
	count := func(prefix string) int {
		n := 0
		for _, x := range *notices {
			if strings.HasPrefix(x, prefix) {
				n++
			}
		}
		return n
	}

	// A to-do with a reminder and a due time: reminder, late notice, done.
	out := tool("utt_1", "add_todo", map[string]string{"title": "Call the plumber", "remind": rfc(time.Second), "due": rfc(2 * time.Second)})
	if !strings.HasPrefix(out, "To-do added: [") {
		t.Fatal(out)
	}
	call := idOf(out)
	waitFor(t, "reminder", func() bool { return count("Reminder: Call the plumber") == 1 })
	waitFor(t, "late notice", func() bool { return count("Late: Call the plumber") == 1 && run(call).CurrentStep == "late" })
	if out := tool("utt_2", "list_todos", nil); !strings.Contains(out, "Call the plumber") || !strings.Contains(out, "late") {
		t.Fatalf("list: %s", out)
	}
	if out := tool("utt_2", "finish_todo", map[string]string{"id": call, "outcome": "done"}); !strings.Contains(out, "done") {
		t.Fatal(out)
	}
	waitFor(t, "done", func() bool { return run(call).Status == "succeeded" && run(call).Vars["result"] == "done" })
	eps, err := s.Knowledge.Search(ctx, knowledgeapi.SearchRequest{Text: ptr("plumber"), Types: []string{"episode"}, ActFor: ptr(graphOwner)})
	if err != nil || len(eps.Episodes) != 1 || deref(eps.Episodes[0].Outcome) != "done" {
		t.Fatalf("episode: %+v %v", eps.Episodes, err)
	}

	// A plan in order: the second step waits for the first.
	first := idOf(tool("utt_3", "add_todo", map[string]string{"title": "Book the venue", "plan": "Party"}))
	second := idOf(tool("utt_3", "add_todo", map[string]string{"title": "Send invitations", "plan": "Party", "after": "previous"}))
	waitFor(t, "blocked", func() bool { return run(second).CurrentStep == "blocked" && run(first).CurrentStep == "do" })
	if out := tool("utt_4", "list_todos", map[string]string{"plan": "party"}); !strings.Contains(out, "waits for the to-do before it") || strings.Contains(out, "plumber") {
		t.Fatalf("plan list: %s", out)
	}
	tool("utt_4", "finish_todo", map[string]string{"id": first, "outcome": "done"})
	waitFor(t, "second step open", func() bool { return run(second).CurrentStep == "do" })

	// A changed to-do is a new run; the old one ends and does not show.
	out = tool("utt_5", "change_todo", map[string]string{"id": second, "title": "Send the invitations by email"})
	nu := idOf(out)
	if nu == second || run(second).Status != "cancelled" {
		t.Fatalf("change: %s, old %s", out, run(second).Status)
	}
	if out := tool("utt_5", "list_todos", map[string]string{"include_ended": "true"}); strings.Contains(out, "\"Send invitations\"") || !strings.Contains(out, "by email") {
		t.Fatalf("list after change: %s", out)
	}

	// A reminder ends when it is sent.
	rem := idOf(tool("utt_6", "set_reminder", map[string]string{"text": "Take the bins out", "at": rfc(time.Second)}))
	waitFor(t, "reminder sent", func() bool { return run(rem).Status == "succeeded" && count("Reminder: Take the bins out") == 1 })

	// Errors the model can repair.
	if out := tool("utt_7", "add_todo", map[string]string{"title": "x", "due": "tomorrow"}); !strings.HasPrefix(out, "error: due:") {
		t.Fatal(out)
	}
	if out := tool("utt_7", "finish_todo", map[string]string{"id": "run_nope", "outcome": "done"}); !strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	// The same call twice in one turn makes one to-do.
	x1 := idOf(tool("utt_8", "add_todo", map[string]string{"title": "Renew passport"}))
	x2 := idOf(tool("utt_8", "add_todo", map[string]string{"title": "Renew passport"}))
	if x1 != x2 {
		t.Fatalf("two runs %s %s", x1, x2)
	}
}
