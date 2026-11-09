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

func TestWaitUntil(t *testing.T) {
	d := api.Definition{Name: "todo", Start: "do", Steps: []api.Step{
		{Id: "do", Kind: "human", Outcomes: []string{"done", "overdue"}, WaitUntil: ptr("${due_ms}"), TimeoutOutcome: ptr("overdue"), Next: map[string]string{}},
	}}
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	bad := d
	bad.Steps = []api.Step{{Id: "do", Kind: "human", WaitUntil: ptr("tomorrow"), Next: map[string]string{}}}
	if Validate(bad) == nil {
		t.Fatal("wait_until text accepted")
	}
	for _, c := range []struct {
		vars   map[string]string
		waitMs *uint64
		want   *uint64
	}{
		{map[string]string{"due_ms": "5000"}, nil, ptr(uint64(5000))},
		{map[string]string{"due_ms": ""}, nil, nil},
		{map[string]string{}, nil, nil},
		{map[string]string{"due_ms": "5000"}, ptr(uint64(1000)), ptr(uint64(1100))},
		{map[string]string{"due_ms": "500"}, ptr(uint64(1000)), ptr(uint64(500))},
	} {
		d.Steps[0].WaitMs = c.waitMs
		r := &api.Run{Id: "run_t", Vars: c.vars}
		enter(r, &d, "do", 1, 100)
		if (r.WaitUntilMs == nil) != (c.want == nil) || r.WaitUntilMs != nil && *r.WaitUntilMs != *c.want {
			t.Errorf("vars %v wait %v: until %v, want %v", c.vars, c.waitMs, r.WaitUntilMs, c.want)
		}
	}
}
