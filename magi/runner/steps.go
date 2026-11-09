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
	res, err := r.exec(ctx, env, w, fmt.Sprintf(`set -e
git clone -q -b %[1]s %[5]s/repo.bundle %[5]s/repo
cd %[5]s/repo
git config user.name %[3]s
git config user.email %[4]s
git checkout -q -b %[2]s
git rev-parse HEAD`, repo.DefaultBranch, branch, shellQuote(r.Cfg.Author.Name), shellQuote(r.Cfg.Author.Email), shellQuote(w)), 120000)
	if err != nil {
		return Result{}, err
	}
	if res.ExitCode != 0 {
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
	dir, err := os.MkdirTemp(r.Cfg.StateDir, "apprentice-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	instr := filepath.Join(dir, "instructions")
	if err := os.WriteFile(instr, []byte(a.Vars["instructions"]), 0o600); err != nil {
		return Result{}, err
	}
	transcript := filepath.Join(dir, "transcript.jsonl")
	args := []string{"-m", "magi_apprentice", "run", "--environment", a.Vars["environment"], "--workdir", a.Vars["workdir"],
		"--instructions", "@" + instr, "--transcript", transcript, "--no-upload"}
	for flag, v := range map[string]string{"--check-command": a.Vars["check_command"], "--check-output": a.Vars["check_output"],
		"--title": a.Vars["title"], "--review-comment": a.Vars["review.comment"], "--model": a.Vars["model"]} {
		if v != "" {
			args = append(args, flag, v)
		}
	}
	if r.Cfg.Apprentice.MaxSteps > 0 {
		args = append(args, "--max-steps", strconv.Itoa(r.Cfg.Apprentice.MaxSteps))
	}
	cmd := exec.CommandContext(ctx, r.Cfg.Apprentice.Python, args...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "PYTHONPATH=" + r.Cfg.Apprentice.Path,
		"MAGI_EXECUTION_URL=" + r.execURL, "MAGI_TOKEN_FILE=" + r.execTokenFile,
		"MAGI_MODEL_BASE_URL=" + strings.TrimRight(r.Cfg.Server, "/") + "/runner/v1",
		"MAGI_MODEL_API_KEY_FILE=" + r.Cfg.TokenFile, "MAGI_MODEL_NAME=coder",
		// The coding model behind /runner/v1 is a large provider model
		// (glm-5.3). The apprentice defaults fit a small local model: at
		// 24000 tokens it removed what it had read and read the same files
		// again until the step limit (world eval, 2026-11-09). A reasoning
		// model also needs room to think before it writes.
		"MAGI_APPRENTICE_CONTEXT_TOKENS=100000", "MAGI_MODEL_MAX_TOKENS=16384",
		"MAGI_APPRENTICE_TOOL_OUTPUT_CHARS=16000", "MAGI_APPRENTICE_MAX_SECONDS=3600",
	}
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
		Status      string `json:"status"`
		Summary     string `json:"summary"`
		Steps       int    `json:"steps"`
		ToolCalls   int    `json:"tool_calls"`
		CheckPassed *bool  `json:"check_passed"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		return Result{}, fmt.Errorf("apprentice failed (%v): %s", waitErr, tail(strings.Join(last, "\n"), 3000))
	}
	outputs := map[string]string{"apprentice.summary": out.Summary, "apprentice.steps": strconv.Itoa(out.Steps),
		"apprentice.status": out.Status, "apprentice.tool_calls": strconv.Itoa(out.ToolCalls)}
	if out.CheckPassed != nil {
		outputs["apprentice.check_passed"] = strconv.FormatBool(*out.CheckPassed)
	}
	if b, err := os.ReadFile(transcript); err == nil {
		if d, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "application/x-ndjson", Body: b,
			Labels: map[string]string{"kind": "apprentice-transcript"}}); err == nil {
			outputs["apprentice.transcript"] = d.Digest
		} else {
			outputs["apprentice.transcript_error"] = tail(err.Error(), 500)
		}
	}
	r.note(ctx, st, fmt.Sprintf("apprentice %s after %d steps, %d tool calls: %s", out.Status, out.Steps, out.ToolCalls, out.Summary))
	if out.Status == "error" {
		return Result{}, fmt.Errorf("apprentice error: %s", firstNonEmpty(out.Error, out.Summary))
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
	r.note(ctx, st, "Checks passed. Waiting for review.")
	r.report(ctx, st, "normal", "Ready for review", fmt.Sprintf("The checks passed. The change on %s is ready for your review: approve, revise with a comment, or reject.", branch))
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
