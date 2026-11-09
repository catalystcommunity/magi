package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
	"github.com/catalystcommunity/magi/primitives/knowledge/store"
)

func ptr[T any](v T) *T { return &v }

func TestValidation(t *testing.T) {
	s := &Service{Store: &store.Store{}}
	ctx := authn.With(context.Background(), authn.Principal{Subject: "service:x", Scopes: []string{ScopeAdmin}})
	if _, err := s.Search(ctx, api.SearchRequest{Types: []string{"node"}}); rpc.CodeOf(err) == 0 {
		t.Error("search accepted a bad type")
	}
	if err := principalList("owners", []string{"alice"}); rpc.CodeOf(err) != 400 {
		t.Errorf("principal without a prefix: %v", err)
	}
	if err := checkRefs([]api.Ref{{Kind: "bogus", Ref: "x"}}); rpc.CodeOf(err) != 400 {
		t.Errorf("ref kind: %v", err)
	}
}

// testDB creates a database for one test and drops it after. setup runs
// in the new database before the store opens it.
func testDB(t *testing.T, setup ...string) *store.Store {
	base := os.Getenv("MAGI_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("MAGI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("knowledge_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	for _, q := range setup {
		c, err := pgx.Connect(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
		c.Close(ctx)
	}
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	return st
}

type env struct {
	st                      *store.Store
	alice, bob, carol, magi *api.KnowledgeClient
}

const (
	alice = "user:alice@example.local"
	bob   = "user:bob@example.local"
	carol = "user:carol@example.local"
)

func newEnv(t *testing.T, setup ...string) env {
	st := testDB(t, setup...)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	s := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	Register(s, &Service{Store: st})
	ts := httptest.NewServer(rpc.Mux(s, nil))
	t.Cleanup(ts.Close)
	client := func(p authn.Principal) *api.KnowledgeClient {
		tok, err := iss.Issue(p, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return api.NewKnowledgeClient(rpc.NewClient(ts.URL, rpc.StaticToken(tok)))
	}
	return env{st: st,
		alice: client(authn.Principal{Subject: alice, Kind: authn.KindUser}),
		bob:   client(authn.Principal{Subject: bob, Kind: authn.KindUser}),
		carol: client(authn.Principal{Subject: carol, Kind: authn.KindUser}),
		magi:  client(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{ScopeActAs}}),
	}
}

// must returns v. An error panics, which fails the test with a stack trace.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func code(t *testing.T, err error, want int) {
	t.Helper()
	if got := rpc.CodeOf(err); got != want {
		t.Fatalf("want code %d, got %v", want, err)
	}
}

var ctx = context.Background()

func values(cs []api.Claim) []string {
	var out []string
	for _, c := range cs {
		v := ""
		if c.Value != nil {
			v = *c.Value
		}
		if c.Object != nil {
			v = *c.Object
		}
		out = append(out, c.Relation+"="+v+"/"+c.Status)
	}
	slices.Sort(out)
	return out
}

// TestMemory makes each record type and reads it with each query.
func TestMemory(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{Label: ptr("Alice")}))
	if again := must(e.alice.Self(ctx, api.SelfRequest{})); again.Id != me.Id || !slices.Contains(me.Aliases, "me") {
		t.Fatalf("self: %+v %+v", me, again)
	}
	rivera := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Dr. Rivera", Utterance: ptr("utt_1")}))
	if dup := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "dr. rivera", Aliases: []string{"Rivera"}, Summary: ptr("Dentist")})); dup.Id != rivera.Id || dup.Summary != "Dentist" || !slices.Contains(dup.Aliases, "Rivera") {
		t.Fatalf("put-entity did not find the same entity: %+v", dup)
	}
	office := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "place", Label: "Maple Street Dental",
		External: &api.ExternalRef{System: "zocdoc", Locator: "maple-street"}}))
	if x := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "place", Label: "Maple St Dental", External: &api.ExternalRef{System: "zocdoc", Locator: "maple-street"}})); x.Id != office.Id {
		t.Fatal("external reference did not match")
	}
	dentist := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "Dentist", Object: &rivera.Id, Utterance: ptr("utt_1")}))
	if dentist.Relation != "dentist" || !dentist.Supported || dentist.Status != "current" {
		t.Fatalf("claim: %+v", dentist)
	}
	if again := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "dentist", Object: &rivera.Id, Utterance: ptr("utt_2")})); again.Id != dentist.Id {
		t.Fatal("assert made a duplicate")
	}
	must(e.alice.Assert(ctx, api.AssertRequest{Subject: rivera.Id, Relation: "works-at", Object: &office.Id}))
	must(e.alice.Assert(ctx, api.AssertRequest{Subject: office.Id, Relation: "books-through", Value: ptr("Zocdoc")}))
	_, err := e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "x", Object: &me.Id})
	code(t, err, 400)
	_, err = e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "x"})
	code(t, err, 400)

	// Search: a question in words finds the claim, and the response names
	// its entities.
	r := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("Who is my dentist?")}))
	if len(r.Claims) == 0 || r.Claims[0].Id != dentist.Id {
		t.Fatalf("search: %+v", r.Hits)
	}
	if !slices.ContainsFunc(r.Entities, func(x api.Entity) bool { return x.Id == rivera.Id }) {
		t.Fatal("search did not give the object entity")
	}
	// A near label match finds the entity.
	r = must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("tell me about rivera"), Types: []string{"entity"}}))
	if len(r.Entities) == 0 || r.Entities[0].Id != rivera.Id {
		t.Fatalf("label search: %+v", r.Hits)
	}
	if r := must(e.alice.Search(ctx, api.SearchRequest{})); len(r.Hits) == 0 {
		t.Fatal("search without text gave nothing")
	}

	about := must(e.alice.About(ctx, api.AboutRequest{Id: rivera.Id}))
	if len(about.Claims) != 2 || len(about.Related) != 2 || len(about.Evidence) < 2 {
		t.Fatalf("about: %d claims, %d related, %d evidence", len(about.Claims), len(about.Related), len(about.Evidence))
	}
	n := must(e.alice.Neighbors(ctx, api.NeighborsRequest{Id: me.Id, Depth: ptr(uint64(2))}))
	if len(n.Entities) != 3 || len(n.Claims) != 2 {
		t.Fatalf("neighbors: %d entities, %d claims", len(n.Entities), len(n.Claims))
	}
	if n := must(e.alice.Neighbors(ctx, api.NeighborsRequest{Id: me.Id})); len(n.Entities) != 2 {
		t.Fatalf("neighbors depth 1: %d", len(n.Entities))
	}

	// Episodes and procedures.
	start := uint64(time.Now().Add(-time.Hour).UnixMilli())
	ep := must(e.alice.RecordEpisode(ctx, api.RecordEpisodeRequest{Kind: "investigation", Title: "Dentist booking", StartedMs: &start,
		Entities: []string{rivera.Id}, Refs: []api.Ref{{Kind: "utterance", Ref: "utt_1"}}}))
	proc := must(e.alice.PutProcedure(ctx, api.PutProcedureRequest{Name: "book-dentist", Goal: "Book a dentist visit", Steps: "1. Open Zocdoc.", Entities: []string{rivera.Id}}))
	proc2 := must(e.alice.PutProcedure(ctx, api.PutProcedureRequest{Name: "book-dentist", Goal: "Book a dentist visit", Steps: "1. Open Zocdoc. 2. Pick a time.", Entities: []string{rivera.Id}}))
	if proc.Revision != 1 || proc2.Revision != 2 {
		t.Fatalf("revisions %d %d", proc.Revision, proc2.Revision)
	}
	ep = must(e.alice.RecordEpisode(ctx, api.RecordEpisodeRequest{Id: &ep.Id, Procedure: &proc2.Id, Outcome: ptr("succeeded"), Refs: []api.Ref{{Kind: "workflow-run", Ref: "run_1"}}}))
	if len(ep.Refs) != 2 || *ep.Outcome != "succeeded" {
		t.Fatalf("episode update: %+v", ep)
	}
	p := must(e.alice.Get(ctx, api.GetRequest{Id: proc2.Id}))
	if p.Procedure.Uses["succeeded"] != 1 {
		t.Fatalf("procedure uses: %+v", p.Procedure.Uses)
	}
	about = must(e.alice.About(ctx, api.AboutRequest{Id: rivera.Id}))
	if len(about.Episodes) != 1 || len(about.Procedures) != 1 || about.Procedures[0].Revision != 2 {
		t.Fatalf("about: %d episodes, procedures %+v", len(about.Episodes), about.Procedures)
	}
	r = must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("how do I book a dentist"), Types: []string{"procedure"}}))
	if len(r.Procedures) != 1 || r.Procedures[0].Revision != 2 {
		t.Fatalf("procedure search: %+v", r.Procedures)
	}
	tl := must(e.alice.Timeline(ctx, api.TimelineRequest{FromMs: start - 1, ToMs: uint64(time.Now().Add(time.Minute).UnixMilli())}))
	if len(tl.Episodes) != 1 || len(tl.Claims) != 3 || len(tl.Changes) == 0 {
		t.Fatalf("timeline: %d episodes, %d claims, %d changes", len(tl.Episodes), len(tl.Claims), len(tl.Changes))
	}
}

