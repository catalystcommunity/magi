package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// After a loop stop, a failed check goes to a person and not to a new
// work round. A passed check goes on as after a finish.
func TestCheckAfterLoopStop(t *testing.T) {
	res, _, _ := checkWithVars(t, `{"verdict": "approve", "findings": []}`, map[string]string{"apprentice.guard_stop": "true", "check_command": "false"})
	if res.Outcome != "blocked" || res.Outputs["apprentice.blocked"] != "true" {
		t.Fatalf("a failed check after a loop stop: %+v", res)
	}
	res, _, _ = checkWithVars(t, `{"verdict": "approve", "findings": []}`, map[string]string{"check_command": "false"})
	if res.Outcome != "fail-checks" {
		t.Fatalf("a failed check after a finish: %+v", res)
	}
	res, _, _ = checkWithVars(t, `{"verdict": "approve", "findings": []}`, map[string]string{"apprentice.guard_stop": "true"})
	if res.Outcome != "pass" {
		t.Fatalf("a passed check after a loop stop: %+v", res)
	}
}

func checkWithChecker(t *testing.T, result string, rounds string) (Result, string, *fakeMagi) {
	t.Helper()
	more := map[string]string{}
	if rounds != "" {
		more["checker.rounds"] = rounds
	}
	return checkWithVars(t, result, more)
}

func checkWithVars(t *testing.T, result string, more map[string]string) (Result, string, *fakeMagi) {
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
	needView(t)
	r.Cfg.Checker.Command, seen = fakeChecker(t, result)
	// The fake writes what it got next to its script.
	r.Cfg.Checker.Writable = []string{filepath.Dir(seen)}
	vars := map[string]string{"repo": "r", "title": "Say hello", "instructions": "Write the greeting.", "base": base[:7], "check_command": "true"}
	for k, v := range out {
		vars[k] = v
	}
	for k, v := range more {
		vars[k] = v
	}
	a := magiapi.RunnerAssignment{Run: "run_1", Step: "check", Claim: "c", Owner: "user:x@example.local", Vars: vars}
	res, err := r.check(context.Background(), a, magiapi.RunnerStep{Run: "run_1", Step: "check", Claim: "c"})
	if err != nil {
		t.Fatal(err)
	}
	return res, seen, fake
}

const checkerRevise = `{"verdict": "revise", "findings": [
 {"severity": "edge", "file": "greeting.txt", "line": 1, "sentence": "Write the greeting.", "problem": "A name of 5000 letters is cut.", "evidence": "a test"},
 {"severity": "requirement", "file": "greeting.txt", "line": 1, "sentence": "Write the greeting.",
 "problem": "The greeting has no name.", "evidence": "cat greeting.txt"}]}`

