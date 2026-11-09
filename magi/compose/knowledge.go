package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/model"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// What the persona tools and the API do with the knowledge primitive
// (docs/knowledge.md). Each call acts for the person (act_for), with the
// token of the magi service.

const knowledgeRules = `
Knowledge:
- Never invent details such as names, phone numbers, addresses, hours, events, or dates. Use only what "recall" returns or what the person said in this conversation. When recall finds nothing, say that you do not know.
- Before you answer a question about the person, their people, things, places, accounts, calendars, or plans, use "recall", also when you think that you know it from the conversation: the record can have changed. For a question about the past ("before", "used to"), set include_past.
- When the person tells you a fact, record it with "remember": one call for each fact, with a subject, a short relation (for example dentist, lives-in, works-at, hosted-in, located-in, birthday, books-through), and an object (a thing with a name) or a value. Use subject "me" for the person.
- When the person changes or corrects a fact, use "recall" to find its id, then "change_knowledge" with one operation:
  - "fix": the record was wrong ("that's wrong", "I misspoke", "it was always", "I said Tuesday, not Thursday").
  - "update": it was true and now it is different ("we moved", "now", "we switched", "it changed to").
  - "retract": it was never true, or the person wants it forgotten ("there is no", "forget").
  When the words do not make clear if it changed or was wrong, ask "Did it change, or was it wrong?" and wait.
- Say that you recorded or changed something only after the tool succeeded.`

func knowledgeTools(obj func(map[string]any, ...string) map[string]any, str func(string) map[string]any) []model.Tool {
	boolean := func(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }
	return []model.Tool{
		{Name: "recall", Description: "Find what Magi knows about the person: things, people, places, and facts about them, with ids.", Parameters: obj(map[string]any{
			"text":         str("The words of the question: names, things, and relations, for example 'dentist' or 'Dana birthday'."),
			"include_past": boolean("True for a question about the past: also facts that are no longer true."),
		}, "text")},
		{Name: "remember", Description: "Record one fact that the person told you.", Parameters: obj(map[string]any{
			"subject":      str("Who or what the fact is about: 'me' for the person, or a name such as 'Dana' or 'family calendar'."),
			"subject_kind": str("Kind of the subject when it is new: person, place, organization, system, calendar, device, account, project, thing."),
			"relation":     str("Short relation name, for example dentist, lives-in, works-at, hosted-in, located-in, birthday, books-through, sister."),
			"object":       str("A thing with a name that the fact relates to, such as 'Dr. Rivera' or 'Nextcloud'. Give object or value."),
			"object_kind":  str("Kind of the object when it is new."),
			"value":        str("A value such as a date, a number, or a description, when there is no object."),
			"summary":      str("Optional. One sentence about the subject."),
			"also":         boolean("True when the fact is in addition to an existing fact with the same relation (for example a second sister)."),
		}, "subject", "relation")},
		{Name: "change_knowledge", Description: "Fix, update, or retract a record that recall gave (by id).", Parameters: obj(map[string]any{
			"id":        str("The id of the fact or thing, from recall."),
			"operation": map[string]any{"type": "string", "enum": []string{"fix", "update", "retract"}, "description": "fix: it was wrong. update: it was true and changed. retract: it was never true, or forget it."},
			"value":     str("The new value or the new name of the object (for fix and update)."),
			"field":     str("Optional. For a fact: value, object, or relation. For a thing: label, summary, or kind."),
			"reason":    str("The words of the person, short."),
		}, "id", "operation")},
	}
}

var firstPerson = regexp.MustCompile(`(?i)\b(i|my|me|mine|myself)\b`)

var selfNames = []string{"me", "i", "my", "myself", "mine", "the person", "user", "the user"}

func (s *System) selfEntity(ctx context.Context, owner, name string) (knowledgeapi.Entity, error) {
	return s.Knowledge.Self(ctx, knowledgeapi.SelfRequest{ActFor: &owner, Label: optional(name)})
}

