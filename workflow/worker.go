package workflow

import (
	"bytes"
	"context"
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	"time"
)

// Worker executes one operator-installed definition in one scope. This is a
// constrained local worker, not a remote registration or general discovery API.
// Replicas can inspect the same candidates; Engine owns dispatch exclusion.
type Worker struct {
	Engine     *Engine
	Definition w.WorkflowDefinitionRef
	Actor      *w.ApplicationRef
	cursor     string
}

// Tick advances its cursor even for denied or unrelated runs. A bad run cannot
// permanently block later candidates. The cursor is an optimization, not state.
func (worker *Worker) Tick(ctx context.Context) (int, error) {
	if worker == nil || worker.Engine == nil || worker.Engine.Store == nil || worker.Engine.Store.Journal == nil || worker.Engine.Store.Journal.Pool == nil || worker.Engine.Store.Journal.Transport == nil || !validScope(worker.Definition.Owner) || len(worker.Definition.Digest) != 32 {
		return 0, ErrInvalid
	}
	s := worker.Engine.Store
	var actor []byte
	if worker.Actor != nil {
		actor = w.EncodeApplicationRef(*worker.Actor)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	rows, e := s.Journal.Pool.Query(queryCtx, `SELECT id,body FROM workflow_runs WHERE owner=$1 AND id>$2 AND ($3::bytea IS NULL OR actor=$3) AND EXISTS
		(SELECT 1 FROM workflow_run_steps step WHERE step.owner=workflow_runs.owner AND step.run_id=workflow_runs.id
		AND step.task IS NOT NULL AND NOT step.dispatched AND step.result IS NULL)
		ORDER BY id LIMIT 16`, w.EncodeScopeRef(worker.Definition.Owner), worker.cursor, actor)
	if e != nil {
		cancel()
		return 0, e
	}
	var candidates []w.StartWorkflow
	scanned := 0
	for rows.Next() {
		scanned++
		var id string
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			break
		}
		worker.cursor = id
		var v w.StartWorkflow
		v, e = w.DecodeStartWorkflow(raw)
		if e != nil {
			break
		}
		if bytes.Equal(w.EncodeWorkflowDefinitionRef(v.Definition), w.EncodeWorkflowDefinitionRef(worker.Definition)) {
			candidates = append(candidates, v)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	cancel()
	if e != nil {
		return 0, e
	}
	if rowErr != nil {
		return 0, rowErr
	}
	if scanned == 0 {
		worker.cursor = ""
		return 0, nil
	}
	budget, e := worker.Engine.callBudget()
	if e != nil {
		return 0, e
	}
	count := 0
	for _, v := range candidates {
		bound, done := context.WithTimeout(ctx, budget+40*time.Second)
		// The definition was validated and pinned at installation. Re-read it
		// under current policy before selecting call steps.
		tx, err := s.Journal.Pool.Begin(bound)
		if err != nil {
			done()
			return count, err
		}
		run, err := readRun(bound, tx, v.Definition.Owner, v.RunId)
		if err == nil {
			err = s.authorize(bound, tx, run.Actor, v.Definition.Owner, "execute")
		}
		var d w.WorkflowDefinition
		if err == nil {
			d, err = readDefinition(bound, tx, v.Definition)
		}
		tx.Rollback(bound)
		if err == nil {
			for _, step := range d.Steps {
				if step.Kind != "call" || step.Capability == nil || worker.Engine.Adapters[*step.Capability] == nil {
					continue
				}
				err = worker.Engine.Execute(bound, v.Definition.Owner, v.RunId, step.Id)
				if err == nil {
					count++
				}
				if bound.Err() != nil {
					break
				}
			}
		}
		done()
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
		if err != nil && !errors.Is(err, ErrDenied) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrStale) && !errors.Is(err, ErrReconcile) {
			return count, err
		}
	}
	return count, nil
}

func (worker *Worker) Run(ctx context.Context, report func(int, error)) error {
	for {
		n, e := worker.Tick(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if report != nil {
			report(n, e)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
