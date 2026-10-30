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
	"strconv"
	"strings"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/compose"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	sourceapi "github.com/catalystcommunity/magi/primitives/source/api"
)

const maxCheckAttempts = 3

// The runner never opens files in a work directory from the host side: the
// agent controls that directory and could put a symbolic link there. All
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
		r.Boxes.Remove(env)
	}
	b := make([]byte, 24)
	rand.Read(b)
	if err := r.Boxes.Start(env, hex.EncodeToString(b), r.sandboxEnv()); err != nil {
		return Result{}, err
	}
	if _, err := r.Boxes.Wait(env, 30*time.Second); err != nil {
		return Result{}, err
	}
	bundle, err := r.Source.Bundle(sourceCtx(ctx), sourceapi.BundleRequest{Repo: name, Refs: []string{repo.DefaultBranch}})
	if err != nil {
		return Result{}, err
	}
	if err := r.writeFile(ctx, env, "/work/repo.bundle", bundle.Bundle); err != nil {
		return Result{}, err
	}
	branch := "task/" + a.Run
	res, err := r.exec(ctx, env, "/work", fmt.Sprintf(`set -e
git clone -q -b %[1]s /work/repo.bundle /work/repo
cd /work/repo
git config user.name Magi
git config user.email magi@example.local
git checkout -q -b %[2]s
git rev-parse HEAD`, repo.DefaultBranch, branch), 120000)
	if err != nil {
		return Result{}, err
	}
	if res.ExitCode != 0 {
		return Result{}, fmt.Errorf("clone failed: %s", tail(string(res.Stderr), 2000))
	}
	r.note(ctx, st, fmt.Sprintf("Environment %s is ready on runner %s, branch %s.", env, r.Cfg.Name, branch))
	r.post(ctx, st, fmt.Sprintf("I started work on %q on runner %s. The agent works on branch %s.", a.Vars["title"], r.Cfg.Name, branch))
	return Result{Outcome: "done", Outputs: map[string]string{
		"environment": env, "workdir": "/work/repo", "branch": branch, "base_commit": strings.TrimSpace(string(res.Stdout)),
		"default_branch": repo.DefaultBranch,
	}}, nil
}

