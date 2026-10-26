package workflow

import (
	"context"
	"errors"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/jackc/pgx/v5"
)

const sweepFreshness = 15 * time.Second

// Sweeper evaluates only queues derived from this service's admitted call steps.
// It never claims work, completes a human step, or repeats an external effect.
type Sweeper struct{ Store *Store }
type SweepReport struct{ Checked, Closed, Deferred int }
type sweepClaim struct {
	owner      w.ScopeRef
	run, step  string
	generation int64
	request    api.CleanUpTimedOutRequest
	task       api.GuardedTask
}

func (s *Store) sweepReady(ctx context.Context, tx pgx.Tx) error {
	var ready bool
	e := tx.QueryRow(ctx, `SELECT COALESCE(checked_at>=clock_timestamp()-$1::bigint*interval '1 millisecond'
		AND checked_at<=clock_timestamp()+interval '5 seconds',false)
		AND NOT EXISTS(SELECT 1 FROM workflow_timeout_work WHERE outcome='error'
		OR due_at<clock_timestamp()-$1::bigint*interval '1 millisecond') FROM workflow_sweep_health WHERE singleton`, sweepFreshness.Milliseconds()).Scan(&ready)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !ready {
		return ErrUnavailable
	}
	return e
}

// Check is for local readiness probes and trusted executors, not public clients.
func (s *Sweeper) Check(ctx context.Context) error {
	if !s.valid() {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, coordinationStorage)
	defer cancel()
	tx, e := s.Store.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	return s.Store.sweepReady(ctx, tx)
}
func (s *Sweeper) valid() bool {
	return s != nil && s.Store != nil && s.Store.Journal != nil && s.Store.Journal.Pool != nil && s.Store.Journal.Transport != nil
}

func (s *Sweeper) claim(ctx context.Context) (*sweepClaim, error) {
	ctx, cancel := context.WithTimeout(ctx, coordinationStorage)
	defer cancel()
	tx, e := s.Store.Journal.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var raw, task []byte
	var clock time.Time
	v := &sweepClaim{}
	e = tx.QueryRow(ctx, `WITH candidate AS (
		SELECT owner,run_id,step_id FROM workflow_timeout_work
		WHERE due_at<=clock_timestamp() AND lease_until<=clock_timestamp()
		ORDER BY due_at,owner,run_id,step_id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE workflow_timeout_work work SET generation=work.generation+1,
		lease_until=clock_timestamp()+interval '60 seconds'
	FROM candidate WHERE work.owner=candidate.owner AND work.run_id=candidate.run_id AND work.step_id=candidate.step_id
	RETURNING work.owner,work.run_id,work.step_id,work.generation,clock_timestamp()`).Scan(&raw, &v.run, &v.step, &v.generation, &clock)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if time.Since(clock) > 5*time.Second || time.Until(clock) > 5*time.Second {
		return nil, ErrReconcile
	}
	v.owner, e = w.DecodeScopeRef(raw)
	if e != nil {
		return nil, e
	}
	e = tx.QueryRow(ctx, `SELECT task FROM workflow_run_steps WHERE owner=$1 AND run_id=$2 AND step_id=$3`, raw, v.run, v.step).Scan(&task)
	if e != nil {
		return nil, e
	}
	v.task, e = api.DecodeGuardedTask(task)
	if e != nil {
		return nil, e
	}
	queue := stepKey(v.owner, v.run, v.step)
	if v.task.Task.Queue != queue || !v.task.Guarded {
		return nil, ErrConflict
	}
	// Clock skew delays expiry; it must not cause early release. Corndogs and the
	// service database still require synchronized clocks within this margin.
	v.request = api.CleanUpTimedOutRequest{Queue: queue, AtTime: clock.Add(-5 * time.Second).UnixNano()}
	_, e = tx.Exec(ctx, `INSERT INTO workflow_timeout_attempts(owner,run_id,step_id,generation,request)
		VALUES($1,$2,$3,$4,$5)`, raw, v.run, v.step, v.generation, api.EncodeCleanUpTimedOutRequest(v.request))
	if e != nil {
		return nil, e
	}
	return v, tx.Commit(ctx)
}

