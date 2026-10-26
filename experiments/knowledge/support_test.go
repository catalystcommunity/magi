package knowledge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	wire "opal.local/experiments/knowledge/generated/go"
)

func TestSupportValidation(t *testing.T) {
	request := supportRequest{Sources: []supportSource{{ID: "e01", Text: "The calendar service is unavailable today."}}}
	valid := `{"mode":"unavailable","evidence":[{"source":"e01","quote":"The calendar service is unavailable today."}],"gap":"Current availability is unknown."}`
	_, err := validateSupport([]byte(valid), request)
	must(t, err)
	for name, raw := range map[string]string{
		"foreign_citation":   strings.Replace(valid, "e01", "e99", 1),
		"invented_quote":     strings.Replace(valid, "The calendar service is unavailable today.", "The calendar service is available today.", 1),
		"unknown_field":      strings.Replace(valid, `"gap":`, `"action":"delete","gap":`, 1),
		"duplicate_mode":     strings.Replace(valid, `"mode":`, `"mode":"answer","mode":`, 1),
		"trailing_object":    valid + `{}`,
		"missing_evidence":   `{"mode":"answer","evidence":[],"gap":""}`,
		"null_evidence":      `{"mode":"no_evidence","evidence":null,"gap":"Nothing found."}`,
		"no_gap":             `{"mode":"no_evidence","evidence":[],"gap":""}`,
		"unsupported_mode":   strings.Replace(valid, "unavailable", "certain", 1),
		"answer_with_gap":    strings.Replace(valid, `"unavailable"`, `"answer"`, 1),
		"duplicate_citation": `{"mode":"answer","evidence":[{"source":"e01","quote":"calendar service"},{"source":"e01","quote":"calendar service"}],"gap":""}`,
		"short_quote":        `{"mode":"answer","evidence":[{"source":"e01","quote":"The"}],"gap":""}`,
		"missing_gap_field":  `{"mode":"answer","evidence":[{"source":"e01","quote":"calendar service"}]}`,
		"null_gap":           `{"mode":"answer","evidence":[{"source":"e01","quote":"calendar service"}],"gap":null}`,
	} {
		t.Run(name, func(t *testing.T) { _, err := validateSupport([]byte(raw), request); wantError(t, err, ErrInvalid) })
	}
}

func TestSupportAccess(t *testing.T) {
	h := startHarness(t)
	ctx := context.Background()
	docs, _ := loadComparison(t)
	for _, name := range []string{"empty_query_actor", "empty_query_processor", "empty_query_recheck"} {
		t.Run(name, func(t *testing.T) {
			f := h.fixture(t)
			f.access.Purpose = "recall"
			p, err := f.s.prepareSupport(ctx, f.access, "private question", nil)
			must(t, err)
			if name == "empty_query_actor" {
				f.access.Actor = wire.IdentityRef{}
			} else {
				_, err = f.admin.Exec(ctx, `DELETE FROM processor_grants WHERE purpose='recall'`)
				must(t, err)
			}
			_, err = f.s.prepareSupport(ctx, f.access, "private question", nil)
			wantError(t, err, ErrDenied)
			wantError(t, f.s.recheckSupport(ctx, f.access, p), ErrDenied)
		})
	}
	for _, name := range []string{"denied_inputs", "source_deleted", "uncited_source_deleted", "processor_revoked", "source_changed", "different_actor", "purpose_changed", "duplicates", "limit"} {
		t.Run(name, func(t *testing.T) {
			f := h.fixture(t)
			f.access.Purpose = "recall"
			seedComparison(t, f, docs)
			refs := []wire.RecordRef{comparisonRef("camera-decision"), comparisonRef("garden-plan")}
			if name == "denied_inputs" {
				refs = append(refs, comparisonRef("bob-journal"), comparisonRef("restricted-derived"), comparisonRef("absent"))
			}
			p, err := f.s.prepareSupport(ctx, f.access, "Why was the lawn alert left for morning?", refs)
			must(t, err)
			if len(p.request.Sources) != 2 || len(p.bindings) != 2 {
				t.Fatal("denied input entered packet")
			}
			payload, err := json.Marshal(p.request)
			must(t, err)
			if strings.Contains(string(payload), "7391") || strings.Contains(string(payload), "bob-journal") {
				t.Fatal("denied metadata escaped")
			}
			switch name {
			case "denied_inputs":
				must(t, f.s.recheckSupport(ctx, f.access, p))
				return
			case "source_deleted", "uncited_source_deleted":
				id := "camera-decision"
				if name == "uncited_source_deleted" {
					id = "garden-plan"
				}
				_, err = f.admin.Exec(ctx, `UPDATE records SET deleted=true WHERE id=$1`, id)
				must(t, err)
			case "processor_revoked":
				_, err = f.admin.Exec(ctx, `DELETE FROM processor_grants WHERE purpose='recall'`)
				must(t, err)
			case "source_changed":
				r, err := f.s.Read(ctx, f.access, refs[0])
				must(t, err)
				source := r.(wire.SourceContext)
				source.Content += " Changed."
				_, err = f.admin.Exec(ctx, `UPDATE records SET payload=$1 WHERE id=$2`, wire.EncodeHistoryPage(wire.HistoryPage{Records: []wire.EvidenceRecord{source}}), "camera-decision")
				must(t, err)
				wantError(t, f.s.recheckSupport(ctx, f.access, p), ErrConflict)
				return
			case "different_actor":
				f.access.Actor = bob
			case "purpose_changed":
				f.access.Purpose = "enrich"
			case "duplicates":
				_, err := f.s.prepareSupport(ctx, f.access, "question", []wire.RecordRef{refs[0], refs[0]})
				wantError(t, err, ErrInvalid)
				return
			case "limit":
				_, err := f.s.prepareSupport(ctx, f.access, "question", make([]wire.RecordRef, 11))
				wantError(t, err, ErrInvalid)
				return
			}
			wantError(t, f.s.recheckSupport(ctx, f.access, p), ErrDenied)
		})
	}
}

