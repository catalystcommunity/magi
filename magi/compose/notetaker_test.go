package compose

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
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
		// Object and value: the fact is about the object (eval round0 s05).
		{noteItem{Op: "remember", Subject: "me", SubjectKind: "person", Relation: "located-in", Object: "kids sports calendar", ObjectKind: "calendar", Value: "Nextcloud"}, "remember",
			map[string]string{"subject": "kids sports calendar", "subject_kind": "calendar", "relation": "located-in", "object": "Nextcloud"}},
		{noteItem{Op: "remember", Subject: "Dana", Relation: "lives-in", Object: "Tucson", ObjectKind: "place", Value: "a city"}, "remember",
			map[string]string{"subject": "Dana", "relation": "lives-in", "object": "Tucson", "object_kind": "place"}},
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

// TestRecordChanges: only operations that can make an earlier answer wrong
// count, and a retract also covers the facts of a retracted entity.
func TestRecordChanges(t *testing.T) {
	after := `{"records":["ent_cal","clm_loc"],"erased":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cbor")
		w.Write(knowledgeapi.EncodeTimelineResponse(knowledgeapi.TimelineResponse{Episodes: []knowledgeapi.Episode{}, Claims: []knowledgeapi.Claim{},
			Changes: []knowledgeapi.Change{{Id: "c1", Record: "clm_new", Operation: "create", AtMs: 50},
				{Id: "c2", Record: "ent_cal", Operation: "retract", After: &after, AtMs: 9},
				{Id: "c3", Record: "clm_x", Operation: "dispute", AtMs: 70}, {Id: "c4", Record: "clm_y", Operation: "fix", AtMs: 30}}}))
	}))
	defer srv.Close()
	sys := New(Config{Knowledge: srv.URL, ServiceToken: "t"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	changes, last := sys.recordChanges(context.Background(), "user:a", 1)
	if last != 30 {
		t.Fatalf("last %d, want 30", last)
	}
	for id, want := range map[string]string{"ent_cal": "c2", "clm_loc": "c2", "clm_y": "c4"} {
		if changes[id].Id != want {
			t.Errorf("%s: change %q, want %q", id, changes[id].Id, want)
		}
	}
	if _, ok := changes["clm_new"]; ok {
		t.Error("a create must not count")
	}
	if _, ok := changes["clm_x"]; ok {
		t.Error("a dispute must not count")
	}
}

// TestSupersededNote: an earlier answer that used a record that changed
// after it is replaced with a note of the change; other answers are not.
func TestSupersededNote(t *testing.T) {
	kc := "- [ent_cal] chores calendar (calendar)\n  - [clm_loc] chores calendar hosted-in Nextcloud\n- [ent_fam] family calendar (calendar)"
	refs := recordRefs(kc)
	if len(refs) != 3 || refs[1].Ref != "clm_loc" || *refs[1].Title != "chores calendar hosted-in Nextcloud" {
		t.Fatalf("refs %+v", refs)
	}
	reason := "There is no chores calendar. Forget it."
	changes := map[string]knowledgeapi.Change{
		"ent_cal": {Id: "c2", Record: "ent_cal", Operation: "retract", Reason: &reason, AtMs: 200},
		"clm_loc": {Id: "c2", Record: "ent_cal", Operation: "retract", Reason: &reason, AtMs: 200},
	}
	old := conversationapi.Utterance{CreatedMs: 100, Attachments: refs, Text: "Family, sports, and chores."}
	note := supersededNote(old, changes)
	for _, want := range []string{supersededPrefix, reason, "retracts it", "chores calendar (calendar); chores calendar hosted-in Nextcloud"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q has no %q", note, want)
		}
	}
	if strings.Contains(note, old.Text) {
		t.Error("the note must not repeat the old answer")
	}
	if supersededNote(conversationapi.Utterance{CreatedMs: 300, Attachments: refs}, changes) != "" {
		t.Error("an answer after the change is current")
	}
	if supersededNote(conversationapi.Utterance{CreatedMs: 100, Attachments: recordRefs("- [ent_fam] family calendar (calendar)")}, changes) != "" {
		t.Error("an answer that did not use a changed record is current")
	}
	if got := supersededCopy.ReplaceAllString("Sure. "+note, ""); got != "Sure. " {
		t.Errorf("copied note stays: %q", got)
	}
	many := append(recordRefs(kc), recordRefs(kc)...)
	many = append(many, conversationapi.Ref{Kind: "workflow-run", Ref: "run_1"})
	if got := capRecordRefs(many, 2); len(got) != 3 || got[2].Kind != "workflow-run" {
		t.Errorf("cap %+v", got)
	}
}
