//go:build integration

package workflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sweepDue(t *testing.T, s *Store) SweepReport {
	t.Helper()
	_, e := s.Journal.Pool.Exec(context.Background(), `UPDATE workflow_timeout_work SET due_at=clock_timestamp() WHERE due_at IS NOT NULL`)
	must(t, e)
	r, e := (&Sweeper{Store: s}).Tick(context.Background(), 16)
	must(t, e)
	return r
}
func guardedRunTask(t *testing.T, s *Store, run, step string) api.GuardedTask {
	t.Helper()
	var raw []byte
	must(t, s.Journal.Pool.QueryRow(context.Background(), `SELECT task FROM workflow_run_steps WHERE owner=$1 AND run_id=$2 AND step_id=$3`, w.EncodeScopeRef(definitionOwner), run, step).Scan(&raw))
	prior, e := api.DecodeGuardedTask(raw)
	must(t, e)
	r, e := api.NewCorndogsClient(s.Journal.Transport).GetGuardedTask(context.Background(), api.GetGuardedTaskRequest{Queue: stepKey(definitionOwner, run, step), Uuid: prior.Task.Uuid})
	must(t, e)
	if r.Task == nil {
		t.Fatal("task missing")
	}
	return *r.Task
}
func uncertainCall(t *testing.T, s *Store, engine *Engine, adapter *fixtureAdapter, id string) w.StartWorkflow {
	t.Helper()
	r := startFixture(t, s, id)
	advanceTwice(t, s, id)
	adapter.fail = true
	if e := engine.Execute(context.Background(), definitionOwner, id, "draft"); !errors.Is(e, ErrReconcile) {
		t.Fatal(e)
	}
	return r
}
func shortenClaim(t *testing.T, s *Store, task api.GuardedTask) api.GuardedTask {
	t.Helper()
	_, e := api.NewCorndogsClient(s.Journal.Transport).UpdateGuardedTask(context.Background(), api.UpdateGuardedTaskRequest{Queue: task.Task.Queue, Uuid: task.Task.Uuid, OperationId: api.NewOperationID(), ExpectedRevision: task.Revision, ExpectedState: &task.Task.CurrentState, NewState: "running", AutoTargetState: "reconcile_required", Timeout: 1})
	must(t, e)
	r, e := api.NewCorndogsClient(s.Journal.Transport).GetGuardedTask(context.Background(), api.GetGuardedTaskRequest{Queue: task.Task.Queue, Uuid: task.Task.Uuid})
	must(t, e)
	if r.Task == nil {
		t.Fatal("task missing")
	}
	return *r.Task
}

