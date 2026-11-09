package compose

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// devConversation answers ListDevices and ListAnnotations.
type devConversation struct {
	annConversation
	devices []conversationapi.Device
}

func (f *devConversation) ListDevices(_ context.Context, r conversationapi.ListDevicesRequest) (conversationapi.ListDevicesResponse, error) {
	return conversationapi.ListDevicesResponse{Devices: f.devices}, nil
}

func TestFindDevice(t *testing.T) {
	fc := &devConversation{devices: []conversationapi.Device{
		{Id: "web-1", Kind: "browser", Name: "Firefox on Linux", Can: []string{}},
		{Id: "phone-1", Kind: "phone", Name: "Pixel 9", Can: []string{}},
		{Id: "desk-1", Kind: "desktop", Name: "Workstation", Can: []string{}},
	}}
	sys := &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sys.Conversation = conversationClient(t, fc)
	sys.Cfg.ServiceToken = testToken
	for name, want := range map[string]string{
		"phone": "phone-1", "my phone": "phone-1", "Pixel 9": "phone-1", "the laptop": "desk-1",
		"firefox": "web-1", "browser": "web-1", "all": "", "": "", "everywhere": "",
	} {
		got, err := sys.findDevice(context.Background(), "user:a@example.local", name)
		if err != nil || got != want {
			t.Errorf("findDevice(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := sys.findDevice(context.Background(), "user:a@example.local", "toaster"); err == nil || !strings.Contains(err.Error(), "Pixel 9 (phone)") {
		t.Fatalf("unknown device: %v", err)
	}
}

func TestAskedBefore(t *testing.T) {
	fc := &devConversation{}
	sys := &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sys.Conversation = conversationClient(t, fc)
	sys.Cfg.ServiceToken = testToken
	log, _ := json.Marshal(turnLog{Calls: []map[string]any{{"tool": "decide_task", "args": map[string]string{"task": "run_1", "outcome": "approve"},
		"result": "confirmation needed: tell the person that you will approve task run_1"}}})
	ask := conversationapi.Utterance{Id: "utt_ask", AuthorKind: "human", CreatedMs: nowMs()}
	reply := conversationapi.Utterance{Id: "utt_reply", AuthorKind: "persona"}
	yes := conversationapi.Utterance{Id: "utt_yes", AuthorKind: "human", Text: "yes, approve it"}
	hist := []conversationapi.Utterance{ask, reply, yes}
	ctx := context.Background()
	if askedBefore(ctx, sys, hist, yes, "run_1", "approve") {
		t.Fatal("no annotation: not asked")
	}
	// An annotation that the person wrote does not count.
	fc.anns = []conversationapi.Annotation{{Utterance: "utt_ask", Kind: "interpretation", Author: "user:a@example.local", Body: string(log)}}
	if askedBefore(ctx, sys, hist, yes, "run_1", "approve") {
		t.Fatal("annotation of a person counts")
	}
	fc.anns[0].Author = "service:magi"
	if !askedBefore(ctx, sys, hist, yes, "run_1", "approve") {
		t.Fatal("asked in the turn before")
	}
	if askedBefore(ctx, sys, hist, yes, "run_1", "reject") || askedBefore(ctx, sys, hist, yes, "run_2", "approve") {
		t.Fatal("another outcome or task")
	}
	// The current message itself is not an earlier request.
	if askedBefore(ctx, sys, []conversationapi.Utterance{reply, ask}, ask, "run_1", "approve") {
		t.Fatal("the same turn")
	}
}

type personaConversation struct {
	conversationapi.ConversationService
}

func (personaConversation) ListPersonas(context.Context, conversationapi.ListPersonasRequest) (conversationapi.ListPersonasResponse, error) {
	return conversationapi.ListPersonasResponse{Personas: []conversationapi.Persona{{Id: "persona:butler", Name: "Jeeves"}}}, nil
}

func TestAddressed(t *testing.T) {
	sys := &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sys.Conversation = conversationClient(t, personaConversation{})
	for text, want := range map[string]string{
		"Computer, what time is it?":        Persona,
		"Engineering: status of the tasks":  Engineering,
		"hey engineering manager, any news": Engineering,
		"Jeeves, the door please":           "persona:butler",
		"computer science is hard, right?":  "",
		"What time is it, computer?":        "",
		"Hello":                             "",
	} {
		if got := sys.addressed(testCtx, text); got != want {
			t.Errorf("addressed(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestFalseClaim(t *testing.T) {
	em := profiles[Engineering].tools
	started := []map[string]any{{"tool": "start_code_change", "result": "Task run_1 started."}}
	failed := []map[string]any{{"tool": "start_code_change", "result": "error: no runner"}}
	for _, tc := range []struct {
		reply string
		calls []map[string]any
		want  string
	}{
		{`Task started: "Add a note". I'll tell you when it's ready.`, nil, "start_code_change"},
		{`Task started: "Add a note".`, started, ""},
		{`Task started: "Add a note".`, failed, "start_code_change"},
		{"I've sent the summary to your phone.", nil, "send_notice"},
		{"Three tasks are running and one waits for review.", nil, ""},
		{"I approved it.", nil, "decide_task"},
		{"Should I approve it?", nil, ""},
	} {
		if got := falseClaim(tc.reply, tc.calls, em); got != tc.want {
			t.Errorf("falseClaim(%q) = %q, want %q", tc.reply, got, tc.want)
		}
	}
	// A tool that the persona does not have is not checked.
	if got := falseClaim("I approved it.", nil, profiles[Persona].tools); got != "" {
		t.Errorf("magi persona: %q", got)
	}
	// A correction of a fact without a record is recorded with remember
	// (by the note taker): "I've corrected" is true.
	pt := profiles[Persona].tools
	noted := []map[string]any{{"tool": "remember", "result": "Recorded: router located-in garage", "by": "note-taker"}}
	if got := falseClaim("I've corrected that. The router is in the garage.", noted, pt); got != "" {
		t.Errorf("correction by remember: %q", got)
	}
	if got := falseClaim("I've corrected that.", []map[string]any{{"tool": "remember", "result": "Not recorded: no fact"}}, pt); got != "change_knowledge" {
		t.Errorf("failed remember: %q", got)
	}
}

func TestAskedInReply(t *testing.T) {
	run := workflowapi.Run{Id: "run_9", Vars: map[string]string{"title": "Add a note"}}
	ask := conversationapi.Utterance{Id: "u1", AuthorKind: "persona", Text: `I will reject the task "Add a note". Please confirm.`}
	yes := conversationapi.Utterance{Id: "u2", AuthorKind: "human", Text: "yes"}
	if !askedInReply([]conversationapi.Utterance{ask, yes}, yes, run, "reject") {
		t.Fatal("asked in the reply before")
	}
	if askedInReply([]conversationapi.Utterance{ask, yes}, yes, run, "approve") {
		t.Fatal("another outcome")
	}
	other := workflowapi.Run{Id: "run_8", Vars: map[string]string{"title": "Other"}}
	if askedInReply([]conversationapi.Utterance{ask, yes}, yes, other, "reject") {
		t.Fatal("another task")
	}
	// A message of the person in between: the question was not the last word.
	mid := conversationapi.Utterance{Id: "u3", AuthorKind: "human", Text: "hmm"}
	if askedInReply([]conversationapi.Utterance{ask, mid, yes}, yes, run, "reject") {
		t.Fatal("not the reply just before")
	}
}

func TestFalseClaimTodos(t *testing.T) {
	at := profiles[Assistant].tools
	added := []map[string]any{{"tool": "add_todo", "result": "To-do added: [run_1] ..."}}
	reminder := []map[string]any{{"tool": "set_reminder", "result": "Reminder added: [run_2] ..."}}
	changed := []map[string]any{{"tool": "change_todo", "result": "Changed the to-do."}}
	for _, tc := range []struct {
		reply string
		calls []map[string]any
		want  string
	}{
		{"I've created a plan for the garage sale. The steps are to sort the boxes, then price everything.", nil, "add_todo"},
		{"I've created a plan for the garage sale.", added, ""},
		{"I've added \"Renew passport\" to your to-do list.", nil, "add_todo"},
		{"OK. I've set a reminder for you to call the vet tomorrow at 9:00 AM.", nil, "set_reminder"},
		{"OK. I've set a reminder for you to call the vet tomorrow at 9:00 AM.", reminder, ""},
		{"I've set a reminder for 9.", added, ""},
		{"I've changed the reminder to 10.", changed, ""},
		{"I've marked it done.", nil, "finish_todo"},
		{"You have a reminder to call the vet tomorrow.", nil, ""},
	} {
		if got := falseClaim(tc.reply, tc.calls, at); got != tc.want {
			t.Errorf("falseClaim(%q) = %q, want %q", tc.reply, got, tc.want)
		}
	}
}
