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
		{noteItem{Op: "remember", Subject: "chores calendar", Relation: "located-in", Object: "chores calendar", Value: "Nextcloud"}, "remember",
			map[string]string{"subject": "chores calendar", "relation": "located-in", "object": "Nextcloud", "value": ""}},
		{noteItem{Op: "remember", Subject: "me", Relation: "works-at", Object: "Brightline Logistics", Value: "operations manager"}, "remember",
			map[string]string{"subject": "me", "subject_kind": "", "relation": "works-at", "object": "Brightline Logistics", "object_kind": "", "value": ""}},
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
	for _, want := range []string{supersededPrefix, reason, "retracts it", "Do not name or repeat what was forgotten"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q has no %q", note, want)
		}
	}
	// A retracted record is not given back to the model.
	if strings.Contains(note, "hosted-in Nextcloud") {
		t.Errorf("note %q gives the retracted record", note)
	}
	fix := map[string]knowledgeapi.Change{"clm_loc": {Id: "c3", Record: "clm_loc", Operation: "fix", AtMs: 200}}
	if n := supersededNote(old, fix); !strings.Contains(n, "The record was: chores calendar hosted-in Nextcloud") {
		t.Errorf("fix note %q has no record", n)
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

func TestRetractMustNameRecord(t *testing.T) {
	notes := "ent_f  [calendar] family calendar\n      clm_f  family calendar located-in Nextcloud\n      clm_s  kids-sports-calendar located-in Nextcloud"
	for _, c := range []struct {
		it   noteItem
		want bool
	}{
		{noteItem{Op: "retract", Id: "clm_f", Object: "chores calendar"}, false},
		{noteItem{Op: "retract", Id: "ent_f", Object: "the family calendar"}, true},
		{noteItem{Op: "retract", Id: "clm_s", Object: "kids' sports calendars"}, true},
		{noteItem{Op: "retract", Id: "clm_s", Subject: "me"}, true},
		{noteItem{Op: "retract", Id: "clm_x", Object: "router"}, false},
	} {
		if got := namesRecord(notes, c.it); got != c.want {
			t.Errorf("%+v: %v, want %v", c.it, got, c.want)
		}
	}
}

func TestDropSelfCopies(t *testing.T) {
	items := []noteItem{
		{Op: "remember", Subject: "Dana", Relation: "birthday", Value: "March 3"},
		{Op: "remember", Subject: "me", Relation: "birthday", Value: "March 3"},
	}
	if got := dropSelfCopies(items, "Dana's birthday is March 3."); len(got) != 1 || got[0].Subject != "Dana" {
		t.Fatalf("copy kept: %+v", got)
	}
	if got := dropSelfCopies(items, "Dana and I both have our birthday on March 3."); len(got) != 2 {
		t.Fatalf("shared fact dropped: %+v", got)
	}
}

func TestRetarget(t *testing.T) {
	notes := `- [ent_me] me (person)
  - [clm_r] me router Ubiquiti Dream Machine
  - [clm_d] me dentist Dr. Rivera
- [ent_u] Ubiquiti Dream Machine (device)
  - [clm_b] Ubiquiti Dream Machine located-in basement closet
- [ent_dana] Dana (person)
  - [clm_t] Dana lives-in Tucson`
	for _, c := range []struct{ text, id, want string }{
		{"Correction: the router is in the garage, not the basement. It was always in the garage.", "clm_r", "clm_b"},
		{"the router is in the garage, not the basement", "clm_b", "clm_b"},
		{"That's wrong: Dana lives in Phoenix, not Tucson.", "clm_t", "clm_t"},
		{"That's wrong: Dana lives in Phoenix, not Tucson.", "clm_d", "clm_t"},
		{"My dentist is Dr. Chen now.", "clm_d", "clm_d"},
		{"not the attic", "clm_r", "clm_r"},
	} {
		if got := retarget(notes, c.text, c.id); got != c.want {
			t.Errorf("%q from %s: %s, want %s", c.text, c.id, got, c.want)
		}
	}
}

func TestKeepTitles(t *testing.T) {
	got := keepTitles([]noteItem{{Op: "remember", Subject: "me", Relation: "works-at", Object: "Brightline Logistics", Value: "operations manager"}})
	if len(got) != 2 || got[1].Relation != "job" || got[1].Value != "operations manager" || got[1].Object != "" {
		t.Fatalf("%+v", got)
	}
	if _, args := got[0].toolCall(""); args["object"] != "Brightline Logistics" || args["value"] != "" {
		t.Fatalf("works-at args %v", args)
	}
}

func TestChangeOp(t *testing.T) {
	for _, c := range []struct{ text, op, want string }{
		{"Correction: the Synology is in the hall closet, not the office closet.", "update", "fix"},
		{"Oops, our anniversary is June 15, not June 14.", "update", "fix"},
		{"We moved our taxes to Ben Okoye at Summit CPA this year.", "fix", "update"},
		{"Leo switched teams. He plays for the Chandler Flyers now.", "update", "update"},
		{"I misspoke, it changed last year", "fix", "fix"},
		{"Dana lives in Phoenix, not Tucson.", "fix", "fix"},
		{"There is no chores calendar.", "retract", "retract"},
	} {
		if got := changeOp(c.text, c.op); got != c.want {
			t.Errorf("%q %s: %s, want %s", c.text, c.op, got, c.want)
		}
	}
}

func TestLabelOf(t *testing.T) {
	notes := "- [ent_g] guest wifi (thing), also called guest network\n  - [clm_1] Chamberlain on guest wifi\n- [ent_s] Synology NAS (device)"
	if got := labelOf(notes, "ent_g"); got != "guest wifi" {
		t.Fatal(got)
	}
	if got := labelOf(notes, "ent_s"); got != "Synology NAS" {
		t.Fatal(got)
	}
	if labelOf(notes, "ent_x") != "" {
		t.Fatal("unknown id")
	}
}

func TestAboutSubject(t *testing.T) {
	for _, c := range []struct {
		line, subject string
		want          bool
	}{
		{"Jamie works-at Juniper Labs (since 3 January 2024)", "me", true},
		{"Jamie works-at Juniper Labs", "Alex", false},
		{"Alex works-from-home Mondays", "Alex", true},
		{"019f0c22-ee82@example.local manager Rafael", "me", true},
		{"Pepper vet Dr. Ito", "pepper", true},
		{"Ruby coach Jen Park", "Theo", false},
	} {
		if got := aboutSubject(c.line, c.subject, "Jamie", "019f0c22-ee82@example.local"); got != c.want {
			t.Errorf("%q / %q: %v", c.line, c.subject, got)
		}
	}
}