// entityFor returns the person's entity of a name: the self for "me", an
// entity with that label or alias, or a new one.
func (s *System) entityFor(ctx context.Context, owner, ownerName, name, kind string, utt *string) (knowledgeapi.Entity, error) {
	name = strings.TrimSpace(name)
	if slices.Contains(selfNames, strings.ToLower(name)) || (ownerName != "" && strings.EqualFold(name, ownerName)) {
		return s.selfEntity(ctx, owner, ownerName)
	}
	if strings.HasPrefix(name, "ent_") || strings.HasPrefix(name, "nod_") {
		if r, err := s.Knowledge.Get(ctx, knowledgeapi.GetRequest{Id: name, ActFor: &owner}); err == nil && r.Entity != nil {
			return *r.Entity, nil
		}
	}
	if e := s.sameEntity(ctx, owner, name); e != nil {
		return *e, nil
	}
	if kind = strings.ToLower(strings.TrimSpace(kind)); kind == "" {
		kind = "thing"
	}
	return s.Knowledge.PutEntity(ctx, knowledgeapi.PutEntityRequest{Kind: kind, Label: name, ActFor: &owner, Utterance: utt})
}

// fact is a remember request.
type fact struct {
	subject, subjectKind, relation, object, objectKind, value, summary string
	also                                                               bool
}

// remember records a fact. When a current fact has the same subject and
// relation and another object or value, it records nothing (unless also)
// and the note names that fact.
func (s *System) remember(ctx context.Context, owner, ownerName string, f fact, authored bool, utt *string) (knowledgeapi.Claim, string, error) {
	f.relation = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(f.relation)), "-"))
	f, more := shapeFact(f)
	if f.subject == "" || f.relation == "" || (f.object == "") == (f.value == "") {
		return knowledgeapi.Claim{}, "", fmt.Errorf("give a subject, a relation, and an object or a value")
	}
	subj, err := s.entityFor(ctx, owner, ownerName, f.subject, f.subjectKind, utt)
	if err != nil {
		return knowledgeapi.Claim{}, "", err
	}
	if f.summary != "" && subj.Summary == "" {
		s.Knowledge.PutEntity(ctx, knowledgeapi.PutEntityRequest{Kind: subj.Kind, Label: subj.Label, Summary: &f.summary, Owners: subj.Owners, ActFor: &owner})
	}
	req := knowledgeapi.AssertRequest{Subject: subj.Id, Relation: f.relation, ActFor: &owner, Utterance: utt, Confidence: ptr(0.8)}
	var objLabel string
	if f.object != "" {
		obj, err := s.entityFor(ctx, owner, ownerName, f.object, f.objectKind, utt)
		if err != nil {
			return knowledgeapi.Claim{}, "", err
		}
		req.Object, objLabel = &obj.Id, obj.Label
	} else {
		req.Value = &f.value
	}
	if authored {
		req.AuthoredBy, req.Confidence = &owner, ptr(1.0)
	}
	if !f.also {
		about, err := s.Knowledge.About(ctx, knowledgeapi.AboutRequest{Id: subj.Id, ActFor: &owner})
		if err == nil {
			for _, c := range about.Claims {
				if c.Subject != subj.Id || c.Relation != f.relation || c.Status != "current" {
					continue
				}
				if (req.Object != nil && c.Object != nil && *c.Object == *req.Object) || (req.Value != nil && c.Value != nil && strings.EqualFold(*c.Value, *req.Value)) {
					break
				}
				return knowledgeapi.Claim{}, fmt.Sprintf("Not recorded: there is a current fact [%s] %s. If it changed, use change_knowledge with operation update on %s; if it was wrong, use fix; if both are true, call remember again with also true.",
					c.Id, describeClaim(c, labelsOf(about)), c.Id), nil
			}
		}
	}
	c, err := s.Knowledge.Assert(ctx, req)
	if err != nil {
		return c, "", err
	}
	other := objLabel
	if other == "" {
		other = f.value
	}
	msg := fmt.Sprintf("Recorded [%s] %s %s %s.", c.Id, subj.Label, f.relation, other)
	for _, m := range more {
		if _, note, err := s.remember(ctx, owner, ownerName, m, authored, utt); err == nil && note != "" && !strings.HasPrefix(note, "Not recorded") {
			msg += " " + note
		}
	}
	return c, msg, nil
}

func labelsOf(a knowledgeapi.AboutResponse) map[string]string {
	l := map[string]string{a.Entity.Id: a.Entity.Label}
	for _, e := range a.Related {
		l[e.Id] = e.Label
	}
	return l
}

func describeClaim(c knowledgeapi.Claim, label map[string]string) string {
	other := ""
	if c.Value != nil {
		other = *c.Value
	} else if c.Object != nil {
		other = firstNonEmpty(label[*c.Object], *c.Object)
	}
	return fmt.Sprintf("%s %s %s", firstNonEmpty(label[c.Subject], c.Subject), c.Relation, other)
}

