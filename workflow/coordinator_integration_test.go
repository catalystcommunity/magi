//go:build integration

package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Advance the local poll clock in tests without expiring any active lease.
func tickDue(t *testing.T, c *Coordinator) CoordinationReport {
	t.Helper()
	_, e := c.Store.Journal.Pool.Exec(context.Background(), `UPDATE workflow_coordination SET due_at=clock_timestamp() WHERE due_at IS NOT NULL`)
	must(t, e)
	report, e := c.Tick(context.Background(), 16)
	must(t, e)
	return report
}

func finishOnly(t *testing.T, s *Store, id string) w.StartWorkflow {
	t.Helper()
	d := sampleDefinition()
	d.Id = "passthrough"
	d.Outputs = textPorts("subject")
	d.Steps = []w.WorkflowStep{{Id: "finish", Kind: "finish", Inputs: textPorts("subject"), Outputs: textPorts("subject"), Bindings: []w.WorkflowBinding{{Input: "subject", SourceStep: "$input", Output: "subject"}}}}
	d.Results = []w.WorkflowBinding{{Input: "subject", SourceStep: "finish", Output: "subject"}}
	ref, e := s.Publish(context.Background(), actor, d, nil)
	must(t, e)
	r := w.StartWorkflow{RunId: id, Definition: ref, Inputs: textValues("subject", "Synthetic pass through"), Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}, Causes: []w.RecordRef{}}
	_, e = s.Start(context.Background(), actor, r, nil)
	must(t, e)
	return r
}

