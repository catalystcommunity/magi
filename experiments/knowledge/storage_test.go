package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	wire "opal.local/experiments/knowledge/generated/go"
)

func wantError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
func inputFor(id, session, content string, r wire.InterpretationRecord, scopes ...wire.ScopeRef) wire.SessionInput {
	return wire.SessionInput{Header: header("session_input", id, scopes...), SessionId: session,
		Interpretation: r.Header.Ref, Sources: r.Sources, Content: content, CommandKey: id, PolicyRevision: 1}
}
func interpretationFor(id string, i wire.InteractionRecord, scopes ...wire.ScopeRef) wire.InterpretationRecord {
	return wire.InterpretationRecord{Header: header("interpretation", id, scopes...), Sources: []wire.RecordRef{i.Header.Ref},
		Destinations: []wire.DestinationMeaning{{SessionId: "C", Content: "Tuesday after lunch"}, {SessionId: "E", Content: "Tuesday"}}, Method: "synthetic supplied interpretation", Uncertainty: "ambiguous"}
}
func project(t *testing.T, f *fixture, r wire.RecordRef, text string) {
	t.Helper()
	_, err := f.admin.Exec(context.Background(), `INSERT INTO segments(authority,id,body) VALUES($1,$2,$3)`, identity(r.Authority), r.RecordId, text)
	must(t, err)
}

