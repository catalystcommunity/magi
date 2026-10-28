package server

import (
	"context"
	"time"

	api "github.com/catalystcommunity/magi/primitives/workflow/api"
	"github.com/catalystcommunity/magi/primitives/workflow/store"
)

// Sweep intervals. Every replica runs the sweep; row locks keep the work
// single. Tests change them.
var (
	sweepEvery = 500 * time.Millisecond
	// settleMs lets the request that wrote a task do its own queue call first.
	settleMs int64 = 1_000
	// staleMs is the age of a queued task before the sweep checks the queue
	// for a claim that was never recorded.
	staleMs int64 = 30_000
)

// flush sends the queue work of tasks that a transition wrote. Failures stay
// in the table for the sweep.
func (s *Service) flush(ctx context.Context, tasks []store.Task) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	for _, t := range tasks {
		s.work(ctx, t.Key)
	}
}

// work moves one task row toward the queue state that it records.
func (s *Service) work(ctx context.Context, key string) {
	err := s.Store.WithTask(ctx, key, func(t store.Task) (store.Task, error) {
		switch t.State {
		case store.TaskSubmit:
			tk, err := s.Queue.Submit(ctx, t.Queue, t.Key)
			if err != nil {
				return t, err
			}
			t.State, t.UUID, t.Revision = store.TaskQueued, tk.UUID, tk.Revision
		case store.TaskFinish:
			if t.UUID == "" { // the submit outcome is unknown; a keyed submit replays it
				tk, err := s.Queue.Submit(ctx, t.Queue, t.Key)
				if err != nil {
					return t, err
				}
				t.UUID, t.Revision = tk.UUID, tk.Revision
			}
			if err := s.Queue.Finish(ctx, t.Queue, Ticket{UUID: t.UUID, Revision: t.Revision}); err != nil {
				return t, err
			}
			t.State = store.TaskDone
		case store.TaskQueued:
			// A claim that reached the queue but not the database leaves the
			// task held. Return it to pending.
			if tk, ok, err := s.Queue.Release(ctx, t.Queue, Ticket{UUID: t.UUID, Revision: t.Revision}); err != nil {
				return t, err
			} else if ok {
				t.Revision = tk.Revision
				if s.Log != nil {
					s.Log.Warn("released unrecorded claim", "key", t.Key)
				}
			}
		}
		return t, nil
	})
	if err != nil && s.Log != nil {
		s.Log.Warn("queue work", "key", key, "err", err)
	}
}

// expire ends a claim whose lease is over and offers the step again.
func (s *Service) expire(ctx context.Context, key, runID string) {
	var tasks []store.Task
	_, _, err := s.Store.UpdateRun(ctx, runID, func(tx *store.RunTx, r *api.Run) error {
		t, err := tx.Task(key, "")
		if err != nil || t == nil || t.State != store.TaskClaimed || t.LeaseUntil >= store.Now() {
			return errorOr(err, store.ErrSkip)
		}
		def, err := s.definition(ctx, r.Definition, r.Revision)
		if err != nil {
			return err
		}
		t.State = store.TaskFinish
		tasks = append(tasks, *t)
		ts := now()
		endStep(&r.History[t.Index], StepFailed, "expired", ts)
		if !finished(r.Status) {
			if nt := retry(r, def, "lease expired", ts); nt != nil {
				tasks = append(tasks, *nt)
			}
		}
		for _, t := range tasks {
			tx.Put(t)
		}
		return nil
	})
	if err != nil && storeErr(err, "") != nil && s.Log != nil {
		s.Log.Warn("expire claim", "key", key, "err", err)
	}
	s.flush(ctx, tasks)
}

func errorOr(err, def error) error {
	if err != nil {
		return err
	}
	return def
}

// Sweep runs until ctx ends. It retries queue work, expires leases, and
// recovers lost claims.
func (s *Service) Sweep(ctx context.Context) {
	tick := time.NewTicker(sweepEvery)
	defer tick.Stop()
	for {
		s.SweepOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// SweepOnce does one pass of the sweep.
func (s *Service) SweepOnce(ctx context.Context) {
	due, err := s.Store.Due(ctx, settleMs, staleMs, 64)
	if err != nil {
		if s.Log != nil && ctx.Err() == nil {
			s.Log.Warn("sweep", "err", err)
		}
		return
	}
	for _, t := range due {
		if t.State == store.TaskClaimed {
			s.expire(ctx, t.Key, t.Run)
		} else {
			s.work(ctx, t.Key)
		}
	}
}