// TestChanges checks the three operations (docs/knowledge.md, "Changes").
func TestChanges(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{}))
	dana := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Dana"}))
	lives := must(e.alice.Assert(ctx, api.AssertRequest{Subject: dana.Id, Relation: "lives-in", Value: ptr("Tucson"), Utterance: ptr("utt_1")}))
	party := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "party-guests", Value: ptr("32")}))
	stew := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "stew-batches", Value: ptr("3"), DerivedFrom: []string{party.Id}}))

	// fix: in place, no record to follow, the old value in the history.
	r := must(e.alice.Change(ctx, api.ChangeRequest{Id: lives.Id, Operation: "fix", Set: map[string]string{"value": "Phoenix"}, Reason: ptr("misspoke"), Utterance: ptr("utt_2")}))
	if r.Claim == nil || r.Claim.Id != lives.Id || *r.Claim.Value != "Phoenix" || r.Claim.Status != "current" {
		t.Fatalf("fix: %+v", r.Claim)
	}
	if s := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("Tucson"), IncludeEnded: ptr(true)})); len(s.Claims) != 0 {
		t.Fatal("a fixed value is still found")
	}
	h := must(e.alice.History(ctx, api.HistoryRequest{Id: lives.Id}))
	if len(h.Changes) != 2 || h.Changes[1].Operation != "fix" || !strings.Contains(*h.Changes[1].Before, "Tucson") || *h.Changes[1].Utterance != "utt_2" {
		t.Fatalf("history: %+v", h.Changes)
	}

	// fix of an object: the wrong object goes too when nothing else names
	// it; an object that another claim names stays.
	closet := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "place", Label: "basement closet"}))
	garage := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "place", Label: "garage"}))
	router := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "device", Label: "router"}))
	at := must(e.alice.Assert(ctx, api.AssertRequest{Subject: router.Id, Relation: "located-in", Object: &closet.Id}))
	home := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "place", Label: "Elm house"}))
	sam := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Sam"}))
	must(e.alice.Assert(ctx, api.AssertRequest{Subject: sam.Id, Relation: "owns", Object: &home.Id}))
	visits := must(e.alice.Assert(ctx, api.AssertRequest{Subject: router.Id, Relation: "shipped-to", Object: &home.Id}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: at.Id, Operation: "fix", Set: map[string]string{"object": garage.Id}}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: visits.Id, Operation: "fix", Set: map[string]string{"object": garage.Id}}))
	// A value fixed to an object (and back): one of the two at all times.
	coach := must(e.alice.Assert(ctx, api.AssertRequest{Subject: sam.Id, Relation: "coach", Value: ptr("Jen Park")}))
	parks := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Jen Parks"}))
	if c := must(e.alice.Change(ctx, api.ChangeRequest{Id: coach.Id, Operation: "fix", Set: map[string]string{"object": parks.Id}})); c.Claim == nil || c.Claim.Object == nil || *c.Claim.Object != parks.Id || c.Claim.Value != nil {
		t.Fatalf("value to object: %+v", c.Claim)
	}
	if c := must(e.alice.Change(ctx, api.ChangeRequest{Id: coach.Id, Operation: "fix", Set: map[string]string{"value": "Jen Parks"}})); c.Claim == nil || c.Claim.Value == nil || c.Claim.Object != nil {
		t.Fatalf("object to value: %+v", c.Claim)
	}
	if s := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("closet")})); len(s.Entities) != 0 {
		t.Errorf("the wrong object is still there: %+v", s.Entities)
	}
	if s := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("Elm house")})); !slices.ContainsFunc(s.Entities, func(x api.Entity) bool { return x.Id == home.Id }) {
		t.Errorf("an object that another claim names is gone: %+v", s.Entities)
	}
	if h := must(e.alice.History(ctx, api.HistoryRequest{Id: at.Id})); !strings.Contains(*h.Changes[len(h.Changes)-1].After, closet.Id) {
		t.Errorf("the fix does not name the removed object: %+v", h.Changes)
	}

	// A retracted claim takes an object that only it named with it.
	gym := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "organization", Label: "Arcadia Climbing Gym"}))
	member := must(e.alice.Assert(ctx, api.AssertRequest{Subject: sam.Id, Relation: "member-of", Object: &gym.Id}))
	owner := must(e.alice.Assert(ctx, api.AssertRequest{Subject: router.Id, Relation: "bought-by", Object: &sam.Id}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: member.Id, Operation: "retract"}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: owner.Id, Operation: "retract"}))
	if s := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("Arcadia")})); len(s.Entities) != 0 {
		t.Errorf("the object of a retracted claim is still there: %+v", s.Entities)
	}
	if s := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("Sam")})); !slices.ContainsFunc(s.Entities, func(x api.Entity) bool { return x.Id == sam.Id }) {
		t.Errorf("an object with other claims is gone: %+v", s.Entities)
	}

	// update: the old claim ends, the new one is current; the past is a query.
	before := uint64(time.Now().UnixMilli())
	time.Sleep(5 * time.Millisecond)
	u := must(e.alice.Change(ctx, api.ChangeRequest{Id: party.Id, Operation: "update", Set: map[string]string{"value": "40"}}))
	if u.Claim.Id == party.Id || *u.Claim.Value != "40" || u.Claim.ValidFromMs == nil {
		t.Fatalf("update: %+v", u.Claim)
	}
	a := must(e.alice.About(ctx, api.AboutRequest{Id: me.Id}))
	if got := values(a.Claims); !slices.Equal(got, []string{"party-guests=40/current", "stew-batches=3/current"}) {
		t.Fatalf("current claims: %v", got)
	}
	a = must(e.alice.About(ctx, api.AboutRequest{Id: me.Id, AtMs: &before}))
	if got := values(a.Claims); !slices.Equal(got, []string{"party-guests=32/ended", "stew-batches=3/current"}) {
		t.Fatalf("claims at a past time: %v", got)
	}
	old := must(e.alice.Get(ctx, api.GetRequest{Id: party.Id}))
	if old.Claim.Status != "ended" || *old.Claim.ReplacedBy != u.Claim.Id {
		t.Fatalf("ended claim: %+v", old.Claim)
	}
	// A claim derived from the changed claim is stale.
	if s := must(e.alice.Get(ctx, api.GetRequest{Id: stew.Id})); !s.Claim.Stale {
		t.Fatal("derived claim is not stale")
	}
	_, err := e.alice.Change(ctx, api.ChangeRequest{Id: party.Id, Operation: "update", Set: map[string]string{"value": "41"}})
	code(t, err, 400)
	_, err = e.alice.Change(ctx, api.ChangeRequest{Id: dana.Id, Operation: "update", Set: map[string]string{"label": "Dana B"}})
	code(t, err, 400)
	_, err = e.alice.Change(ctx, api.ChangeRequest{Id: dana.Id, Operation: "fix", Set: map[string]string{"relation": "x"}})
	code(t, err, 400)

	// fix of an entity: label, alias, property.
	f := must(e.alice.Change(ctx, api.ChangeRequest{Id: dana.Id, Operation: "fix", Set: map[string]string{"label": "Dana Smith", "aliases": "Dana, sis", "property.phone": "555"}}))
	if f.Entity.Label != "Dana Smith" || !slices.Contains(f.Entity.Aliases, "sis") || f.Entity.Properties["phone"] != "555" {
		t.Fatalf("entity fix: %+v", f.Entity)
	}
	if s := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("where does sis live"), Types: []string{"claim"}})); len(s.Claims) != 1 {
		t.Fatalf("claim search after an alias fix: %+v", s.Hits)
	}
	f = must(e.alice.Change(ctx, api.ChangeRequest{Id: dana.Id, Operation: "fix", Set: map[string]string{"property.phone": ""}}))
	if _, ok := f.Entity.Properties["phone"]; ok {
		t.Fatal("empty property value did not remove it")
	}

	// retract: gone from queries; the history keeps it; derived claims stale.
	must(e.alice.Change(ctx, api.ChangeRequest{Id: stew.Id, Operation: "fix", Set: map[string]string{"value": "4"}}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: dana.Id, Operation: "retract", Reason: ptr("no such person")}))
	_, err = e.alice.Get(ctx, api.GetRequest{Id: lives.Id})
	code(t, err, 404)
	_, err = e.alice.About(ctx, api.AboutRequest{Id: dana.Id})
	code(t, err, 404)
	if s := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("Dana Phoenix")})); len(s.Hits) != 0 {
		t.Fatalf("retracted records found: %+v", s.Hits)
	}
	if h := must(e.alice.History(ctx, api.HistoryRequest{Id: dana.Id})); h.Changes[len(h.Changes)-1].Operation != "retract" {
		t.Fatal("no retraction in the history")
	}
	_, err = e.alice.Change(ctx, api.ChangeRequest{Id: dana.Id, Operation: "fix", Set: map[string]string{"label": "x"}})
	code(t, err, 404)

	// erase: no content in the history.
	ugly := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "password", Value: ptr("hunter2")}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: ugly.Id, Operation: "fix", Set: map[string]string{"value": "hunter3"}}))
	must(e.alice.Change(ctx, api.ChangeRequest{Id: ugly.Id, Operation: "retract", Erase: ptr(true)}))
	for _, c := range must(e.alice.History(ctx, api.HistoryRequest{Id: ugly.Id})).Changes {
		if c.Operation != "retract" && (c.Before != nil || c.After != nil) {
			t.Fatalf("erased content in the history: %+v", c)
		}
	}
	var n int
	e.st.DB.QueryRow(ctx, `SELECT count(*) FROM claims WHERE id = $1`, ugly.Id).Scan(&n)
	if n != 0 {
		t.Fatal("erased claim is still stored")
	}

	// evidence: when the last support goes, the claim is unsupported.
	job := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "works-at", Value: ptr("Brightline"), Utterance: ptr("utt_9")}))
	ev := must(e.alice.About(ctx, api.AboutRequest{Id: me.Id})).Evidence
	i := slices.IndexFunc(ev, func(v api.Evidence) bool { return v.Subject == job.Id })
	must(e.alice.Change(ctx, api.ChangeRequest{Id: ev[i].Id, Operation: "retract"}))
	if c := must(e.alice.Get(ctx, api.GetRequest{Id: job.Id})); c.Claim.Supported {
		t.Fatal("claim without evidence is supported")
	}
}

