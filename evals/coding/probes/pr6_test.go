// Probes of case pr6. Target: magi/compose
package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// The approved change gives the locator from the embed directory, so a
// probe takes the path of the task and each shorter form of it.
const probeFile = "magi/compose/baseknowledge/review-lessons.md"

// probeKnowledge answers like the real service: put-entity returns an entity
// that exists as it is, a retracted entity is not in a search, and a search
// does not filter by the external system.
type probeKnowledge struct {
	knowledgeapi.KnowledgeService
	mu        sync.Mutex
	ents      []knowledgeapi.Entity
	puts      []knowledgeapi.PutEntityRequest
	changes   []knowledgeapi.ChangeRequest
	retracted []string
	down      bool
}

func (f *probeKnowledge) PutEntity(_ context.Context, r knowledgeapi.PutEntityRequest) (knowledgeapi.Entity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return knowledgeapi.Entity{}, errors.New("the service is not ready")
	}
	f.puts = append(f.puts, r)
	for i, e := range f.ents {
		if e.External != nil && r.External != nil && *e.External == *r.External && !slices.Contains(f.retracted, e.Id) {
			return f.ents[i], nil
		}
	}
	e := knowledgeapi.Entity{Id: "ent_" + string(rune('a'+len(f.ents))), Kind: r.Kind, Label: r.Label,
		Properties: map[string]string{}, External: r.External, Owners: r.Owners, Readers: []string{}, Confidence: 1,
		CreatedMs: 1, UpdatedMs: 1}
	if r.Summary != nil {
		e.Summary = *r.Summary
	}
	for k, v := range r.Properties {
		e.Properties[k] = v
	}
	f.ents = append(f.ents, e)
	return e, nil
}

func (f *probeKnowledge) Change(_ context.Context, r knowledgeapi.ChangeRequest) (knowledgeapi.ChangeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return knowledgeapi.ChangeResponse{}, errors.New("the service is not ready")
	}
	f.changes = append(f.changes, r)
	for i := range f.ents {
		if f.ents[i].Id != r.Id {
			continue
		}
		ch := knowledgeapi.Change{Id: "chg_1", Record: r.Id, Operation: r.Operation, Actor: "service:magi", AtMs: 1}
		if r.Operation == "retract" {
			f.retracted = append(f.retracted, r.Id)
			return knowledgeapi.ChangeResponse{Change: ch}, nil
		}
		for k, v := range r.Set {
			switch k {
			case "label":
				f.ents[i].Label = v
			case "summary":
				f.ents[i].Summary = v
			default:
				f.ents[i].Properties[strings.TrimPrefix(k, "property.")] = v
			}
		}
		return knowledgeapi.ChangeResponse{Change: ch, Entity: &f.ents[i]}, nil
	}
	return knowledgeapi.ChangeResponse{}, rpc.NotFound("no %s", r.Id)
}

func (f *probeKnowledge) Search(_ context.Context, r knowledgeapi.SearchRequest) (knowledgeapi.SearchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := knowledgeapi.SearchResponse{Hits: []knowledgeapi.Hit{}, Entities: []knowledgeapi.Entity{}, Claims: []knowledgeapi.Claim{},
		Episodes: []knowledgeapi.Episode{}, Procedures: []knowledgeapi.Procedure{}}
	if f.down {
		return out, errors.New("the service is not ready")
	}
	for _, e := range f.ents {
		if slices.Contains(f.retracted, e.Id) || (r.Kind != nil && *r.Kind != "" && e.Kind != *r.Kind) {
			continue
		}
		out.Hits = append(out.Hits, knowledgeapi.Hit{Type: "entity", Id: e.Id})
		out.Entities = append(out.Entities, e)
	}
	return out, nil
}

// probeImport starts a System with the fake on the real HTTP transport and
// gives a function that runs ImportBaseKnowledge. The function returns the
// error of the import: the task does not give the return values, so the
// call uses reflection. noError tells that the method returns no error.
func probeImport(t *testing.T, f *probeKnowledge) (run func() (err error, noError bool), log *strings.Builder) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	tok, err := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"knowledge:act-as"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("KnowledgeService", knowledgeapi.KnowledgeServiceOps(f))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	log = &strings.Builder{}
	s := &System{Log: slog.New(slog.NewJSONHandler(log, nil)), Cfg: Config{ServiceToken: tok},
		Knowledge: knowledgeapi.NewKnowledgeClient(rpc.NewClient(h.URL, rpc.StaticToken(tok)))}
	m := reflect.ValueOf(s).MethodByName("ImportBaseKnowledge")
	if !m.IsValid() {
		t.Fatal("no method (*System).ImportBaseKnowledge")
	}
	return func() (error, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out := m.Call([]reflect.Value{reflect.ValueOf(ctx)})
		for _, v := range out {
			if v.Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
				if v.IsNil() {
					return nil, false
				}
				return v.Interface().(error), false
			}
		}
		return nil, true
	}, log
}

