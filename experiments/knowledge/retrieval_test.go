package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	wire "opal.local/experiments/knowledge/generated/go"
)

type recallQuestion struct{ Source, Exact, Paraphrase string }
type recallCase struct {
	question, expected, kind string
	at                       time.Time
}
type recallMeasurement struct {
	duration time.Duration
	found    bool
	kind     string
	err      error
}

func testRetrievalCorpus(t *testing.T, h *harness) {
	if os.Getenv("OPAL_KNOWLEDGE_CORPUS") != "1" {
		t.Skip("set OPAL_KNOWLEDGE_CORPUS=1 for the 200,000 segment experiment")
	}
	f := h.fixture(t)
	ctx := context.Background()
	data, err := os.ReadFile("fixtures/recall-questions.json")
	must(t, err)
	var questions []recallQuestion
	must(t, json.Unmarshal(data, &questions))
	if len(questions) != 10 {
		t.Fatal("expected ten labelled subjects")
	}
	household := wire.ScopeRef{Authority: primary, ScopeId: "household"}
	bobPrivate := wire.ScopeRef{Authority: primary, ScopeId: "bob-private"}
	f.grant(t, alice, household, true)
	_, err = f.admin.Exec(ctx, `INSERT INTO processor_grants VALUES($1,$2,'recall')`, identity(localProcessor), scopeKey(household))
	must(t, err)
	scopeFor := func(authority wire.IdentityRef, n int) []string {
		slot := n % 10000
		names := []string{"alice-private", "household", "work"}
		scopes := []string{scopeKey(wire.ScopeRef{Authority: authority, ScopeId: names[slot%3]})}
		if slot%5 == 0 {
			scopes = append(scopes, scopeKey(wire.ScopeRef{Authority: authority, ScopeId: "work"}))
		}
		if slot >= 150 && slot < 200 {
			scopes = []string{scopeKey(wire.ScopeRef{Authority: authority, ScopeId: bobPrivate.ScopeId})}
		}
		return scopes
	}
	seedStart := time.Now()
	for _, authority := range []wire.IdentityRef{primary, secondary} {
		// Bulk seeding bypasses admission intentionally. All records are synthetic.
		n := 0
		_, err = f.admin.CopyFrom(ctx, pgx.Identifier{"sessions"}, []string{"authority", "id", "scopes"}, pgx.CopyFromFunc(func() ([]any, error) {
			if n == 10000 {
				return nil, nil
			}
			slot := n
			n++
			return []any{identity(authority), fmt.Sprintf("corpus-session-%05d", slot), scopeFor(authority, slot)}, nil
		}))
		must(t, err)
		n = 0
		_, err = f.admin.CopyFrom(ctx, pgx.Identifier{"records"}, []string{"authority", "id", "kind", "producer", "stream", "source_sequence", "payload", "scopes", "purposes", "session_id", "observed_seconds", "observed_nanos"}, pgx.CopyFromFunc(func() ([]any, error) {
			if n == 100000 {
				return nil, nil
			}
			index := n
			n++
			id := fmt.Sprintf("segment-%06d", index)
			content := fmt.Sprintf("Routine inventory weather transport notes item %d.", index)
			observed := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC).Add(time.Duration(index%180) * 24 * time.Hour)
			if index >= 100 && index < 200 {
				topic := (index - 100) % 50
				content = questions[topic%10].Source
				if index >= 150 {
					content += " Hidden private distractor."
				}
				observed = fixedTime.Add(-time.Duration(21+topic) * 24 * time.Hour)
			}
			i := interaction(id, content)
			i.Header.Ref.Authority = authority
			i.Header.ObservedAt = &observed
			i.ConversationIds = []string{fmt.Sprintf("corpus-conversation-%05d", index%10000)}
			i.Header.Restrictions.RequiredScopes = nil
			// Decode scope keys by choosing the same declared control areas.
			slot := index % 10000
			names := []string{"alice-private", "household", "work"}
			i.Header.Restrictions.RequiredScopes = []wire.ScopeRef{{Authority: authority, ScopeId: names[slot%3]}}
			if slot%5 == 0 {
				i.Header.Restrictions.RequiredScopes = append(i.Header.Restrictions.RequiredScopes, wire.ScopeRef{Authority: authority, ScopeId: "work"})
			}
			if slot >= 150 && slot < 200 {
				i.Header.Restrictions.RequiredScopes = []wire.ScopeRef{{Authority: authority, ScopeId: bobPrivate.ScopeId}}
			}
			payload := wire.EncodeHistoryPage(wire.HistoryPage{Records: []wire.EvidenceRecord{i}})
			return []any{identity(authority), id, "interaction", identity(alice), id, int64(1), payload, scopeFor(authority, index), purposes, fmt.Sprintf("corpus-session-%05d", index%10000), observed.Unix(), observed.Nanosecond()}, nil
		}))
		must(t, err)
		// Decode canonical payloads in Go rather than convert CBOR through JSON.
		n = 0
		_, err = f.admin.CopyFrom(ctx, pgx.Identifier{"segments"}, []string{"authority", "id", "body"}, pgx.CopyFromFunc(func() ([]any, error) {
			if n == 100000 {
				return nil, nil
			}
			index := n
			n++
			body := fmt.Sprintf("Routine inventory weather transport notes item %d.", index)
			if index >= 100 && index < 200 {
				body = questions[((index-100)%50)%10].Source
				if index >= 150 {
					body += " Hidden private distractor."
				}
			}
			return []any{identity(authority), fmt.Sprintf("segment-%06d", index), body}, nil
		}))
		must(t, err)
	}
	_, err = f.admin.Exec(ctx, `ANALYZE records; ANALYZE segments; ANALYZE sessions; CHECKPOINT`)
	must(t, err)
	t.Logf("Seeded 200000 segments and 20000 corpus sessions in %.3fs; formula seed=1; no private input", time.Since(seedStart).Seconds())
	cases := []recallCase{}
	for n := range 50 {
		at := fixedTime.Add(-time.Duration(21+n) * 24 * time.Hour)
		expected := fmt.Sprintf("segment-%06d", 100+n)
		cases = append(cases, recallCase{questions[n%10].Exact, expected, "lexical", at}, recallCase{questions[n%10].Paraphrase, expected, "paraphrase", at})
	}
	a := f.access
	a.Purpose = "recall"
	run := func(name string) {
		t.Run(name, func(t *testing.T) {
			jobs := make(chan recallCase)
			measurements := make(chan recallMeasurement, len(cases))
			var workers sync.WaitGroup
			for range 10 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					for c := range jobs {
						start := time.Now()
						deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
						before := c.at.Add(time.Second)
						after := c.at.Add(-time.Second)
						result, err := f.s.Search(deadline, a, wire.KnowledgeQuery{Question: c.question, Purpose: "recall", Limit: 10, After: &after, Before: &before})
						cancel()
						found := false
						for _, m := range result.Matches {
							if m.Source.Authority != primary || m.Source.RecordId != c.expected {
								err = fmt.Errorf("unexpected or unauthorized result: %v", m.Source)
							}
							if m.Source.RecordId == c.expected {
								found = true
							}
						}
						measurements <- recallMeasurement{time.Since(start), found, c.kind, err}
					}
				}()
			}
			for _, c := range cases {
				jobs <- c
			}
			close(jobs)
			workers.Wait()
			close(measurements)
			times := []float64{}
			hits, errorsCount := 0, 0
			lexical, paraphrase := 0, 0
			for m := range measurements {
				times = append(times, float64(m.duration.Microseconds())/1000)
				if m.err != nil {
					errorsCount++
					t.Error(m.err)
				}
				if m.found {
					hits++
					if m.kind == "lexical" {
						lexical++
					} else {
						paraphrase++
					}
				}
			}
			sort.Float64s(times)
			report := map[string]any{"pass": name, "segments": 200000, "queries": len(cases), "concurrency": 10,
				"p50_ms": times[49], "p95_ms": times[94], "max_ms": times[len(times)-1], "errors": errorsCount,
				"hits_at_10": hits, "lexical_hits": lexical, "lexical_total": 50, "paraphrase_hits": paraphrase, "paraphrase_total": 50,
				"recall_at_10": float64(hits) / 100, "quality_target": 0.95, "quality_target_met": hits >= 95,
				"timing_scope": "Go store call including SQL; no model, public RPC, or speech",
				"cache_limit":  "OS cache not cleared"}
			encoded, err := json.Marshal(report)
			must(t, err)
			t.Log("RETRIEVAL_RESULT", string(encoded))
			if lexical != 50 {
				t.Fatalf("lexical regression: %d/50", lexical)
			}
			// Quality is a measured result, not a requirement that can be faked
			// by changing the expected source or deleting hard queries.
		})
	}
	docker(t, "restart", h.container)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := f.s.db.Ping(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("database restart timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
	run("database_restart")
	run("warm")
}
