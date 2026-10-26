//go:build integration

package workflow

import (
	"context"
	"errors"
	"fmt"
	w "github.com/catalystcommunity/magi/generated/go"
	"sync"
	"testing"
	"time"
)

type recordingAdapter struct {
	store *Store
	base  CallAdapter
}

func (a recordingAdapter) Call(ctx context.Context, r CallRequest) (w.WorkflowValues, w.RecordRef, error) {
	call := w.ReadWorkflowCall{Owner: r.Definition.Owner, RunId: r.RunID, StepId: r.StepID}
	e := a.store.BeginCallEvidence(ctx, w.WorkflowCallEvidence{Call: call, Capability: "fixture.draft", AdapterRevision: "fixture-v1", Destination: "fixture", MediaType: "application/json", Request: []byte(`{"question":"synthetic"}`), Outcome: "prepared"})
	if e != nil {
		return w.WorkflowValues{}, w.RecordRef{}, e
	}
	result, ref, e := a.base.Call(ctx, r)
	if e != nil {
		return result, ref, e
	}
	e = a.store.FinishCallEvidence(ctx, call, []byte(`{"answer":"synthetic"}`), "")
	return result, ref, e
}
func testCallWorkers(t *testing.T, f *fixture) {
	ctx := context.Background()
	t.Run("worker-replicas-dispatch-once", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		r := startFixture(t, s, "worker-replicas")
		advanceTwice(t, s, r.RunId)
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		for range 3 {
			wg.Go(func() { _, e := (&Worker{Engine: engine, Definition: r.Definition}).Tick(ctx); errs <- e })
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			must(t, e)
		}
		if adapter.calls.Load() != 1 {
			t.Fatal("worker repeated effect")
		}
	})
	t.Run("worker-cursor-passes-unrelated-definitions", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		d := sampleDefinition()
		d.Id = "other-definition"
		ref, e := s.Publish(ctx, actor, d, nil)
		must(t, e)
		for i := 0; i < 17; i++ {
			r := w.StartWorkflow{RunId: fmt.Sprintf("before-%02d", i), Definition: ref, Inputs: textValues("subject", "unrelated"), Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}, Causes: []w.RecordRef{}}
			_, e = s.Start(ctx, actor, r, nil)
			must(t, e)
			advanceTwice(t, s, r.RunId)
		}
		r := startFixture(t, s, "z-target")
		advanceTwice(t, s, r.RunId)
		worker := &Worker{Engine: engine, Definition: r.Definition}
		n, e := worker.Tick(ctx)
		must(t, e)
		if n != 0 {
			t.Fatal("unrelated work selected")
		}
		n, e = worker.Tick(ctx)
		must(t, e)
		if n != 1 || adapter.calls.Load() != 1 {
			t.Fatal("cursor starved target", n)
		}
	})
	t.Run("worker-respects-current-execute-grant", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		r := startFixture(t, s, "worker-denied")
		advanceTwice(t, s, r.RunId)
		must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: "execute", Allowed: false}))
		_, e := (&Worker{Engine: engine, Definition: r.Definition}).Tick(ctx)
		must(t, e)
		if adapter.calls.Load() != 0 {
			t.Fatal("worker ignored withdrawal")
		}
	})
	t.Run("call-evidence-is-retained-and-not-replaced", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		r := startFixture(t, s, "evidence")
		advanceTwice(t, s, r.RunId)
		engine.Adapters["fixture.draft"] = recordingAdapter{store: s, base: adapter}
		must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
		call := w.ReadWorkflowCall{Owner: definitionOwner, RunId: r.RunId, StepId: "draft"}
		v, e := s.ReadCall(ctx, actor, call)
		must(t, e)
		if v.Outcome != "returned" || v.Response == nil || v.FinishedAt == nil {
			t.Fatal("evidence missing")
		}
		must(t, s.FinishCallEvidence(ctx, call, []byte(`{"answer":"synthetic"}`), ""))
		if e = s.FinishCallEvidence(ctx, call, []byte(`{"answer":"changed"}`), ""); !errors.Is(e, ErrConflict) {
			t.Fatal("evidence replaced", e)
		}
		must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: "read", Allowed: false}))
		if _, e = s.ReadCall(ctx, actor, call); !errors.Is(e, ErrDenied) {
			t.Fatal("evidence bypassed policy", e)
		}
	})
	t.Run("result-wakes-an-active-coordinator", func(t *testing.T) {
		s, engine, _ := serviceFixture(t, f)
		r := startFixture(t, s, "wake-result")
		advanceTwice(t, s, r.RunId)
		coordinator := &Coordinator{Store: s}
		claim, e := coordinator.claim(ctx)
		must(t, e)
		if claim == nil {
			t.Fatal("no coordination claim")
		}
		must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
		must(t, coordinator.finish(ctx, *claim, "reconcile_required"))
		var due time.Time
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT due_at FROM workflow_coordination WHERE run_id=$1`, r.RunId).Scan(&due))
		if time.Until(due) > time.Second {
			t.Fatal("old observation lost result wake", due)
		}
	})
	t.Run("migration-v3-retains-admission-and-result", func(t *testing.T) {
		s, engine, _ := serviceFixture(t, f)
		r := startFixture(t, s, "migration-v3")
		advanceTwice(t, s, r.RunId)
		must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
		_, e := s.Journal.Pool.Exec(ctx, `DROP TABLE workflow_call_evidence; ALTER TABLE workflow_coordination DROP COLUMN wake_requested;
			ALTER TABLE workflow_service DROP CONSTRAINT workflow_service_version_check;
			ALTER TABLE workflow_service ADD CONSTRAINT workflow_service_version_check CHECK(version=3) NOT VALID;
			UPDATE workflow_service SET version=3; ALTER TABLE workflow_service VALIDATE CONSTRAINT workflow_service_version_check`)
		must(t, e)
		if e = s.Check(ctx); !errors.Is(e, ErrConflict) {
			t.Fatal("old schema accepted", e)
		}
		must(t, s.Initialize(ctx))
		must(t, s.Initialize(ctx))
		must(t, s.Check(ctx))
		if observe(t, s, r.RunId).Steps[0].Result == nil {
			t.Fatal("migration lost result")
		}
	})
}