func (s *Sweeper) evaluate(ctx context.Context, v sweepClaim) (reply, observation []byte, closed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := api.NewCorndogsClient(s.Store.Journal.Transport)
	r, e := client.CleanUpTimedOut(ctx, v.request)
	if e != nil {
		return nil, nil, false, e
	}
	reply = api.EncodeCleanUpTimedOutResponse(r)
	if r.TimedOut < 0 || r.TimedOut > 1 {
		return reply, nil, false, ErrReconcile
	}
	got, e := client.GetGuardedTask(ctx, api.GetGuardedTaskRequest{Queue: v.request.Queue, Uuid: v.task.Task.Uuid})
	if e != nil {
		return reply, nil, false, e
	}
	if got.Task == nil || !got.Task.Guarded || got.Task.Task.Queue != v.request.Queue || got.Task.Task.Uuid != v.task.Task.Uuid {
		return reply, nil, false, ErrReconcile
	}
	observation = api.EncodeGuardedTask(*got.Task)
	closed = got.Task.Terminal || got.Task.Task.Timeout == 0 && got.Task.Task.CurrentState == "reconcile_required"
	return reply, observation, closed, nil
}

func (s *Sweeper) finish(ctx context.Context, v sweepClaim, reply, observation []byte, outcome string) error {
	ctx, cancel := context.WithTimeout(ctx, coordinationStorage)
	defer cancel()
	tx, e := s.Store.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE workflow_timeout_attempts SET reply=$5,observation=$6,outcome=$7,finished_at=clock_timestamp()
		WHERE owner=$1 AND run_id=$2 AND step_id=$3 AND generation=$4 AND finished_at IS NULL`,
		w.EncodeScopeRef(v.owner), v.run, v.step, v.generation, reply, observation, outcome)
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE workflow_timeout_work SET outcome=$5,lease_until='-infinity',
		due_at=CASE WHEN $5='closed' THEN NULL ELSE clock_timestamp()+interval '5 seconds' END
		WHERE owner=$1 AND run_id=$2 AND step_id=$3 AND generation=$4 AND lease_until>clock_timestamp()`,
		w.EncodeScopeRef(v.owner), v.run, v.step, v.generation, outcome)
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	// Keep a late reply as evidence, without changing a newer owner's schedule.
	if tag.RowsAffected() != 1 {
		return ErrStale
	}
	return nil
}

func (s *Sweeper) Tick(ctx context.Context, limit int) (SweepReport, error) {
	var report SweepReport
	if !s.valid() || limit < 1 || limit > 64 {
		return report, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for range limit {
		v, e := s.claim(ctx)
		if e != nil {
			return report, e
		}
		if v == nil {
			break
		}
		reply, observation, closed, workErr := s.evaluate(ctx, *v)
		outcome := "checked"
		if workErr != nil {
			outcome = "error"
		} else if closed {
			outcome = "closed"
		}
		if e = s.finish(ctx, *v, reply, observation, outcome); e != nil {
			return report, e
		}
		report.Checked++
		if closed {
			report.Closed++
		}
		if workErr != nil {
			report.Deferred++
		}
	}
	bound, stop := context.WithTimeout(ctx, coordinationStorage)
	defer stop()
	_, e := s.Store.Journal.Pool.Exec(bound, `UPDATE workflow_sweep_health SET checked_at=clock_timestamp() WHERE singleton`)
	return report, e
}

func (s *Sweeper) Run(ctx context.Context, onReport func(SweepReport, error)) error {
	if !s.valid() {
		return ErrInvalid
	}
	for {
		r, e := s.Tick(ctx, 16)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if onReport != nil {
			onReport(r, e)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
