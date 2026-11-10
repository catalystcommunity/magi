package compose

import (
	"regexp"
	"strconv"
	"strings"
)

// The note-taker benchmark (evals/notetaker) scores the graph that the items
// make, not only the items: "Bob moved to Austin" is right as an update of
// the fact of Robert Chen, or as an end and a new fact, but wrong as a new
// person "Bob". NoteGraph is the graph of the notes, and Apply applies the
// items as applyNotes and the knowledge store do (entityFor, sameEntity,
// remember, change_knowledge), without a store.

// SimEntity is an entity of the graph.
type SimEntity struct {
	Id, Label, Kind string
	Aliases         []string
	New             bool
}

// SimClaim is a fact of the graph. Other is the label of the object or the
// value.
type SimClaim struct {
	Id, Subject, Relation, Other string
	Current, New, Changed        bool
}

// SimEvent is a recorded event.
type SimEvent struct{ Subject, Title string }

// NoteGraph is the graph of the notes and of the applied items.
type NoteGraph struct {
	Self     *SimEntity
	Entities []*SimEntity
	Claims   []*SimClaim
	Events   []SimEvent
	Log      []string
	n        int
}

var (
	simEntity = regexp.MustCompile(`^- \[((?:ent|nod)_[^\]]+)\] (.+?) \(([^)]*)\)(?:, also called (.+))?$`)
	simClaim  = regexp.MustCompile(`^- \[(clm_[^\]]+)\] (.+)$`)
	simSince  = regexp.MustCompile(`\s+\((?:since|until|from)[^)]*\)$`)
)

// ParseNotes reads the notes (the text of describeKnowledge). self is the
// name of the person; the entity with the alias "me" is the self.
func ParseNotes(notes, self string) *NoteGraph {
	g := &NoteGraph{}
	var cur *SimEntity
	for _, l := range strings.Split(notes, "\n") {
		t := strings.TrimSpace(l)
		if m := simEntity.FindStringSubmatch(t); m != nil && !strings.HasPrefix(l, " ") {
			e := &SimEntity{Id: m[1], Label: m[2], Kind: m[3]}
			if m[4] != "" {
				for _, a := range strings.Split(m[4], ",") {
					e.Aliases = append(e.Aliases, strings.TrimSpace(a))
				}
			}
			g.Entities = append(g.Entities, e)
			cur = e
			for _, a := range e.Aliases {
				if isSelf(a) {
					g.Self = e
				}
			}
			continue
		}
		if m := simClaim.FindStringSubmatch(t); m != nil && cur != nil {
			rest := strings.TrimSpace(strings.TrimPrefix(m[2], cur.Label))
			current := !strings.Contains(rest, "(until ") && !strings.Contains(rest, "(ended")
			rest = simSince.ReplaceAllString(rest, "")
			rel, other, _ := strings.Cut(rest, " ")
			g.Claims = append(g.Claims, &SimClaim{Id: m[1], Subject: cur.Id, Relation: strings.ToLower(rel), Other: strings.TrimSpace(other), Current: current})
		}
	}
	if g.Self == nil {
		g.Self = &SimEntity{Id: "ent_self", Label: self, Kind: "person", Aliases: []string{"me", "myself"}}
		g.Entities = append([]*SimEntity{g.Self}, g.Entities...)
	} else if self != "" && !strings.EqualFold(g.Self.Label, self) {
		g.Self.Aliases = append(g.Self.Aliases, self)
	}
	return g
}

// Find gives the entity of a name as entityFor finds it, or nil.
func (g *NoteGraph) Find(name string) *SimEntity {
	name = strings.TrimSpace(name)
	if isSelf(name) {
		return g.Self
	}
	for _, e := range g.Entities {
		if e.Id == name || strings.EqualFold(e.Label, name) {
			return e
		}
	}
	for _, e := range g.Entities {
		for _, a := range e.Aliases {
			if strings.EqualFold(a, name) {
				return e
			}
		}
	}
	core := coreName(name)
	if core == "" || len([]rune(core)) < 3 {
		return nil
	}
	for _, e := range g.Entities {
		if coreName(e.Label) == core {
			return e
		}
		for _, a := range e.Aliases {
			if coreName(a) == core {
				return e
			}
		}
	}
	return nil
}

func (g *NoteGraph) entity(name, kind string) *SimEntity {
	if e := g.Find(name); e != nil {
		return e
	}
	if kind == "" {
		kind = "thing"
	}
	g.n++
	e := &SimEntity{Id: "ent_new" + strconv.Itoa(g.n), Label: strings.TrimSpace(name), Kind: kind, New: true}
	g.Entities = append(g.Entities, e)
	return e
}

