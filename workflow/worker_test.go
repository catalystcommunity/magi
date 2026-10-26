package workflow

import (
	"context"
	"errors"
	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"testing"
	"time"
)

func TestCallBudgets(t *testing.T) {
	for _, budget := range []time.Duration{-time.Second, time.Millisecond, 121 * time.Second} {
		if _, e := (&Engine{CallTimeout: budget}).callBudget(); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid budget accepted", budget, e)
		}
	}
	for _, budget := range []time.Duration{time.Second, 8 * time.Second, 120 * time.Second} {
		t.Run(budget.String(), func(t *testing.T) {
			e := &Engine{CallTimeout: budget}
			got, err := e.callBudget()
			if err != nil || got != budget {
				t.Fatal(got, err)
			}
			now := time.Now()
			task := api.GuardedTask{Guarded: true, Task: api.Task{CurrentState: "running", AutoTargetState: "reconcile_required", Timeout: int64(budget/time.Second) + 22, UpdateTime: now.UnixNano()}}
			if err = claimWindow(task, now, budget); err != nil {
				t.Fatal(err)
			}
			if err = claimWindow(task, now.Add(17*time.Second), budget); !errors.Is(err, ErrStale) {
				t.Fatal("late claim admitted", err)
			}
			task.Task.Timeout++
			if err = claimWindow(task, now, budget); !errors.Is(err, ErrStale) {
				t.Fatal("configuration changed claim", err)
			}
		})
	}
	if budget, e := (&Engine{}).callBudget(); e != nil || budget != 8*time.Second {
		t.Fatal(budget, e)
	}
}
func TestMissingWorkerStore(t *testing.T) {
	for _, worker := range []*Worker{nil, {}, {Engine: &Engine{}}, {Engine: &Engine{Store: &Store{}}}} {
		if _, e := worker.Tick(context.Background()); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
}
