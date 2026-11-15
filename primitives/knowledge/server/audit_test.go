package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/catalystcommunity/magi/lib/authn"
	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
	"github.com/catalystcommunity/magi/primitives/knowledge/store"
)

// Audit log and undo (risk-remediation 3.3, requirement 9.3). Most of these
// need PostgreSQL (MAGI_TEST_DATABASE_URL).

func TestCanUndo(t *testing.T) {
	cases := []struct {
		name string
		p    authn.Principal
		who  string
		want bool
	}{
		{"the person", authn.Principal{Subject: alice, Kind: authn.KindUser}, alice, true},
		{"another member for the person", authn.Principal{Subject: bob, Kind: authn.KindUser}, alice, false},
		{"an admin user", authn.Principal{Subject: "user:root", Kind: authn.KindUser, Scopes: []string{ScopeAdmin}}, alice, true},
		{"a service that acts for people", authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{ScopeActAs}}, alice, false},
		{"a service with every scope", authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"*"}}, alice, false},
		{"an admin service", authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{ScopeAdmin, ScopeActAs}}, alice, false},
		{"a service with knowledge:undo", authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{ScopeActAs, ScopeUndo}}, alice, true},
	}
	for _, c := range cases {
		ctx := authn.With(context.Background(), c.p)
		if got := canUndo(ctx, store.Who{Subject: c.who}); got != c.want {
			t.Errorf("%s: canUndo = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOriginValidation(t *testing.T) {
	for _, bad := range []string{"not json", "[1]", "null", `"s"`, `{"a":"` + strings.Repeat("x", maxOrigin) + `"}`} {
		if _, err := withOrigin(store.Who{}, ptr(bad)); err == nil {
			t.Errorf("origin %.20q was accepted", bad)
		}
	}
	if w, err := withOrigin(store.Who{}, ptr(`{"persona":"p"}`)); err != nil || w.Origin == nil {
		t.Errorf("good origin: %v", err)
	}
}

func historyOf(t *testing.T, c *api.KnowledgeClient, id string, actFor *string) []api.Change {
	t.Helper()
	return must(c.History(ctx, api.HistoryRequest{Id: id, ActFor: actFor})).Changes
}

func lastOp(cs []api.Change, op string) api.Change {
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].Operation == op {
			return cs[i]
		}
	}
	panic("no change " + op)
}

// Who, how, what, trust, and why are in each row.
func TestAuditRowsSayWhoAndHow(t *testing.T) {
	e := newEnv(t)
	origin := `{"persona":"assistant","tool":"remember","turn":"utt_9","run":"run_1","confirmed":true}`
	ent := must(e.magi.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Dana", ActFor: ptr(alice), Trust: ptr("untrusted"), Origin: &origin}))
	h := historyOf(t, e.alice, ent.Id, nil)
	if len(h) != 1 {
		t.Fatalf("history: %+v", h)
	}
	c := h[0]
	if c.Operation != "create" || c.Actor != alice || c.Via == nil || *c.Via != "service:magi" || c.Origin == nil || *c.Origin != origin ||
		c.Trust == nil || *c.Trust != "untrusted" || c.AtMs == 0 || c.After == nil {
		t.Fatalf("audit row: %+v", c)
	}
	var o map[string]any
	if json.Unmarshal([]byte(*c.Origin), &o) != nil || o["tool"] != "remember" {
		t.Fatalf("origin: %s", *c.Origin)
	}
	// The person's own call has no via and no origin.
	own := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "place", Label: "Home"}))
	oc := historyOf(t, e.alice, own.Id, nil)[0]
	if oc.Via != nil || oc.Origin != nil || oc.Trust == nil || *oc.Trust != "own" {
		t.Fatalf("own row: %+v", oc)
	}
	// A reason is kept.
	cl := must(e.alice.Assert(ctx, api.AssertRequest{Subject: own.Id, Relation: "note", Value: ptr("a")}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: cl.Id, Operation: "fix", Set: map[string]string{"value": "b"}, Reason: ptr("typo")}))
	if f := lastOp(historyOf(t, e.alice, cl.Id, nil), "fix"); f.Reason == nil || *f.Reason != "typo" || f.Before == nil || f.After == nil {
		t.Fatalf("fix row: %+v", f)
	}
	// A bad origin is refused.
	_, err := e.magi.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "X", ActFor: ptr(alice), Origin: ptr("not json")})
	code(t, err, 400)
}

