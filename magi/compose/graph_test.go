package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	knowledgeserver "github.com/catalystcommunity/magi/primitives/knowledge/server"
	knowledgestore "github.com/catalystcommunity/magi/primitives/knowledge/store"
)

// testDatabase makes a new database on MAGI_TEST_DATABASE_URL and drops
// it after the test.
func testDatabase(t *testing.T, prefix string) string {
	t.Helper()
	base := os.Getenv("MAGI_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("MAGI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	return u.String()
}

// graphSystem is a System with a knowledge service on a new test database
// (MAGI_TEST_DATABASE_URL). The magi service acts for the person.
func graphSystem(t *testing.T) *System {
	t.Helper()
	ctx := context.Background()
	st, err := knowledgestore.Open(ctx, testDatabase(t, "compose_graph"))
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, Log: slog.New(slog.DiscardHandler)}
	knowledgeserver.Register(srv, &knowledgeserver.Service{Store: st})
	ts := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(func() {
		ts.Close()
		st.Close()
	})
	testIssuer, testVerifier = iss, srv.Verifier
	tok, err := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService,
		Scopes: []string{knowledgeserver.ScopeActAs, "workflow:act-as", "workflow:worker", "workflow:decide"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &System{Log: slog.New(slog.DiscardHandler), Cfg: Config{ServiceToken: tok},
		Knowledge: knowledgeapi.NewKnowledgeClient(rpc.NewClient(ts.URL, rpc.StaticToken(tok)))}
}

// The issuer and verifier of the newest graphSystem, for more services.
var (
	testIssuer   *authn.Issuer
	testVerifier *authn.Verifier
)

const graphOwner = "user:kg-test@example.local"

func rememberAll(t *testing.T, s *System, facts ...fact) {
	t.Helper()
	for _, f := range facts {
		if _, note, err := s.remember(context.Background(), graphOwner, "Kim", f, true, nil); err != nil || strings.HasPrefix(note, "Not recorded") {
			t.Fatalf("remember %+v: %v %s", f, err, note)
		}
	}
}

// A question about the person reaches two steps: me -> vet -> clinic,
// also when other words of the question ("book") match other entities.
func TestContextFollowsTwoSteps(t *testing.T) {
	s := graphSystem(t)
	rememberAll(t, s,
		fact{subject: "me", relation: "dentist", object: "Dr. Rivera at Maple Street Dental", objectKind: "person"},
		fact{subject: "Maple Street Dental", relation: "books-through", object: "Zocdoc"},
		fact{subject: "me", relation: "vet", object: "Dr. Okafor at Birch Lane Animal Clinic", objectKind: "person"},
		fact{subject: "Birch Lane Animal Clinic", relation: "books-through", object: "PetDesk"},
		fact{subject: "me", relation: "sister", object: "Dana", objectKind: "person"},
	)
	got := s.knowledgeContext(context.Background(), graphOwner, "How do I book the vet?")
	if !strings.Contains(got, "Birch Lane Animal Clinic books-through PetDesk") {
		t.Fatalf("context has no clinic booking:\n%s", got)
	}
}

// A fix with "X at Y" keeps the shape of the graph.
func TestFixShapesObject(t *testing.T) {
	s := graphSystem(t)
	ctx := context.Background()
	rememberAll(t, s, fact{subject: "me", relation: "dentist", object: "Dr. Rivera", objectKind: "person"})
	items, _, err := s.knowledgeView(ctx, graphOwner, "dentist", "", 4, false)
	if err != nil || len(items) == 0 {
		t.Fatal(err)
	}
	id := ""
	for _, it := range items {
		for _, f := range it.Facts {
			if f.Relation == "dentist" {
				id = f.Id
			}
		}
	}
	if _, err := s.changeRecord(ctx, graphOwner, "Kim", id, "update", "", "Dr. Chen at Oak Hill Dental", "switched", false, nil); err != nil {
		t.Fatal(err)
	}
	got := s.knowledgeContext(ctx, graphOwner, "Who is my dentist?")
	// A fact can end with "(since <date>)"; the time of the run decides.
	if !regexp.MustCompile(`dentist Dr\. Chen( \(since [^)]*\))?(\n|$)`).MatchString(got) || strings.Contains(got, "Chen at Oak") {
		t.Fatalf("context:\n%s", got)
	}
	if got := s.knowledgeContext(ctx, graphOwner, "Where does Dr. Chen work?"); !strings.Contains(got, "Dr. Chen works-at Oak Hill Dental") {
		t.Fatalf("context:\n%s", got)
	}
}