func TestStorage(t *testing.T) {
	h := startHarness(t)
	ctx := context.Background()
	t.Run("shared_reply_correction", func(t *testing.T) {
		f := h.fixture(t)
		content, err := os.ReadFile("fixtures/shared-reply.json")
		must(t, err)
		var text map[string]string
		must(t, json.Unmarshal(content, &text))
		p1, p2 := interaction("prompt-C", text["calendar_prompt"]), interaction("prompt-E", text["engineering_prompt"])
		p1.Channel = "voice"
		f.put(t, p1)
		f.put(t, p2)
		i := interaction("I1", text["source"])
		f.put(t, i)
		r1 := interpretationFor("R1", i)
		r1.ContextRefs = []wire.RecordRef{p1.Header.Ref, p2.Header.Ref}
		r1.Destinations[0].Content, r1.Destinations[1].Content = text["calendar_mistaken"], text["engineering_mistaken"]
		f.put(t, r1)
		c1, e1 := inputFor("C1", "C", r1.Destinations[0].Content, r1), inputFor("E1", "E", r1.Destinations[1].Content, r1)
		f.put(t, c1)
		f.put(t, e1)
		var received int
		must(t, f.admin.QueryRow(ctx, `SELECT count(*) FROM deliveries WHERE received`).Scan(&received))
		if received != 0 || f.count(t, "deliveries") != 2 {
			t.Fatal("admission was reported as delivery")
		}
		wantError(t, f.s.Deliver(ctx, f.access, c1.Header.Ref, "ctx-before-receipt"), ErrConflict)
		for range 3 {
			must(t, f.s.Deliver(ctx, f.access, c1.Header.Ref, ""))
		}
		for range 3 {
			must(t, f.s.Deliver(ctx, f.access, c1.Header.Ref, "ctx-1"))
		}
		must(t, f.s.Deliver(ctx, f.access, c1.Header.Ref, "ctx-2"))
		if f.count(t, "context_uses") != 2 {
			t.Fatal("context uses lost or duplicated")
		}
		i2 := interaction("I2", text["correction"])
		i2.Header.Causes = []wire.RecordRef{i.Header.Ref}
		f.put(t, i2)
		r2 := interpretationFor("R2", i2)
		r2.Sources = []wire.RecordRef{i.Header.Ref, i2.Header.Ref}
		r2.Prior = &r1.Header.Ref
		r2.Destinations[0].Content, r2.Destinations[1].Content = text["calendar_corrected"], text["engineering_corrected"]
		expected := uint64(1)
		_, err = f.s.put(ctx, f.access, "R2", r2, &expected)
		must(t, err)
		if f.count(t, "deliveries") != 2 || f.count(t, "context_uses") != 2 {
			t.Fatal("correction repeated effects")
		}
		r3 := r2
		r3.Header = header("interpretation", "R3")
		_, err = f.s.put(ctx, f.access, "R3", r3, &expected)
		wantError(t, err, ErrConflict)
		history, err := f.s.History(ctx, f.access, i.Header.Ref)
		must(t, err)
		records := map[string]wire.EvidenceRecord{}
		for _, record := range history.Records {
			rh, _, _, _, _ := describe(record)
			records[rh.Ref.RecordId] = record
		}
		if records["C1"].(wire.SessionInput).Content != text["calendar_mistaken"] || records["E1"].(wire.SessionInput).Content != text["engineering_mistaken"] {
			t.Fatal("history was rewritten")
		}
		if _, ok := records["R2"]; !ok {
			t.Fatal("correction missing from history")
		}
		c2 := inputFor("C2", "C", r2.Destinations[0].Content, r2)
		f.put(t, c2)
		// A second command key is not permission to deliver the same route twice.
		duplicate := c2
		duplicate.Header = header("session_input", "C2-again")
		duplicate.CommandKey = "C2-again"
		_, err = f.s.put(ctx, f.access, duplicate.CommandKey, duplicate, nil)
		wantError(t, err, ErrConflict)
		if f.count(t, "deliveries") != 3 {
			t.Fatal("duplicate route persisted")
		}
		stale := inputFor("stale", "E", r1.Destinations[1].Content, r1)
		_, err = f.s.put(ctx, f.access, stale.CommandKey, stale, nil)
		wantError(t, err, ErrConflict)
		t.Log("One source, two admissions, separate receipt/use, preserved history, and explicit correction passed")
	})
	t.Run("input_must_match_interpretation", func(t *testing.T) {
		f := h.fixture(t)
		i := interaction("original", "Tuesday for both")
		f.put(t, i)
		r := interpretationFor("meaning", i)
		f.put(t, r)
		for _, input := range []wire.SessionInput{
			inputFor("changed-content", "C", "Monday instead", r),
			inputFor("changed-session", "group", r.Destinations[0].Content, r),
		} {
			_, err := f.s.put(ctx, f.access, input.CommandKey, input, nil)
			wantError(t, err, ErrInvalid)
		}
		input := inputFor("missing-source", "C", r.Destinations[0].Content, r)
		input.Sources = nil
		_, err := f.s.put(ctx, f.access, input.CommandKey, input, nil)
		wantError(t, err, ErrInvalid)
		if f.count(t, "deliveries") != 0 || f.count(t, "records") != 2 {
			t.Fatal("changed input was admitted")
		}
	})
	t.Run("admission_serializes_with_correction", func(t *testing.T) {
		f := h.fixture(t)
		i := interaction("original", "Tuesday for both")
		f.put(t, i)
		r := interpretationFor("meaning", i)
		f.put(t, r)
		blocked, err := f.admin.Begin(ctx)
		must(t, err)
		defer blocked.Rollback(ctx)
		_, err = blocked.Exec(ctx, `SELECT id FROM sessions WHERE id='C' FOR UPDATE`)
		must(t, err)
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		input := inputFor("input", "C", r.Destinations[0].Content, r)
		admitted := make(chan error, 1)
		go func() { _, err := f.s.put(bounded, f.access, input.CommandKey, input, nil); admitted <- err }()
		waitForLock := func(fragment string) {
			t.Helper()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				must(t, f.admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
                    WHERE usename='opal_fixture' AND wait_event_type='Lock' AND position($1 in query)>0)`, fragment).Scan(&waiting))
				if waiting {
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("operation did not wait for the expected lock:", fragment)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		waitForLock("SELECT scopes,revision FROM sessions")
		correction := interpretationFor("correction", i)
		correction.Prior = &r.Header.Ref
		correction.Destinations[0].Content = "Tuesday morning"
		expected := uint64(1)
		corrected := make(chan error, 1)
		go func() { _, err := f.s.put(bounded, f.access, "correction", correction, &expected); corrected <- err }()
		waitForLock("UPDATE interpretation_heads SET current_id=")
		must(t, blocked.Commit(ctx))
		must(t, <-admitted)
		must(t, <-corrected)
		if f.count(t, "deliveries") != 1 {
			t.Fatal("correction changed an admitted route")
		}
		stale := inputFor("stale", "E", r.Destinations[1].Content, r)
		_, err = f.s.put(ctx, f.access, stale.CommandKey, stale, nil)
		wantError(t, err, ErrConflict)
	})
	t.Run("concurrent_retry_and_conflict", func(t *testing.T) {
		f := h.fixture(t)
		i := interaction("same", "Tuesday")
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		receipts := make(chan wire.Receipt, 12)
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := f.s.put(ctx, f.access, "same-key", i, nil)
				errs <- err
				receipts <- r
			}()
		}
		wg.Wait()
		close(errs)
		close(receipts)
		for err := range errs {
			must(t, err)
		}
		var first *wire.Receipt
		for r := range receipts {
			if first == nil {
				copy := r
				first = &copy
			} else if !reflect.DeepEqual(*first, r) {
				t.Fatal("retry changed receipt")
			}
		}
		if f.count(t, "records") != 1 || f.count(t, "outbox") != 1 || f.count(t, "command_receipts") != 1 {
			t.Fatal("retry created duplicates")
		}
		changed := i
		changed.Content = "different"
		_, err := f.s.put(ctx, f.access, "same-key", changed, nil)
		wantError(t, err, ErrConflict)
		changed = i
		changed.Header.Ref.RecordId = "different-id"
		_, err = f.s.put(ctx, f.access, "new-key", changed, nil)
		wantError(t, err, ErrConflict)
	})
	t.Run("mixed_private_reply", func(t *testing.T) {
		f := h.fixture(t)
		i := interaction("mixed", "Tuesday for both. Synthetic private journal detail.")
		f.put(t, i)
		a := f.access
		a.ControlScope = workScope
		r := interpretationFor("unsafe", i, workScope)
		r.Destinations = []wire.DestinationMeaning{{SessionId: "group", Content: "Tuesday"}}
		_, err := f.s.put(ctx, a, "unsafe", r, nil)
		wantError(t, err, ErrDenied)
		r.Header = header("interpretation", "safe", privateScope, workScope)
		_, err = f.s.put(ctx, a, "safe", r, nil)
		must(t, err)
		input := inputFor("group-input", "group", "Tuesday", r, privateScope, workScope)
		_, err = f.s.put(ctx, a, input.CommandKey, input, nil)
		wantError(t, err, ErrDenied)
		// Explicit fixture policy change; the actual policy API is not implemented.
		_, err = f.admin.Exec(ctx, `UPDATE sessions SET scopes=$1,revision=2 WHERE id='group'`, []string{scopeKey(privateScope), scopeKey(workScope)})
		must(t, err)
		_, err = f.s.put(ctx, a, input.CommandKey, input, nil)
		wantError(t, err, ErrConflict)
		input.PolicyRevision = 2
		_, err = f.s.put(ctx, a, input.CommandKey, input, nil)
		must(t, err)
		b := a
		b.Actor = bob
		_, err = f.s.Read(ctx, b, input.Header.Ref)
		wantError(t, err, ErrDenied)
		if f.count(t, "deliveries") != 1 {
			t.Fatal("denied admission persisted")
		}
	})
	t.Run("current_policy_overrides_stale_projection", func(t *testing.T) {
		f := h.fixture(t)
		a := f.access
		a.ControlScope = workScope
		i := interaction("work-source", "white dwarf star mass", workScope)
		_, err := f.s.put(ctx, a, "work-source", i, nil)
		must(t, err)
		r := interpretationFor("work-route", i, workScope)
		r.Destinations = []wire.DestinationMeaning{{SessionId: "group", Content: "white dwarf star mass"}}
		_, err = f.s.put(ctx, a, "work-route", r, nil)
		must(t, err)
		input := inputFor("work-input", "group", "white dwarf star mass", r, workScope)
		_, err = f.s.put(ctx, a, "work-input", input, nil)
		must(t, err)
		project(t, f, input.Header.Ref, input.Content)
		b := a
		b.Actor = bob
		b.Purpose = "recall"
		q := wire.KnowledgeQuery{Question: "white dwarf", Purpose: "recall", Limit: 10}
		result, err := f.s.Search(ctx, b, q)
		must(t, err)
		if len(result.Matches) != 1 {
			t.Fatal("fixture is not initially visible")
		}
		_, err = f.admin.Exec(ctx, `UPDATE sessions SET scopes=$1,revision=revision+1 WHERE id='group'`, []string{scopeKey(workScope), scopeKey(privateScope)})
		must(t, err)
		result, err = f.s.Search(ctx, b, q)
		must(t, err)
		if len(result.Matches) != 0 {
			t.Fatal("stale index exposed private session")
		}
		_, err = f.s.Read(ctx, b, input.Header.Ref)
		wantError(t, err, ErrDenied)
		history, err := f.s.History(ctx, b, i.Header.Ref)
		must(t, err)
		for _, r := range history.Records {
			rh, _, _, _, _ := describe(r)
			if rh.Ref.RecordId == "work-input" {
				t.Fatal("history exposed private descendant")
			}
		}
		// Current source restrictions also apply to retained derived records.
		_, err = f.admin.Exec(ctx, `UPDATE records SET scopes=$1 WHERE id='work-source'`, []string{scopeKey(workScope), scopeKey(privateScope)})
		must(t, err)
		_, err = f.s.Read(ctx, b, r.Header.Ref)
		wantError(t, err, ErrDenied)
	})
	t.Run("purpose_processor_and_representation", func(t *testing.T) {
		f := h.fixture(t)
		source := wire.SourceContext{Header: header("source", "journal"), ResourceId: "journal", Adapter: "fixture", Content: "white dwarf", EnrichmentAllowed: false}
		f.put(t, source)
		project(t, f, source.Header.Ref, source.Content)
		a := f.access
		a.Purpose = "history"
		_, err := f.s.Read(ctx, a, source.Header.Ref)
		must(t, err)
		a.Purpose = "enrich"
		_, err = f.s.Read(ctx, a, source.Header.Ref)
		wantError(t, err, ErrDenied)
		result, err := f.s.Search(ctx, a, wire.KnowledgeQuery{Question: "white dwarf", Purpose: "enrich", Limit: 10})
		must(t, err)
		if len(result.Matches) != 0 {
			t.Fatal("journal used for enrichment")
		}
		a = f.access
		a.Processor = wire.IdentityRef{Domain: "external.test", AccountId: "model"}
		_, err = f.s.Read(ctx, a, source.Header.Ref)
		wantError(t, err, ErrDenied)
		a = f.access
		a.Actor = janaya
		a.Persona = &janaya
		_, err = f.s.Read(ctx, a, source.Header.Ref)
		wantError(t, err, ErrDenied)
		a.RepresentedIdentity = &alice
		_, err = f.s.Read(ctx, a, source.Header.Ref)
		wantError(t, err, ErrDenied)
		_, err = f.admin.Exec(ctx, `INSERT INTO representations VALUES($1,$2,$3,$4)`, identity(janaya), identity(alice), scopeKey(privateScope), a.Purpose)
		must(t, err)
		_, err = f.s.Read(ctx, a, source.Header.Ref)
		must(t, err)
		_, err = f.admin.Exec(ctx, `DELETE FROM representations`)
		must(t, err)
		_, err = f.s.Read(ctx, a, source.Header.Ref)
		wantError(t, err, ErrDenied)
	})
	t.Run("delete_blocks_retained_derivative", func(t *testing.T) {
		f := h.fixture(t)
		i := interaction("original", "white dwarf")
		f.put(t, i)
		r := interpretationFor("derived", i)
		f.put(t, r)
		// Simulate an authorized removal barrier. Physical erasure is not implemented.
		_, err := f.admin.Exec(ctx, `UPDATE records SET deleted=true WHERE id='original'`)
		must(t, err)
		_, err = f.s.Read(ctx, f.access, r.Header.Ref)
		wantError(t, err, ErrDenied)
		_, err = f.s.put(ctx, f.access, "original", i, nil)
		wantError(t, err, ErrDenied)
	})
	t.Run("authority_qualified_ids_and_exact_time", func(t *testing.T) {
		f := h.fixture(t)
		i := interaction("same-id", "primary")
		f.put(t, i)
		secondScope := wire.ScopeRef{Authority: secondary, ScopeId: "alice-private"}
		f.grant(t, alice, secondScope, true)
		_, err := f.admin.Exec(ctx, `INSERT INTO processor_grants VALUES($1,$2,$3)`, identity(localProcessor), scopeKey(secondScope), "capture")
		must(t, err)
		s2 := *f.s
		s2.authority = secondary
		a2 := f.access
		a2.ControlScope = secondScope
		i2 := interaction("same-id", "secondary", secondScope)
		i2.Header.Ref.Authority = secondary
		_, err = s2.put(ctx, a2, "same-id", i2, nil)
		must(t, err)
		got, err := f.s.Read(ctx, f.access, i.Header.Ref)
		must(t, err)
		if got.(wire.InteractionRecord).Content != "primary" {
			t.Fatal("authority collision")
		}
		got, err = s2.Read(ctx, a2, i2.Header.Ref)
		must(t, err)
		if got.(wire.InteractionRecord).Content != "secondary" {
			t.Fatal("authority collision")
		}
		var sec int64
		var nano int
		must(t, f.admin.QueryRow(ctx, `SELECT observed_seconds,observed_nanos FROM records WHERE authority=$1 AND id=$2`, identity(primary), "same-id").Scan(&sec, &nano))
		if !time.Unix(sec, int64(nano)).Equal(fixedTime) {
			t.Fatal("database lost nanoseconds")
		}
		// A valid fixture identity from another authority is not a scope grant.
		a2.Actor = bob
		_, err = s2.Read(ctx, a2, i2.Header.Ref)
		wantError(t, err, ErrDenied)
	})
	t.Run("late_commit_cursor_and_concurrent_publication", func(t *testing.T) {
		f := h.fixture(t)
		late := interaction("late", "old event arrives late")
		late.Header.StreamId = "shared-stream"
		payload := wire.EncodeHistoryPage(wire.HistoryPage{Records: []wire.EvidenceRecord{late}})
		tx, err := f.admin.Begin(ctx)
		must(t, err)
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `INSERT INTO records(authority,id,kind,producer,stream,source_sequence,payload,scopes,purposes,observed_seconds,observed_nanos)
            VALUES($1,'late','interaction',$2,'shared-stream',1,$3,$4,$5,$6,$7)`, identity(primary), identity(alice), payload, []string{scopeKey(privateScope)}, purposes, fixedTime.Unix(), fixedTime.Nanosecond())
		must(t, err)
		_, err = tx.Exec(ctx, `INSERT INTO outbox(authority,id) VALUES($1,'late')`, identity(primary))
		must(t, err)
		early := interaction("early", "newer source sequence commits first")
		early.Header.StreamId = "shared-stream"
		early.Header.SourceSequence = 2
		f.put(t, early)
		must(t, f.s.Publish(ctx))
		var before int64
		must(t, f.admin.QueryRow(ctx, `SELECT max(position) FROM feed`).Scan(&before))
		must(t, tx.Commit(ctx))
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() { defer wg.Done(); errs <- f.s.Publish(ctx) }()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			must(t, e)
		}
		var latePosition int64
		must(t, f.admin.QueryRow(ctx, `SELECT position FROM feed WHERE id='late'`).Scan(&latePosition))
		if latePosition <= before || f.count(t, "feed") != 2 {
			t.Fatal("late commit was skipped or publication duplicated")
		}
	})
	t.Run("bounded_queries_and_failed_source", func(t *testing.T) {
		f := h.fixture(t)
		_, err := f.s.Search(ctx, f.access, wire.KnowledgeQuery{Question: "x", Purpose: "capture", Limit: 10000})
		wantError(t, err, ErrInvalid)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		result, err := f.s.Search(cancelled, f.access, wire.KnowledgeQuery{Question: "x", Purpose: "capture", Limit: 10})
		wantError(t, err, context.Canceled)
		if result.Status != "cancelled" {
			t.Fatal("cancellation reported as no match")
		}
		a := f.access
		a.ControlScope = workScope
		a.Actor = bob
		record := interaction("unauthorized-write", "x", workScope)
		record.Header.Producer = bob
		_, err = f.s.put(ctx, a, "write", record, nil)
		wantError(t, err, ErrDenied)
		corrupt := interaction("corrupt", "white dwarf")
		f.put(t, corrupt)
		project(t, f, corrupt.Header.Ref, corrupt.Content)
		_, err = f.admin.Exec(ctx, `UPDATE records SET payload=$1 WHERE id='corrupt'`, []byte{0xff})
		must(t, err)
		result, err = f.s.Search(ctx, f.access, wire.KnowledgeQuery{Question: "white dwarf", Purpose: "capture", Limit: 10})
		wantError(t, err, ErrUnavailable)
		if result.Status != "unavailable" {
			t.Fatal("unreadable evidence reported as no match")
		}
		f.s.db.Close()
		result, err = f.s.Search(ctx, f.access, wire.KnowledgeQuery{Question: "x", Purpose: "capture", Limit: 10})
		wantError(t, err, ErrUnavailable)
		if result.Status != "unavailable" {
			t.Fatal("failed store reported as no match")
		}
	})
	t.Run("retrieval_corpus", func(t *testing.T) { testRetrievalCorpus(t, h) })
	t.Run("hard_restart_receipt_recovery", func(t *testing.T) {
		f := h.fixture(t)
		i := interaction("restart-witness", "durable source")
		original := f.put(t, i)
		docker(t, "kill", "--signal=KILL", h.container)
		docker(t, "start", h.container)
		t.Log("Restart binding:", docker(t, "port", h.container, "5432/tcp"))
		deadline := time.Now().Add(30 * time.Second)
		var lastErr error
		for {
			if err := f.s.db.Ping(ctx); err == nil {
				break
			} else {
				lastErr = err
			}
			if time.Now().After(deadline) {
				t.Fatal("restart timed out:", lastErr)
			}
			time.Sleep(50 * time.Millisecond)
		}
		receipt, err := f.s.put(ctx, f.access, "restart-witness", i, nil)
		must(t, err)
		if !reflect.DeepEqual(original, receipt) || f.count(t, "records") != 1 {
			t.Fatal("receipt was not recovered")
		}
		must(t, f.s.Publish(ctx))
		if f.count(t, "feed") != 1 {
			t.Fatal("outbox did not recover")
		}
		t.Log(fmt.Sprintf("Recovered accepted record, exact receipt, and pending publication after SIGKILL"))
	})
}