func TestAuditLogOfTheCaller(t *testing.T) {
	e := newEnv(t)
	a1 := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "A1"}))
	must(e.bob.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "B1"}))
	a2 := must(e.magi.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "A2", ActFor: ptr(alice)}))
	log := historyOf(t, e.alice, "", nil)
	if len(log) != 2 || log[0].Record != a2.Id || log[1].Record != a1.Id {
		t.Fatalf("alice's audit log (newest first): %+v", log)
	}
	for _, c := range log {
		if c.Actor != alice {
			t.Fatalf("a change of another person: %+v", c)
		}
	}
	if got := must(e.alice.History(ctx, api.HistoryRequest{Id: "", Limit: ptr(uint64(1))})).Changes; len(got) != 1 || got[0].Record != a2.Id {
		t.Fatalf("limit: %+v", got)
	}
	if got := must(e.alice.History(ctx, api.HistoryRequest{Id: "", SinceMs: ptr(log[0].AtMs + 1)})).Changes; len(got) != 0 {
		t.Fatalf("since: %+v", got)
	}
	// An admin reads the log of the person that it acts for.
	if got := historyOf(t, e.admin, "", ptr(bob)); len(got) == 0 {
		t.Fatal("admin audit log empty")
	}
}

func TestUndoPermissions(t *testing.T) {
	e := newEnv(t)
	ent := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Dana"}))
	chg := historyOf(t, e.alice, ent.Id, nil)[0]
	undo := func(c *api.KnowledgeClient, actFor *string) error {
		_, err := c.Change(ctx, api.ChangeRequest{Id: chg.Id, Operation: "undo", ActFor: actFor})
		return err
	}
	// The service that acts for people cannot, with or without admin.
	code(t, undo(e.magi, ptr(alice)), 403)
	// Another member cannot see it.
	code(t, undo(e.bob, nil), 404)
	// The person can; and then it is done once.
	if err := undo(e.alice, nil); err != nil {
		t.Fatal(err)
	}
	code(t, undo(e.alice, nil), 400)
	if _, err := e.alice.Get(ctx, api.GetRequest{Id: ent.Id}); err == nil {
		t.Fatal("the entity is still there after the undo of its creation")
	}
	// A service with knowledge:undo, and an admin, can.
	e2 := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Erin"}))
	chg = historyOf(t, e.alice, e2.Id, nil)[0]
	if err := undo(e.undoer, ptr(alice)); err != nil {
		t.Fatal(err)
	}
	e3 := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Fay"}))
	chg = historyOf(t, e.alice, e3.Id, nil)[0]
	if err := undo(e.admin, ptr(alice)); err != nil {
		t.Fatal(err)
	}
	// The undo is itself in the audit log: who, what it undid, and why.
	u := lastOp(historyOf(t, e.alice, e3.Id, nil), "undo")
	if u.Undoes == nil || *u.Undoes != chg.Id || u.Actor != alice {
		t.Fatalf("undo row: %+v", u)
	}
	if first := historyOf(t, e.alice, e3.Id, nil)[0]; first.UndoneBy == nil || *first.UndoneBy != u.Id {
		t.Fatalf("undone_by: %+v", first)
	}
	// An undo cannot be undone.
	_, err := e.alice.Change(ctx, api.ChangeRequest{Id: u.Id, Operation: "undo"})
	code(t, err, 400)
	// undo takes no set.
	_, err = e.alice.Change(ctx, api.ChangeRequest{Id: u.Id, Operation: "undo", Set: map[string]string{"a": "b"}})
	code(t, err, 400)
}