// TestAccess checks owners, readers, groups, disputes, and act_for.
func TestAccess(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{}))
	secret := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "gift-for-bob", Value: ptr("a bike")}))
	_, err := e.bob.Get(ctx, api.GetRequest{Id: secret.Id})
	code(t, err, 404)
	if r := must(e.bob.Search(ctx, api.SearchRequest{Text: ptr("bike")})); len(r.Hits) != 0 {
		t.Fatal("bob found alice's record")
	}
	_, err = e.bob.Change(ctx, api.ChangeRequest{Id: secret.Id, Operation: "retract"})
	code(t, err, 404)
	if h := must(e.bob.History(ctx, api.HistoryRequest{Id: secret.Id})); len(h.Changes) != 0 {
		t.Fatal("bob read alice's history")
	}

	// A household group: members co-own its records.
	_, err = e.alice.PutGroup(ctx, api.PutGroupRequest{Id: "group:house", Label: "House", Members: []string{alice, bob}, Owners: []string{bob}})
	code(t, err, 400)
	must(e.alice.PutGroup(ctx, api.PutGroupRequest{Id: "group:house", Label: "House", Members: []string{alice, bob}}))
	_, err = e.bob.PutGroup(ctx, api.PutGroupRequest{Id: "group:house", Label: "Mine", Members: []string{bob}})
	code(t, err, 404)
	_, err = e.carol.PutEntity(ctx, api.PutEntityRequest{Kind: "place", Label: "House", Owners: []string{"group:house"}})
	code(t, err, 403)
	router := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "device", Label: "Router", Owners: []string{"group:house"}}))
	where := must(e.alice.Assert(ctx, api.AssertRequest{Subject: router.Id, Relation: "located-in", Value: ptr("basement"), Owners: []string{"group:house"}}))
	fixed := must(e.bob.Change(ctx, api.ChangeRequest{Id: where.Id, Operation: "fix", Set: map[string]string{"value": "garage"}}))
	if *fixed.Claim.Value != "garage" {
		t.Fatal("co-owner fix")
	}
	if h := must(e.alice.History(ctx, api.HistoryRequest{Id: where.Id})); h.Changes[len(h.Changes)-1].Actor != bob {
		t.Fatalf("history actor: %+v", h.Changes)
	}

	// A reader can read and dispute, not change. An own claim is allowed.
	garden := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "place", Label: "Garden", Readers: []string{carol}}))
	shared := must(e.alice.Assert(ctx, api.AssertRequest{Subject: garden.Id, Relation: "fence-color", Value: ptr("green"), Readers: []string{carol}}))
	if c := must(e.carol.Get(ctx, api.GetRequest{Id: shared.Id})); c.Claim == nil {
		t.Fatal("reader cannot read")
	}
	_, err = e.carol.Change(ctx, api.ChangeRequest{Id: shared.Id, Operation: "fix", Set: map[string]string{"value": "blue"}})
	code(t, err, 404)
	d := must(e.carol.Dispute(ctx, api.DisputeRequest{Record: shared.Id, Reason: "she told me blue", Proposed: ptr("blue")}))
	if d.State != "open" || !slices.Equal(d.Owners, []string{alice}) {
		t.Fatalf("dispute: %+v", d)
	}
	_, err = e.bob.Dispute(ctx, api.DisputeRequest{Record: secret.Id, Reason: "x"})
	code(t, err, 404)
	if l := must(e.alice.ListDisputes(ctx, api.ListDisputesRequest{State: ptr("open")})); len(l.Disputes) != 1 {
		t.Fatal("owner does not see the dispute")
	}
	if a := must(e.alice.About(ctx, api.AboutRequest{Id: garden.Id})); len(a.Disputes) != 1 {
		t.Fatal("about does not give the dispute")
	}
	_, err = e.carol.ResolveDispute(ctx, api.ResolveDisputeRequest{Id: d.Id, Accept: true})
	code(t, err, 404)
	if r := must(e.alice.ResolveDispute(ctx, api.ResolveDisputeRequest{Id: d.Id, Accept: false, Resolution: ptr("it is green")})); r.State != "rejected" {
		t.Fatal("resolve")
	}
	_, err = e.carol.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "x", Value: ptr("y")})
	code(t, err, 404)
	carolSees := must(e.carol.Assert(ctx, api.AssertRequest{Subject: garden.Id, Relation: "fence-color", Value: ptr("blue")}))
	if !slices.Equal(carolSees.Owners, []string{carol}) {
		t.Fatal("own claim owners")
	}
	if c := must(e.alice.About(ctx, api.AboutRequest{Id: garden.Id})); slices.ContainsFunc(c.Claims, func(x api.Claim) bool { return x.Id == carolSees.Id }) {
		t.Fatal("alice sees carol's private claim")
	}

	// act_for.
	_, err = e.bob.Search(ctx, api.SearchRequest{ActFor: ptr(alice)})
	code(t, err, 403)
	if r := must(e.magi.Search(ctx, api.SearchRequest{Text: ptr("bike"), ActFor: ptr(alice)})); len(r.Claims) != 1 {
		t.Fatal("act_for search")
	}
	x := must(e.magi.PutEntity(ctx, api.PutEntityRequest{Kind: "thing", Label: "x", ActFor: ptr(alice)}))
	if !slices.Equal(x.Owners, []string{alice}) {
		t.Fatalf("act_for owners: %v", x.Owners)
	}
}

