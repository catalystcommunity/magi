package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// fakeKnowledge keeps the lesson entities that the import makes. It answers
// like the real service: put-entity returns an existing entity as it is, so
// the import must fix the differences.
type fakeKnowledge struct {
	knowledgeapi.KnowledgeService
	mu       sync.Mutex
	ents     []knowledgeapi.Entity
	changes  []knowledgeapi.ChangeRequest
	searches int
}

func (f *fakeKnowledge) PutEntity(_ context.Context, r knowledgeapi.PutEntityRequest) (knowledgeapi.Entity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, e := range f.ents {
		if e.External != nil && e.External.System == r.External.System && e.External.Locator == r.External.Locator {
			// The real service adds only the absent fields; the rest needs a fix.
			return f.ents[i], nil
		}
	}
	e := knowledgeapi.Entity{Id: "ent_" + r.Label[:8], Kind: r.Kind, Label: r.Label, Summary: deref(r.Summary),
		Properties: map[string]string{}, External: r.External, Owners: r.Owners, Readers: []string{}, Confidence: 1,
		CreatedMs: 1, UpdatedMs: 1}
	for k, v := range r.Properties {
		e.Properties[k] = v
	}
	f.ents = append(f.ents, e)
	return e, nil
}

func (f *fakeKnowledge) Change(_ context.Context, r knowledgeapi.ChangeRequest) (knowledgeapi.ChangeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, r)
	for i := range f.ents {
		if f.ents[i].Id != r.Id {
			continue
		}
		if r.Operation == "retract" {
			f.ents = slices.Delete(f.ents, i, i+1)
			return knowledgeapi.ChangeResponse{Change: knowledgeapi.Change{Id: "chg_1", Record: r.Id, Operation: r.Operation, Actor: lessonOwner, AtMs: 1}}, nil
		}
		for k, v := range r.Set {
			switch k {
			case "label":
				f.ents[i].Label = v
			case "summary":
				f.ents[i].Summary = v
			default:
				f.ents[i].Properties[k[len("property."):]] = v
			}
		}
		return knowledgeapi.ChangeResponse{Change: knowledgeapi.Change{Id: "chg_2", Record: r.Id, Operation: r.Operation, Actor: lessonOwner, AtMs: 1}, Entity: &f.ents[i]}, nil
	}
	return knowledgeapi.ChangeResponse{}, rpc.NotFound("no %s", r.Id)
}

func (f *fakeKnowledge) Search(_ context.Context, r knowledgeapi.SearchRequest) (knowledgeapi.SearchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches++
	out := knowledgeapi.SearchResponse{Hits: []knowledgeapi.Hit{}, Entities: []knowledgeapi.Entity{}, Claims: []knowledgeapi.Claim{},
		Episodes: []knowledgeapi.Episode{}, Procedures: []knowledgeapi.Procedure{}}
	for _, e := range f.ents {
		if r.Kind == nil || *r.Kind == "" || e.Kind == *r.Kind {
			out.Hits = append(out.Hits, knowledgeapi.Hit{Type: "entity", Id: e.Id})
			out.Entities = append(out.Entities, e)
		}
	}
	return out, nil
}

// lessonRepoSystem starts a System with a fake KnowledgeService on the real HTTP
// transport, and a client that speaks for the magi service.
func lessonRepoSystem(t *testing.T, f *fakeKnowledge) *System {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	tok, err := iss.Issue(authn.Principal{Subject: lessonOwner, Kind: authn.KindService, Scopes: []string{"knowledge:act-as"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("KnowledgeService", knowledgeapi.KnowledgeServiceOps(f))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	return &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Cfg: Config{ServiceToken: tok},
		Knowledge: knowledgeapi.NewKnowledgeClient(rpc.NewClient(h.URL, rpc.StaticToken(tok)))}
}

func lessonOf(t *testing.T, f *fakeKnowledge, id string) knowledgeapi.Entity {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.ents {
		if e.Properties["lesson_id"] == id {
			return e
		}
	}
	t.Fatalf("no lesson %s", id)
	return knowledgeapi.Entity{}
}

// The first import makes one entity for each lesson of the file.
func TestImportBaseKnowledgeAdds(t *testing.T) {
	f := &fakeKnowledge{}
	s := lessonRepoSystem(t, f)
	if err := s.ImportBaseKnowledge(context.Background()); err != nil {
		t.Fatal(err)
	}
	lessons, _ := ReviewLessons()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ents) != len(lessons) || len(f.changes) != 0 {
		t.Fatalf("entities = %d, changes = %d", len(f.ents), len(f.changes))
	}
	for _, e := range f.ents {
		if e.Kind != "lesson" || e.External == nil || e.External.System != "magi-repo" ||
			e.External.Locator != "baseknowledge/review-lessons.md#"+e.Properties["lesson_id"] ||
			!slices.Contains(e.Owners, "service:magi") || e.Summary == "" {
			t.Fatalf("bad entity %+v", e)
		}
	}
}

// A second import changes nothing: put-entity returns the same entity.
func TestImportBaseKnowledgeTwice(t *testing.T) {
	f := &fakeKnowledge{}
	s := lessonRepoSystem(t, f)
	ctx := context.Background()
	for range 2 {
		if err := s.ImportBaseKnowledge(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.changes) != 0 {
		t.Fatalf("changes = %d", len(f.changes))
	}
}

// A lesson that changed in the file gets a fix; a lesson that the file no
// longer has gets a retraction.
func TestImportBaseKnowledgeFixAndRetract(t *testing.T) {
	f := &fakeKnowledge{}
	s := lessonRepoSystem(t, f)
	ctx := context.Background()
	if err := s.ImportBaseKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	// A wrong copy in the graph: the text and the area differ.
	f.mu.Lock()
	for i := range f.ents {
		if f.ents[i].Properties["lesson_id"] == "rl-0002" {
			f.ents[i].Summary, f.ents[i].Properties["area"] = "old text", "old area"
		}
	}
	// A lesson that the file does not have any more.
	f.ents = append(f.ents, knowledgeapi.Entity{Id: "ent_gone", Kind: "lesson", Label: "Gone", Summary: "gone",
		Properties: map[string]string{"lesson_id": "rl-9999"}, External: &knowledgeapi.ExternalRef{System: "magi-repo",
			Locator: "baseknowledge/review-lessons.md#rl-9999"}, Owners: []string{"service:magi"}, Readers: []string{}})
	f.mu.Unlock()

	if err := s.ImportBaseKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	e := lessonOf(t, f, "rl-0002")
	lessons, _ := ReviewLessons()
	want := ""
	for _, l := range lessons {
		if l.ID == "rl-0002" {
			want = l.Text
		}
	}
	if e.Summary != want || e.Properties["area"] != "all code" {
		t.Fatalf("fixed entity = %+v", e)
	}
	var fixed, retracted int
	for _, c := range f.changes {
		switch c.Operation {
		case "fix":
			fixed++
			if c.Set["summary"] != want || c.Set["property.area"] != "all code" {
				t.Fatalf("fix set = %v", c.Set)
			}
		case "retract":
			retracted++
			if deref(c.Reason) != "removed from review-lessons.md" {
				t.Fatalf("retract reason = %q", deref(c.Reason))
			}
		}
	}
	if fixed != 1 || retracted != 1 {
		t.Fatalf("fixes = %d, retractions = %d", fixed, retracted)
	}
	lessonOf(t, f, "rl-0001") // the other lessons stay
}