func TestUndoFixAndUpdate(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{}))
	rivera := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Dr. Rivera"}))
	chen := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Dr. Chen"}))
	cl := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "dentist", Object: &rivera.Id}))
	// A fix of the object, which retracts the orphaned old object? Rivera has
	// a claim, so it stays. Fix, then undo.
	fixed := must(e.alice.Change(ctx, api.ChangeRequest{Id: cl.Id, Operation: "fix", Set: map[string]string{"object": chen.Id}}))
	if fixed.Claim == nil || fixed.Claim.Object == nil || *fixed.Claim.Object != chen.Id {
		t.Fatalf("fix: %+v", fixed.Claim)
	}
	r := must(e.alice.Change(ctx, api.ChangeRequest{Id: fixed.Change.Id, Operation: "undo", Reason: ptr("wrong fix")}))
	if r.Claim == nil || r.Claim.Object == nil || *r.Claim.Object != rivera.Id || r.Change.Operation != "undo" {
		t.Fatalf("undo fix: %+v", r)
	}
	// A fix from a value to an object and back; fields and the orphan.
	cl2 := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "city", Value: ptr("Tucson")}))
	f2 := must(e.alice.Change(ctx, api.ChangeRequest{Id: cl2.Id, Operation: "fix", Set: map[string]string{"value": "Phoenix", "confidence": "0.5"}}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: f2.Change.Id, Operation: "undo"}))
	back := must(e.alice.Get(ctx, api.GetRequest{Id: cl2.Id})).Claim
	if back.Value == nil || *back.Value != "Tucson" || back.Confidence != 1 {
		t.Fatalf("after undo: %+v", back)
	}
	// An entity fix: label, aliases, and a property.
	f3 := must(e.alice.Change(ctx, api.ChangeRequest{Id: rivera.Id, Operation: "fix", Set: map[string]string{"label": "Dr. Rivero", "aliases": "Rivero,Dr R", "property.phone": "555"}}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: f3.Change.Id, Operation: "undo"}))
	ent := must(e.alice.Get(ctx, api.GetRequest{Id: rivera.Id})).Entity
	if ent.Label != "Dr. Rivera" || len(ent.Aliases) != 0 || len(ent.Properties) != 0 {
		t.Fatalf("entity after undo: %+v", ent)
	}
	// An update: the old claim is current again, the new one is gone.
	up := must(e.alice.Change(ctx, api.ChangeRequest{Id: cl.Id, Operation: "update", Set: map[string]string{"object": chen.Id}}))
	if up.Claim == nil || up.Claim.Id == cl.Id {
		t.Fatalf("update: %+v", up)
	}
	// The creation of the replacement points at the update.
	create := lastOp(historyOf(t, e.alice, up.Claim.Id, nil), "create")
	_, err := e.alice.Change(ctx, api.ChangeRequest{Id: create.Id, Operation: "undo"})
	code(t, err, 400)
	r = must(e.alice.Change(ctx, api.ChangeRequest{Id: up.Change.Id, Operation: "undo"}))
	if r.Claim == nil || r.Claim.Id != cl.Id || r.Claim.Status != "current" || r.Claim.ValidToMs != nil || r.Claim.ReplacedBy != nil {
		t.Fatalf("undo update: %+v", r.Claim)
	}
	if _, err := e.alice.Get(ctx, api.GetRequest{Id: up.Claim.Id}); err == nil {
		t.Fatal("the replacement claim is still there")
	}
	// The search text follows: the old object is found again.
	res := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("dentist Rivera")}))
	if len(res.Hits) == 0 {
		t.Fatal("search finds nothing after the undo")
	}
}