func TestSupportInference(t *testing.T) {
	if os.Getenv("OPAL_SUPPORT_INFERENCE") != "1" {
		t.Skip("use node sufficiency/run.mjs")
	}
	split := os.Getenv("OPAL_SUPPORT_SPLIT")
	if split != "dev" && split != "test" {
		t.Fatal("invalid split")
	}
	docs, questions := loadComparison(t)
	for n := 0; n < 300; n++ {
		topics := []string{"weather forecast", "office inventory", "vehicle maintenance", "weekly meal planning", "software deployment", "garden tool storage", "library opening hours", "household travel", "camera product review", "calendar synchronization"}
		docs = append(docs, comparisonDoc{ID: fmt.Sprintf("distractor-%03d", n), Session: fmt.Sprintf("noise-%03d", n), Scope: "work", At: "2026-09-10T09:00:00Z", Text: fmt.Sprintf("Routine %s note number %d. This entry records a scheduled check and no incident or personal decision.", topics[n%len(topics)], n)})
	}
	data, err := os.ReadFile(os.Getenv("OPAL_SUPPORT_CANDIDATES"))
	must(t, err)
	var candidates []struct {
		Question, Method string
		Retrieved        []string
	}
	must(t, json.Unmarshal(data, &candidates))
	rankings := map[string][]string{}
	for _, c := range candidates {
		if c.Method == "hybrid_links" {
			rankings[c.Question] = c.Retrieved
		}
	}
	h := startHarness(t)
	f := h.fixture(t)
	f.access.Purpose = "recall"
	f.s.now = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }
	seedComparison(t, f, docs)
	bounded, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(bounded, "/usr/bin/python3.14", "sufficiency/worker.py")
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	must(t, err)
	output, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() { input.Close(); must(t, cmd.Wait()) })
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), `"ready": true`) {
		t.Fatal("support worker did not start")
	}
	for _, q := range questions {
		if q.Split != split {
			continue
		}
		ids, ok := rankings[q.ID]
		if !ok {
			t.Fatal("missing frozen candidates", q.ID)
		}
		refs := []wire.RecordRef{}
		for _, id := range ids {
			refs = append(refs, comparisonRef(id))
		}
		p, err := f.s.prepareSupport(bounded, f.access, q.Question, refs)
		must(t, err)
		request, err := json.Marshal(p.request)
		must(t, err)
		_, err = input.Write(append(request, '\n'))
		must(t, err)
		if !scanner.Scan() {
			t.Fatalf("support worker stopped: %v", scanner.Err())
		}
		var reply struct {
			Raw, Error   string
			Milliseconds float64
			Timings      map[string]any
			Truncated    bool
			LimitReached bool `json:"limit_reached"`
		}
		must(t, json.Unmarshal(scanner.Bytes(), &reply))
		assessment, validation := validateSupport([]byte(reply.Raw), p.request)
		if reply.Truncated || reply.LimitReached {
			validation = ErrLimit
		}
		if reply.Error != "" {
			validation = ErrUnavailable
		}
		must(t, f.s.recheckSupport(bounded, f.access, p))
		selected := []string{}
		irrelevant := []string{}
		if validation == nil {
			for _, quote := range assessment.Evidence {
				for n, source := range p.request.Sources {
					if source.ID == quote.Source {
						id := p.bindings[n].ref.RecordId
						selected = append(selected, id)
						if !slices.Contains(q.Relevant, id) {
							irrelevant = append(irrelevant, id)
						}
					}
				}
			}
		}
		complete := len(q.Required) > 0 && validation == nil
		for _, id := range q.Required {
			if !slices.Contains(selected, id) {
				complete = false
			}
		}
		status := "valid"
		if validation != nil {
			status = validation.Error()
		}
		row := map[string]any{"question": q.ID, "split": split, "request": p.request, "raw": reply.Raw, "assessment": assessment,
			"validation": status, "error": reply.Error, "expected_mode": q.Mode, "mode_match": validation == nil && q.Mode == assessment.Mode,
			"selected": selected, "irrelevant_citations": irrelevant, "complete_evidence": complete, "required_count": len(q.Required),
			"elapsed_ms": reply.Milliseconds, "timings": reply.Timings, "truncated": reply.Truncated, "limit_reached": reply.LimitReached}
		t.Log("SUPPORT_CASE", jsonBytes(row))
		if errors.Is(validation, ErrUnavailable) {
			t.Error("inference runtime failed", q.ID)
		}
	}
}