func testWorkflowCoordination(t *testing.T, f *fixture) {
	ctx := context.Background()
	t.Run("coordination-admission-is-atomic", func(t *testing.T) {
		s, _, _ := serviceFixture(t, f)
		_, e := s.Journal.Pool.Exec(ctx, `CREATE FUNCTION reject_schedule() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic schedule failure'; END $$;
			CREATE TRIGGER reject_schedule BEFORE INSERT ON workflow_coordination FOR EACH ROW EXECUTE FUNCTION reject_schedule()`)
		must(t, e)
		ref, e := s.Publish(ctx, actor, sampleDefinition(), nil)
		must(t, e)
		r := w.StartWorkflow{RunId: "atomic", Definition: ref, Inputs: textValues("subject", "Synthetic"), Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}, Causes: []w.RecordRef{}}
		if _, e = s.Start(ctx, actor, r, nil); e == nil {
			t.Fatal("failed schedule was admitted")
		}
		var count int
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_runs`).Scan(&count))
		if count != 0 {
			t.Fatal("run survived failed admission", count)
		}
		_, e = s.Journal.Pool.Exec(ctx, `DROP TRIGGER reject_schedule ON workflow_coordination`)
		must(t, e)
		_, e = s.Start(ctx, actor, r, nil)
		must(t, e)
		c := &Coordinator{Store: s}
		claim, e := c.claim(ctx)
		must(t, e)
		_, e = s.Start(ctx, actor, r, nil)
		must(t, e)
		other, e := c.claim(ctx)
		must(t, e)
		if claim == nil || other != nil {
			t.Fatal("start replay cleared an active lease")
		}
	})
	t.Run("coordination-bounded-fair-batches", func(t *testing.T) {
		s, _, adapter := serviceFixture(t, f)
		for i := range 7 {
			startFixture(t, s, fmt.Sprintf("batch-%d", i))
		}
		c := &Coordinator{Store: s}
		for _, limit := range []int{0, -1, 65} {
			if _, e := c.Tick(ctx, limit); !errors.Is(e, ErrInvalid) {
				t.Fatal(e)
			}
		}
		for range 2 {
			r, e := c.Tick(ctx, 3)
			must(t, e)
			if r.Evaluated != 3 || r.Completed != 0 || r.Deferred != 0 {
				t.Fatal(r)
			}
		}
		var count int
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_coordination WHERE generation=1`).Scan(&count))
		if count != 6 || adapter.calls.Load() != 0 {
			t.Fatal("unfair selection or implicit tool execution", count)
		}
		if observe(t, s, "batch-0").Steps[0].Phase != "ready" {
			t.Fatal("missing worker was treated as human work")
		}
	})
	t.Run("coordination-storage-wait-has-a-deadline", func(t *testing.T) {
		s, _, _ := serviceFixture(t, f)
		startFixture(t, s, "pool-exhausted")
		pool := s.Journal.Pool
		var held []*pgxpool.Conn
		defer func() {
			for _, conn := range held {
				conn.Release()
			}
		}()
		for range pool.Config().MaxConns {
			conn, e := pool.Acquire(ctx)
			must(t, e)
			held = append(held, conn)
		}
		began := time.Now()
		if _, e := (&Coordinator{Store: s}).Tick(ctx, 1); !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal("unbounded database acquisition", e)
		}
		if elapsed := time.Since(began); elapsed < coordinationStorage || elapsed > coordinationStorage+2*time.Second {
			t.Fatal("unexpected database deadline", elapsed)
		}
	})
	t.Run("coordination-replicas-claim-distinct-runs", func(t *testing.T) {
		s, _, _ := serviceFixture(t, f)
		for i := range 8 {
			startFixture(t, s, fmt.Sprintf("replicas-%d", i))
		}
		var wg sync.WaitGroup
		claims := make(chan *coordinationClaim, 12)
		errs := make(chan error, 12)
		for range 12 {
			wg.Go(func() {
				v, e := (&Coordinator{Store: s}).claim(ctx)
				claims <- v
				errs <- e
			})
		}
		wg.Wait()
		close(claims)
		close(errs)
		for e := range errs {
			must(t, e)
		}
		seen := map[string]bool{}
		for claim := range claims {
			if claim == nil {
				continue
			}
			if seen[claim.run] || claim.generation != 1 {
				t.Fatal("two owners for a live lease", claim)
			}
			seen[claim.run] = true
		}
		if len(seen) != 8 {
			t.Fatal(seen)
		}
	})
	t.Run("coordination-locked-run-does-not-block-another", func(t *testing.T) {
		s, _, _ := serviceFixture(t, f)
		startFixture(t, s, "locked")
		startFixture(t, s, "available")
		tx, e := s.Journal.Pool.Begin(ctx)
		must(t, e)
		defer tx.Rollback(ctx)
		_, e = tx.Exec(ctx, `SELECT run_id FROM workflow_coordination WHERE run_id='locked' FOR UPDATE`)
		must(t, e)
		bound, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		claim, e := (&Coordinator{Store: s}).claim(bound)
		must(t, e)
		if claim == nil || claim.run != "available" {
			t.Fatal(claim)
		}
	})
	t.Run("coordination-expired-and-stale-leases-cannot-finish", func(t *testing.T) {
		s, _, _ := serviceFixture(t, f)
		startFixture(t, s, "lease")
		c := &Coordinator{Store: s}
		first, e := c.claim(ctx)
		must(t, e)
		if first == nil {
			t.Fatal("no lease")
		}
		_, e = s.Journal.Pool.Exec(ctx, `UPDATE workflow_coordination SET lease_until=clock_timestamp()-interval '1 second'`)
		must(t, e)
		if e = c.finish(ctx, *first, "complete"); !errors.Is(e, ErrStale) {
			t.Fatal("expired lease finished", e)
		}
		next, e := (&Coordinator{Store: s}).claim(ctx)
		must(t, e)
		if next == nil || next.generation != first.generation+1 {
			t.Fatal("lease not recovered", next)
		}
		if e = c.finish(ctx, *first, "complete"); !errors.Is(e, ErrStale) {
			t.Fatal("late owner replaced new lease", e)
		}
		must(t, c.finish(ctx, *next, "evaluated"))
	})
	t.Run("coordination-background-completion-and-stop", func(t *testing.T) {
		s, _, adapter := serviceFixture(t, f)
		r := finishOnly(t, s, "background")
		c := &Coordinator{Store: s}
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		completed := make(chan struct{}, 1)
		go func() {
			done <- c.Run(runCtx, func(report CoordinationReport, err error) {
				if report.Completed == 1 {
					completed <- struct{}{}
				}
			})
		}()
		select {
		case <-completed:
		case <-time.After(10 * time.Second):
			t.Fatal("background coordinator did not complete")
		}
		cancel()
		select {
		case e := <-done:
			if !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		case <-time.After(time.Second):
			t.Fatal("coordinator did not stop")
		}
		_, e := s.Start(ctx, actor, r, nil)
		must(t, e)
		must(t, s.Initialize(ctx))
		if tickDue(t, c).Evaluated != 0 || observe(t, s, r.RunId).Result == nil || adapter.calls.Load() != 0 {
			t.Fatal("completion was reopened or tool was invoked")
		}
	})
	t.Run("coordination-human-response-resumes-run", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		r := startFixture(t, s, "human-scheduled")
		c := &Coordinator{Store: s}
		tickDue(t, c)
		must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
		for range 3 {
			tickDue(t, c)
		}
		human := observe(t, s, r.RunId).Steps[1]
		if human.Phase != "waiting_human" {
			t.Fatal(human)
		}
		_, e := s.CompleteHuman(ctx, actor, w.CompleteWorkflowHuman{Owner: definitionOwner, RunId: r.RunId, StepId: "review", TaskId: *human.TaskId, TaskRevision: *human.TaskRevision, Result: textValues("approved", "Reviewed"), Evidence: w.RecordRef{Authority: actor.Identity, Kind: "human-response", RecordId: "scheduled-response"}})
		must(t, e)
		for range 4 {
			tickDue(t, c)
		}
		if observe(t, s, r.RunId).Result == nil || adapter.calls.Load() != 1 {
			t.Fatal("human response did not advance original run")
		}
	})
	t.Run("coordination-unknown-effect-remains-unknown", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		adapter.fail = true
		r := startFixture(t, s, "uncertain-scheduled")
		c := &Coordinator{Store: s}
		tickDue(t, c)
		if e := engine.Execute(ctx, definitionOwner, r.RunId, "draft"); !errors.Is(e, ErrReconcile) {
			t.Fatal(e)
		}
		for range 3 {
			if tickDue(t, c).Deferred != 1 {
				t.Fatal("uncertain result not deferred")
			}
		}
		v := observe(t, s, r.RunId)
		if adapter.calls.Load() != 1 || v.Result != nil || v.Steps[0].Phase != "outcome_unknown" || v.Steps[1].Phase != "dependencies_pending" {
			t.Fatal("uncertain effect was repeated or accepted", v)
		}
	})
	t.Run("coordination-policy-withdrawal-and-restoration", func(t *testing.T) {
		s, _, _ := serviceFixture(t, f)
		r := startFixture(t, s, "withdrawn")
		c := &Coordinator{Store: s}
		must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: "run", Allowed: false}))
		if tickDue(t, c).Deferred != 1 {
			t.Fatal("denial was not deferred")
		}
		var count int
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_intents`).Scan(&count))
		if count != 0 {
			t.Fatal("new work after withdrawal")
		}
		must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: "run", Allowed: true}))
		tickDue(t, c)
		if observe(t, s, r.RunId).Steps[0].Phase != "ready" {
			t.Fatal("restored grant did not resume original admission")
		}
	})
	t.Run("coordination-lost-submission-reply", func(t *testing.T) {
		s, _, _ := serviceFixture(t, f)
		r := startFixture(t, s, "lost-scheduled")
		s.Journal.Transport = &faultTransport{base: s.Journal.Transport, method: "SubmitKeyedTask"}
		c := &Coordinator{Store: s}
		if tickDue(t, c).Deferred != 1 {
			t.Fatal("lost reply not retained")
		}
		tickDue(t, c)
		var count int
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_intents`).Scan(&count))
		if count != 1 || observe(t, s, r.RunId).Steps[0].Phase != "ready" {
			t.Fatal("lost reply created another intent", count)
		}
	})
	t.Run("coordination-migrates-existing-admissions", func(t *testing.T) {
		s, _, _ := serviceFixture(t, f)
		r := startFixture(t, s, "legacy")
		_, e := s.Journal.Pool.Exec(ctx, `DROP TABLE workflow_coordination;
			ALTER TABLE workflow_service DROP CONSTRAINT workflow_service_version_check;
			ALTER TABLE workflow_service ADD CONSTRAINT workflow_service_version_check CHECK(version=1) NOT VALID;
			UPDATE workflow_service SET version=1;
			ALTER TABLE workflow_service VALIDATE CONSTRAINT workflow_service_version_check`)
		must(t, e)
		if e = s.Check(ctx); !errors.Is(e, ErrConflict) {
			t.Fatal("unmigrated store accepted", e)
		}
		wrong := *s
		wrong.Application.InstanceId = "another-installation"
		if e = wrong.Initialize(ctx); !errors.Is(e, ErrConflict) {
			t.Fatal("migration changed service binding", e)
		}
		must(t, s.Initialize(ctx))
		must(t, s.Initialize(ctx))
		must(t, s.Check(ctx))
		c := &Coordinator{Store: s}
		if tickDue(t, c).Evaluated != 1 || observe(t, s, r.RunId).Steps[0].Phase != "ready" {
			t.Fatal("legacy admission was not scheduled")
		}
	})
	t.Run("coordination-runtime-role-cannot-edit-policy-or-schema", func(t *testing.T) {
		admin, _, adapter := serviceFixture(t, f)
		_, e := f.pool.Exec(ctx, `CREATE ROLE coordinator_runtime LOGIN PASSWORD 'synthetic-coordinator' NOSUPERUSER NOBYPASSRLS`)
		must(t, e)
		_, e = admin.Journal.Pool.Exec(ctx, `GRANT USAGE ON SCHEMA public TO coordinator_runtime;
			GRANT SELECT ON workflow_service,workflow_grants,workflow_journal_binding TO coordinator_runtime;
			GRANT SELECT,INSERT,UPDATE ON workflow_definitions,workflow_drafts,workflow_runs,workflow_run_steps,
			workflow_coordination,workflow_intents,workflow_results,workflow_human_bindings,
			workflow_sweep_health,workflow_timeout_work,workflow_timeout_attempts,workflow_call_evidence TO coordinator_runtime`)
		must(t, e)
		cfg := admin.Journal.Pool.Config()
		cfg.ConnConfig.User = "coordinator_runtime"
		cfg.ConnConfig.Password = "synthetic-coordinator"
		pool, e := pgxpool.NewWithConfig(ctx, cfg)
		must(t, e)
		defer pool.Close()
		s := &Store{Application: admin.Application, Journal: &Journal{Pool: pool, Installation: admin.Journal.Installation, Transport: admin.Journal.Transport}}
		must(t, s.Check(ctx))
		_, e = s.SaveDraft(ctx, actor, w.SaveWorkflowDraft{Draft: w.WorkflowDraft{Definition: sampleDefinition(), Positions: []w.WorkflowPosition{}}})
		must(t, e)
		r := startFixture(t, s, "limited-role")
		c := &Coordinator{Store: s}
		tickDue(t, c)
		engine := &Engine{Store: s, Adapters: map[string]CallAdapter{"fixture.draft": adapter}}
		must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
		for range 3 {
			tickDue(t, c)
		}
		human := observe(t, s, r.RunId).Steps[1]
		if human.Phase != "waiting_human" {
			t.Fatal(human)
		}
		_, e = s.CompleteHuman(ctx, actor, w.CompleteWorkflowHuman{Owner: definitionOwner, RunId: r.RunId, StepId: "review", TaskId: *human.TaskId, TaskRevision: *human.TaskRevision, Result: textValues("approved", "Limited runtime"), Evidence: w.RecordRef{Authority: actor.Identity, Kind: "human-response", RecordId: "limited-response"}})
		must(t, e)
		for range 4 {
			tickDue(t, c)
		}
		if observe(t, s, r.RunId).Result == nil || adapter.calls.Load() != 1 {
			t.Fatal("restricted runtime could not complete")
		}
		if report := sweepDue(t, s); report.Closed != 1 || report.Deferred != 0 {
			t.Fatal("restricted runtime could not sweep", report)
		}
		must(t, (&Sweeper{Store: s}).Check(ctx))
		if e = s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: "run", Allowed: false}); e == nil {
			t.Fatal("runtime changed policy")
		}
		if e = s.Initialize(ctx); e == nil {
			t.Fatal("runtime migrated schema")
		}
		for _, statement := range []string{
			`CREATE TABLE forbidden_runtime_ddl (id text)`,
			`DELETE FROM workflow_runs`,
			`UPDATE workflow_service SET version=4`,
			`UPDATE workflow_journal_binding SET installation='forbidden'`,
		} {
			if _, e = pool.Exec(ctx, statement); e == nil {
				t.Fatal("runtime exceeded required privileges")
			}
		}
	})
	t.Run("coordination-process-crash-after-advance", func(t *testing.T) {
		j, dsn := f.journal(t)
		s := &Store{Journal: j, Application: actor}
		must(t, s.Initialize(ctx))
		for _, action := range []string{"read", "write", "run"} {
			must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: action, Allowed: true}))
		}
		r := finishOnly(t, s, "crash-coordination")
		bound, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		child := exec.CommandContext(bound, os.Args[0], "-test.run=^TestWorkflowCoordinatorCrashChild$")
		child.Env = append(os.Environ(), "OPAL_COORDINATOR_CRASH_DSN="+dsn, "OPAL_COORDINATOR_CRASH_SEED="+f.seeds[0])
		output, e := child.CombinedOutput()
		var exit *exec.ExitError
		if bound.Err() != nil || !errors.As(e, &exit) || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("coordinator did not crash at the lease boundary: %v %s", e, output)
		}
		var leased bool
		must(t, j.Pool.QueryRow(ctx, `SELECT lease_until>clock_timestamp() FROM workflow_coordination`).Scan(&leased))
		if !leased || observe(t, s, r.RunId).Result == nil {
			t.Fatal("crash did not retain completed transitions and unfinished lease")
		}
		// Simulate passage of the lease deadline without delaying the whole matrix.
		_, e = j.Pool.Exec(ctx, `UPDATE workflow_coordination SET lease_until=clock_timestamp()-interval '1 second'`)
		must(t, e)
		if tickDue(t, &Coordinator{Store: s}).Completed != 1 {
			t.Fatal("replacement coordinator did not recover")
		}
		var count int
		must(t, j.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_intents`).Scan(&count))
		if count != 2 {
			t.Fatal("crash recovery created new remote operations", count)
		}
	})
}

func TestWorkflowCoordinatorCrashChild(t *testing.T) {
	dsn := os.Getenv("OPAL_COORDINATOR_CRASH_DSN")
	if dsn == "" {
		return
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, dsn)
	must(t, e)
	defer pool.Close()
	s := &Store{Application: actor, Journal: &Journal{Pool: pool, Installation: "fixture", Transport: &Transport{Seeds: []string{os.Getenv("OPAL_COORDINATOR_CRASH_SEED")}}}}
	c := &Coordinator{Store: s}
	claim, e := c.claim(ctx)
	must(t, e)
	if claim == nil {
		t.Fatal("missing admission")
	}
	outcome, e := c.evaluate(ctx, *claim)
	must(t, e)
	if outcome != "complete" {
		t.Fatal(outcome)
	}
	must(t, syscall.Kill(os.Getpid(), syscall.SIGKILL))
	t.Fatal("SIGKILL did not stop coordinator")
}
