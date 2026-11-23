package runner

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/compose"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
	sourceapi "github.com/catalystcommunity/magi/primitives/source/api"
)

const maxCheckAttempts = 3

// The runner never opens files in a work directory from the host side: the
// apprentice controls that directory and could put a symbolic link there. All
// file access goes through envd, inside the sandbox.

func (r *Runner) exec(ctx context.Context, env, cwd, script string, timeoutMs uint64) (executionapi.ExecResult, error) {
	c, err := r.envd(env)
	if err != nil {
		return executionapi.ExecResult{}, err
	}
	return c.Exec(ctx, executionapi.ExecRequest{Environment: env, Argv: []string{"bash", "-c", script}, Cwd: ptr(cwd),
		TimeoutMs: ptr(timeoutMs), MaxOutputBytes: ptr(uint64(256 << 10))})
}

func (r *Runner) writeFile(ctx context.Context, env, path string, body []byte) error {
	c, err := r.envd(env)
	if err != nil {
		return err
	}
	_, err = c.WriteFile(ctx, executionapi.WriteFileRequest{Environment: env, Path: path, Body: body})
	return err
}

func (r *Runner) readFile(ctx context.Context, env, path string, max uint64) ([]byte, error) {
	c, err := r.envd(env)
	if err != nil {
		return nil, err
	}
	f, err := c.ReadFile(ctx, executionapi.ReadFileRequest{Environment: env, Path: path, MaxBytes: ptr(max)})
	if err != nil {
		return nil, err
	}
	if f.Truncated {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, max)
	}
	return f.Body, nil
}

func (r *Runner) repo(a magiapi.RunnerAssignment) (string, Repo, error) {
	name := a.Vars["repo"]
	repo, ok := r.Cfg.Repos[name]
	if !ok {
		return name, repo, fmt.Errorf("runner %s has no repository %q", r.Cfg.Name, name)
	}
	return name, repo, nil
}

func (r *Runner) prepare(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	name, repo, err := r.repo(a)
	if err != nil {
		return Result{}, err
	}
	sync, err := r.Source.Sync(sourceCtx(ctx), sourceapi.SyncRequest{Repo: name})
	if err != nil {
		return Result{}, fmt.Errorf("sync %s: %w", name, err)
	}
	r.note(ctx, st, "Upstream sync: "+sync.Status)
	if sync.Status == "diverged" || sync.Status == "ahead" {
		return Result{}, fmt.Errorf("the mirror of %s is %s from its upstream; fix it by hand", name, sync.Status)
	}
	env := a.Run
	if r.Boxes.Exists(env) { // an earlier attempt
		r.removeBox(env)
	}
	proxy, err := r.egressProxy(ctx, st)
	if err != nil {
		return Result{}, err
	}
	b := make([]byte, 24)
	rand.Read(b)
	if err := r.startBox(env, hex.EncodeToString(b), proxy); err != nil {
		return Result{}, err
	}
	if _, err := r.Boxes.Wait(env, r.Boxes.StartTimeout()); err != nil {
		return Result{}, err
	}
	if r.Boxes.Cfg.VM != nil && r.Boxes.Cfg.VM.Template != "" {
		if proxy != nil {
			r.note(ctx, st, "The environment is a macOS VM with no network card; tools reach the egress proxy at "+sandbox.ProxyAddr+".")
		} else {
			r.note(ctx, st, "The environment is a macOS VM with no network (no egress proxy).")
		}
	} else if r.Boxes.Cfg.VM != nil && proxy != nil {
		r.note(ctx, st, "The environment is a VM with no network card; each TCP connection goes through the egress proxy.")
	} else if r.Boxes.Cfg.VM != nil {
		r.note(ctx, st, "The environment is a VM with no network (no egress proxy).")
	} else if proxy != nil {
		r.note(ctx, st, "The environment has no network; its traffic goes through the egress proxy.")
	} else {
		r.note(ctx, st, "The environment uses the network of the runner machine (no egress proxy).")
	}
	bundle, err := r.Source.Bundle(sourceCtx(ctx), sourceapi.BundleRequest{Repo: name, Refs: []string{repo.DefaultBranch}})
	if err != nil {
		return Result{}, err
	}
	w := r.Boxes.Work(env)
	if err := r.writeFile(ctx, env, w+"/repo.bundle", bundle.Bundle); err != nil {
		return Result{}, err
	}
	branch := "task/" + a.Run
	// A base starts the branch at an older commit of the default branch; the
	// bundle of the default branch has its history, so the clone stays the
	// same.
	checkout := ""
	if base := a.Vars["base"]; base != "" {
		if !compose.IsCommit(base) {
			return Result{}, fmt.Errorf("base %q is not a commit (7 to 40 hex characters)", base)
		}
		checkout = " " + base
	}
	res, err := r.exec(ctx, env, w, fmt.Sprintf(`set -e
git clone -q -b %[1]s %[5]s/repo.bundle %[5]s/repo
cd %[5]s/repo
git config user.name %[3]s
git config user.email %[4]s
git checkout -q -b %[2]s%[6]s
git rev-parse HEAD`, repo.DefaultBranch, branch, shellQuote(r.Cfg.Author.Name), shellQuote(r.Cfg.Author.Email), shellQuote(w), checkout), 120000)
	if err != nil {
		return Result{}, err
	}
	if res.ExitCode != 0 {
		// A base that the bundle does not have fails here: git names it.
		return Result{}, fmt.Errorf("clone failed: %s", tail(string(res.Stderr), 2000))
	}
	r.note(ctx, st, fmt.Sprintf("Environment %s is ready on runner %s, branch %s.", env, r.Cfg.Name, branch))
	r.post(ctx, st, fmt.Sprintf("I started work on %q on runner %s. The apprentice works on branch %s.", a.Vars["title"], r.Cfg.Name, branch))
	return Result{Outcome: "done", Outputs: map[string]string{
		"environment": env, "workdir": w + "/repo", "branch": branch, "base_commit": strings.TrimSpace(string(res.Stdout)),
		"default_branch": repo.DefaultBranch,
	}}, nil
}

