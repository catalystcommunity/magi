package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
)

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// fakeApprentice writes a Python package that stands in for
// `python -m magi_apprentice run`: it prints the JSON result line that the
// test wants. It also writes the arguments that it got to args.txt in the
// package, so that a test can check the flags of the call.
func fakeApprentice(t *testing.T, result string) (python, path string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	d := t.TempDir()
	pkg := filepath.Join(d, "magi_apprentice")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "import json, os, sys\n" +
		"open(os.path.join(os.path.dirname(os.path.abspath(__file__)), 'args.txt'), 'w').write('\\n'.join(sys.argv[1:]))\n" +
		"print(json.dumps(" + result + "))\n"
	if err := os.WriteFile(filepath.Join(pkg, "__main__.py"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return python, d
}

// workStep runs the work step with a fake apprentice. It returns the outputs
// and the notes that the runner sent.
func workStep(t *testing.T, result string) (map[string]string, []string) {
	t.Helper()
	outputs, notes, _ := workStepVars(t, result, nil)
	return outputs, notes
}

// workStepVars also sets vars of the assignment, and gives the arguments that
// the apprentice got.
func workStepVars(t *testing.T, result string, vars map[string]string) (map[string]string, []string, string) {
	t.Helper()
	res, notes, args := workStepResult(t, result, vars)
	return res.Outputs, notes, args
}

// workStepResult runs the work step and gives the whole result.
func workStepResult(t *testing.T, result string, vars map[string]string) (Result, []string, string) {
	t.Helper()
	python, path := fakeApprentice(t, result)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	fake := &fakeMagi{artifacts: map[string][]byte{}}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, MaxBody: 64 << 20}
	srv.Register("RunnerService", magiapi.RunnerServiceOps(fake))
	ts := httptest.NewServer(rpc.Mux(srv, nil))
	defer ts.Close()
	tok, _ := iss.Issue(authn.Principal{Subject: "runner:test", Kind: authn.KindService, Scopes: []string{"magi:runner"}}, time.Hour)
	state := t.TempDir()
	r := &Runner{Cfg: Config{StateDir: state}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Magi: magiapi.NewRunnerClient(rpc.NewClient(ts.URL, rpc.StaticToken(tok)))}
	r.Cfg.Apprentice.Python, r.Cfg.Apprentice.Path = python, path
	if vars == nil {
		vars = map[string]string{}
	}
	for k, v := range map[string]string{"environment": "e", "workdir": "/work/repo", "instructions": "do it"} {
		vars[k] = v
	}
	res, err := r.work(context.Background(),
		magiapi.RunnerAssignment{Run: "run_w", Step: "work", Vars: vars},
		magiapi.RunnerStep{Run: "run_w", Step: "work", Claim: "c"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(path, "magi_apprentice", "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return res, fake.notes, string(b)
}

// A guard stop or a gone endpoint is the outcome "blocked", not a failed
// step: the work stays, and a person decides. The error goes to the outputs.
func TestWorkStepBlocked(t *testing.T) {
	res, notes, _ := workStepResult(t, `{"status": "blocked_provider", "outcome": "blocked", "summary": "The model call failed.",
		"steps": 1, "tool_calls": 0, "error": "HTTP 429: BudgetLimitError"}`, nil)
	if res.Outcome != "blocked" || res.Outputs["apprentice.blocked"] != "true" ||
		res.Outputs["apprentice.error"] != "HTTP 429: BudgetLimitError" || res.Outputs["apprentice.status"] != "blocked_provider" {
		t.Fatalf("result = %+v", res)
	}
	if !hasNote(notes, "apprentice blocked_provider after 1 steps, 0 tool calls: The model call failed.") {
		t.Errorf("notes = %q", notes)
	}
	// A done round clears the flag of an earlier blocked round.
	res, _, _ = workStepResult(t, `{"status": "finished", "outcome": "done", "summary": "done", "steps": 2, "tool_calls": 2}`,
		map[string]string{"apprentice.blocked": "true"})
	if v, ok := res.Outputs["apprentice.blocked"]; res.Outcome != "done" || !ok || v != "" {
		t.Fatalf("result = %+v", res)
	}
}

// After a blocked round the apprentice gets the comment of the "blocked"
// decision; after a review, the review comment.
func TestWorkStepRoundComment(t *testing.T) {
	vars := map[string]string{"review.comment": "fix the test", "blocked.comment": "use the cache", "apprentice.blocked": "true"}
	_, _, args := workStepResult(t, `{"status": "finished", "outcome": "done", "summary": "ok", "steps": 1, "tool_calls": 1}`, vars)
	if !strings.Contains(args, "--review-comment\nuse the cache") {
		t.Errorf("arguments after a blocked round = %q", args)
	}
	vars = map[string]string{"review.comment": "fix the test", "blocked.comment": "use the cache", "apprentice.blocked": ""}
	_, _, args = workStepResult(t, `{"status": "finished", "outcome": "done", "summary": "ok", "steps": 1, "tool_calls": 1}`, vars)
	if !strings.Contains(args, "--review-comment\nfix the test") {
		t.Errorf("arguments after a review = %q", args)
	}
}

// The work step turns the usage of the apprentice into outputs and a note
// with the cost.
func TestWorkStepCostOutputs(t *testing.T) {
	outputs, notes := workStep(t, `{"status": "finished", "summary": "done", "steps": 3, "tool_calls": 7,
		"usage": {"prompt_tokens": 1200, "completion_tokens": 300, "cached_tokens": 400, "cost": 0.031}}`)
	if outputs["apprentice.cost_usd"] != "0.031" {
		t.Errorf("cost_usd = %q", outputs["apprentice.cost_usd"])
	}
	if outputs["apprentice.prompt_tokens"] != "1200" {
		t.Errorf("prompt_tokens = %q", outputs["apprentice.prompt_tokens"])
	}
	if !hasNote(notes, "apprentice finished after 3 steps, 7 tool calls (cost $0.031): done") {
		t.Errorf("no note with the cost: %q", notes)
	}
}

// A provider that reports no cost leaves the outputs and the note out.
func TestWorkStepWithoutCost(t *testing.T) {
	outputs, notes := workStep(t, `{"status": "finished", "summary": "done", "steps": 2, "tool_calls": 4,
		"usage": {"prompt_tokens": 900, "completion_tokens": 100}}`)
	for _, k := range []string{"apprentice.cost_usd", "apprentice.prompt_tokens"} {
		if v, ok := outputs[k]; ok {
			t.Errorf("%s is set without a cost: %q", k, v)
		}
	}
	for _, n := range notes {
		if strings.Contains(n, "cost") {
			t.Errorf("note tells a cost without one: %q", n)
		}
	}
	if !hasNote(notes, "apprentice finished after 2 steps, 4 tool calls: done") {
		t.Errorf("no note without the cost: %q", notes)
	}
}

// The work step passes the lessons of the run to the apprentice as --lessons,
// and leaves the flag out when the run has none.
func TestWorkStepPassesLessons(t *testing.T) {
	lessons := "- rl-0001 Keep the state of one run: Set the flags at the start."
	_, _, args := workStepVars(t, `{"status": "finished", "summary": "done", "steps": 1, "tool_calls": 1}`,
		map[string]string{"lessons": lessons})
	if !strings.Contains(args, "--lessons\n"+lessons) {
		t.Errorf("arguments = %q, want --lessons with the lessons", args)
	}
	_, _, args = workStepVars(t, `{"status": "finished", "summary": "done", "steps": 1, "tool_calls": 1}`, nil)
	if strings.Contains(args, "--lessons") {
		t.Errorf("arguments = %q: --lessons without a value", args)
	}
}

// The check step sends a failure to the blocked step only when the output
// names an environment error.
func TestEnvironmentLine(t *testing.T) {
	for _, c := range []struct{ output, want string }{
		{"== rust primitives/egress\nerror: failed to get `serde` as a dependency\n  download of config.json failed\n  Connection reset by peer (os error 104)\nFAIL: rust",
			"download of config.json failed"},
		{"== go\n--- FAIL: TestX (0.01s)\n    x_test.go:9: got 2, want 3\nFAIL", ""},
		{"write /work/repo/x: no space left on device", "write /work/repo/x: no space left on device"},
	} {
		if got := environmentLine(c.output); got != c.want {
			t.Errorf("environmentLine(%q) = %q, want %q", c.output, got, c.want)
		}
	}
}