func TestUndoRetract(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{}))
	pet := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "thing", Label: "Rex"}))
	c1 := must(e.alice.Assert(ctx, api.AssertRequest{Subject: pet.Id, Relation: "species", Value: ptr("dog")}))
	c2 := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "has", Object: &pet.Id}))
	// A claim that ended stays ended after retract and undo.
	old := must(e.alice.Assert(ctx, api.AssertRequest{Subject: pet.Id, Relation: "color", Value: ptr("brown")}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: old.Id, Operation: "update", Set: map[string]string{"value": "black"}}))

	r := must(e.alice.Change(ctx, api.ChangeRequest{Id: pet.Id, Operation: "retract"}))
	if _, err := e.alice.Get(ctx, api.GetRequest{Id: pet.Id}); err == nil {
		t.Fatal("entity still visible")
	}
	// The claim of the person that names the entity cannot come back alone.
	cr := lastOp(historyOf(t, e.alice, pet.Id, nil), "retract")
	if cr.Id != r.Change.Id {
		t.Fatal("history")
	}
	must(e.alice.Change(ctx, api.ChangeRequest{Id: r.Change.Id, Operation: "undo"}))
	for _, id := range []string{pet.Id, c1.Id, c2.Id} {
		if _, err := e.alice.Get(ctx, api.GetRequest{Id: id}); err != nil {
			t.Fatalf("%s not back: %v", id, err)
		}
	}
	got := must(e.alice.Get(ctx, api.GetRequest{Id: old.Id}))
	if got.Claim.Status != "ended" {
		t.Fatalf("the ended claim is %s after undo", got.Claim.Status)
	}
	// With erase there is nothing to restore.
	e2 := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "thing", Label: "Gone"}))
	er := must(e.alice.Change(ctx, api.ChangeRequest{Id: e2.Id, Operation: "retract", Erase: ptr(true)}))
	_, err := e.alice.Change(ctx, api.ChangeRequest{Id: er.Change.Id, Operation: "undo"})
	code(t, err, 400)
	// A retracted claim cannot come back under a retracted entity.
	c3 := must(e.alice.Assert(ctx, api.AssertRequest{Subject: pet.Id, Relation: "age", Value: ptr("3")}))
	rc := must(e.alice.Change(ctx, api.ChangeRequest{Id: c3.Id, Operation: "retract"}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: pet.Id, Operation: "retract"}))
	_, err = e.alice.Change(ctx, api.ChangeRequest{Id: rc.Change.Id, Operation: "undo"})
	code(t, err, 400)
}

// A change that later changes depend on is refused, until they are undone.
func TestUndoRefusesWhenLaterChangesDepend(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{}))
	// A summary keeps the entity when its only claim is retracted.
	ent := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Dana", Summary: ptr("a friend")}))
	create := historyOf(t, e.alice, ent.Id, nil)[0]
	cl := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "friend", Object: &ent.Id}))
	// A claim names the entity: its creation cannot be undone.
	_, err := e.alice.Change(ctx, api.ChangeRequest{Id: create.Id, Operation: "undo"})
	code(t, err, 400)
	must(e.alice.Change(ctx, api.ChangeRequest{Id: cl.Id, Operation: "retract"}))

	// Two fixes of one record: the first cannot be undone before the second.
	f1 := must(e.alice.Change(ctx, api.ChangeRequest{Id: ent.Id, Operation: "fix", Set: map[string]string{"summary": "one"}}))
	f2 := must(e.alice.Change(ctx, api.ChangeRequest{Id: ent.Id, Operation: "fix", Set: map[string]string{"summary": "two"}}))
	_, err = e.alice.Change(ctx, api.ChangeRequest{Id: f1.Change.Id, Operation: "undo"})
	code(t, err, 400)
	if err == nil || !strings.Contains(err.Error(), f2.Change.Id) {
		t.Fatalf("the answer should name the later change: %v", err)
	}
	// The creation is blocked by both.
	_, err = e.alice.Change(ctx, api.ChangeRequest{Id: create.Id, Operation: "undo"})
	code(t, err, 400)
	must(e.alice.Change(ctx, api.ChangeRequest{Id: f2.Change.Id, Operation: "undo"}))
	if got := must(e.alice.Get(ctx, api.GetRequest{Id: ent.Id})).Entity.Summary; got != "one" {
		t.Fatalf("summary after the first undo: %q", got)
	}
	must(e.alice.Change(ctx, api.ChangeRequest{Id: f1.Change.Id, Operation: "undo"}))
	if got := must(e.alice.Get(ctx, api.GetRequest{Id: ent.Id})).Entity.Summary; got != "a friend" {
		t.Fatalf("summary after the second undo: %q", got)
	}
	must(e.alice.Change(ctx, api.ChangeRequest{Id: create.Id, Operation: "undo"}))
}