// work runs the apprentice on this machine. Its tools use the sandbox
// through the loopback execution API; its model calls go through Magi.
func (r *Runner) work(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	why, err := r.preflight(ctx, a.Vars["environment"], a.Vars["workdir"])
	if err != nil {
		return Result{}, err
	}
	if why != "" {
		r.report(ctx, st, "normal", "Task blocked", "The work did not start: the environment check failed. "+why+". The work is kept. Decide on the task: resume tries again.")
		return Result{Outcome: "blocked", Outputs: map[string]string{"apprentice.status": "preflight", "apprentice.blocked": "true",
			"apprentice.error": why, "apprentice.summary": "The environment check failed before the work started."}}, nil
	}
	dir, err := os.MkdirTemp(r.Cfg.StateDir, "apprentice-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	instr := filepath.Join(dir, "instructions")
	plan, planTried := r.plan(ctx, a, st)
	instructions := a.Vars["instructions"]
	if plan != "" {
		instructions += "\n\n" + planNote + plan
	}
	if err := os.WriteFile(instr, []byte(instructions), 0o600); err != nil {
		return Result{}, err
	}
	transcript := filepath.Join(dir, "transcript.jsonl")
	args := []string{"-m", "magi_apprentice", "run", "--environment", a.Vars["environment"], "--workdir", a.Vars["workdir"],
		"--instructions", "@" + instr, "--transcript", transcript, "--no-upload"}
	for flag, v := range map[string]string{"--check-command": a.Vars["check_command"], "--check-output": a.Vars["check_output"],
		"--title": a.Vars["title"], "--review-comment": roundComment(a.Vars), "--lessons": a.Vars["lessons"], "--model": a.Vars["model"]} {
		if v != "" {
			args = append(args, flag, v)
		}
	}
	if r.Cfg.Apprentice.MaxSteps > 0 {
		args = append(args, "--max-steps", strconv.Itoa(r.Cfg.Apprentice.MaxSteps))
	}
	if r.Cfg.Apprentice.SupervisorModel != "" {
		args = append(args, "--supervisor-model", r.Cfg.Apprentice.SupervisorModel)
	}
	if r.Cfg.Apprentice.WorkReview && r.Cfg.Apprentice.ReviewModel != "" {
		args = append(args, "--review-model", r.Cfg.Apprentice.ReviewModel)
		if r.Cfg.Apprentice.Tester {
			args = append(args, "--tester")
		}
	}
	cmd := exec.CommandContext(ctx, r.Cfg.Apprentice.Python, args...)
	cmd.Env = append(r.apprenticeEnv(dir),
		// The coding model behind /runner/v1 is a large provider model
		// (glm-5.3). The apprentice defaults fit a small local model: at
		// 24000 tokens it removed what it had read and read the same files
		// again until the step limit (world eval, 2026-11-09). A reasoning
		// model also needs room to think before it writes.
		"MAGI_APPRENTICE_CONTEXT_TOKENS=100000", "MAGI_MODEL_MAX_TOKENS=16384",
		"MAGI_APPRENTICE_TOOL_OUTPUT_CHARS=16000", "MAGI_APPRENTICE_MAX_SECONDS=3600",
		// GLM makes about one tool call for each model call: a change of
		// five files with tests used all of 60 steps (2026-11-14).
		"MAGI_APPRENTICE_MAX_STEPS=150",
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	var stdout strings.Builder
	cmd.Stdout = &stdout
	r.post(ctx, st, "The apprentice started.")
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	// The apprentice logs "progress: ..." lines; they become step notes.
	var last []string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "progress: "); i >= 0 {
			r.note(ctx, st, strings.TrimRight(strings.TrimSpace(line[i+10:]), `"'}`))
		}
		last = append(last, line)
		if len(last) > 40 {
			last = last[1:]
		}
	}
	waitErr := cmd.Wait()
	var out struct {
		Status string `json:"status"`
		// Outcome is the workflow outcome of the status (guards.outcome_of in
		// the apprentice): done, blocked, fail, or retry.
		Outcome     string `json:"outcome"`
		Summary     string `json:"summary"`
		Steps       int    `json:"steps"`
		ToolCalls   int    `json:"tool_calls"`
		CheckPassed *bool  `json:"check_passed"`
		Error       string `json:"error"`
		Usage       struct {
			PromptTokens int     `json:"prompt_tokens"`
			Cost         float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		return Result{}, fmt.Errorf("apprentice failed (%v): %s", waitErr, tail(strings.Join(last, "\n"), 3000))
	}
	outputs := map[string]string{"apprentice.summary": out.Summary, "apprentice.steps": strconv.Itoa(out.Steps),
		"apprentice.status": out.Status, "apprentice.tool_calls": strconv.Itoa(out.ToolCalls)}
	if planTried {
		// The later rounds get the same plan and start no planner.
		outputs["plan"], outputs["plan.tried"] = plan, "true"
	}
	if out.CheckPassed != nil {
		outputs["apprentice.check_passed"] = strconv.FormatBool(*out.CheckPassed)
	}
	// A provider that reports no cost leaves the outputs and the note out.
	cost := ""
	if out.Usage.Cost > 0 {
		cost = fmt.Sprintf(" (cost $%.3f)", out.Usage.Cost)
		outputs["apprentice.cost_usd"] = fmt.Sprintf("%.3f", out.Usage.Cost)
		outputs["apprentice.prompt_tokens"] = strconv.Itoa(out.Usage.PromptTokens)
	}
	if b, err := os.ReadFile(transcript); err == nil {
		if d, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "application/x-ndjson", Body: b,
			Labels: map[string]string{"kind": "apprentice-transcript"}}); err == nil {
			outputs["apprentice.transcript"] = d.Digest
		} else {
			outputs["apprentice.transcript_error"] = tail(err.Error(), 500)
		}
	}
	r.note(ctx, st, fmt.Sprintf("apprentice %s after %d steps, %d tool calls%s: %s",
		out.Status, out.Steps, out.ToolCalls, cost, out.Summary))
	// Each round sets it, so that a later round does not see an old value.
	outputs["apprentice.blocked"], outputs["apprentice.loop_stop"] = "", ""
	if out.Status == "loop" && r.changed(ctx, a) {
		// The model made a change and then sent the same call again and
		// again. The change can be complete, so the check decides. A change
		// that fails the check goes to a person (check), not to a new round.
		outputs["apprentice.loop_stop"] = "true"
		r.note(ctx, st, "The apprentice stopped in a loop, and the environment has a change. The checks decide about the change.")
		return Result{Outcome: "done", Outputs: outputs}, nil
	}
	switch out.Outcome {
	case "fail", "":
		if out.Status == "error" || out.Outcome == "fail" {
			return Result{}, fmt.Errorf("apprentice error: %s", firstNonEmpty(out.Error, out.Summary))
		}
	case "blocked":
		// The endpoint or the money is gone, or a guard stopped a run that
		// went wrong. The work stays in the environment, and a person
		// decides: resume, check what is there, or reject.
		outputs["apprentice.blocked"] = "true"
		if out.Error != "" {
			outputs["apprentice.error"] = tail(out.Error, 2000)
		}
		// Without this report, a blocked task waits and nobody knows.
		r.report(ctx, st, "normal", "Task blocked", fmt.Sprintf("The apprentice stopped (%s) after %d steps. The work stays in the environment. Resume it, run the checks on it, or reject it.", out.Status, out.Steps))
		return Result{Outcome: "blocked", Outputs: outputs}, nil
	case "retry":
		return Result{Outcome: "retry", Outputs: outputs}, nil
	}
	return Result{Outcome: "done", Outputs: outputs}, nil
}