// changeRecord does a fix, update, or retraction. value is the new value:
// for a fact with an object, the name of the new object.
func (s *System) changeRecord(ctx context.Context, owner, ownerName, id, op, field, value, reason string, erase bool, utt *string) (knowledgeapi.ChangeResponse, error) {
	req := knowledgeapi.ChangeRequest{Id: id, Operation: op, Reason: optional(reason), ActFor: &owner, Utterance: utt}
	var more []fact
	if erase {
		req.Erase = ptr(true)
	}
	if op != "retract" {
		if value == "" {
			return knowledgeapi.ChangeResponse{}, fmt.Errorf("%s needs a value", op)
		}
		cur, err := s.Knowledge.Get(ctx, knowledgeapi.GetRequest{Id: id, ActFor: &owner})
		if err != nil {
			return knowledgeapi.ChangeResponse{}, err
		}
		switch {
		case cur.Claim != nil:
			if field == "" {
				field = "value"
				if cur.Claim.Object != nil {
					field = "object"
				}
			}
			if field == "object" {
				// The same shape as remember: "Dr. Chen at Oak Hill
				// Dental" is Dr. Chen, who works at Oak Hill Dental.
				g, m := shapeFact(fact{subject: "me", relation: cur.Claim.Relation, object: value})
				if g.object != "" {
					value, more = g.object, m
				}
				kind := ""
				if cur.Claim.Object != nil {
					if o, err := s.Knowledge.Get(ctx, knowledgeapi.GetRequest{Id: *cur.Claim.Object, ActFor: &owner}); err == nil && o.Entity != nil {
						kind = o.Entity.Kind
					}
				}
				obj, err := s.entityFor(ctx, owner, ownerName, value, kind, utt)
				if err != nil {
					return knowledgeapi.ChangeResponse{}, err
				}
				value = obj.Id
			}
		case cur.Entity != nil:
			if field == "" {
				field = "label"
			}
		default:
			if field == "" {
				return knowledgeapi.ChangeResponse{}, fmt.Errorf("field is required for this record")
			}
		}
		req.Set = map[string]string{field: value}
	}
	r, err := s.Knowledge.Change(ctx, req)
	if err == nil {
		for _, m := range more {
			if _, _, err := s.remember(ctx, owner, ownerName, m, false, utt); err != nil {
				s.Log.Warn("change: second fact", "err", err)
			}
		}
	}
	return r, err
}

// knowledgeView finds what the person knows: the entities that match the
// text or that the matching facts name, with their facts, and the episodes
// and procedures that match.
func (s *System) knowledgeView(ctx context.Context, owner, text, kind string, limit int, includeEnded bool) ([]magiapi.KnowledgeItem, []magiapi.KnowledgeRecord, error) {
	res, err := s.Knowledge.Search(ctx, knowledgeapi.SearchRequest{Text: optional(text), Kind: optional(kind), Limit: ptr(uint64(30)),
		IncludeEnded: &includeEnded, ActFor: &owner})
	if err != nil {
		return nil, nil, err
	}
	claims := map[string]knowledgeapi.Claim{}
	for _, c := range res.Claims {
		claims[c.Id] = c
	}
	var ids []string
	add := func(id string) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	records := []magiapi.KnowledgeRecord{}
	for _, h := range res.Hits {
		switch h.Type {
		case "entity":
			add(h.Id)
		case "claim":
			if c, ok := claims[h.Id]; ok {
				add(c.Subject)
				if c.Object != nil {
					add(*c.Object)
				}
			}
		case "episode":
			for _, e := range res.Episodes {
				if e.Id == h.Id {
					records = append(records, magiapi.KnowledgeRecord{Id: e.Id, Type: "episode", Kind: e.Kind, Title: e.Title, Summary: e.Summary, AtMs: e.StartedMs})
				}
			}
		case "procedure":
			for _, p := range res.Procedures {
				if p.Id == h.Id {
					records = append(records, magiapi.KnowledgeRecord{Id: p.Id, Type: "procedure", Kind: "procedure", Title: p.Name, Summary: p.Goal, AtMs: p.UpdatedMs})
				}
			}
		}
	}
	// A question about the person ("Where do I work?") needs the facts
	// of the self, which other entities with the same words can push out
	// of the limit ("work calendar").
	if firstPerson.MatchString(text) && len(ids) > 1 {
		// The self is the entity of the person with the alias "me"
		// (knowledge Self). Search gives the entities of the claims.
		for _, e := range res.Entities {
			if !slices.Contains(e.Owners, owner) || !slices.Contains(e.Aliases, "me") {
				continue
			}
			// The self first, then the things that its matching facts name
			// ("my dentist" gives Dr. Rivera, who has the works-at fact).
			first := []string{e.Id}
			for _, h := range res.Hits {
				if c, ok := claims[h.Id]; ok && h.Type == "claim" && c.Subject == e.Id && c.Object != nil && slices.Contains(ids, *c.Object) && !slices.Contains(first, *c.Object) {
					first = append(first, *c.Object)
				}
			}
			// One more step: the things that those things name ("my
			// vet" gives Dr. Okafor, who works at the clinic that books
			// through PetDesk). Words such as "book" can match other
			// entities, which then push the clinic out of the limit.
			for _, id := range slices.Clone(first[1:]) {
				if len(first) >= limit {
					break
				}
				a, err := s.Knowledge.About(ctx, knowledgeapi.AboutRequest{Id: id, ActFor: &owner})
				if err != nil {
					continue
				}
				for _, c := range a.Claims {
					if c.Subject == id && c.Object != nil && *c.Object != e.Id && !slices.Contains(first, *c.Object) && len(first) < limit {
						first = append(first, *c.Object)
					}
				}
			}
			if len(first) > 1 || slices.Index(ids, e.Id) > 0 {
				rest := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return slices.Contains(first, id) })
				ids = append(first, rest...)
			}
			break
		}
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	items := []magiapi.KnowledgeItem{}
	for _, id := range ids {
		a, err := s.Knowledge.About(ctx, knowledgeapi.AboutRequest{Id: id, IncludeEnded: &includeEnded, ActFor: &owner})
		if err != nil {
			continue
		}
		items = append(items, knowledgeItem(a, owner, claims))
	}
	return items, records, nil
}