func TestMerge(t *testing.T) {
	e := newEnv(t)
	me := must(e.alice.Self(ctx, api.SelfRequest{}))
	a := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Dr. Rivera", Summary: ptr("Dentist")}))
	b := must(e.alice.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Doctor Rivera", Properties: map[string]string{"phone": "555"}}))
	c1 := must(e.alice.Assert(ctx, api.AssertRequest{Subject: me.Id, Relation: "dentist", Object: &b.Id}))
	must(e.alice.Assert(ctx, api.AssertRequest{Subject: b.Id, Relation: "works-at", Value: ptr("Maple Street Dental")}))
	other := must(e.bob.PutEntity(ctx, api.PutEntityRequest{Kind: "person", Label: "Rivera"}))
	_, err := e.alice.Merge(ctx, api.MergeRequest{Keep: a.Id, Merge: other.Id})
	code(t, err, 404)

	m := must(e.alice.Merge(ctx, api.MergeRequest{Keep: a.Id, Merge: b.Id, Reason: ptr("the same dentist")}))
	got := must(e.alice.About(ctx, api.AboutRequest{Id: a.Id}))
	if len(got.Claims) != 2 || !slices.Contains(got.Entity.Aliases, "Doctor Rivera") || got.Entity.Properties["phone"] != "555" {
		t.Fatalf("after merge: %+v %d", got.Entity, len(got.Claims))
	}
	// The merged entity's id leads to the kept entity.
	if v := must(e.alice.About(ctx, api.AboutRequest{Id: b.Id})); v.Entity.Id != a.Id {
		t.Fatal("merged id does not lead to the kept entity")
	}
	if r := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("Doctor Rivera"), Types: []string{"entity"}})); len(r.Entities) != 1 || r.Entities[0].Id != a.Id {
		t.Fatalf("search after merge: %+v", r.Entities)
	}

	must(e.alice.Unmerge(ctx, api.UnmergeRequest{Change: m.Id}))
	got = must(e.alice.About(ctx, api.AboutRequest{Id: a.Id}))
	if len(got.Claims) != 0 || slices.Contains(got.Entity.Aliases, "Doctor Rivera") || got.Entity.Properties["phone"] != "" {
		t.Fatalf("after unmerge: %+v %d", got.Entity, len(got.Claims))
	}
	if c := must(e.alice.Get(ctx, api.GetRequest{Id: c1.Id})); *c.Claim.Object != b.Id {
		t.Fatal("unmerge did not move the claim back")
	}
	_, err = e.alice.Unmerge(ctx, api.UnmergeRequest{Change: m.Id})
	code(t, err, 400)
}