// environmentPatterns are errors that the environment causes. The apprentice
// has the same list (guards.ENVIRONMENT_PATTERNS) for its tool results.
var environmentPatterns = regexp.MustCompile(`(?i)connection reset by peer|connection refused|could not resolve host|` +
	`temporary failure in name resolution|no such host|CONNECT tunnel failed|unable to update registry|download of \S+ failed|` +
	`no space left on device|virtiofsd did not start|read-only file system`)

// environmentLine is the first line of output with an environment error, or "".
// apprenticeEnv is the environment of a magi_apprentice command: the
// execution service and the model route of this runner, nothing else of
// the runner's own environment.
func (r *Runner) apprenticeEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "PYTHONPATH=" + r.Cfg.Apprentice.Path,
		"MAGI_EXECUTION_URL=" + r.execURL, "MAGI_TOKEN_FILE=" + r.execTokenFile,
		"MAGI_MODEL_BASE_URL=" + strings.TrimRight(r.Cfg.Server, "/") + "/runner/v1",
		"MAGI_MODEL_API_KEY_FILE=" + r.Cfg.TokenFile, "MAGI_MODEL_NAME=coder",
	}
}

// modelReviewTimeout: one sample of each lens takes about 3 minutes on a
// local model on one GPU (evals/review, 2026-11-17).
const modelReviewTimeout = 20 * time.Minute