// probeLessons counts the lessons of the file of the change.
func probeLessons(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile("baseknowledge/review-lessons.md")
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count("\n"+string(b), "\n## rl-")
	if n < 3 {
		t.Fatalf("%d lessons in the file, want the 3 of the task", n)
	}
	return n
}

func probeMust(t *testing.T, run func() (error, bool)) {
	t.Helper()
	if err, _ := run(); err != nil {
		t.Fatalf("import: %v", err)
	}
}

func probeCounts(t *testing.T, log *strings.Builder) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, l := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(l), &line) != nil || line["msg"] != "base knowledge" {
			continue
		}
		out = map[string]float64{}
		for _, k := range []string{"added", "fixed", "retracted", "unchanged"} {
			v, ok := line[k].(float64)
			if !ok {
				t.Fatalf("no count %s in the line %v", k, line)
			}
			out[k] = v
		}
	}
	if len(out) == 0 {
		t.Fatalf("no line \"base knowledge\" in the log: %s", log.String())
	}
	return out
}

// For each lesson, call put-entity with kind "lesson", label = title, summary = text, the three properties, the external record, and owners ["service:magi"].
func TestProbeFirstImportAddsTheLessons(t *testing.T) {
	f := &probeKnowledge{}
	run, _ := probeImport(t, f)
	probeMust(t, run)
	if len(f.ents) != probeLessons(t) {
		t.Fatalf("%d entities, want the %d lessons of the file", len(f.ents), probeLessons(t))
	}
	ids := []string{}
	for _, e := range f.ents {
		id := e.Properties["lesson_id"]
		ids = append(ids, id)
		if e.Kind != "lesson" || e.Label == "" || e.Summary == "" || e.Properties["area"] == "" || e.Properties["source"] == "" {
			t.Errorf("entity %+v", e)
		}
		if e.External == nil || e.External.System != "magi-repo" || !strings.HasSuffix(probeFile+"#"+id, e.External.Locator) || !strings.HasSuffix(e.External.Locator, "baseknowledge/review-lessons.md#"+id) {
			t.Errorf("external of %s: %+v", id, e.External)
		}
		if !slices.Equal(e.Owners, []string{"service:magi"}) {
			t.Errorf("owners of %s: %v", id, e.Owners)
		}
	}
	for _, id := range []string{"rl-0001", "rl-0002", "rl-0003"} {
		if !slices.Contains(ids, id) {
			t.Errorf("no %s in the lesson ids %v", id, ids)
		}
	}
	for _, e := range f.ents {
		if e.Properties["lesson_id"] == "rl-0001" && (e.Properties["source"] != "run_DnUog7zOwB5NxHpymwkz (review 2026-11-14)" || e.Properties["area"] != "primitives/apprentice") {
			t.Errorf("rl-0001: %v", e.Properties)
		}
	}
}

// A second import changes nothing.
func TestProbeSecondImportChangesNothing(t *testing.T) {
	f := &probeKnowledge{}
	run, log := probeImport(t, f)
	probeMust(t, run)
	probeMust(t, run)
	n := float64(probeLessons(t))
	if len(f.ents) != int(n) || len(f.changes) != 0 {
		t.Errorf("%d entities, changes %+v", len(f.ents), f.changes)
	}
	if c := probeCounts(t, log); c["added"] != 0 || c["fixed"] != 0 || c["retracted"] != 0 || c["unchanged"] != n {
		t.Errorf("counts of the second import: %v", c)
	}
}

// Log one line: "base knowledge" with the counts (added, fixed, retracted, unchanged).
func TestProbeLogLineWithTheCounts(t *testing.T) {
	f := &probeKnowledge{}
	run, log := probeImport(t, f)
	probeMust(t, run)
	if c := probeCounts(t, log); c["added"] != float64(probeLessons(t)) || c["fixed"] != 0 || c["retracted"] != 0 || c["unchanged"] != 0 {
		t.Errorf("counts of the first import: %v", c)
	}
	if n := strings.Count(log.String(), `"msg":"base knowledge"`); n != 1 {
		t.Errorf("%d lines \"base knowledge\", want 1", n)
	}
}