// Label gives the label of an entity id, or the text.
func (g *NoteGraph) Label(id string) string {
	for _, e := range g.Entities {
		if e.Id == id {
			return e.Label
		}
	}
	return id
}

func (g *NoteGraph) claim(id string) *SimClaim {
	for _, c := range g.Claims {
		if c.Id == id {
			return c
		}
	}
	return nil
}

// Apply applies the items (after guardNotes) to the graph. text is the
// message of the person.
func (g *NoteGraph) Apply(items []NoteItem, text string) {
	for _, it := range items {
		tool, args := it.toolCall(text)
		switch tool {
		case "":
			g.Log = append(g.Log, "skipped incomplete "+it.Op)
		case "record_event":
			g.Events = append(g.Events, SimEvent{Subject: args["subject"], Title: args["title"]})
		case "remember":
			g.remember(fact{subject: args["subject"], subjectKind: args["subject_kind"], relation: args["relation"], object: args["object"],
				objectKind: args["object_kind"], value: args["value"], also: args["also"] == "true"}, text)
		case "change_knowledge":
			c := g.claim(it.Id)
			if c == nil {
				g.Log = append(g.Log, "no claim "+it.Id)
				continue
			}
			switch it.Op {
			case "end":
				c.Current = false
			case "retract":
				if namesRecordIn(g, c, it) {
					c.Current = false
				}
			default: // update, fix
				c.Other, c.Changed, c.Current = strings.TrimSpace(args["value"]), true, true
			}
		}
	}
}

// namesRecordIn is namesRecord on the graph.
func namesRecordIn(g *NoteGraph, c *SimClaim, it NoteItem) bool {
	line := "- [" + c.Id + "] " + g.Label(c.Subject) + " " + c.Relation + " " + c.Other
	return namesRecord(line, it)
}

func (g *NoteGraph) remember(f fact, text string) {
	f.relation = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(f.relation)), "-"))
	f, more := shapeFact(f)
	if f.subject == "" || f.relation == "" || (f.object == "") == (f.value == "") {
		g.Log = append(g.Log, "bad remember")
		return
	}
	subj := g.entity(f.subject, f.subjectKind)
	other := f.value
	if f.object != "" {
		other = g.entity(f.object, f.objectKind).Label
	}
	if !f.also && !manyValued(f.relation) {
		for _, c := range g.Claims {
			if c.Subject != subj.Id || c.Relation != f.relation || !c.Current {
				continue
			}
			if strings.EqualFold(c.Other, other) {
				return // the store has it
			}
			// "Not recorded: there is a current fact": with words of a
			// change, applyNotes updates it.
			if changeWords.MatchString(text) {
				c.Other, c.Changed = other, true
			} else {
				g.Log = append(g.Log, "not recorded: current "+c.Id)
			}
			return
		}
	}
	g.n++
	g.Claims = append(g.Claims, &SimClaim{Id: "clm_new" + strconv.Itoa(g.n), Subject: subj.Id, Relation: f.relation, Other: other, Current: true, New: true})
	for _, m := range more {
		g.remember(m, "")
	}
}

// Has reports whether a current claim of the subject has one of the
// relations (as a part of its name; any relation when none) and an object
// or value that contains other (any when "").
func (g *NoteGraph) Has(subject string, relations []string, other string) bool {
	return len(g.Matching(subject, relations, other)) > 0
}

// Matching gives the current claims that Has counts.
func (g *NoteGraph) Matching(subject string, relations []string, other string) []*SimClaim {
	e := g.Find(subject)
	if e == nil {
		return nil
	}
	var out []*SimClaim
	for _, c := range g.Claims {
		if !c.Current || c.Subject != e.Id {
			continue
		}
		if len(relations) > 0 {
			ok := false
			for _, r := range relations {
				if strings.Contains(c.Relation, strings.ToLower(r)) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		if other != "" && !g.otherHas(c.Other, other) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// otherHas: the object or value has the text, or the object is an entity
// that has it as an alias.
func (g *NoteGraph) otherHas(have, want string) bool {
	if strings.Contains(strings.ToLower(have), strings.ToLower(want)) {
		return true
	}
	if e := g.Find(have); e != nil {
		for _, a := range append([]string{e.Label}, e.Aliases...) {
			if strings.Contains(strings.ToLower(a), strings.ToLower(want)) {
				return true
			}
		}
	}
	return false
}
