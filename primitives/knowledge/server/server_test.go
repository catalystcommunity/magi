package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
	"github.com/catalystcommunity/magi/primitives/knowledge/store"
)

func ptr[T any](v T) *T { return &v }

func TestOwner(t *testing.T) {
	user := authn.Principal{Subject: "user:a@example.local"}
	svc := authn.Principal{Subject: "service:magi", Scopes: []string{ScopeActAs}}
	if o, err := owner(user, nil); err != nil || o != user.Subject {
		t.Fatalf("default owner: %q %v", o, err)
	}
	if _, err := owner(user, ptr("user:b@example.local")); rpc.CodeOf(err) != 403 {
		t.Fatalf("user acting for another: %v", err)
	}
	if o, err := owner(svc, ptr("user:b@example.local")); err != nil || o != "user:b@example.local" {
		t.Fatalf("act-as: %q %v", o, err)
	}
	if !canAccess(svc, "user:x") || canAccess(user, "user:x") || !canAccess(user, user.Subject) {
		t.Fatal("canAccess")
	}
}

func TestValidation(t *testing.T) {
	s := &Service{}
	ctx := authn.With(context.Background(), authn.Principal{Subject: "user:a@example.local"})
	cases := []api.PutNodeRequest{
		{Kind: "", Label: "x"},
		{Kind: "fact", Label: " "},
		{Kind: "fact", Label: "x", Confidence: ptr(1.5)},
		{Kind: "fact", Label: "x", External: &api.ExternalRef{System: "a"}},
	}
	for i, c := range cases {
		if _, err := s.PutNode(ctx, c); rpc.CodeOf(err) != 400 {
			t.Errorf("case %d: %v", i, err)
		}
	}
	if _, err := s.LinkEvidence(ctx, api.LinkEvidenceRequest{Subject: "nod_x", RefKind: "bogus", Ref: "r"}); rpc.CodeOf(err) != 400 {
		t.Errorf("ref_kind: %v", err)
	}
	if _, err := s.LinkEvidence(ctx, api.LinkEvidenceRequest{Subject: "nod_x", RefKind: "url", Ref: "r", Stance: ptr("maybe")}); rpc.CodeOf(err) != 400 {
		t.Errorf("stance: %v", err)
	}
}

// testDB creates a database for one test and drops it after.
func testDB(t *testing.T) *store.Store {
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
	alice, bob, magi *api.KnowledgeClient
}

const (
	alice = "user:alice@example.local"
	bob   = "user:bob@example.local"
)