// When the returned entity has a different label, summary, or properties than the file, call change with operation "fix" and set the new values.
func TestProbeADifferenceGivesAFix(t *testing.T) {
	for _, field := range []string{"summary", "label", "area"} {
		f := &probeKnowledge{}
		run, log := probeImport(t, f)
		probeMust(t, run)
		want := f.ents[1]
		want.Properties = map[string]string{"area": f.ents[1].Properties["area"]}
		switch field {
		case "summary":
			f.ents[1].Summary = "An older text."
		case "label":
			f.ents[1].Label = "An older title"
		default:
			f.ents[1].Properties["area"] = "an older area"
		}
		probeMust(t, run)
		if len(f.changes) != 1 || f.changes[0].Operation != "fix" || f.changes[0].Id != f.ents[1].Id {
			t.Errorf("%s: changes %+v", field, f.changes)
			continue
		}
		got := f.ents[1]
		if got.Summary != want.Summary || got.Label != want.Label || got.Properties["area"] != want.Properties["area"] {
			t.Errorf("%s: after the fix %+v", field, got)
		}
		if c := probeCounts(t, log); c["fixed"] != 1 || c["unchanged"] != float64(probeLessons(t)-1) {
			t.Errorf("%s: counts %v", field, c)
		}
	}
}

// Find the lessons in the knowledge graph that are not in the file any more, and retract them (change operation "retract", reason "removed from review-lessons.md").
func TestProbeARemovedLessonIsRetracted(t *testing.T) {
	f := &probeKnowledge{}
	run, log := probeImport(t, f)
	probeMust(t, run)
	f.ents = append(f.ents, knowledgeapi.Entity{Id: "ent_old", Kind: "lesson", Label: "An old lesson", Summary: "Old.",
		Properties: map[string]string{"lesson_id": "rl-0099", "area": "all code", "source": "run_x (review 2026-01-01)"},
		External:   &knowledgeapi.ExternalRef{System: "magi-repo", Locator: probeFile + "#rl-0099"},
		Owners:     []string{"service:magi"}, Readers: []string{}, Confidence: 1, CreatedMs: 1, UpdatedMs: 1})
	probeMust(t, run)
	if len(f.changes) != 1 || f.changes[0].Operation != "retract" || f.changes[0].Id != "ent_old" ||
		f.changes[0].Reason == nil || *f.changes[0].Reason != "removed from review-lessons.md" {
		t.Fatalf("changes %+v", f.changes)
	}
	if c := probeCounts(t, log); c["retracted"] != 1 || c["unchanged"] != float64(probeLessons(t)) {
		t.Errorf("counts %v", c)
	}
	probeMust(t, run)
	if len(f.changes) != 1 {
		t.Errorf("a retracted lesson is retracted again: %+v", f.changes[1:])
	}
}

// The retract is only for lessons of external system "magi-repo" with owner service:magi.
func TestProbeOtherLessonsStay(t *testing.T) {
	f := &probeKnowledge{}
	run, _ := probeImport(t, f)
	probeMust(t, run)
	f.ents = append(f.ents,
		knowledgeapi.Entity{Id: "ent_other", Kind: "lesson", Label: "A lesson of a person", Summary: "S.",
			Properties: map[string]string{}, External: &knowledgeapi.ExternalRef{System: "notes", Locator: "n1"},
			Owners: []string{"service:magi"}, Readers: []string{}, Confidence: 1, CreatedMs: 1, UpdatedMs: 1},
		knowledgeapi.Entity{Id: "ent_none", Kind: "lesson", Label: "A lesson with no record", Summary: "S.",
			Properties: map[string]string{}, Owners: []string{"user:someone"}, Readers: []string{}, Confidence: 1, CreatedMs: 1, UpdatedMs: 1})
	probeMust(t, run)
	if len(f.changes) != 0 {
		t.Errorf("changes to lessons that are not from the repository: %+v", f.changes)
	}
}

// With a retry (the knowledge service can start later than magi): an import that the service refuses must tell its caller, or no retry can run.
func TestProbeAFailedImportTellsTheCaller(t *testing.T) {
	f := &probeKnowledge{down: true}
	run, _ := probeImport(t, f)
	err, noError := run()
	if noError {
		t.Fatal("ImportBaseKnowledge returns no error, so the caller cannot retry")
	}
	if err == nil {
		t.Fatal("the service refused each call, and the import returned no error")
	}
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	probeMust(t, run)
	if len(f.ents) != probeLessons(t) {
		t.Errorf("%d entities after the service came up", len(f.ents))
	}
}