// knowledgeItem is an entity with its facts. The facts that the search
// found come first; an entity with many facts (the self) gives at most 20.
func knowledgeItem(a knowledgeapi.AboutResponse, owner string, matched map[string]knowledgeapi.Claim) magiapi.KnowledgeItem {
	e := a.Entity
	it := magiapi.KnowledgeItem{Id: e.Id, Kind: e.Kind, Label: e.Label, Aliases: e.Aliases, Summary: e.Summary, Properties: e.Properties,
		Facts: []magiapi.KnowledgeFact{}, Mine: slices.Contains(e.Owners, owner)}
	if it.Aliases == nil {
		it.Aliases = []string{}
	}
	if it.Properties == nil {
		it.Properties = map[string]string{}
	}
	if e.External != nil {
		it.System, it.Locator = &e.External.System, &e.External.Locator
	}
	label := labelsOf(a)
	disputed := map[string]bool{}
	for _, d := range a.Disputes {
		disputed[d.Record] = true
	}
	cs := slices.Clone(a.Claims)
	slices.SortStableFunc(cs, func(x, y knowledgeapi.Claim) int {
		_, mx := matched[x.Id]
		_, my := matched[y.Id]
		switch {
		case mx && !my:
			return -1
		case my && !mx:
			return 1
		}
		return 0
	})
	for _, c := range cs[:min(len(cs), 20)] {
		f := magiapi.KnowledgeFact{Id: c.Id, Relation: c.Relation, Direction: "out", Value: c.Value, Status: c.Status,
			ValidFromMs: c.ValidFromMs, ValidToMs: c.ValidToMs, Supported: c.Supported, Stale: c.Stale,
			Mine: slices.Contains(c.Owners, owner), Disputed: disputed[c.Id]}
		other := c.Object
		if c.Subject != e.Id {
			f.Direction, other = "in", &c.Subject
		}
		if other != nil {
			f.OtherId, f.Other = other, ptr(firstNonEmpty(label[*other], *other))
		}
		it.Facts = append(it.Facts, f)
	}
	return it
}

