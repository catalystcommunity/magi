package compose

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

func TestNoteItemToolCall(t *testing.T) {
	cases := []struct {
		in   noteItem
		tool string
		want map[string]string
	}{
		{noteItem{Op: "remember", Subject: "me", Relation: "sister", Object: "Dana", ObjectKind: "person"}, "remember",
			map[string]string{"subject": "me", "relation": "sister", "object": "Dana", "object_kind": "person"}},
		{noteItem{Op: "remember", Subject: "Dana", Relation: "birthday", Value: "March 3", Also: true}, "remember",
			map[string]string{"subject": "Dana", "relation": "birthday", "value": "March 3", "also": "true"}},
		{noteItem{Op: "remember", Subject: "me", Relation: "dentist"}, "", nil},
		{noteItem{Op: "fix", Id: "clm_x", Value: "Phoenix", Field: "object"}, "change_knowledge",
			map[string]string{"id": "clm_x", "operation": "fix", "value": "Phoenix", "field": "object", "reason": "r"}},
		{noteItem{Op: "update", Id: "Dr. Rivera", Value: "Dr. Chen"}, "", nil},
		{noteItem{Op: "update", Id: "clm_x"}, "", nil},
		{noteItem{Op: "retract", Id: "ent_y"}, "change_knowledge", map[string]string{"id": "ent_y", "operation": "retract", "reason": "r"}},
	}
	for i, c := range cases {
		tool, args := c.in.toolCall("r")
		if tool != c.tool {
			t.Errorf("%d: tool %q, want %q", i, tool, c.tool)
			continue
		}
		for k, v := range c.want {
			if args[k] != v {
				t.Errorf("%d: %s = %q, want %q", i, k, args[k], v)
			}
		}
		for k, v := range args {
			if v != "" && c.want[k] == "" {
				t.Errorf("%d: unexpected %s = %q", i, k, v)
			}
		}
	}
}

func TestRecordOf(t *testing.T) {
	notes := "ent_a  [person] me\n      clm_b  me chores-calendar-location Nextcloud"
	if got := recordOf(notes, "clm_b"); got != " (the record was: clm_b  me chores-calendar-location Nextcloud)" {
		t.Fatalf("got %q", got)
	}
	if recordOf(notes, "clm_x") != "" || recordOf(notes, "") != "" {
		t.Fatal("no match must give nothing")
	}
}

// TestLastChange: only operations that can make an earlier answer wrong
// count; a create or a dispute does not.
func TestLastChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cbor")
		w.Write(knowledgeapi.EncodeTimelineResponse(knowledgeapi.TimelineResponse{Episodes: []knowledgeapi.Episode{}, Claims: []knowledgeapi.Claim{},
			Changes: []knowledgeapi.Change{{Id: "c1", Operation: "create", AtMs: 50}, {Id: "c2", Operation: "retract", AtMs: 9},
				{Id: "c3", Operation: "dispute", AtMs: 70}, {Id: "c4", Operation: "fix", AtMs: 30}}}))
	}))
	defer srv.Close()
	sys := New(Config{Knowledge: srv.URL, ServiceToken: "t"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if got := sys.lastChange(context.Background(), "user:a", 1); got != 30 {
		t.Fatalf("got %d, want 30", got)
	}
}