func newEnv(t *testing.T) env {
	st := testDB(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	s := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}}
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
	return env{
		alice: client(authn.Principal{Subject: alice, Kind: authn.KindUser}),
		bob:   client(authn.Principal{Subject: bob, Kind: authn.KindUser}),
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

// TestCalendars stores "alice has three calendars in system A and her work
// calendar in system B" and reads it back through every operation.
func TestCalendars(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.alice

	person := must(c.PutNode(ctx, api.PutNodeRequest{Kind: "person", Label: "Alice Example", Summary: ptr("Owner of the household calendars")}))
	sysA := must(c.PutNode(ctx, api.PutNodeRequest{Kind: "system", Label: "Calendar host A"}))
	sysB := must(c.PutNode(ctx, api.PutNodeRequest{Kind: "system", Label: "Work suite B"}))
	if person.Owner != alice || person.Scope != "private" || person.Confidence != 1 || person.Properties == nil {
		t.Fatalf("defaults: %+v", person)
	}
	var cals []api.Node
	for i, l := range []string{"Family", "Garden", "Kids school", "Work"} {
		sys := sysA
		if i == 3 {
			sys = sysB
		}
		n := must(c.PutNode(ctx, api.PutNodeRequest{Kind: "calendar", Label: l,
			Properties: map[string]string{"timezone": "America/Denver"},
			External:   &api.ExternalRef{System: sys.Id, Locator: "https://cal.example.local/" + l, Context: ptr("calendar list")}}))
		must(c.PutEdge(ctx, api.PutEdgeRequest{FromNode: person.Id, ToNode: n.Id, Relation: "owns"}))
		must(c.PutEdge(ctx, api.PutEdgeRequest{FromNode: n.Id, ToNode: sys.Id, Relation: "hosted-in"}))
		cals = append(cals, n)
	}

	// put-node without id matches owner, system, and locator.
	again := must(c.PutNode(ctx, api.PutNodeRequest{Kind: "calendar", Label: "Work (renamed)",
		External: &api.ExternalRef{System: sysB.Id, Locator: "https://cal.example.local/Work"}}))
	if again.Id != cals[3].Id || again.Label != "Work (renamed)" || again.Properties["timezone"] != "America/Denver" || again.External.Context == nil {
		t.Fatalf("external upsert: %+v", again)
	}
	// put-node with id updates.
	upd := must(c.PutNode(ctx, api.PutNodeRequest{Id: &person.Id, Kind: "person", Label: "Alice Example", Confidence: ptr(0.9)}))
	if upd.Id != person.Id || upd.Summary != person.Summary || upd.Confidence != 0.9 || upd.UpdatedMs < person.UpdatedMs {
		t.Fatalf("id update: %+v", upd)
	}
	_, err := c.PutNode(ctx, api.PutNodeRequest{Id: ptr("nod_missing"), Kind: "x", Label: "y"})
	code(t, err, 404)

	// put-edge is idempotent.
	e1 := must(c.PutEdge(ctx, api.PutEdgeRequest{FromNode: person.Id, ToNode: cals[0].Id, Relation: "owns", Properties: map[string]string{"role": "primary"}}))
	view := must(c.GetNode(ctx, api.GetNodeRequest{Id: person.Id}))
	if len(view.Edges) != 4 {
		t.Fatalf("person edges: %d", len(view.Edges))
	}

	// link-evidence on a node and an edge.
	ev := must(c.LinkEvidence(ctx, api.LinkEvidenceRequest{Subject: cals[0].Id, RefKind: "artifact", Ref: "art_abc", Note: ptr("export")}))
	must(c.LinkEvidence(ctx, api.LinkEvidenceRequest{Subject: e1.Id, RefKind: "utterance", Ref: "utt_1", Stance: ptr("mentions")}))
	ev2 := must(c.LinkEvidence(ctx, api.LinkEvidenceRequest{Subject: cals[0].Id, RefKind: "artifact", Ref: "art_abc", Stance: ptr("contradicts")}))
	if ev2.Id != ev.Id || ev2.Stance != "contradicts" || ev2.Note == nil || *ev2.Note != "export" || ev.Stance != "supports" {
		t.Fatalf("evidence upsert: %+v %+v", ev, ev2)
	}
	_, err = c.LinkEvidence(ctx, api.LinkEvidenceRequest{Subject: "nod_missing", RefKind: "url", Ref: "x"})
	code(t, err, 404)
	view = must(c.GetNode(ctx, api.GetNodeRequest{Id: cals[0].Id}))
	if len(view.Evidence) != 2 || len(view.Edges) != 2 {
		t.Fatalf("calendar view: %d evidence, %d edges", len(view.Evidence), len(view.Edges))
	}

	// query: text with full text and partial words, kind, near, depth.
	q := func(r api.QueryRequest) api.QueryResponse { return must(c.Query(ctx, r)) }
	if r := q(api.QueryRequest{Text: ptr("households")}); len(r.Nodes) != 1 || r.Nodes[0].Id != person.Id {
		t.Fatalf("full text stem: %+v", r.Nodes)
	}
	if r := q(api.QueryRequest{Text: ptr("gard")}); len(r.Nodes) != 1 || r.Nodes[0].Label != "Garden" {
		t.Fatalf("partial word: %+v", r.Nodes)
	}
	if r := q(api.QueryRequest{Text: ptr("Where is my gard plot?")}); len(r.Nodes) == 0 || r.Nodes[0].Label != "Garden" {
		t.Fatalf("any word of a question: %+v", r.Nodes)
	}
	if r := q(api.QueryRequest{Text: ptr("denver"), Kind: ptr("calendar")}); len(r.Nodes) != 4 {
		t.Fatalf("property text: %d", len(r.Nodes))
	}
	if r := q(api.QueryRequest{Kind: ptr("system")}); len(r.Nodes) != 2 || len(r.Edges) != 0 {
		t.Fatalf("kind: %+v", r)
	}
	if r := q(api.QueryRequest{Near: &person.Id}); len(r.Nodes) != 5 || r.Nodes[0].Id != person.Id || len(r.Edges) != 4 {
		t.Fatalf("near depth 1: %d nodes %d edges", len(r.Nodes), len(r.Edges))
	}
	if r := q(api.QueryRequest{Near: &person.Id, Depth: ptr(uint64(2))}); len(r.Nodes) != 7 || len(r.Edges) != 8 {
		t.Fatalf("near depth 2: %d nodes %d edges", len(r.Nodes), len(r.Edges))
	}
	if r := q(api.QueryRequest{Near: &person.Id, Depth: ptr(uint64(2)), Kind: ptr("system")}); len(r.Nodes) != 2 {
		t.Fatalf("near and kind: %d", len(r.Nodes))
	}
	if r := q(api.QueryRequest{Limit: ptr(uint64(3))}); len(r.Nodes) != 3 {
		t.Fatalf("limit: %d", len(r.Nodes))
	}
	if r := q(api.QueryRequest{Text: ptr("100%_")}); len(r.Nodes) != 0 {
		t.Fatalf("like escape: %d", len(r.Nodes))
	}

	// delete an edge, then a node with its edges and evidence.
	if d := must(c.Delete(ctx, api.DeleteRequest{Id: e1.Id})); !d.Deleted {
		t.Fatal("edge not deleted")
	}
	if d := must(c.Delete(ctx, api.DeleteRequest{Id: cals[0].Id})); !d.Deleted {
		t.Fatal("node not deleted")
	}
	_, err = c.GetNode(ctx, api.GetNodeRequest{Id: cals[0].Id})
	code(t, err, 404)
	if v := must(c.GetNode(ctx, api.GetNodeRequest{Id: person.Id})); len(v.Edges) != 3 {
		t.Fatalf("edges after delete: %d", len(v.Edges))
	}
	if d := must(c.Delete(ctx, api.DeleteRequest{Id: cals[0].Id})); d.Deleted {
		t.Fatal("second delete")
	}
}

func TestAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	n := must(e.alice.PutNode(ctx, api.PutNodeRequest{Kind: "fact", Label: "Alice likes tea"}))
	m := must(e.alice.PutNode(ctx, api.PutNodeRequest{Kind: "fact", Label: "Alice likes rain"}))
	edge := must(e.alice.PutEdge(ctx, api.PutEdgeRequest{FromNode: n.Id, ToNode: m.Id, Relation: "related"}))

	// Bob cannot see or change Alice's records.
	_, err := e.bob.GetNode(ctx, api.GetNodeRequest{Id: n.Id})
	code(t, err, 404)
	_, err = e.bob.PutNode(ctx, api.PutNodeRequest{Id: &n.Id, Kind: "fact", Label: "changed"})
	code(t, err, 404)
	_, err = e.bob.PutNode(ctx, api.PutNodeRequest{Kind: "fact", Label: "x", Owner: ptr(alice)})
	code(t, err, 403)
	_, err = e.bob.PutEdge(ctx, api.PutEdgeRequest{FromNode: n.Id, ToNode: m.Id, Relation: "x"})
	code(t, err, 404)
	_, err = e.bob.LinkEvidence(ctx, api.LinkEvidenceRequest{Subject: n.Id, RefKind: "url", Ref: "https://example.local"})
	code(t, err, 404)
	_, err = e.bob.Query(ctx, api.QueryRequest{Owner: ptr(alice)})
	code(t, err, 403)
	_, err = e.bob.Query(ctx, api.QueryRequest{Near: &n.Id})
	code(t, err, 404)
	if r := must(e.bob.Query(ctx, api.QueryRequest{Text: ptr("tea")})); len(r.Nodes) != 0 {
		t.Fatal("bob sees alice's node")
	}
	for _, id := range []string{n.Id, edge.Id} {
		if d := must(e.bob.Delete(ctx, api.DeleteRequest{Id: id})); d.Deleted {
			t.Fatal("bob deleted alice's record")
		}
	}

	// A service with act-as works on Alice's records.
	sn := must(e.magi.PutNode(ctx, api.PutNodeRequest{Kind: "fact", Label: "Alice drinks green tea", Owner: ptr(alice)}))
	if sn.Owner != alice {
		t.Fatalf("act-as owner: %s", sn.Owner)
	}
	must(e.magi.PutEdge(ctx, api.PutEdgeRequest{FromNode: sn.Id, ToNode: n.Id, Relation: "refines", Owner: ptr(alice)}))
	_, err = e.magi.PutEdge(ctx, api.PutEdgeRequest{FromNode: sn.Id, ToNode: n.Id, Relation: "refines"})
	code(t, err, 404) // the nodes do not belong to the service itself
	must(e.magi.LinkEvidence(ctx, api.LinkEvidenceRequest{Subject: sn.Id, RefKind: "utterance", Ref: "utt_2"}))
	if r := must(e.magi.Query(ctx, api.QueryRequest{Text: ptr("tea"), Owner: ptr(alice)})); len(r.Nodes) != 2 || len(r.Edges) != 1 {
		t.Fatalf("act-as query: %d nodes %d edges", len(r.Nodes), len(r.Edges))
	}
	if r := must(e.magi.Query(ctx, api.QueryRequest{Text: ptr("tea")})); len(r.Nodes) != 0 {
		t.Fatal("service sees alice's nodes without owner")
	}
	if v := must(e.magi.GetNode(ctx, api.GetNodeRequest{Id: sn.Id})); len(v.Evidence) != 1 {
		t.Fatal("act-as get")
	}
	if v := must(e.alice.GetNode(ctx, api.GetNodeRequest{Id: sn.Id})); v.Node.Owner != alice {
		t.Fatal("alice cannot read node made for her")
	}
	if d := must(e.magi.Delete(ctx, api.DeleteRequest{Id: sn.Id})); !d.Deleted {
		t.Fatal("act-as delete")
	}
}
