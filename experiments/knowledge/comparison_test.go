package knowledge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"slices"
	"sort"
	"testing"
	"time"

	wire "opal.local/experiments/knowledge/generated/go"
)

type comparisonDoc struct {
	ID, Authority, Session, At, Scope, Text string
	Links, Requires                         []string
	Deleted                                 bool
}
type comparisonQuestion struct {
	ID, Split, Question, After, Before, Mode string
	Relevant, Required                       []string
}
type inferenceReply struct {
	Ready        bool
	Error        string
	Vectors      [][]float64
	Queries      []string
	Raw          string
	Milliseconds float64
	Tokens       int
	Timings      map[string]any
}
type inferenceWorker struct {
	input  io.WriteCloser
	output *bufio.Scanner
}

func startInference(t *testing.T) *inferenceWorker {
	t.Helper()
	python := os.Getenv("OPAL_RETRIEVAL_PYTHON")
	if python == "" {
		t.Fatal("OPAL_RETRIEVAL_PYTHON is required")
	}
	cmd := exec.Command(python, "retrieval/worker.py")
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	must(t, err)
	out, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() { input.Close(); must(t, cmd.Wait()) })
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	w := &inferenceWorker{input, scanner}
	if !w.receive(t).Ready {
		t.Fatal("inference worker did not initialize")
	}
	return w
}
func (w *inferenceWorker) receive(t *testing.T) inferenceReply {
	t.Helper()
	if !w.output.Scan() {
		t.Fatalf("inference worker ended: %v", w.output.Err())
	}
	var reply inferenceReply
	must(t, json.Unmarshal(w.output.Bytes(), &reply))
	return reply
}
func (w *inferenceWorker) call(t *testing.T, request any) inferenceReply {
	t.Helper()
	data, err := json.Marshal(request)
	must(t, err)
	_, err = w.input.Write(append(data, '\n'))
	must(t, err)
	return w.receive(t)
}
func loadComparison(t *testing.T) ([]comparisonDoc, []comparisonQuestion) {
	t.Helper()
	var docs []comparisonDoc
	var questions []comparisonQuestion
	b, err := os.ReadFile("retrieval/corpus.json")
	must(t, err)
	must(t, json.Unmarshal(b, &docs))
	b, err = os.ReadFile("retrieval/questions.json")
	must(t, err)
	must(t, json.Unmarshal(b, &questions))
	return docs, questions
}
func comparisonRef(id string) wire.RecordRef {
	return wire.RecordRef{Authority: primary, Kind: "source", RecordId: id}
}