// describeKnowledge is the text of a view for the model, with the id of
// each fact.
func describeKnowledge(items []magiapi.KnowledgeItem, records []magiapi.KnowledgeRecord, now time.Time) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, it := range items {
		fmt.Fprintf(&b, "- [%s] %s (%s)", it.Id, it.Label, it.Kind)
		if len(it.Aliases) > 0 {
			fmt.Fprintf(&b, ", also called %s", strings.Join(it.Aliases, ", "))
		}
		if it.Summary != "" {
			fmt.Fprintf(&b, ": %s", it.Summary)
		}
		if !it.Mine {
			b.WriteString(" (shared with the person)")
		}
		b.WriteString("\n")
		for _, f := range it.Facts {
			// A fact shows once, except under the thing that it points to:
			// then that thing lists all that is in it ("household
			// Nextcloud" lists each calendar in it).
			if seen[f.Id] && f.Direction != "in" {
				continue
			}
			seen[f.Id] = true
			other := deref(f.Value)
			if f.Other != nil {
				other = *f.Other
			}
			line := fmt.Sprintf("%s %s %s", it.Label, f.Relation, other)
			if f.Direction == "in" {
				line = fmt.Sprintf("%s %s %s", other, f.Relation, it.Label)
			}
			var notes []string
			if f.Status == "ended" {
				n := "no longer true"
				if f.ValidToMs != nil {
					n += " since " + when(time.UnixMilli(int64(*f.ValidToMs)), now)
				}
				notes = append(notes, n)
			}
			if !f.Supported {
				notes = append(notes, "no evidence: ask before you rely on it")
			}
			if f.Stale {
				notes = append(notes, "it came from a fact that changed")
			}
			if f.Disputed {
				notes = append(notes, "disputed")
			}
			if !f.Mine {
				notes = append(notes, "someone else's record: you cannot change it")
			}
			if len(notes) > 0 {
				line += " (" + strings.Join(notes, "; ") + ")"
			}
			fmt.Fprintf(&b, "  - [%s] %s\n", f.Id, line)
		}
	}
	for _, r := range records {
		fmt.Fprintf(&b, "- [%s] %s %q: %s\n", r.Id, r.Kind, r.Title, r.Summary)
	}
	return b.String()
}

func knowledgeChange(c knowledgeapi.Change) magiapi.KnowledgeChange {
	return magiapi.KnowledgeChange{Id: c.Id, Record: c.Record, Operation: c.Operation, Actor: c.Actor, Reason: c.Reason, Before: c.Before, After: c.After, AtMs: c.AtMs}
}

// changedItem is the item of the entity that a change response names.
func (s *System) changedItem(ctx context.Context, owner string, r knowledgeapi.ChangeResponse) *magiapi.KnowledgeItem {
	id := ""
	switch {
	case r.Entity != nil:
		id = r.Entity.Id
	case r.Claim != nil:
		id = r.Claim.Subject
	}
	if id == "" {
		return nil
	}
	a, err := s.Knowledge.About(ctx, knowledgeapi.AboutRequest{Id: id, ActFor: &owner})
	if err != nil {
		return nil
	}
	it := knowledgeItem(a, owner, nil)
	return &it
}

// knowledgeContext is the text of the records that match the words of an
// utterance, for the prompt of a persona turn. It is short: at most four
// entities and 30 lines.
func (s *System) knowledgeContext(ctx context.Context, owner, text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	// A question about the past ("before", "used to") needs the facts
	// that ended.
	items, records, err := s.knowledgeView(ctx, owner, text, "", 4, pastWords.MatchString(text))
	if err != nil {
		s.Log.Warn("knowledge context", "err", err)
		return ""
	}
	lines := strings.Split(strings.TrimRight(describeKnowledge(items, records, time.Now()), "\n"), "\n")
	if len(lines) > 30 {
		lines = lines[:30]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

var pastWords = regexp.MustCompile(`(?i)\b(before|used to|previous(?:ly)?|earlier|formerly|former|last (?:one|time)|in the past)\b`)

// staleMark starts an earlier answer of the persona that came before a
// change of the records, when Magi does not know which records the answer
// used (an answer from before record refs).
const staleMark = "[Before a change of the records: this answer can be out of date.] "

// recordRefKind is the kind of an attachment of a persona reply: a record
// that the persona got before the reply (the records of the prompt, or a
// recall result). The title is the line of the record.
const recordRefKind = "knowledge-record"

// changeOps are the operations that can make an earlier answer wrong.
var changeOps = []string{"fix", "update", "retract", "merge", "unmerge"}

var recordID = regexp.MustCompile(`\[((?:clm|ent)_[^\]\s]+)\]`)

// recordRefs gives a ref for each record id in text (describeKnowledge),
// with the line of the record as the title.
func recordRefs(text string) []conversationapi.Ref {
	var refs []conversationapi.Ref
	seen := map[string]bool{}
	for _, l := range strings.Split(text, "\n") {
		for _, m := range recordID.FindAllStringSubmatch(l, -1) {
			if seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			title := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(recordID.ReplaceAllString(l, "")), "-"))
			if len(title) > 200 {
				title = title[:200]
			}
			refs = append(refs, conversationapi.Ref{Kind: recordRefKind, Ref: m[1], Title: &title})
		}
	}
	return refs
}

