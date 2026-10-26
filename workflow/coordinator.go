package workflow

import (
	"context"
	"errors"
	"time"

	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/jackc/pgx/v5"
)

const (
	coordinationLease   = 60 * time.Second
	coordinationWork    = 30 * time.Second
	coordinationStorage = 5 * time.Second
	coordinationPoll    = 2 * time.Second
	coordinationRetry   = 30 * time.Second
)

// Coordinator recovers admitted transitions only. It has no call adapters and
// cannot execute tools or answer human steps. Its lease is not an effect fence;
// the journal and Corndogs revisions still protect every remote transition.
type Coordinator struct{ Store *Store }

type CoordinationReport struct {
	Evaluated, Completed, Deferred int
}

type coordinationClaim struct {
	owner      w.ScopeRef
	run        string
	generation int64
}

func (c *Coordinator) claim(ctx context.Context) (*coordinationClaim, error) {
	ctx, cancel := context.WithTimeout(ctx, coordinationStorage)
	defer cancel()
	var raw []byte
	v := &coordinationClaim{}
	e := c.Store.Journal.Pool.QueryRow(ctx, `WITH candidate AS (
		SELECT owner,run_id FROM workflow_coordination
		WHERE due_at<=clock_timestamp() AND lease_until<=clock_timestamp()
		ORDER BY due_at,owner,run_id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE workflow_coordination AS work SET generation=work.generation+1,
		wake_requested=false,
		lease_until=clock_timestamp()+$1::bigint*interval '1 millisecond'
	FROM candidate WHERE work.owner=candidate.owner AND work.run_id=candidate.run_id
	RETURNING work.owner,work.run_id,work.generation`, coordinationLease.Milliseconds()).Scan(&raw, &v.run, &v.generation)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	v.owner, e = w.DecodeScopeRef(raw)
	return v, e
}

// finish cannot overwrite a newer or expired lease. Only a confirmed complete
// snapshot removes a run from periodic inspection. Failure reasons are bounded
// codes, not raw provider errors or task data.
func (c *Coordinator) finish(ctx context.Context, v coordinationClaim, outcome string) error {
	ctx, cancel := context.WithTimeout(ctx, coordinationStorage)
	defer cancel()
	delay := coordinationPoll
	if outcome != "evaluated" && outcome != "complete" {
		delay = coordinationRetry
	}
	result, e := c.Store.Journal.Pool.Exec(ctx, `UPDATE workflow_coordination SET
		due_at=CASE WHEN $4='complete' THEN NULL WHEN wake_requested THEN clock_timestamp() ELSE clock_timestamp()+$5::bigint*interval '1 millisecond' END,
		wake_requested=false,
		lease_until='-infinity',checked_at=clock_timestamp(),outcome=$4
		WHERE owner=$1 AND run_id=$2 AND generation=$3 AND lease_until>clock_timestamp()`,
		w.EncodeScopeRef(v.owner), v.run, v.generation, outcome, delay.Milliseconds())
	if e == nil && result.RowsAffected() != 1 {
		return ErrStale
	}
	return e
}

func (c *Coordinator) evaluate(ctx context.Context, claim coordinationClaim) (string, error) {
	if e := c.Store.Advance(ctx, claim.owner, claim.run); e != nil {
		return "", e
	}
	tx, e := c.Store.Journal.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	run, e := readRun(ctx, tx, claim.owner, claim.run)
	if e != nil {
		return "", e
	}
	d, e := readDefinition(ctx, tx, run.Request.Definition)
	if e != nil {
		return "", e
	}
	run, e = c.Store.snapshot(ctx, tx, run, d)
	if e != nil {
		return "", e
	}
	if run.Result != nil {
		return "complete", nil
	}
	for _, step := range run.Steps {
		if step.Phase == "outcome_unknown" || step.Phase == "reconcile_required" || step.Phase == "rejected" {
			return "reconcile_required", nil
		}
	}
	return "evaluated", nil
}

// Tick processes at most limit runs, without keeping a database connection
// between admission and remote recovery. It requires a trusted local caller;
// this method is not an unauthenticated API endpoint.
func (c *Coordinator) Tick(ctx context.Context, limit int) (CoordinationReport, error) {
	var report CoordinationReport
	if c == nil || c.Store == nil || c.Store.Journal == nil || c.Store.Journal.Pool == nil || limit < 1 || limit > 64 {
		return report, ErrInvalid
	}
	for range limit {
		if e := ctx.Err(); e != nil {
			return report, e
		}
		claim, e := c.claim(ctx)
		if e != nil || claim == nil {
			return report, e
		}
		bound, cancel := context.WithTimeout(ctx, coordinationWork)
		outcome, workErr := c.evaluate(bound, *claim)
		cancel()
		if ctx.Err() != nil {
			// A process can stop at any point. Leave the lease for another replica.
			return report, ctx.Err()
		}
		if workErr != nil {
			switch {
			case errors.Is(workErr, ErrDenied):
				outcome = "denied"
			case errors.Is(workErr, ErrReconcile), errors.Is(workErr, ErrRejected), errors.Is(workErr, ErrStale):
				outcome = "reconcile_required"
			default:
				outcome = "error"
			}
		}
		if e = c.finish(ctx, *claim, outcome); e != nil {
			return report, e
		}
		report.Evaluated++
		if outcome == "complete" {
			report.Completed++
		} else if outcome != "evaluated" {
			report.Deferred++
		}
	}
	return report, nil
}

// Run uses a bounded batch and waits after failures as well as idle polls.
// onReport must return promptly and must not log private run data.
func (c *Coordinator) Run(ctx context.Context, onReport func(CoordinationReport, error)) error {
	if c == nil || c.Store == nil || c.Store.Journal == nil || c.Store.Journal.Pool == nil {
		return ErrInvalid
	}
	for {
		report, e := c.Tick(ctx, 16)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if onReport != nil {
			onReport(report, e)
		}
		timer := time.NewTimer(coordinationPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