func testWorkflowSweeping(t *testing.T, f *fixture) {
	ctx := context.Background()
	t.Run("sweep-readiness-gates-new-dispatch", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		r := startFixture(t, s, "no-sweeper")
		advanceTwice(t, s, r.RunId)
		for _, statement := range []string{`UPDATE workflow_sweep_health SET checked_at=NULL`, `UPDATE workflow_sweep_health SET checked_at=clock_timestamp()-interval '20 seconds'`, `UPDATE workflow_sweep_health SET checked_at=clock_timestamp()+interval '20 seconds'`} {
			_, e := s.Journal.Pool.Exec(ctx, statement)
			must(t, e)
			if e = (&Sweeper{Store: s}).Check(ctx); !errors.Is(e, ErrUnavailable) {
				t.Fatal(e)
			}
			if e = engine.Execute(ctx, definitionOwner, r.RunId, "draft"); !errors.Is(e, ErrUnavailable) {
				t.Fatal(e)
			}
		}
		if adapter.calls.Load() != 0 {
			t.Fatal("effect without timeout owner")
		}
		sweepDue(t, s)
		must(t, (&Sweeper{Store: s}).Check(ctx))
		must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
		if adapter.calls.Load() != 1 {
			t.Fatal("no dispatch after recovery")
		}
	})
	t.Run("sweep-admission-and-claim-are-atomic", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		r := startFixture(t, s, "atomic-sweep")
		advanceTwice(t, s, r.RunId)
		_, e := s.Journal.Pool.Exec(ctx, `CREATE FUNCTION reject_timeout() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$;
			CREATE TRIGGER reject_timeout BEFORE INSERT ON workflow_timeout_work FOR EACH ROW EXECUTE FUNCTION reject_timeout()`)
		must(t, e)
		if e = engine.Execute(ctx, definitionOwner, r.RunId, "draft"); e == nil {
			t.Fatal("claim without timeout admission")
		}
		var count int
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_intents WHERE method='ClaimGuardedTask'`).Scan(&count))
		if count != 0 || adapter.calls.Load() != 0 || guardedRunTask(t, s, r.RunId, "draft").Revision != 1 {
			t.Fatal("failed admission escaped transaction")
		}
	})
	t.Run("sweep-expiry-is-scoped-and-never-replays-effects", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		r := uncertainCall(t, s, engine, adapter, "timed-out")
		before := shortenClaim(t, s, guardedRunTask(t, s, r.RunId, "draft"))
		client := api.NewCorndogsClient(s.Journal.Transport)
		foreign, e := client.SubmitKeyedTask(ctx, api.SubmitKeyedTaskRequest{Queue: "foreign-" + api.NewOperationID(), SubmissionKey: api.NewOperationID(), Guarded: true, CurrentState: "foreign-running", AutoTargetState: "foreign-ready", Timeout: 1})
		must(t, e)
		adapter.fail = false
		humanRun := startFixture(t, s, "human-waits")
		advanceTwice(t, s, humanRun.RunId)
		must(t, engine.Execute(ctx, definitionOwner, humanRun.RunId, "draft"))
		advanceTwice(t, s, humanRun.RunId)
		humanBefore := guardedRunTask(t, s, humanRun.RunId, "review")
		// Real elapsed time, with a one-second fixture claim and five-second margin.
		time.Sleep(6200 * time.Millisecond)
		s.Journal.Transport = &faultTransport{base: s.Journal.Transport, method: "CleanUpTimedOut"}
		if report := sweepDue(t, s); report.Deferred != 1 {
			t.Fatal("lost reply not recorded", report)
		}
		if e = (&Sweeper{Store: s}).Check(ctx); !errors.Is(e, ErrUnavailable) {
			t.Fatal("failed sweep did not block new calls", e)
		}
		sweepDue(t, s)
		after := guardedRunTask(t, s, r.RunId, "draft")
		if after.Revision != before.Revision+1 || after.Task.CurrentState != "reconcile_required" || after.Task.Timeout != 0 {
			t.Fatal(after)
		}
		if adapter.calls.Load() != 2 || observe(t, s, r.RunId).Steps[0].Phase != "outcome_unknown" {
			t.Fatal("expiry repeated or accepted an unknown effect")
		}
		if e = engine.Execute(ctx, definitionOwner, r.RunId, "draft"); !errors.Is(e, ErrReconcile) {
			t.Fatal(e)
		}
		humanAfter := guardedRunTask(t, s, humanRun.RunId, "review")
		if humanAfter.Revision != humanBefore.Revision || humanAfter.Task.CurrentState != "waiting_human" {
			t.Fatal("human wait changed")
		}
		other, e := client.GetGuardedTask(ctx, api.GetGuardedTaskRequest{Queue: foreign.Receipt.Queue, Uuid: foreign.Receipt.TaskUuid})
		must(t, e)
		if other.Task == nil || other.Task.Revision != 1 || other.Task.Task.CurrentState != "foreign-running" {
			t.Fatal("sweep escaped owned queues", other)
		}
		var pending, errorsRecorded, emptyQueues int
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_timeout_work WHERE due_at IS NOT NULL`).Scan(&pending))
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_timeout_attempts WHERE outcome='error'`).Scan(&errorsRecorded))
		rows, e := s.Journal.Pool.Query(ctx, `SELECT request FROM workflow_timeout_attempts`)
		must(t, e)
		for rows.Next() {
			var raw []byte
			must(t, rows.Scan(&raw))
			v, e := api.DecodeCleanUpTimedOutRequest(raw)
			must(t, e)
			if v.Queue == "" {
				emptyQueues++
			}
		}
		must(t, rows.Err())
		rows.Close()
		if pending != 0 || errorsRecorded != 1 || emptyQueues != 0 {
			t.Fatal("incorrect retained sweep evidence", pending, errorsRecorded, emptyQueues)
		}
		must(t, (&Sweeper{Store: s}).Check(ctx))
	})
	t.Run("sweep-replicas-and-stale-generation", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		uncertainCall(t, s, engine, adapter, "lease-sweep")
		_, e := s.Journal.Pool.Exec(ctx, `UPDATE workflow_timeout_work SET due_at=clock_timestamp()`)
		must(t, e)
		var wg sync.WaitGroup
		claims := make(chan *sweepClaim, 4)
		errs := make(chan error, 4)
		for range 4 {
			wg.Go(func() { v, e := (&Sweeper{Store: s}).claim(ctx); claims <- v; errs <- e })
		}
		wg.Wait()
		close(claims)
		close(errs)
		for e := range errs {
			must(t, e)
		}
		var first *sweepClaim
		for v := range claims {
			if v != nil {
				if first != nil {
					t.Fatal("two live leases")
				}
				first = v
			}
		}
		if first == nil {
			t.Fatal("no sweep lease")
		}
		_, e = s.Journal.Pool.Exec(ctx, `UPDATE workflow_timeout_work SET lease_until=clock_timestamp()-interval '1 second'`)
		must(t, e)
		sweeper := &Sweeper{Store: s}
		next, e := sweeper.claim(ctx)
		must(t, e)
		if next == nil || next.generation != first.generation+1 {
			t.Fatal("lease not recovered")
		}
		if e = sweeper.finish(ctx, *first, nil, nil, "error"); !errors.Is(e, ErrStale) {
			t.Fatal("late owner changed newer lease", e)
		}
		var outcome string
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT outcome FROM workflow_timeout_attempts WHERE generation=$1`, first.generation).Scan(&outcome))
		if outcome != "error" {
			t.Fatal("late evidence was lost")
		}
		reply, observation, closed, e := sweeper.evaluate(ctx, *next)
		must(t, e)
		if closed {
			t.Fatal("unexpired claim closed")
		}
		must(t, sweeper.finish(ctx, *next, reply, observation, "checked"))
	})
	t.Run("sweep-backlog-fails-readiness", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		uncertainCall(t, s, engine, adapter, "overdue-sweep")
		_, e := s.Journal.Pool.Exec(ctx, `UPDATE workflow_timeout_work SET due_at=clock_timestamp()-interval '20 seconds',lease_until=clock_timestamp()+interval '1 minute'`)
		must(t, e)
		_, e = (&Sweeper{Store: s}).Tick(ctx, 1)
		must(t, e)
		if e = (&Sweeper{Store: s}).Check(ctx); !errors.Is(e, ErrUnavailable) {
			t.Fatal("heartbeat hid stale queue", e)
		}
	})
	t.Run("sweep-migration-backfills-existing-claims", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		uncertainCall(t, s, engine, adapter, "migration-sweep")
		_, e := s.Journal.Pool.Exec(ctx, `DROP TABLE workflow_timeout_attempts; DROP TABLE workflow_timeout_work; DROP TABLE workflow_sweep_health;
			ALTER TABLE workflow_service DROP CONSTRAINT workflow_service_version_check;
			ALTER TABLE workflow_service ADD CONSTRAINT workflow_service_version_check CHECK(version=2) NOT VALID;
			UPDATE workflow_service SET version=2; ALTER TABLE workflow_service VALIDATE CONSTRAINT workflow_service_version_check`)
		must(t, e)
		must(t, s.Initialize(ctx))
		must(t, s.Initialize(ctx))
		must(t, s.Check(ctx))
		if e = (&Sweeper{Store: s}).Check(ctx); !errors.Is(e, ErrUnavailable) {
			t.Fatal("migration invented readiness", e)
		}
		var count int
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FROM workflow_timeout_work`).Scan(&count))
		if count != 1 {
			t.Fatal("existing claim not backfilled")
		}
		sweepDue(t, s)
		must(t, (&Sweeper{Store: s}).Check(ctx))
	})
	t.Run("sweep-background-and-cancellation", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		uncertainCall(t, s, engine, adapter, "background-sweep")
		_, e := s.Journal.Pool.Exec(ctx, `UPDATE workflow_timeout_work SET due_at=clock_timestamp()`)
		must(t, e)
		background, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		checked := make(chan struct{}, 1)
		go func() {
			done <- (&Sweeper{Store: s}).Run(background, func(r SweepReport, e error) {
				if r.Checked > 0 {
					select {
					case checked <- struct{}{}:
					default:
					}
				}
			})
		}()
		select {
		case <-checked:
		case <-time.After(10 * time.Second):
			t.Fatal("sweeper did not run")
		}
		cancel()
		select {
		case e = <-done:
			if !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		case <-time.After(time.Second):
			t.Fatal("sweeper did not stop")
		}
	})
	t.Run("sweep-process-crash-retains-original-attempt", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		uncertainCall(t, s, engine, adapter, "crash-sweep")
		_, e := s.Journal.Pool.Exec(ctx, `UPDATE workflow_timeout_work SET due_at=clock_timestamp()`)
		must(t, e)
		bound, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		child := exec.CommandContext(bound, os.Args[0], "-test.run=^TestWorkflowSweepCrashChild$")
		child.Env = append(os.Environ(), "OPAL_SWEEP_CRASH_DSN="+s.Journal.Pool.Config().ConnString(), "OPAL_SWEEP_CRASH_SEED="+f.seeds[0])
		output, e := child.CombinedOutput()
		var exit *exec.ExitError
		if bound.Err() != nil || !errors.As(e, &exit) || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("sweep crash did not occur: %v %s", e, output)
		}
		_, e = s.Journal.Pool.Exec(ctx, `UPDATE workflow_timeout_work SET lease_until=clock_timestamp()-interval '1 second'`)
		must(t, e)
		sweepDue(t, s)
		var pending, checked int
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE outcome='pending'),count(*) FILTER(WHERE outcome='checked') FROM workflow_timeout_attempts`).Scan(&pending, &checked))
		if pending != 1 || checked != 1 || adapter.calls.Load() != 1 {
			t.Fatal("crash evidence or effect changed", pending, checked)
		}
	})
	t.Run("sweep-crashed-claim-cannot-start-a-tool-after-expiry", func(t *testing.T) {
		s, engine, adapter := serviceFixture(t, f)
		r := startFixture(t, s, "crash-claim")
		advanceTwice(t, s, r.RunId)
		bound, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		child := exec.CommandContext(bound, os.Args[0], "-test.run=^TestWorkflowSweepCrashChild$")
		child.Env = append(os.Environ(), "OPAL_SWEEP_CRASH_DSN="+s.Journal.Pool.Config().ConnString(), "OPAL_SWEEP_CRASH_SEED="+f.seeds[0], "OPAL_SWEEP_CRASH_KIND=claim")
		output, e := child.CombinedOutput()
		var exit *exec.ExitError
		if bound.Err() != nil || !errors.As(e, &exit) || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("claim crash did not occur: %v %s", e, output)
		}
		remote := guardedRunTask(t, s, r.RunId, "draft")
		if remote.Task.CurrentState != "running" || remote.Revision != 2 {
			t.Fatal("claim was not applied", remote)
		}
		shortenClaim(t, s, remote)
		time.Sleep(6200 * time.Millisecond)
		if report := sweepDue(t, s); report.Closed != 1 {
			t.Fatal(report)
		}
		if phase := observe(t, s, r.RunId).Steps[0].Phase; phase != "reconcile_required" {
			t.Fatal("pre-dispatch expiry not visible", phase)
		}
		if e = engine.Execute(ctx, definitionOwner, r.RunId, "draft"); !errors.Is(e, ErrStale) {
			t.Fatal("expired claim reused", e)
		}
		if adapter.calls.Load() != 0 {
			t.Fatal("late tool executed")
		}
		var dispatched bool
		must(t, s.Journal.Pool.QueryRow(ctx, `SELECT dispatched FROM workflow_run_steps WHERE run_id=$1 AND step_id='draft'`, r.RunId).Scan(&dispatched))
		if dispatched {
			t.Fatal("late dispatch was admitted")
		}
	})
}

func TestWorkflowSweepCrashChild(t *testing.T) {
	dsn := os.Getenv("OPAL_SWEEP_CRASH_DSN")
	if dsn == "" {
		return
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, dsn)
	must(t, e)
	defer pool.Close()
	transport := &faultTransport{base: &Transport{Seeds: []string{os.Getenv("OPAL_SWEEP_CRASH_SEED")}}, method: "CleanUpTimedOut", crash: true}
	s := &Store{Application: actor, Journal: &Journal{Pool: pool, Installation: "fixture", Transport: transport}}
	if os.Getenv("OPAL_SWEEP_CRASH_KIND") == "claim" {
		transport.method = "ClaimGuardedTask"
		adapter := &fixtureAdapter{caller: actor}
		e = (&Engine{Store: s, Adapters: map[string]CallAdapter{"fixture.draft": adapter}}).Execute(ctx, definitionOwner, "crash-claim", "draft")
		must(t, e)
		t.Fatal("claim crash hook did not run")
	}
	_, e = (&Sweeper{Store: s}).Tick(ctx, 1)
	must(t, e)
	t.Fatal("crash hook did not run")
}
