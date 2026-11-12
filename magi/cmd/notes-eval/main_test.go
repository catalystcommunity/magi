package main

import (
	"testing"

	"github.com/catalystcommunity/magi/magi/compose"
)

func TestAnyOfAndAbsent(t *testing.T) {
	k := Case{Self: "Jamie", Text: "Ezra lost a first tooth today! Maybe we go to Porto in May.",
		Graph: []Check{
			{AnyOf: []Check{{Event: "tooth"}, {Has: &Fact{Subject: "Ezra", Other: "tooth"}}}},
			{Absent: "Porto"},
		}}
	event := []compose.NoteItem{{Op: "event", Subject: "Ezra", SubjectKind: "person", Value: "lost a first tooth"}}
	fact := []compose.NoteItem{{Op: "remember", Subject: "Ezra", SubjectKind: "person", Relation: "milestone", Value: "lost a first tooth"}}
	plan := append(append([]compose.NoteItem{}, event...), compose.NoteItem{Op: "remember", Subject: "me", Relation: "trip", Value: "Porto in May"})
	for name, tc := range map[string]struct {
		items []compose.NoteItem
		ok    bool
	}{"event": {event, true}, "fact": {fact, true}, "none": {nil, false}, "plan recorded": {plan, false}} {
		if ok, why := scoreGraph(k, tc.items); ok != tc.ok {
			t.Errorf("%s: ok %v, want %v (%s)", name, ok, tc.ok, why)
		}
	}
}