// The first findings of the checker go back to the apprentice as the output
// of a failed check. The checker gets the task, the diff, and the tree.
func TestCheckerFindingsGoToTheApprenticeOnce(t *testing.T) {
	res, seen, _ := checkWithChecker(t, checkerRevise, "")
	if res.Outcome != "fail-checks" || res.Outputs["checker.rounds"] != "1" || res.Outputs["check.attempts"] != "0" {
		t.Fatalf("result = %+v", res)
	}
	// Only the finding about a requirement goes to the apprentice.
	if o := res.Outputs["check_output"]; !strings.Contains(o, "1. greeting.txt:1: The greeting has no name.") ||
		!strings.Contains(o, "Task: Write the greeting.") || !strings.Contains(o, "Evidence: cat greeting.txt") || strings.Contains(o, "5000") {
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
	if !hasNote(fake.posts, "The checker still finds these defects after one round of corrections:\n1. greeting.txt:1: The greeting has no name.") ||
		!hasNote(fake.posts, "The checker also names these smaller defects (unusual inputs, weak tests):\n1. greeting.txt:1: A name of 5000 letters is cut.") {
		t.Fatalf("reports = %q", fake.posts)
	}
}

// An approval and a failed checker both let the task go to the review.
func TestCheckerApprovalAndFailure(t *testing.T) {
	res, _, fake := checkWithChecker(t, `{"verdict": "approve", "findings": []}`, "")
	if res.Outcome != "pass" || res.Outputs["checker.verdict"] != "approve" || !hasNote(fake.posts, "The checker found no defect in the requirements of the task.") {
		t.Fatalf("approve: %+v %q", res, fake.posts)
	}
	// A finding that is not about a requirement does not start a work round.
	res, _, fake = checkWithChecker(t, `{"verdict": "revise", "findings": [{"severity": "edge", "file": "greeting.txt", "line": 1,
 "sentence": "Write the greeting.", "problem": "A name of 5000 letters is cut.", "evidence": "a test"}]}`, "")
	if res.Outcome != "pass" || res.Outputs["checker.verdict"] != "approve" || !hasNote(fake.posts, "1. greeting.txt:1: A name of 5000 letters is cut.") {
		t.Fatalf("edge only: %+v %q", res, fake.posts)
	}
	res, _, fake = checkWithChecker(t, `not json`, "")
	if res.Outcome != "pass" || res.Outputs["checker.verdict"] != "" || !hasNote(fake.posts, "The checker failed: the result of the checker is not JSON") {
		t.Fatalf("failure: %+v %q", res, fake.posts)
	}
}

// needView skips a test on a Linux host where bubblewrap cannot make a
// view (not installed, or no user namespaces).
func needView(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	if err := exec.Command("bwrap", "--unshare-all", "--ro-bind", "/", "/", "true").Run(); err != nil {
		t.Skipf("bubblewrap cannot make a view on this host: %v", err)
	}
}

// The checker command does not see the home directory of the user or the
// variables of the runner. It sees its work directory and the paths of the
// configuration.
func TestCheckerViewHidesTheHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the view is for Linux")
	}
	needView(t)
	home, state, tools := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MAGI_TEST_RUNNER_SECRET", "runner-secret")
	for path, body := range map[string]string{
		filepath.Join(home, ".ssh", "id"):          "private key",
		filepath.Join(home, ".agent", "login"):     "sign-in",
		filepath.Join(home, "bin", "real-checker"): "#!/bin/sh\n. \"$1\"\n",
		filepath.Join(tools, "version"):            "tool 1",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The command is a link in a directory that the view does not have.
	link := filepath.Join(home, "link-checker")
	if err := os.Symlink(filepath.Join(home, "bin", "real-checker"), link); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "checker-1")
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "probe.sh")
	probe := `{
echo "home: $(ls -A "$HOME" | tr '\n' ' ')"
cat "$HOME/.ssh/id" 2>/dev/null && echo "READ THE KEY"
echo "login: $(cat "$HOME/.agent/login")"
echo "tool: $(cat ` + tools + `/version)"
echo "secret: [$MAGI_TEST_RUNNER_SECRET] flag: [$CHECKER_FLAG] home: [$HOME]"
echo "token: [$CHECKER_TOKEN]"
touch ` + tools + `/new 2>/dev/null && echo "WROTE THE TOOLS"
touch "$HOME/.agent/refreshed" && echo "wrote the sign-in directory"
touch /usr/magi-test 2>/dev/null && echo "WROTE /usr"
} > ` + dir + `/out 2>&1
exit 0
`
	if err := os.WriteFile(script, []byte(probe), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{}
	r.Cfg.Checker.Writable = []string{filepath.Join(home, ".agent")}
	r.Cfg.Checker.ReadOnly = []string{tools}
	r.Cfg.Checker.Env = map[string]string{"CHECKER_FLAG": "on"}
	if err := os.WriteFile(filepath.Join(state, "token"), []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Cfg.Checker.EnvFiles = map[string]string{"CHECKER_TOKEN": filepath.Join(state, "token")}
	argv, env, err := r.checkerCommand(dir, []string{link, script})
	if err != nil {
		t.Fatal(err)
	}
	// The value of a file is not in the argument list, which each user of
	// the host can read.
	if strings.Contains(strings.Join(argv, " "), "file-token") {
		t.Errorf("the token is in the arguments: %q", argv)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = filepath.Join(dir, "tree"), env
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "out"))
	got := string(b)
	for _, want := range []string{"login: sign-in", "tool: tool 1", "secret: [] flag: [on] home: [" + home + "]", "wrote the sign-in directory", "token: [file-token]"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"READ THE KEY", "private key", "WROTE", ".ssh", "link-checker"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q in:\n%s", bad, got)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".agent", "refreshed")); err != nil {
		t.Errorf("the writable path did not keep the file: %v", err)
	}
	r.Cfg.Checker.EnvFiles = map[string]string{"CHECKER_TOKEN": filepath.Join(state, "none")}
	if _, _, err := r.checkerCommand(dir, []string{link}); err == nil {
		t.Error("a missing token file was accepted")
	}
	r.Cfg.Checker.EnvFiles = nil
	// A relative path of the configuration is an error.
	r.Cfg.Checker.ReadOnly = []string{"tools"}
	if _, _, err := r.checkerCommand(dir, []string{link}); err == nil {
		t.Error("a relative path was accepted")
	}
}

// A checker with no result file prints the result. Claude Code prints an
// object with the result in "structured_output".
func TestParseCheckerResult(t *testing.T) {
	res, err := parseCheckerResult([]byte(`{"type": "result", "is_error": false, "result": "done",
		"structured_output": {"verdict": "revise", "findings": [{"severity": "requirement", "file": "a.go", "line": 3,
		"sentence": "s", "problem": "p", "evidence": "e"}]}}`))
	if err != nil || res.Verdict != "revise" || len(res.Findings) != 1 || res.Findings[0].File != "a.go" {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = parseCheckerResult([]byte(`{"verdict": "approve", "findings": []}`))
	if err != nil || res.Verdict != "approve" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := parseCheckerResult([]byte(`{"is_error": true, "result": "Credit balance is too low"}`)); err == nil || !strings.Contains(err.Error(), "Credit balance") {
		t.Fatalf("an error result: %v", err)
	}
	if _, err := parseCheckerResult([]byte("I checked the change.")); err == nil {
		t.Fatal("text is not a result")
	}
}
