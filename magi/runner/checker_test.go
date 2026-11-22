package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// fakeChecker is a checker command: a shell script that writes the result
// and keeps what it got (the task on standard input, the diff, the tree).
func fakeChecker(t *testing.T, result string) (command []string, seen string) {
	t.Helper()
	dir := t.TempDir()
	seen = filepath.Join(dir, "seen")
	script := filepath.Join(dir, "checker.sh")
	body := "#!/bin/sh\ncat > " + seen + ".task\ncp \"$2\" " + seen + ".diff\nls > " + seen + ".tree\ncat \"$3\" > " + seen + ".schema\n" +
		"cat > \"$1\" <<'JSON'\n" + result + "\nJSON\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{script, "{out}", "{diff}", "{schema}"}, seen
}

func checkWithChecker(t *testing.T, result string, rounds string) (Result, string, *fakeMagi) {
	t.Helper()
	up, base, _ := evalUpstream(t)
	r, out, fake, err := prepareVars(t, up, map[string]string{"repo": "r", "title": "Say hello", "base": base[:7]})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.writeFile(context.Background(), "run_1", out["workdir"]+"/greeting.txt", []byte("hello there\n")); err != nil {
		t.Fatal(err)
	}
	var seen string
	r.Cfg.Checker.Command, seen = fakeChecker(t, result)
	vars := map[string]string{"repo": "r", "title": "Say hello", "instructions": "Write the greeting.", "base": base[:7], "check_command": "true"}
	for k, v := range out {
		vars[k] = v
	}
	if rounds != "" {
		vars["checker.rounds"] = rounds
	}
	a := magiapi.RunnerAssignment{Run: "run_1", Step: "check", Claim: "c", Owner: "user:x@example.local", Vars: vars}
	res, err := r.check(context.Background(), a, magiapi.RunnerStep{Run: "run_1", Step: "check", Claim: "c"})
	if err != nil {
		t.Fatal(err)
	}
	return res, seen, fake
}

const checkerRevise = `{"verdict": "revise", "findings": [{"file": "greeting.txt", "line": 1, "sentence": "Write the greeting.",
 "problem": "The greeting has no name.", "evidence": "cat greeting.txt"}]}`

// The first findings of the checker go back to the apprentice as the output
// of a failed check. The checker gets the task, the diff, and the tree.
func TestCheckerFindingsGoToTheApprenticeOnce(t *testing.T) {
	res, seen, _ := checkWithChecker(t, checkerRevise, "")
	if res.Outcome != "fail-checks" || res.Outputs["checker.rounds"] != "1" || res.Outputs["check.attempts"] != "0" {
		t.Fatalf("result = %+v", res)
	}
	if o := res.Outputs["check_output"]; !strings.Contains(o, "1. greeting.txt:1: The greeting has no name.") ||
		!strings.Contains(o, "Task: Write the greeting.") || !strings.Contains(o, "Evidence: cat greeting.txt") {
		t.Fatalf("check_output = %q", o)
	}
	read := func(suffix string) string {
		b, err := os.ReadFile(seen + suffix)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if task := read(".task"); !strings.Contains(task, "TASK:\n\nSay hello\n\nWrite the greeting.") {
		t.Fatalf("task = %q", task)
	}
	if d := read(".diff"); !strings.Contains(d, "+hello there") {
		t.Fatalf("diff = %q", d)
	}
	if tree := read(".tree"); !strings.Contains(tree, "greeting.txt") || strings.Contains(tree, "CHANGE.diff") {
		t.Fatalf("tree = %q", tree)
	}
	if !strings.Contains(read(".schema"), `"verdict"`) {
		t.Fatal("no schema")
	}
}

// After one round of corrections the findings are a hint for the person:
// the task goes to the review.
func TestCheckerFindingsAreAHintAfterOneRound(t *testing.T) {
	res, _, fake := checkWithChecker(t, checkerRevise, "1")
	if res.Outcome != "pass" || res.Outputs["checker.verdict"] != "revise" || res.Outputs["check_output"] != "" {
		t.Fatalf("result = %+v", res)
	}
	if !hasNote(fake.posts, "The checker still finds these defects after one round of corrections:\n1. greeting.txt:1: The greeting has no name.") {
		t.Fatalf("reports = %q", fake.posts)
	}
}

// An approval and a failed checker both let the task go to the review.
func TestCheckerApprovalAndFailure(t *testing.T) {
	res, _, fake := checkWithChecker(t, `{"verdict": "approve", "findings": []}`, "")
	if res.Outcome != "pass" || res.Outputs["checker.verdict"] != "approve" || !hasNote(fake.posts, "The checker found no defect.") {
		t.Fatalf("approve: %+v %q", res, fake.posts)
	}
	res, _, fake = checkWithChecker(t, `not json`, "")
	if res.Outcome != "pass" || res.Outputs["checker.verdict"] != "" || !hasNote(fake.posts, "The checker failed: the result of the checker is not JSON") {
		t.Fatalf("failure: %+v %q", res, fake.posts)
	}
}