// modelReview runs the review of a model on a change whose checks passed
// (primitives/apprentice prereview.py). The result is a hint in the review
// request of a person, so a failed review gives a text that says so and
// the review of the person goes on.
func (r *Runner) modelReview(ctx context.Context, a magiapi.RunnerAssignment) (verdict, summary string) {
	fail := func(why string) (string, string) { return "", "The model review failed: " + why }
	dir, err := os.MkdirTemp(r.Cfg.StateDir, "review-")
	if err != nil {
		return fail(err.Error())
	}
	defer os.RemoveAll(dir)
	// The reviewer checks each item of an earlier review comment too.
	task := strings.TrimSpace(a.Vars["title"] + "\n\n" + a.Vars["instructions"])
	if c := roundComment(a.Vars); c != "" {
		task += "\n\nThe review comment of the earlier round (each item must be done):\n" + c
	}
	instr := filepath.Join(dir, "instructions")
	if err := os.WriteFile(instr, []byte(task), 0o600); err != nil {
		return fail(err.Error())
	}
	args := []string{"-m", "magi_apprentice", "review", "--environment", a.Vars["environment"], "--workdir", a.Vars["workdir"],
		"--base", a.Vars["base_commit"], "--instructions", "@" + instr, "--model", r.Cfg.Apprentice.ReviewModel}
	if l := a.Vars["lessons"]; l != "" {
		args = append(args, "--lessons", l)
	}
	ctx, cancel := context.WithTimeout(ctx, modelReviewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Cfg.Apprentice.Python, args...)
	cmd.Env = r.apprenticeEnv(dir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	var res struct {
		Verdict string `json:"verdict"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(stdout, &res); err != nil || res.Summary == "" {
		why := tail(stderr.String(), 300)
		if runErr != nil {
			why = runErr.Error() + ": " + why
		}
		return fail(why)
	}
	return res.Verdict, res.Summary
}

func environmentLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if environmentPatterns.MatchString(line) {
			return tail(strings.TrimSpace(line), 300)
		}
	}
	return ""
}

// preflight runs the preflight command of the runner configuration in the
// sandbox. It returns "" when the command passes (or there is none), and the
// reason when it fails.
func (r *Runner) preflight(ctx context.Context, env, workdir string) (string, error) {
	cmd := r.Cfg.Sandbox.Preflight
	if cmd == "" {
		return "", nil
	}
	res, err := r.exec(ctx, env, workdir, cmd+" 2>&1", 120000)
	if err != nil {
		return "", err
	}
	if res.ExitCode == 0 && !res.TimedOut {
		return "", nil
	}
	return fmt.Sprintf("%s: %s: %s", cmd, exitText(res), tail(strings.TrimSpace(string(res.Stdout)+string(res.Stderr)), 500)), nil
}

// changed reports whether the work tree of the task has a change against
// the base commit: a file that git does not ignore, or a commit. An error
// is "no change": the stop then goes to a person, as before.
func (r *Runner) changed(ctx context.Context, a magiapi.RunnerAssignment) bool {
	if r.changedHook != nil {
		return r.changedHook(a)
	}
	res, err := r.exec(ctx, a.Vars["environment"], a.Vars["workdir"],
		"git status --porcelain | head -n 1; git rev-list --max-count=1 "+shellQuote(a.Vars["base_commit"])+"..HEAD", 60000)
	return err == nil && res.ExitCode == 0 && strings.TrimSpace(string(res.Stdout)) != ""
}

// roundComment is the comment of a person for the next work round: after a
// blocked round, the comment of the "blocked" decision (resume); otherwise
// the review comment (revise). The apprentice worker has the same rule
// (guards.round_comment).
func roundComment(vars map[string]string) string {
	if vars["apprentice.blocked"] == "true" {
		return vars["blocked.comment"]
	}
	return vars["review.comment"]
}

func (r *Runner) check(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	env, workdir, branch, name := a.Vars["environment"], a.Vars["workdir"], a.Vars["branch"], a.Vars["repo"]
	command := firstNonEmpty(a.Vars["check_command"], "./tools/check.sh")
	r.note(ctx, st, "Running "+command)
	res, err := r.exec(ctx, env, workdir, command+" 2>&1", 30*60000)
	if err != nil {
		return Result{}, err
	}
	output := string(res.Stdout) + string(res.Stderr)
	art, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "text/plain", Body: []byte(output),
		Labels: map[string]string{"kind": "check-output"}})
	if err != nil {
		return Result{}, fmt.Errorf("store check output: %w", err)
	}
	if (res.ExitCode != 0 || res.TimedOut) && environmentLine(output) != "" {
		// The environment broke the check, not the change: another work round
		// cannot fix it, and it must not count toward give-up. A person
		// decides: run the check again, resume, or reject.
		line := environmentLine(output)
		r.report(ctx, st, "normal", "Task blocked", "The checks failed because of the environment, not the change: "+line+
			". The work is kept. Decide on the task: check runs the checks again.")
		return Result{Outcome: "blocked", Outputs: map[string]string{"check.output": art.Digest, "check.blocked": line,
			"check_output": tail(output, 6000), "apprentice.blocked": "true"}}, nil
	}
	attempts, _ := strconv.Atoi(a.Vars["check.attempts"])
	attempts++
	// A check that ran on its own terms ends a blocked state: the next round
	// gets the review comment, not an old comment of a blocked decision.
	out := map[string]string{"check.output": art.Digest, "check.attempts": strconv.Itoa(attempts), "apprentice.blocked": ""}
	failed := func(why string) (Result, error) {
		out["check_output"] = why
		if a.Vars["apprentice.loop_stop"] == "true" {
			// The work round ended in a loop, not with a finish. One more
			// round of the same model has a small chance: a person decides.
			out["apprentice.blocked"] = "true"
			r.report(ctx, st, "normal", "Task blocked", "The apprentice stopped in a loop, and the checks failed on the change that it left. The work is kept. Resume it, run the checks again, or reject it.")
			return Result{Outcome: "blocked", Outputs: out}, nil
		}
		if attempts >= maxCheckAttempts {
			out["task.result"] = "gave-up"
			r.report(ctx, st, "normal", "Task stopped", fmt.Sprintf("The checks failed %d times. I stopped the task. The last output is in artifact %s.", attempts, art.Digest))
			return Result{Outcome: "give-up", Outputs: out}, nil
		}
		r.post(ctx, st, fmt.Sprintf("The checks failed (attempt %d of %d, %s). I sent the output back to the apprentice.", attempts, maxCheckAttempts, exitText(res)))
		return Result{Outcome: "fail-checks", Outputs: out}, nil
	}
	if res.ExitCode != 0 || res.TimedOut {
		return failed(tail(output, 6000))
	}
	msg := a.Vars["title"]
	if sum := a.Vars["apprentice.summary"]; sum != "" {
		msg += "\n\n" + sum
	}
	w := r.Boxes.Work(env)
	if err := r.writeFile(ctx, env, w+"/commit-message", []byte(msg)); err != nil {
		return Result{}, err
	}
	res, err = r.exec(ctx, env, workdir, fmt.Sprintf(`set -e
git add -A
if git diff --cached --quiet; then echo NO_CHANGES; else git commit -q -F %[3]s/commit-message; fi
git bundle create -q %[3]s/out.bundle %[1]s..%[2]s 2>/dev/null || echo EMPTY_BUNDLE
git rev-parse HEAD`, a.Vars["base_commit"], branch, shellQuote(w)), 120000)
	if err != nil {
		return Result{}, err
	}
	if res.ExitCode != 0 {
		return Result{}, fmt.Errorf("commit failed: %s", tail(string(res.Stdout)+string(res.Stderr), 2000))
	}
	if strings.Contains(string(res.Stdout), "EMPTY_BUNDLE") {
		return failed("The checks passed, but no files changed. Make the requested change.")
	}
	bundle, err := r.readFile(ctx, env, w+"/out.bundle", 64<<20)
	if err != nil {
		return Result{}, err
	}
	if _, err := r.Source.PushBundle(sourceCtx(ctx), sourceapi.PushBundleRequest{Repo: name, Bundle: bundle, Force: ptr(true)}); err != nil {
		return Result{}, fmt.Errorf("store branch: %w", err)
	}
	diff, err := r.Source.Diff(sourceCtx(ctx), sourceapi.DiffRequest{Repo: name, Base: a.Vars["base_commit"], Head: branch, MaxBytes: ptr(uint64(4 << 20))})
	if err != nil {
		return Result{}, err
	}
	patch, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "text/x-diff", Body: []byte(diff.Patch),
		Labels: map[string]string{"kind": "patch"}})
	if err != nil {
		return Result{}, err
	}
	files, _ := json.Marshal(diff.Files)
	out["head_commit"] = lastLine(string(res.Stdout))
	out["patch"], out["patch.files"] = patch.Digest, string(files)
	out["check_output"], out["check.attempts"] = "", "0"
	hint := ""
	if len(r.Cfg.Checker.Command) > 0 {
		r.note(ctx, st, "Checks passed. The checker started.")
		must, edge, err := r.checkerFindings(ctx, a, env, workdir, w, diff.Patch)
		if edge != "" {
			edge = "\n\nThe checker also names these smaller defects (unusual inputs, weak tests):\n" + edge
		}
		switch {
		case err != nil:
			// The checker is a help, not a gate: its failure does not stop the task.
			r.Log.Warn("checker", "run", a.Run, "err", err)
			hint = "\n\nThe checker failed: " + err.Error()
		case must == "":
			out["checker.verdict"] = "approve"
			hint = "\n\nThe checker found no defect in the requirements of the task." + edge
		case a.Vars["checker.rounds"] == "":
			// The apprentice gets the findings one time. The count is kept, so
			// the next pass goes to the review of a person.
			out["checker.verdict"], out["checker.rounds"] = "revise", "1"
			out["check_output"] = "The check command passed. A checker then read the change and ran tests of its own on it. " +
				"It found these defects. Correct each one and add a test for it. If a finding is wrong, " +
				"tell why in the finish summary.\n\n" + must
			r.post(ctx, st, "The checks passed. The checker found defects; I sent them back to the apprentice.")
			return Result{Outcome: "fail-checks", Outputs: out}, nil
		default:
			out["checker.verdict"] = "revise"
			hint = "\n\nThe checker still finds these defects after one round of corrections:\n" + must + edge
		}
	}
	if r.Cfg.Apprentice.ReviewModel != "" {
		r.note(ctx, st, "Checks passed. The model review started.")
		verdict, sum := r.modelReview(ctx, a)
		out["review.model_verdict"], hint = verdict, "\n\n"+sum
	}
	r.note(ctx, st, "Checks passed. Waiting for review.")
	review := fmt.Sprintf("The checks passed. The change on %s is ready for your review: approve, revise with a comment, or reject.", branch)
	if base := a.Vars["base"]; base != "" {
		// An eval run never merges: the grade of the eval decides, so the
		// reviewer must not approve it.
		review = fmt.Sprintf("The checks passed. This is an eval run (base %s): it ends at review and never merges. Reject it after the grade. The change on %s is ready for your review.", base, branch)
		r.note(ctx, st, "Eval run (base "+base+"): it ends at review; approve is refused.")
	}
	r.report(ctx, st, "normal", "Ready for review", review+hint)
	return Result{Outcome: "pass", Outputs: out}, nil
}

// checkerFindings runs the checker on the tree of the branch. It gives the
// findings about the requirements of the task and the other findings, each
// as a list, "" for none.
func (r *Runner) checkerFindings(ctx context.Context, a magiapi.RunnerAssignment, env, workdir, w, patch string) (must, edge string, err error) {
	res, err := r.exec(ctx, env, workdir, "git archive --format=tar.gz -o "+shellQuote(w+"/tree.tgz")+" HEAD", 120000)
	if err != nil {
		return "", "", err
	}
	if res.ExitCode != 0 {
		return "", "", fmt.Errorf("archive of the tree: %s", tail(string(res.Stdout)+string(res.Stderr), 300))
	}
	tree, err := r.readFile(ctx, env, w+"/tree.tgz", 64<<20)
	if err != nil {
		return "", "", err
	}
	c, err := r.runChecker(ctx, a, tree, patch)
	if err != nil {
		return "", "", err
	}
	return c.text("requirement"), c.text("edge"), nil
}

// merge merges the reviewed branch onto the newest upstream commit in the
// sandbox and builds the release there. The mirror's default branch does
// not change until publish, so an unreleased merge is never pushed.
func (r *Runner) merge(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	env, workdir, branch, name := a.Vars["environment"], a.Vars["workdir"], a.Vars["branch"], a.Vars["repo"]
	_, repo, err := r.repo(a)
	if err != nil {
		return Result{}, err
	}
	sync, err := r.Source.Sync(sourceCtx(ctx), sourceapi.SyncRequest{Repo: name})
	if err != nil {
		return Result{}, err
	}
	bundle, err := r.Source.Bundle(sourceCtx(ctx), sourceapi.BundleRequest{Repo: name, Refs: []string{repo.DefaultBranch}})
	if err != nil {
		return Result{}, err
	}
	w := r.Boxes.Work(env)
	if err := r.writeFile(ctx, env, w+"/upstream.bundle", bundle.Bundle); err != nil {
		return Result{}, err
	}
	res, err := r.exec(ctx, env, workdir, fmt.Sprintf(`set -e
git fetch -q %[5]s/upstream.bundle +refs/heads/%[1]s:refs/remotes/upstream/%[1]s
git checkout -q -B %[4]s refs/remotes/upstream/%[1]s
if ! git merge -q --no-edit -m "Merge %[2]s: %[3]s" %[2]s; then git merge --abort; echo MERGE_CONFLICT; exit 0; fi
git rev-parse HEAD`, repo.DefaultBranch, branch, strings.NewReplacer(`"`, `'`, "`", "'", "$", "").Replace(a.Vars["title"]), candidate(a.Run), shellQuote(w)), 120000)
	if err != nil {
		return Result{}, err
	}
	if res.ExitCode != 0 || strings.Contains(string(res.Stdout), "MERGE_CONFLICT") {
		r.report(ctx, st, "normal", "Merge failed", fmt.Sprintf("I could not merge %s onto the newest %s (upstream %.12s): there is a conflict.", branch, repo.DefaultBranch, sync.UpstreamCommit))
		return Result{Outcome: "conflict", Outputs: map[string]string{"task.result": "conflict"}}, nil
	}
	commit := lastLine(string(res.Stdout))
	out := map[string]string{"merge.commit": commit}
	r.note(ctx, st, "Merged as "+commit)
	// The merge leaves as a bundle, so that any runner that can push
	// publishes it.
	res, err = r.exec(ctx, env, workdir, fmt.Sprintf("set -e\ngit bundle create -q %s/merged.bundle refs/remotes/upstream/%s..%s",
		shellQuote(w), repo.DefaultBranch, candidate(a.Run)), 120000)
	if err != nil || res.ExitCode != 0 {
		return Result{}, fmt.Errorf("bundle merge: %v %s", err, tail(string(res.Stderr), 1000))
	}
	mb, err := r.readFile(ctx, env, w+"/merged.bundle", 64<<20)
	if err != nil {
		return Result{}, err
	}
	md, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "application/x-git-bundle", Body: mb,
		Labels: map[string]string{"kind": "merge-bundle", "commit": commit}})
	if err != nil {
		return Result{}, fmt.Errorf("upload merge: %w", err)
	}
	out["merge.bundle"] = md.Digest
	if repo.Build {
		r.note(ctx, st, "Building magi-server from the merged change.")
		res, err := r.exec(ctx, env, workdir, compose.BuildScript(w), 20*60000)
		if err != nil {
			return Result{}, err
		}
		if res.ExitCode != 0 {
			r.report(ctx, st, "normal", "Release failed", "The release build failed. Nothing was released or published.")
			out["release.output"] = tail(string(res.Stdout)+string(res.Stderr), 4000)
			out["task.result"] = "build-failed"
			return Result{Outcome: "build-failed", Outputs: out}, nil
		}
		bin, err := r.readFile(ctx, env, w+"/out/app", 64<<20)
		if err != nil {
			return Result{}, err
		}
		d, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "application/octet-stream", Body: bin,
			Labels: map[string]string{"kind": "build", "commit": commit}})
		if err != nil {
			return Result{}, fmt.Errorf("upload build: %w", err)
		}
		out["build.binary"] = d.Digest
		if err := r.buildRunnerPackages(ctx, env, workdir, commit, st, out); err != nil {
			r.report(ctx, st, "normal", "Release failed", "The runner package build failed. Nothing was released or published.")
			out["release.output"] = tail(err.Error(), 4000)
			out["task.result"] = "build-failed"
			return Result{Outcome: "build-failed", Outputs: out}, nil
		}
	}
	r.report(ctx, st, "low", "Merged", fmt.Sprintf("I merged %s onto the newest %s as %.12s.", branch, repo.DefaultBranch, commit))
	out["task.result"] = "merged"
	return Result{Outcome: "done", Outputs: out}, nil
}

// publish pushes the merged commit to the upstream with this machine's Git
// credentials. It runs only after the release stayed healthy (or when
// there is no release). It needs no environment: the merge comes as a
// bundle, so this runner need not be the one that did the work.
func (r *Runner) publish(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	name, commit := a.Vars["repo"], a.Vars["merge.commit"]
	_, repo, err := r.repo(a)
	if err != nil {
		return Result{}, err
	}
	if !repo.Publish {
		return Result{}, fmt.Errorf("runner %s does not publish %s", r.Cfg.Name, name)
	}
	if repo.PullRequests {
		return r.proposePR(ctx, a, st, repo)
	}
	if _, err := r.Source.Sync(sourceCtx(ctx), sourceapi.SyncRequest{Repo: name}); err != nil {
		return Result{}, err
	}
	mb, err := r.Magi.GetArtifact(ctx, magiapi.RunnerGetArtifactRequest{Step: st, Digest: a.Vars["merge.bundle"]})
	if err != nil {
		return Result{}, fmt.Errorf("get merge bundle: %w", err)
	}
	// Store the merge as candidate/<run>, then fast-forward the default
	// branch to it. If the upstream moved since the merge, the released
	// commit is not the one to publish: stop and say so.
	if _, err := r.Source.PushBundle(sourceCtx(ctx), sourceapi.PushBundleRequest{Repo: name, Bundle: mb.Body, Force: ptr(true)}); err != nil {
		return Result{}, fmt.Errorf("store merge: %w", err)
	}
	m, err := r.Source.Merge(sourceCtx(ctx), sourceapi.MergeRequest{Repo: name, Source: commit, Target: repo.DefaultBranch, FastForwardOnly: ptr(true)})
	if err != nil {
		return Result{}, err
	}
	if !m.Merged {
		r.report(ctx, st, "normal", "Not pushed", fmt.Sprintf("The upstream %s moved after the release, so I did not push. The released commit is %.12s.", repo.DefaultBranch, commit))
		return Result{Outcome: "rejected", Outputs: map[string]string{"publish.error": "upstream moved"}}, nil
	}
	p, err := r.Source.Publish(sourceCtx(ctx), sourceapi.PublishRequest{Repo: name})
	if err != nil {
		return Result{}, err
	}
	if !p.Pushed {
		r.report(ctx, st, "normal", "Push failed", "I could not push to the upstream: "+deref(p.Message))
		return Result{Outcome: "rejected", Outputs: map[string]string{"publish.error": deref(p.Message)}}, nil
	}
	r.report(ctx, st, "low", "Done", fmt.Sprintf("The release stayed healthy, so runner %s pushed %.12s to the upstream %s.", r.Cfg.Name, p.Commit, repo.DefaultBranch))
	return Result{Outcome: "done", Outputs: map[string]string{"publish.commit": p.Commit, "task.result": "published"}}, nil
}

// buildRunnerPackages builds the runner packages of the merged change in
// the sandbox and uploads them. The rollout step makes them the runner
// release after the change is published.
func (r *Runner) buildRunnerPackages(ctx context.Context, env, workdir, commit string, st magiapi.RunnerStep, out map[string]string) error {
	w := r.Boxes.Work(env)
	r.note(ctx, st, "Building the runner packages.")
	res, err := r.exec(ctx, env, workdir, fmt.Sprintf("set -e\ncd magi && GOWORK=off go run ./cmd/runner-package -src .. -out %s/out/runner", shellQuote(w)), 30*60000)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s%s", res.Stdout, res.Stderr)
	}
	version := commit
	if len(version) > 12 {
		version = version[:12]
	}
	pk := map[string]string{}
	for _, p := range Platforms {
		goos, arch, _ := strings.Cut(p, "/")
		b, err := r.readFile(ctx, env, fmt.Sprintf("%s/out/runner/magi-runner-%s-%s.tar.gz", w, goos, arch), 128<<20)
		if err != nil {
			return err
		}
		d, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "application/gzip", Body: b,
			Labels: map[string]string{"kind": "runner-package-build", "version": version, "os": goos, "arch": arch}})
		if err != nil {
			return fmt.Errorf("upload runner package %s: %w", p, err)
		}
		pk[p] = d.Digest
	}
	b, _ := json.Marshal(pk)
	out["runner.version"], out["runner.packages"] = version, string(b)
	return nil
}

func (r *Runner) cleanup(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	if env := a.Vars["environment"]; env != "" && r.Boxes.Exists(env) {
		if err := r.removeBox(env); err != nil {
			r.Log.Warn("remove environment", "env", env, "err", err)
		}
	}
	result := a.Vars["task.result"]
	if result == "" {
		result = "rejected"
		r.report(ctx, st, "low", "Rejected", "The change was rejected. I removed the environment. The branch stays for reference.")
	}
	return Result{Outcome: "done", Outputs: map[string]string{"task.result": result}}, nil
}

// candidate is the branch that holds the merged, released change of a run.
// shellQuote quotes s for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func candidate(run string) string { return "candidate/" + run }

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// exitText describes how a command ended.
func exitText(r executionapi.ExecResult) string {
	if r.TimedOut {
		return "timed out"
	}
	return fmt.Sprintf("exit status %d", r.ExitCode)
}