// Undo does not raise trust.
func TestUndoNeverRaisesTrust(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{}))
	// A fix that lowers trust cannot be undone: that would raise it.
	cl := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "note", Value: ptr("x"), Trust: ptr("person")}))
	low := must(e.alice.Change(ctx, api.ChangeRequest{Id: cl.Id, Operation: "fix", Set: map[string]string{"trust": "untrusted"}}))
	_, err := e.alice.Change(ctx, api.ChangeRequest{Id: low.Change.Id, Operation: "undo"})
	code(t, err, 400)
	if got := must(e.alice.Get(ctx, api.GetRequest{Id: cl.Id})).Claim; got.Trust == nil || *got.Trust != "untrusted" {
		t.Fatalf("trust after the refused undo: %v", got.Trust)
	}
	// Promote by the person, then undo the promotion: back to untrusted.
	pr := must(e.alice.Change(ctx, api.ChangeRequest{Id: cl.Id, Operation: "promote"}))
	if got := must(e.alice.Get(ctx, api.GetRequest{Id: cl.Id})).Claim; *got.Trust != "person" {
		t.Fatal("not promoted")
	}
	must(e.alice.Change(ctx, api.ChangeRequest{Id: pr.Change.Id, Operation: "undo"}))
	if got := must(e.alice.Get(ctx, api.GetRequest{Id: cl.Id})).Claim; *got.Trust != "untrusted" {
		t.Fatalf("trust after undo of promote: %v", *got.Trust)
	}
	// The promote row says the trust label of the record after the change.
	if p := lastOp(historyOf(t, e.alice, cl.Id, nil), "promote"); p.Trust == nil || *p.Trust != "person" {
		t.Fatalf("promote row trust: %v", p.Trust)
	}
	// The undo row carries the trust now.
	if u := lastOp(historyOf(t, e.alice, cl.Id, nil), "undo"); u.Trust == nil || *u.Trust != "untrusted" {
		t.Fatalf("undo row trust: %v", u.Trust)
	}
}

