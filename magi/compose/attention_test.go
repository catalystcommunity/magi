package compose

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
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
