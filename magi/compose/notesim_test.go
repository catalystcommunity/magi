package compose

import "testing"

const simNotes = `- [ent_self] Kai (person), also called me, myself
  - [clm_home] Kai lives-in Pine Street in Denver (since 3 May 2024)
  - [clm_sis] Kai sister Maya Chen
- [ent_maya] Maya Chen (person), also called my sister
  - [clm_mjob] Maya Chen works-at Copperleaf
- [ent_bob] Robert Holt (person), also called Bob
  - [clm_bob] Robert Holt lives-in Austin`

func TestNoteGraph(t *testing.T) {
	g := ParseNotes(simNotes, "Kai")
	if g.Self.Id != "ent_self" || len(g.Claims) != 4 || g.Claims[0].Relation != "lives-in" || g.Claims[0].Other != "Pine Street in Denver" {
		t.Fatalf("parse: %+v %+v", g.Self, g.Claims[0])
	}
	if g.Find("Bob") == nil || g.Find("Bob").Id != "ent_bob" || g.Find("my sister").Id != "ent_maya" || g.Find("Sam") != nil {
		t.Fatal("find")
	}
	// An update of the alias's fact, a remember with words of a change, an
	// end, a new person, and an event.
	g.Apply([]NoteItem{
		{Op: "update", Id: "clm_bob", Value: "Chicago"},
		{Op: "remember", Subject: "Maya Chen", Relation: "works-at", Object: "Juniper Labs"},
		{Op: "end", Id: "clm_home"},
		{Op: "remember", Subject: "Sam", Relation: "lives-in", Value: "Boulder"},
		{Op: "event", Subject: "me", Value: "back from Porto"},
	}, "Bob moved to Chicago, and my sister works at Juniper Labs now.")
	if !g.Has("Bob", []string{"lives"}, "Chicago") || g.Has("Robert Holt", []string{"lives"}, "Austin") {
		t.Error("alias update")
	}
	if !g.Has("Maya Chen", []string{"works-at"}, "Juniper") || g.Has("my sister", []string{"works-at"}, "Copperleaf") {
		t.Error("change by remember")
	}
	if g.Has("me", []string{"lives"}, "") {
		t.Error("end")
	}
	if e := g.Find("Sam"); e == nil || !e.New {
		t.Error("new entity")
	}
	if len(g.Events) != 1 {
		t.Error("event")
	}
	// Without words of a change, a second value is not recorded.
	g2 := ParseNotes(simNotes, "Kai")
	g2.Apply([]NoteItem{{Op: "remember", Subject: "Maya Chen", Relation: "works-at", Object: "Juniper Labs"}}, "Maya works at Juniper Labs.")
	if g2.Has("Maya Chen", nil, "Juniper") {
		t.Error("not recorded")
	}
}