// recordChanges gives the newest change (changeOps) of each record of
// owner since from (ms), and the time of the newest change, or 0.
func (s *System) recordChanges(ctx context.Context, owner string, from uint64) (map[string]knowledgeapi.Change, uint64) {
	res, err := s.Knowledge.Timeline(ctx, knowledgeapi.TimelineRequest{FromMs: from, ToMs: uint64(time.Now().UnixMilli()) + 1,
		Limit: ptr(uint64(1000)), ActFor: &owner})
	if err != nil {
		s.Log.Warn("knowledge changes", "err", err)
		return nil, 0
	}
	changes := map[string]knowledgeapi.Change{}
	var last uint64
	for _, c := range res.Changes {
		if !slices.Contains(changeOps, c.Operation) {
			continue
		}
		last = max(last, c.AtMs)
		ids := []string{c.Record}
		// A retract also removes the facts of a retracted entity.
		if c.After != nil {
			var after struct {
				Records []string `json:"records"`
			}
			if json.Unmarshal([]byte(*c.After), &after) == nil {
				ids = append(ids, after.Records...)
			}
		}
		for _, id := range ids {
			if old, ok := changes[id]; !ok || c.AtMs >= old.AtMs {
				changes[id] = c
			}
		}
	}
	return changes, last
}

var opWords = map[string]string{
	"retract": "retracts it: it was never true, or the person asked to forget it",
	"fix":     "fixes it: it was wrong",
	"update":  "updates it: it changed",
	"merge":   "merges it with another record of the same thing",
	"unmerge": "separates it from a record that it was merged with",
}

// supersededPrefix starts the note that replaces an earlier answer.
const supersededPrefix = "[This answer was superseded"

// supersededCopy matches a superseded note that a model copied.
var supersededCopy = regexp.MustCompile(`\[This answer was superseded[^\]]*\]`)

// supersededNote gives the text that replaces an earlier answer u when a
// record that u used changed after u, or "". The answer text is not given:
// a small model copies its own earlier answer.
func supersededNote(u conversationapi.Utterance, changes map[string]knowledgeapi.Change) string {
	type hit struct {
		c      knowledgeapi.Change
		titles []string
	}
	var hits []*hit
	byID := map[string]*hit{}
	for _, r := range u.Attachments {
		c, ok := changes[r.Ref]
		if r.Kind != recordRefKind || !ok || c.AtMs <= u.CreatedMs {
			continue
		}
		h := byID[c.Id]
		if h == nil {
			h = &hit{c: c}
			byID[c.Id] = h
			hits = append(hits, h)
		}
		if r.Title != nil && *r.Title != "" && !slices.Contains(h.titles, *r.Title) {
			h.titles = append(h.titles, *r.Title)
		}
	}
	if len(hits) == 0 {
		return ""
	}
	var parts []string
	for _, h := range hits {
		p := "a later change of the records"
		if h.c.Reason != nil && strings.TrimSpace(*h.c.Reason) != "" {
			p += fmt.Sprintf(" (the person said: %q)", strings.TrimSpace(*h.c.Reason))
		}
		p += " " + opWords[h.c.Operation]
		if len(h.titles) > 0 {
			p += ". The record was: " + strings.Join(h.titles, "; ")
		}
		parts = append(parts, p)
	}
	return supersededPrefix + " by " + strings.Join(parts, ". Also by ") + ". Do not repeat the earlier answer; use the current records.]"
}

// capRecordRefs keeps the first n record refs (recordRefKind) and all
// other refs, and drops repeated record refs.
func capRecordRefs(refs []conversationapi.Ref, n int) []conversationapi.Ref {
	out := refs[:0:0]
	seen := map[string]bool{}
	for _, r := range refs {
		if r.Kind == recordRefKind {
			if seen[r.Ref] || len(seen) >= n {
				continue
			}
			seen[r.Ref] = true
		}
		out = append(out, r)
	}
	return out
}

// hasRecordRefs tells if Magi knows the records that u used.
func hasRecordRefs(u conversationapi.Utterance) bool {
	return slices.ContainsFunc(u.Attachments, func(r conversationapi.Ref) bool { return r.Kind == recordRefKind })
}