func TestUndoMergeAndVisibility(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{}))
	a := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Robert"}))
	b := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Bob"}))
	cb := must(e.alice.Assert(ctx, api.AssertRequest{Subject: b.Id, Relation: "likes", Value: ptr("tea")}))
	_ = me
	m := must(e.alice.Merge(ctx, api.MergeRequest{Keep: a.Id, Merge: b.Id, Origin: ptr(`{"by":"api"}`)}))
	if m.Origin == nil {
		t.Fatalf("merge row: %+v", m)
	}
	r := must(e.alice.Change(ctx, api.ChangeRequest{Id: m.Id, Operation: "undo", Reason: ptr("not the same")}))
	if r.Change.Operation != "unmerge" || r.Change.Undoes == nil || *r.Change.Undoes != m.Id || r.Change.Reason == nil {
		t.Fatalf("undo merge: %+v", r.Change)
	}
	if got := must(e.alice.Get(ctx, api.GetRequest{Id: cb.Id})).Claim; got.Subject != b.Id {
		t.Fatalf("the claim did not move back: %+v", got)
	}
	_, err := e.alice.Change(ctx, api.ChangeRequest{Id: m.Id, Operation: "undo"})
	code(t, err, 400) // undone already

	// Readers: a put that adds a reader is a visibility change; undo narrows it.
	must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Robert", Readers: []string{bob}}))
	v := lastOp(historyOf(t, e.alice, a.Id, nil), "visibility")
	if v.Before == nil || v.After == nil || !strings.Contains(*v.After, bob) {
		t.Fatalf("visibility row: %+v", v)
	}
	if _, err := e.bob.Get(ctx, api.GetRequest{Id: a.Id}); err != nil {
		t.Fatalf("bob cannot read after the put: %v", err)
	}
	must(e.alice.Change(ctx, api.ChangeRequest{Id: v.Id, Operation: "undo"}))
	if _, err := e.bob.Get(ctx, api.GetRequest{Id: a.Id}); err == nil {
		t.Fatal("bob still reads the entity after the undo of the visibility change")
	}
	// Other in-place edits are logged and can be undone: a summary added.
	must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Robert", Summary: ptr("A friend"), Aliases: []string{"Rob"}}))
	ed := lastOp(historyOf(t, e.alice, a.Id, nil), "edit")
	must(e.alice.Change(ctx, api.ChangeRequest{Id: ed.Id, Operation: "undo"}))
	if got := must(e.alice.Get(ctx, api.GetRequest{Id: a.Id})).Entity; got.Summary != "" || len(got.Aliases) != 0 {
		t.Fatalf("after the undo of the edit: %+v", got)
	}
	// A group change is logged, and cannot be undone.
	must(e.alice.PutGroup(ctx, api.PutGroupRequest{Id: "group:family", Label: "Family", Members: []string{alice}}))
	must(e.alice.PutGroup(ctx, api.PutGroupRequest{Id: "group:family", Label: "Family", Members: []string{alice, bob}}))
	g := lastOp(historyOf(t, e.alice, "group:family", nil), "visibility")
	_, err = e.alice.Change(ctx, api.ChangeRequest{Id: g.Id, Operation: "undo"})
	code(t, err, 400)
}

// Existing audit rows (from before these columns) keep reading, and the
// migration is safe to run again.
func TestAuditMigrationOfExistingSchema(t *testing.T) {
	st := testDB(t)
	url := st.DB.Config().ConnString()
	for _, q := range []string{
		`DROP INDEX changes_undoes`, `DROP INDEX changes_owners`,
		`ALTER TABLE changes ALTER COLUMN seq DROP DEFAULT`,
		`ALTER TABLE changes DROP COLUMN via, DROP COLUMN trust, DROP COLUMN origin, DROP COLUMN undoes, DROP COLUMN seq`,
		`DROP SEQUENCE changes_seq`,
		`INSERT INTO entities (id, kind, label, owners, confidence, created_ms, updated_ms) VALUES ('ent_old', 'person', 'Old', ARRAY['user:a'], 1, 1, 1)`,
		`INSERT INTO changes (id, record, operation, actor, after, owners, at_ms) VALUES ('chg_old', 'ent_old', 'create', 'user:a', '{"kind":"person","label":"Old"}', ARRAY['user:a'], 1)`,
	} {
		if _, err := st.DB.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()
	for i := 0; i < 2; i++ {
		s, err := store.Open(ctx, url)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		who := store.Who{Subject: "user:a", Principals: []string{"user:a"}}
		h, err := s.History(ctx, who, "ent_old")
		if err != nil || len(h) < 1 || h[0].Id != "chg_old" || h[0].Via != nil || h[0].Trust != nil {
			t.Fatalf("old row after open %d: %+v %v", i, h, err)
		}
		if i == 0 {
			// The old creation can be undone: rows without seq sort first.
			if _, err := s.Undo(ctx, who, "chg_old", nil, nil); err != nil {
				t.Fatalf("undo of an old row: %v", err)
			}
		} else if len(h) != 2 || h[0].UndoneBy == nil || h[1].Undoes == nil {
			t.Fatalf("the undo of the old row is not kept: %+v", h)
		}
		s.Close()
	}
}