func seedComparison(t *testing.T, f *fixture, docs []comparisonDoc) {
	t.Helper()
	ctx := context.Background()
	for _, d := range docs {
		authority := primary
		if d.Authority == "secondary" {
			authority = secondary
		}
		scope := privateScope
		if d.Scope == "work" {
			scope = workScope
		}
		if d.Scope == "bob-private" {
			scope = wire.ScopeRef{Authority: primary, ScopeId: "bob-private"}
		}
		if authority == secondary {
			scope.Authority = secondary
		}
		_, err := f.admin.Exec(ctx, `INSERT INTO sessions(authority,id,scopes) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, identity(authority), d.Session, []string{scopeKey(scope)})
		must(t, err)
		at, err := time.Parse(time.RFC3339, d.At)
		must(t, err)
		h := header("source", d.ID, scope)
		h.Ref.Authority = authority
		h.ObservedAt = &at
		for _, id := range d.Requires {
			h.Causes = append(h.Causes, comparisonRef(id))
		}
		r := wire.SourceContext{Header: h, ResourceId: d.ID, Adapter: "synthetic-comparison", Content: d.Text, EnrichmentAllowed: false}
		payload := wire.EncodeHistoryPage(wire.HistoryPage{Records: []wire.EvidenceRecord{r}})
		_, err = f.admin.Exec(ctx, `INSERT INTO records(authority,id,kind,producer,stream,source_sequence,payload,scopes,purposes,session_id,observed_seconds,observed_nanos,deleted,enrichment_allowed)
            VALUES($1,$2,'source',$3,$2,1,$4,$5,$6,$7,$8,0,$9,false)`, identity(authority), d.ID, identity(alice), payload, []string{scopeKey(scope)}, purposes, d.Session, at.Unix(), d.Deleted)
		must(t, err)
		_, err = f.admin.Exec(ctx, `INSERT INTO segments(authority,id,body) VALUES($1,$2,$3)`, identity(authority), d.ID, d.Text)
		must(t, err)
	}
	for _, d := range docs {
		for _, id := range d.Requires {
			_, err := f.admin.Exec(ctx, `INSERT INTO dependencies VALUES($1,$2,$1,$3)`, identity(primary), d.ID, id)
			must(t, err)
		}
	}
}

func fuse(rankings ...[]string) []string {
	scores := map[string]float64{}
	for _, ranking := range rankings {
		seen := map[string]bool{}
		for n, id := range ranking {
			if !seen[id] {
				scores[id] += 1 / float64(60+n+1)
				seen[id] = true
			}
		}
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] == scores[ids[j]] {
			return ids[i] < ids[j]
		}
		return scores[ids[i]] > scores[ids[j]]
	})
	if len(ids) > 10 {
		ids = ids[:10]
	}
	return ids
}
func vectorRanking(vectors map[string][]float64, query []float64, permitted map[string]bool) ([]string, error) {
	if len(query) != 384 {
		return nil, ErrInvalid
	}
	scores := map[string]float64{}
	for id, v := range vectors {
		if !permitted[id] {
			continue
		}
		if len(v) != len(query) {
			return nil, ErrInvalid
		}
		score := 0.0
		for n, x := range v {
			score += x * query[n]
		}
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, ErrInvalid
		}
		scores[id] = score
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] == scores[ids[j]] {
			return ids[i] < ids[j]
		}
		return scores[ids[i]] > scores[ids[j]]
	})
	if len(ids) > 10 {
		ids = ids[:10]
	}
	return ids, nil
}
func permittedComparison(t *testing.T, f *fixture, docs []comparisonDoc, q wire.KnowledgeQuery) map[string]bool {
	t.Helper()
	permitted := map[string]bool{}
	for _, d := range docs {
		if d.Authority == "secondary" {
			continue
		}
		at, err := time.Parse(time.RFC3339, d.At)
		must(t, err)
		if q.After != nil && at.Before(*q.After) || q.Before != nil && at.After(*q.Before) {
			continue
		}
		_, err = f.s.Read(context.Background(), f.access, comparisonRef(d.ID))
		if errors.Is(err, ErrDenied) {
			continue
		}
		must(t, err)
		permitted[d.ID] = true
	}
	return permitted
}
func linkedRanking(ids []string, docs map[string]comparisonDoc, permitted map[string]bool) []string {
	result := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if permitted[id] && !seen[id] && len(result) < 10 {
			result = append(result, id)
			seen[id] = true
		}
	}
	anchors := ids
	if len(anchors) > 3 {
		anchors = anchors[:3]
	}
	for _, id := range anchors {
		add(id)
	}
	for _, id := range anchors {
		for _, link := range docs[id].Links {
			add(link)
		}
	}
	for _, id := range ids {
		add(id)
	}
	return result
}

func TestComparisonFixtures(t *testing.T) {
	docs, questions := loadComparison(t)
	byID := map[string]comparisonDoc{}
	for _, d := range docs {
		if _, exists := byID[d.ID]; exists {
			t.Fatal("duplicate document")
		}
		byID[d.ID] = d
	}
	for _, d := range docs {
		for _, id := range append(slices.Clone(d.Links), d.Requires...) {
			if _, ok := byID[id]; !ok {
				t.Fatal("missing link", id)
			}
		}
	}
	seen := map[string]bool{}
	for _, q := range questions {
		if seen[q.ID] || q.Question == "" || !slices.Contains([]string{"dev", "test"}, q.Split) {
			t.Fatal("invalid question", q.ID)
		}
		seen[q.ID] = true
		for _, id := range q.Relevant {
			d, ok := byID[id]
			if !ok || d.Deleted || d.Authority == "secondary" || d.Scope == "bob-private" || len(d.Requires) > 0 {
				t.Fatal("invalid relevance label", q.ID, id)
			}
		}
		for _, id := range q.Required {
			if !slices.Contains(q.Relevant, id) {
				t.Fatal("required record must be relevant")
			}
		}
		if (q.Mode == "no_evidence") != (len(q.Relevant) == 0) {
			t.Fatal("invalid missing-evidence label")
		}
	}
	if len(questions) != 30 {
		t.Fatal("question set changed; review the evaluation version")
	}
	if got := fuse([]string{"b", "a"}, []string{"a", "b"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatal("unstable fusion tie")
	}
	if got := linkedRanking([]string{"one"}, map[string]comparisonDoc{"one": {Links: []string{"private", "two"}}}, map[string]bool{"one": true, "two": true}); !slices.Equal(got, []string{"one", "two"}) {
		t.Fatal("link expansion escaped permissions")
	}
	vector := make([]float64, 384)
	vector[0] = 1
	vectors := map[string][]float64{"permitted": vector}
	for n := 0; n < 100; n++ {
		vectors[fmt.Sprintf("denied-%d", n)] = vector
	}
	ranked, err := vectorRanking(vectors, vector, map[string]bool{"permitted": true})
	must(t, err)
	if !slices.Equal(ranked, []string{"permitted"}) {
		t.Fatal("filtering occurred after candidate limit")
	}
	vector[0] = math.NaN()
	_, err = vectorRanking(vectors, vector, map[string]bool{"permitted": true})
	wantError(t, err, ErrInvalid)
	_, err = vectorRanking(vectors, []float64{1}, map[string]bool{"permitted": true})
	wantError(t, err, ErrInvalid)
}

func TestRetrievalComparison(t *testing.T) {
	if os.Getenv("OPAL_RETRIEVAL_COMPARISON") != "1" {
		t.Skip("use node retrieval/compare.mjs")
	}
	docs, questions := loadComparison(t)
	// Add fixed distractors without using query terms or relevance labels.
	for n := 0; n < 300; n++ {
		topics := []string{"weather forecast", "office inventory", "vehicle maintenance", "weekly meal planning", "software deployment", "garden tool storage", "library opening hours", "household travel", "camera product review", "calendar synchronization"}
		docs = append(docs, comparisonDoc{ID: fmt.Sprintf("distractor-%03d", n), Session: fmt.Sprintf("noise-%03d", n), Scope: "work", At: "2026-09-10T09:00:00Z", Text: fmt.Sprintf("Routine %s note number %d. This entry records a scheduled check and no incident or personal decision.", topics[n%len(topics)], n)})
	}
	h := startHarness(t)
	f := h.fixture(t)
	f.access.Purpose = "recall"
	seedComparison(t, f, docs)
	w := startInference(t)
	all := permittedComparison(t, f, docs, wire.KnowledgeQuery{})
	byID := map[string]comparisonDoc{}
	vectors := map[string][]float64{}
	indexed := []string{}
	texts := []string{}
	for _, d := range docs {
		byID[d.ID] = d
		if all[d.ID] {
			indexed = append(indexed, d.ID)
			texts = append(texts, d.Text)
		}
	}
	indexStart := time.Now()
	indexTokens := 0
	for start := 0; start < len(texts); start += 32 {
		end := min(start+32, len(texts))
		r := w.call(t, map[string]any{"op": "embed", "texts": texts[start:end]})
		if r.Error != "" {
			t.Fatal(r.Error)
		}
		if len(r.Vectors) != end-start {
			t.Fatal("incomplete embeddings")
		}
		indexTokens += r.Tokens
		for n, v := range r.Vectors {
			vectors[indexed[start+n]] = v
		}
	}
	t.Logf("COMPARISON_INDEX %s", jsonBytes(map[string]any{"records": len(docs), "indexed": len(indexed), "embedding_tokens": indexTokens, "index_ms": float64(time.Since(indexStart).Microseconds()) / 1000, "exact_scan": true, "concurrency": 1}))
	methods := []string{"text_all", "text_any", "model_text", "embedding", "hybrid", "hybrid_links"}
	for _, question := range questions {
		if split := os.Getenv("OPAL_RETRIEVAL_SPLIT"); split != "all" && split != question.Split {
			continue
		}
		q := wire.KnowledgeQuery{Question: question.Question, Purpose: "recall", Limit: 10}
		if question.After != "" {
			at, err := time.Parse(time.RFC3339, question.After)
			must(t, err)
			q.After = &at
		}
		if question.Before != "" {
			at, err := time.Parse(time.RFC3339, question.Before)
			must(t, err)
			q.Before = &at
		}
		for _, method := range methods {
			started := time.Now()
			permitted := permittedComparison(t, f, docs, q)
			ids := []string{}
			plans := []string{}
			tokens := 0
			modelMS := 0.0
			errorText := ""
			var timings map[string]any
			textSearch := func(query string, anyTerm bool) []string {
				copy := q
				copy.Question = query
				r, err := f.s.search(context.Background(), f.access, copy, anyTerm)
				if err != nil {
					errorText = err.Error()
					return nil
				}
				out := []string{}
				for _, m := range r.Matches {
					if m.Source.Authority != primary {
						t.Fatal("foreign candidate")
					}
					out = append(out, m.Source.RecordId)
				}
				return out
			}
			switch method {
			case "text_all":
				ids = textSearch(q.Question, false)
			case "text_any":
				ids = textSearch(q.Question, true)
			case "model_text":
				r := w.call(t, map[string]any{"op": "plan", "question": q.Question})
				plans = r.Queries
				modelMS = r.Milliseconds
				timings = r.Timings
				if r.Error != "" {
					errorText = r.Error
				} else {
					rankings := [][]string{}
					for _, plan := range plans {
						rankings = append(rankings, textSearch(plan, true))
					}
					ids = fuse(rankings...)
				}
			default:
				r := w.call(t, map[string]any{"op": "embed", "texts": []string{q.Question}})
				modelMS = r.Milliseconds
				tokens = r.Tokens
				if r.Error != "" {
					errorText = r.Error
				} else if len(r.Vectors) != 1 {
					errorText = "incomplete query embedding"
				} else {
					var err error
					ids, err = vectorRanking(vectors, r.Vectors[0], permitted)
					if err != nil {
						errorText = err.Error()
					}
					if method != "embedding" {
						ids = fuse(textSearch(q.Question, true), ids)
					}
					if method == "hybrid_links" {
						ids = linkedRanking(ids, byID, permitted)
					}
				}
			}
			hits := 0
			mrr := 0.0
			for n, id := range ids {
				if !permitted[id] {
					t.Fatal("unauthorized result", method, question.ID, id)
				}
				if slices.Contains(question.Relevant, id) {
					hits++
					if mrr == 0 {
						mrr = 1 / float64(n+1)
					}
				}
			}
			complete := len(question.Required) > 0
			for _, id := range question.Required {
				if !slices.Contains(ids, id) {
					complete = false
				}
			}
			row := map[string]any{"question": question.ID, "split": question.Split, "method": method, "expected_mode": question.Mode, "retrieved": ids, "hits": hits, "relevant_count": len(question.Relevant), "complete_evidence": complete, "mrr": mrr, "precision_at_10": float64(hits) / 10, "elapsed_ms": float64(time.Since(started).Microseconds()) / 1000, "model_ms": modelMS, "embedding_tokens": tokens, "plans": plans, "planner_timings": timings, "error": errorText}
			t.Log("COMPARISON_CASE", jsonBytes(row))
			if errorText != "" {
				t.Errorf("%s %s retrieval failed: %s", question.ID, method, errorText)
			}
		}
	}
	// Prove current restrictions apply to an already-built embedding index.
	_, err := f.admin.Exec(context.Background(), `UPDATE records SET deleted=true WHERE id='star-discussion'`)
	must(t, err)
	current := permittedComparison(t, f, docs, wire.KnowledgeQuery{})
	ids, err := vectorRanking(vectors, vectors["star-discussion"], current)
	must(t, err)
	if slices.Contains(ids, "star-discussion") {
		t.Fatal("stale vector escaped deletion barrier")
	}
	for _, id := range []string{"bob-journal", "foreign-star", "restricted-star", "restricted-derived"} {
		if _, ok := vectors[id]; ok {
			t.Fatal("denied source sent to embedding processor")
		}
	}
	t.Log("COMPARISON_SAFETY Current access rechecked; denied sources not embedded; stale vector not returned")
}
func jsonBytes(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