// TestLegacy opens a database of the first schema.
func TestLegacy(t *testing.T) {
	old := `CREATE TABLE nodes (id text PRIMARY KEY, owner text NOT NULL, kind text NOT NULL, label text NOT NULL, summary text NOT NULL DEFAULT '',
		properties jsonb NOT NULL DEFAULT '{}', ext_system text, ext_locator text, ext_context text, scope text NOT NULL, authored_by text,
		confidence double precision NOT NULL, created_ms bigint NOT NULL, updated_ms bigint NOT NULL, search_text text NOT NULL DEFAULT '');
	CREATE TABLE edges (id text PRIMARY KEY, owner text NOT NULL, from_node text NOT NULL REFERENCES nodes(id), to_node text NOT NULL REFERENCES nodes(id),
		relation text NOT NULL, properties jsonb NOT NULL DEFAULT '{}', confidence double precision NOT NULL, created_ms bigint NOT NULL);
	CREATE TABLE evidence (id text PRIMARY KEY, owner text NOT NULL, subject text NOT NULL, ref_kind text NOT NULL, ref text NOT NULL,
		stance text NOT NULL, note text, created_ms bigint NOT NULL, UNIQUE (subject, ref_kind, ref));
	INSERT INTO nodes VALUES ('nod_cal', 'user:alice@example.local', 'calendar', 'Family', 'Family calendar', '{}', 'nod_nc', 'family', NULL, 'private', NULL, 0.7, 1, 1, ''),
		('nod_nc', 'user:alice@example.local', 'system', 'Nextcloud', '', '{}', NULL, NULL, NULL, 'private', NULL, 0.6, 1, 1, '');
	INSERT INTO edges VALUES ('edg_1', 'user:alice@example.local', 'nod_cal', 'nod_nc', 'hosted-in', '{}', 1, 1);
	INSERT INTO evidence VALUES ('evd_1', 'user:alice@example.local', 'nod_cal', 'utterance', 'utt_1', 'supports', NULL, 1);`
	e := newEnv(t, old)
	a := must(e.alice.About(ctx, api.AboutRequest{Id: "nod_cal"}))
	if a.Entity.External == nil || len(a.Claims) != 1 || a.Claims[0].Relation != "hosted-in" || len(a.Evidence) != 1 {
		t.Fatalf("legacy: %+v", a)
	}
	if r := must(e.alice.Search(ctx, api.SearchRequest{Text: ptr("which calendar is in nextcloud")})); len(r.Claims) != 1 {
		t.Fatalf("legacy search: %+v", r.Hits)
	}
	// Opening again does not copy again.
	st2, err := store.Open(ctx, e.st.DB.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()
	var n int
	e.st.DB.QueryRow(ctx, `SELECT count(*) FROM entities`).Scan(&n)
	if n != 2 {
		t.Fatalf("entities after reopening: %d", n)
	}
}
