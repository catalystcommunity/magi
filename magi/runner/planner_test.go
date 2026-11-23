package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// fakePlanner is a planner command: it keeps what it got and prints body.
func fakePlanner(t *testing.T, body string, exit int) ([]string, string) {
	t.Helper()
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	script := filepath.Join(dir, "planner.sh")
	text := fmt.Sprintf("#!/bin/sh\ncat > %[1]s.task\nls -a > %[1]s.tree\ncat <<'PLAN'\n%[2]s\nPLAN\nexit %[3]d\n", seen, body, exit)
	if err := os.WriteFile(script, []byte(text), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{script}, seen
}

func planWith(t *testing.T, body string, exit int, more map[string]string) (string, bool, string, *fakeMagi) {
	t.Helper()
	up, base, _ := evalUpstream(t)
	r, out, fake, err := prepareVars(t, up, map[string]string{"repo": "r", "title": "Say hello", "base": base[:7]})
	if err != nil {
		t.Fatal(err)
	}
	needView(t)
	var seen string
	r.Cfg.Planner.Command, seen = fakePlanner(t, body, exit)
	r.Cfg.Planner.Writable = []string{filepath.Dir(seen)}
	vars := map[string]string{"repo": "r", "title": "Say hello", "instructions": "Write the greeting."}
	for k, v := range out {
		vars[k] = v
	}
	for k, v := range more {
		vars[k] = v
	}
	text, tried := r.plan(context.Background(), magiapi.RunnerAssignment{Run: "run_1", Step: "work", Claim: "c", Vars: vars},
		magiapi.RunnerStep{Run: "run_1", Step: "work", Claim: "c"})
	return text, tried, seen, fake
}

// The planner gets the task and the tree of the work directory with no git
// directory. Its text is the plan.
func TestPlannerWritesThePlan(t *testing.T) {
	text, tried, seen, fake := planWith(t, "Acceptance checks\n1. \"Write the greeting.\" Result: greeting.txt has hello.", 0, nil)
	if !tried || !strings.HasPrefix(text, "Acceptance checks\n1. ") {
		t.Fatalf("plan = %q, tried = %v", text, tried)
	}
	task, _ := os.ReadFile(seen + ".task")
	if !strings.Contains(string(task), "TASK:\n\nSay hello\n\nWrite the greeting.") || !strings.Contains(string(task), "Acceptance checks") {
		t.Fatalf("the planner got: %s", task)
	}
	tree, _ := os.ReadFile(seen + ".tree")
	if len(strings.Fields(string(tree))) == 0 || strings.Contains(string(tree), ".git\n") {
		t.Fatalf("the tree of the planner: %q", tree)
	}
	if !hasNote(fake.notes, "The planner started.") {
		t.Errorf("notes = %q", fake.notes)
	}
}

// A planner that fails gives no plan and does not run again. A later round
// gets the plan of the first round and starts no planner.
func TestPlannerFailureAndLaterRounds(t *testing.T) {
	text, tried, _, fake := planWith(t, "no sign-in", 1, nil)
	if text != "" || !tried {
		t.Fatalf("plan = %q, tried = %v", text, tried)
	}
	found := false
	for _, n := range fake.notes {
		found = found || strings.HasPrefix(n, "The planner failed. The work starts without a plan: ")
	}
	if !found {
		t.Errorf("notes = %q", fake.notes)
	}
	text, tried, seen, _ := planWith(t, "a new plan", 0, map[string]string{"plan": "the first plan", "plan.tried": "true"})
	if text != "the first plan" || tried {
		t.Fatalf("plan = %q, tried = %v", text, tried)
	}
	if _, err := os.Stat(seen + ".task"); err == nil {
		t.Fatal("the planner ran in a later round")
	}
}

func TestParsePlan(t *testing.T) {
	for _, c := range []struct{ body, want, err string }{
		{"  Plan\n1. Do it.\n", "Plan\n1. Do it.", ""},
		{`{"type": "result", "is_error": false, "result": "Plan\n1. Do it."}`, "Plan\n1. Do it.", ""},
		{`{"is_error": true, "result": "Invalid API key"}`, "", "Invalid API key"},
		{" \n", "", "no text"},
	} {
		got, err := parsePlan([]byte(c.body))
		if got != c.want || (err == nil) != (c.err == "") || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("parsePlan(%q) = %q, %v", c.body, got, err)
		}
	}
	long, err := parsePlan([]byte(strings.Repeat("x", plannerMaxChars+50)))
	if err != nil || !strings.HasSuffix(long, "[The plan is cut here.]") || len(long) > plannerMaxChars+40 {
		t.Errorf("a long plan: %d characters, %v", len(long), err)
	}
}
