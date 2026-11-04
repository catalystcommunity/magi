package server

import (
	"strings"
	"testing"

	api "github.com/catalystcommunity/magi/primitives/workflow/api"
)

func testDef() api.Definition {
	return api.Definition{Name: "change", Start: "build", Steps: []api.Step{
		{Id: "build", Kind: "task", Queue: ptr("builds"), Next: map[string]string{"ok": "review"}, MaxAttempts: ptr(uint64(2))},
		{Id: "review", Kind: "human", Outcomes: []string{"approve", "reject"}, Next: map[string]string{"approve": "deploy", "reject": ""}},
		{Id: "deploy", Kind: "task", Queue: ptr("deploys"), Next: map[string]string{"ok": ""}},
	}}
}

func TestValidate(t *testing.T) {
	if err := Validate(testDef()); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*api.Definition){
		"no start":     func(d *api.Definition) { d.Start = "nope" },
		"dup id":       func(d *api.Definition) { d.Steps[1].Id = "build" },
		"bad kind":     func(d *api.Definition) { d.Steps[0].Kind = "robot" },
		"no queue":     func(d *api.Definition) { d.Steps[0].Queue = nil },
		"bad target":   func(d *api.Definition) { d.Steps[0].Next["ok"] = "missing" },
		"no steps":     func(d *api.Definition) { d.Steps = nil },
		"no name":      func(d *api.Definition) { d.Name = "" },
		"empty stepid": func(d *api.Definition) { d.Steps[2].Id = "" },
		"bad var":      func(d *api.Definition) { d.Steps[0].Queue = ptr("q.${bad name}") },
		"open var":     func(d *api.Definition) { d.Steps[0].Queue = ptr("q.${runner") },
	}
	for name, mutate := range bad {
		d := testDef()
		mutate(&d)
		if Validate(d) == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAdvance(t *testing.T) {
	d := testDef()
	newRun := func() *api.Run {
		r := &api.Run{Id: "run_x", Vars: map[string]string{}}
		if task := enter(r, &d, d.Start, 1, 1); task == nil || task.Key != "run_x/0" || task.Queue != "builds" {
			t.Fatalf("enter start: %+v", task)
		}
		return r
	}
	r := newRun()
	if advance(r, &d, "ok", "", 2) != nil || r.Status != RunWaiting || r.CurrentStep != "review" {
		t.Fatalf("after ok: %+v", r)
	}
	if advance(r, &d, "reject", "", 3) != nil || r.Status != RunSucceeded {
		t.Fatalf("mapped to empty: %+v", r)
	}

	r = newRun()
	if advance(r, &d, "unmapped", "", 2) != nil || r.Status != RunSucceeded {
		t.Fatalf("unmapped outcome: %+v", r)
	}
	r = newRun()
	if advance(r, &d, "fail", "boom", 2) != nil || r.Status != RunFailed || *r.Error != "step build failed: boom" {
		t.Fatalf("fail: %+v", r)
	}
	r = newRun()
	task := advance(r, &d, "retry", "", 2)
	if task == nil || task.Key != "run_x/1" || r.History[1].Attempt != 2 {
		t.Fatalf("retry: %+v %+v", task, r)
	}
	if advance(r, &d, "retry", "", 3) != nil || r.Status != RunFailed {
		t.Fatalf("retry past max_attempts: %+v", r)
	}
}

func TestQueueVariables(t *testing.T) {
	d := testDef()
	d.Steps[0].Queue = ptr("runner.${runner}.build")
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	r := &api.Run{Id: "run_v", Vars: map[string]string{"runner": "pi"}}
	if task := enter(r, &d, d.Start, 1, 1); task == nil || task.Queue != "runner.pi.build" {
		t.Fatalf("expanded queue: %+v", task)
	}
	r = &api.Run{Id: "run_w", Vars: map[string]string{}}
	if task := enter(r, &d, d.Start, 1, 1); task != nil || r.Status != RunFailed || !strings.Contains(*r.Error, "needs run variable runner") {
		t.Fatalf("missing variable: %+v %+v", task, r)
	}
}