// work runs the coding agent on this machine. Its tools use the sandbox
// through the loopback execution API; its model calls go through Magi.
func (r *Runner) work(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	dir, err := os.MkdirTemp(r.Cfg.StateDir, "agent-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	instr := filepath.Join(dir, "instructions")
	if err := os.WriteFile(instr, []byte(a.Vars["instructions"]), 0o600); err != nil {
		return Result{}, err
	}
	transcript := filepath.Join(dir, "transcript.jsonl")
	args := []string{"-m", "magi_agent", "run", "--environment", a.Vars["environment"], "--workdir", a.Vars["workdir"],
		"--instructions", "@" + instr, "--transcript", transcript, "--no-upload"}
	for flag, v := range map[string]string{"--check-command": a.Vars["check_command"], "--check-output": a.Vars["check_output"],
		"--title": a.Vars["title"], "--review-comment": a.Vars["review.comment"], "--model": a.Vars["model"]} {
		if v != "" {
			args = append(args, flag, v)
		}
	}
	if r.Cfg.Agent.MaxSteps > 0 {
		args = append(args, "--max-steps", strconv.Itoa(r.Cfg.Agent.MaxSteps))
	}
	cmd := exec.CommandContext(ctx, r.Cfg.Agent.Python, args...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "PYTHONPATH=" + r.Cfg.Agent.Path,
		"MAGI_EXECUTION_URL=" + r.execURL, "MAGI_TOKEN_FILE=" + r.execTokenFile,
		"MAGI_MODEL_BASE_URL=" + strings.TrimRight(r.Cfg.Server, "/") + "/runner/v1",
		"MAGI_MODEL_API_KEY_FILE=" + r.Cfg.TokenFile, "MAGI_MODEL_NAME=coder",
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	var stdout strings.Builder
	cmd.Stdout = &stdout
	r.post(ctx, st, "The coding agent started.")
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	// The agent logs "progress: ..." lines; they become step notes.
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
		Status      string `json:"status"`
		Summary     string `json:"summary"`
		Steps       int    `json:"steps"`
		ToolCalls   int    `json:"tool_calls"`
		CheckPassed *bool  `json:"check_passed"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		return Result{}, fmt.Errorf("agent failed (%v): %s", waitErr, tail(strings.Join(last, "\n"), 3000))
	}
	outputs := map[string]string{"agent.summary": out.Summary, "agent.steps": strconv.Itoa(out.Steps),
		"agent.status": out.Status, "agent.tool_calls": strconv.Itoa(out.ToolCalls)}
	if out.CheckPassed != nil {
		outputs["agent.check_passed"] = strconv.FormatBool(*out.CheckPassed)
	}
	if b, err := os.ReadFile(transcript); err == nil {
		if d, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "application/x-ndjson", Body: b,
			Labels: map[string]string{"kind": "agent-transcript"}}); err == nil {
			outputs["agent.transcript"] = d.Digest
		} else {
			outputs["agent.transcript_error"] = tail(err.Error(), 500)
		}
	}
	r.note(ctx, st, fmt.Sprintf("agent %s after %d steps, %d tool calls: %s", out.Status, out.Steps, out.ToolCalls, out.Summary))
	if out.Status == "error" {
		return Result{}, fmt.Errorf("agent error: %s", firstNonEmpty(out.Error, out.Summary))
	}
	return Result{Outcome: "done", Outputs: outputs}, nil
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
	attempts, _ := strconv.Atoi(a.Vars["check.attempts"])
	attempts++
	out := map[string]string{"check.output": art.Digest, "check.attempts": strconv.Itoa(attempts)}
	failed := func(why string) (Result, error) {
		out["check_output"] = why
		if attempts >= maxCheckAttempts {
			out["task.result"] = "gave-up"
			r.post(ctx, st, fmt.Sprintf("The checks failed %d times. I stopped the task. The last output is in artifact %s.", attempts, art.Digest))
			return Result{Outcome: "give-up", Outputs: out}, nil
		}
		r.post(ctx, st, fmt.Sprintf("The checks failed (attempt %d of %d). I sent the output back to the agent.", attempts, maxCheckAttempts))
		return Result{Outcome: "fail-checks", Outputs: out}, nil
	}
	if res.ExitCode != 0 || res.TimedOut {
		return failed(tail(output, 6000))
	}
	msg := a.Vars["title"]
	if sum := a.Vars["agent.summary"]; sum != "" {
		msg += "\n\n" + sum
	}
	if err := r.writeFile(ctx, env, "/work/commit-message", []byte(msg)); err != nil {
		return Result{}, err
	}
	res, err = r.exec(ctx, env, workdir, fmt.Sprintf(`set -e
git add -A
if git diff --cached --quiet; then echo NO_CHANGES; else git commit -q -F /work/commit-message; fi
git bundle create -q /work/out.bundle %s..%s 2>/dev/null || echo EMPTY_BUNDLE
git rev-parse HEAD`, a.Vars["base_commit"], branch), 120000)
	if err != nil {
		return Result{}, err
	}
	if res.ExitCode != 0 {
		return Result{}, fmt.Errorf("commit failed: %s", tail(string(res.Stdout)+string(res.Stderr), 2000))
	}
	if strings.Contains(string(res.Stdout), "EMPTY_BUNDLE") {
		return failed("The checks passed, but no files changed. Make the requested change.")
	}
	bundle, err := r.readFile(ctx, env, "/work/out.bundle", 64<<20)
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
	r.note(ctx, st, "Checks passed. Waiting for review.")
	r.post(ctx, st, fmt.Sprintf("The checks passed. The change on %s is ready for your review: approve, revise with a comment, or reject.", branch))
	return Result{Outcome: "pass", Outputs: out}, nil
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
	if err := r.writeFile(ctx, env, "/work/upstream.bundle", bundle.Bundle); err != nil {
		return Result{}, err
	}
	res, err := r.exec(ctx, env, workdir, fmt.Sprintf(`set -e
git fetch -q /work/upstream.bundle +refs/heads/%[1]s:refs/remotes/upstream/%[1]s
git checkout -q -B %[4]s refs/remotes/upstream/%[1]s
if ! git merge -q --no-edit -m "Merge %[2]s: %[3]s" %[2]s; then git merge --abort; echo MERGE_CONFLICT; exit 0; fi
git rev-parse HEAD`, repo.DefaultBranch, branch, strings.NewReplacer(`"`, `'`, "`", "'", "$", "").Replace(a.Vars["title"]), candidate(a.Run)), 120000)
	if err != nil {
		return Result{}, err
	}
	if res.ExitCode != 0 || strings.Contains(string(res.Stdout), "MERGE_CONFLICT") {
		r.post(ctx, st, fmt.Sprintf("I could not merge %s onto the newest %s (upstream %.12s): there is a conflict.", branch, repo.DefaultBranch, sync.UpstreamCommit))
		return Result{Outcome: "conflict", Outputs: map[string]string{"task.result": "conflict"}}, nil
	}
	commit := lastLine(string(res.Stdout))
	out := map[string]string{"merge.commit": commit}
	r.note(ctx, st, "Merged as "+commit)
	if repo.Build {
		r.note(ctx, st, "Building magi-server from the merged change.")
		res, err := r.exec(ctx, env, workdir, compose.BuildScript, 20*60000)
		if err != nil {
			return Result{}, err
		}
		if res.ExitCode != 0 {
			r.post(ctx, st, "The release build failed. Nothing was released or published.")
			out["release.output"] = tail(string(res.Stdout)+string(res.Stderr), 4000)
			out["task.result"] = "build-failed"
			return Result{Outcome: "build-failed", Outputs: out}, nil
		}
		bin, err := r.readFile(ctx, env, "/work/out/app", 64<<20)
		if err != nil {
			return Result{}, err
		}
		d, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "application/octet-stream", Body: bin,
			Labels: map[string]string{"kind": "build", "commit": commit}})
		if err != nil {
			return Result{}, fmt.Errorf("upload build: %w", err)
		}
		out["build.binary"] = d.Digest
	}
	r.post(ctx, st, fmt.Sprintf("I merged %s onto the newest %s as %.12s.", branch, repo.DefaultBranch, commit))
	out["task.result"] = "merged"
	return Result{Outcome: "done", Outputs: out}, nil
}

// publish pushes the merged commit to the upstream with this machine's Git
// credentials. It runs only after the release stayed healthy (or when
// there is no release).
func (r *Runner) publish(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	env, workdir, name, commit := a.Vars["environment"], a.Vars["workdir"], a.Vars["repo"], a.Vars["merge.commit"]
	_, repo, err := r.repo(a)
	if err != nil {
		return Result{}, err
	}
	if _, err := r.Source.Sync(sourceCtx(ctx), sourceapi.SyncRequest{Repo: name}); err != nil {
		return Result{}, err
	}
	res, err := r.exec(ctx, env, workdir, fmt.Sprintf("set -e\ngit bundle create -q /work/merged.bundle refs/remotes/upstream/%s..%s", repo.DefaultBranch, candidate(a.Run)), 120000)
	if err != nil || res.ExitCode != 0 {
		return Result{}, fmt.Errorf("bundle merge: %v %s", err, tail(string(res.Stderr), 1000))
	}
	bundle, err := r.readFile(ctx, env, "/work/merged.bundle", 64<<20)
	if err != nil {
		return Result{}, err
	}
	// Store the merge as candidate/<run>, then fast-forward the default
	// branch to it. If the upstream moved since the merge, the released
	// commit is not the one to publish: stop and say so.
	if _, err := r.Source.PushBundle(sourceCtx(ctx), sourceapi.PushBundleRequest{Repo: name, Bundle: bundle, Force: ptr(true)}); err != nil {
		return Result{}, fmt.Errorf("store merge: %w", err)
	}
	m, err := r.Source.Merge(sourceCtx(ctx), sourceapi.MergeRequest{Repo: name, Source: commit, Target: repo.DefaultBranch, FastForwardOnly: ptr(true)})
	if err != nil {
		return Result{}, err
	}
	if !m.Merged {
		r.post(ctx, st, fmt.Sprintf("The upstream %s moved after the release, so I did not push. The released commit is %.12s.", repo.DefaultBranch, commit))
		return Result{Outcome: "rejected", Outputs: map[string]string{"publish.error": "upstream moved"}}, nil
	}
	p, err := r.Source.Publish(sourceCtx(ctx), sourceapi.PublishRequest{Repo: name})
	if err != nil {
		return Result{}, err
	}
	if !p.Pushed {
		r.post(ctx, st, "I could not push to the upstream: "+deref(p.Message))
		return Result{Outcome: "rejected", Outputs: map[string]string{"publish.error": deref(p.Message)}}, nil
	}
	r.post(ctx, st, fmt.Sprintf("The release stayed healthy, so I pushed %.12s to the upstream %s.", p.Commit, repo.DefaultBranch))
	return Result{Outcome: "done", Outputs: map[string]string{"publish.commit": p.Commit, "task.result": "published"}}, nil
}

func (r *Runner) cleanup(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	if env := a.Vars["environment"]; env != "" && r.Boxes.Exists(env) {
		if err := r.Boxes.Remove(env); err != nil {
			r.Log.Warn("remove environment", "env", env, "err", err)
		}
	}
	result := a.Vars["task.result"]
	if result == "" {
		result = "rejected"
		r.post(ctx, st, "The change was rejected. I removed the environment. The branch stays for reference.")
	}
	return Result{Outcome: "done", Outputs: map[string]string{"task.result": result}}, nil
}

// candidate is the branch that holds the merged, released change of a run.
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
