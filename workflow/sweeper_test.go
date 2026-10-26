package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

func TestSweeperRequiresStore(t *testing.T) {
	for _, s := range []*Sweeper{nil, {}, {Store: &Store{}}, {Store: &Store{Journal: &Journal{}}}} {
		if _, e := s.Tick(context.Background(), 1); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if e := s.Check(context.Background()); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if e := s.Run(context.Background(), nil); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
}
func TestCallClaimWindow(t *testing.T) {
	now := time.Now()
	task := api.GuardedTask{Guarded: true, Revision: 2, Task: api.Task{CurrentState: "running", AutoTargetState: "reconcile_required", Timeout: 30, UpdateTime: now.UnixNano()}}
	if e := callClaimWindow(task, now); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*api.GuardedTask){
		"expired":             func(v *api.GuardedTask) { v.Task.UpdateTime = now.Add(-31 * time.Second).UnixNano() },
		"insufficient-budget": func(v *api.GuardedTask) { v.Task.UpdateTime = now.Add(-17 * time.Second).UnixNano() },
		"future-clock":        func(v *api.GuardedTask) { v.Task.UpdateTime = now.Add(6 * time.Second).UnixNano() },
		"terminal":            func(v *api.GuardedTask) { v.Terminal = true },
		"unguarded":           func(v *api.GuardedTask) { v.Guarded = false },
		"wrong-state":         func(v *api.GuardedTask) { v.Task.CurrentState = "ready" },
		"unsafe-target":       func(v *api.GuardedTask) { v.Task.AutoTargetState = "ready" },
		"wrong-timeout":       func(v *api.GuardedTask) { v.Task.Timeout = 300 },
	} {
		t.Run(name, func(t *testing.T) {
			v := task
			change(&v)
			if e := callClaimWindow(v, now); !errors.Is(e, ErrStale) {
				t.Fatal(e)
			}
		})
	}
}
