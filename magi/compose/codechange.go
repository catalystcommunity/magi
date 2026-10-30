package compose

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/catalystcommunity/magi/lib/rpc"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	sourceapi "github.com/catalystcommunity/magi/primitives/source/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// CodeChange is the workflow for one change to a repository.
//
// The environment never receives Git credentials. Code enters it as a
// bundle from the source service and leaves it as a bundle of the task
// branch, both through the execution file API.
const CodeChange = "code-change"

const maxCheckAttempts = 3

// codeChangeDefinition returns the code-change workflow. With a runner,
// the steps that use the environment or the repository run on that runner
// (queues runner.<name>.<step>); Magi keeps the review and the release.
func codeChangeDefinition(runner string) workflowapi.Definition {
	task := func(id, queue string, next map[string]string, attempts uint64) workflowapi.Step {
		if runner != "" && id != "release" {
			queue = RunnerQueue(runner, id)
		}
		return workflowapi.Step{Id: id, Kind: "task", Queue: ptr(queue), Next: next, MaxAttempts: ptr(attempts)}
	}
	return workflowapi.Definition{
		Name:        CodeChange,
		Description: ptr("An agent changes a repository in an environment. Checks run, a person reviews, and Magi merges."),
		Start:       "prepare",
		Steps: []workflowapi.Step{
			task("prepare", "magi.prepare", map[string]string{"done": "work"}, 2),
			task("work", "agent", map[string]string{"done": "check"}, 2),
			task("check", "magi.check", map[string]string{"pass": "review", "fail-checks": "work", "give-up": "cleanup"}, 2),
			{Id: "review", Kind: "human", Prompt: ptr("Read the change and the check results. Approve to merge, revise to send it back to the agent with a comment, or reject."),
				Outcomes: []string{"approve", "revise", "reject"}, Next: map[string]string{"approve": "merge", "revise": "work", "reject": "cleanup"}},
			task("merge", "magi.merge", map[string]string{"done": "release", "conflict": "cleanup", "build-failed": "cleanup"}, 2),
			task("release", "magi.release", map[string]string{"done": "publish", "skipped": "publish", "build-failed": "cleanup", "rolled-back": "cleanup", "fail": "cleanup"}, 3),
			// Only a change that released and stayed healthy (or that has
			// no release) goes to the upstream.
			task("publish", "magi.publish", map[string]string{"done": "cleanup", "skipped": "cleanup", "rejected": "cleanup"}, 3),
			task("cleanup", "magi.cleanup", map[string]string{}, 3),
		},
	}
}

// InstallDefinitions writes the workflow definitions that Magi uses.
func (s *System) InstallDefinitions(ctx context.Context) error {
	ctx = s.AsService(ctx)
	for _, d := range []workflowapi.Definition{codeChangeDefinition(s.Cfg.Runner), personaTurnDefinition()} {
		if _, err := s.Workflow.PutDefinition(ctx, d); err != nil {
			return fmt.Errorf("put definition %s: %w", d.Name, err)
		}
	}
	return nil
}

// RunWorkers starts the code-change step workers.
func (s *System) RunWorkers(ctx context.Context) {
	go s.Work(ctx, "magi.prepare", s.prepare)
	go s.Work(ctx, "magi.check", s.check)
	go s.Work(ctx, "magi.merge", s.merge)
	go s.Work(ctx, "magi.cleanup", s.cleanup)
	go s.Work(ctx, "magi.release", s.release)
	go s.Work(ctx, "magi.publish", s.publish)
	go s.Work(ctx, "magi.persona", s.personaTurn)
}

func (s *System) exec(ctx context.Context, envID, cwd, script string, timeoutMs uint64) (executionapi.ExecResult, error) {
	return s.Execution.Exec(ctx, executionapi.ExecRequest{
		Environment: envID, Argv: []string{"bash", "-c", script}, Cwd: ptr(cwd),
		TimeoutMs: ptr(timeoutMs), MaxOutputBytes: ptr(uint64(256 << 10)),
	})
}

func (s *System) prepare(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	repoName := a.Vars["repo"]
	if repoName == "" {
		repoName = s.Cfg.Repo
	}
	repo, err := s.Source.GetRepo(ctx, sourceapi.GetRepoRequest{Name: repoName})
	if err != nil {
		return Result{}, fmt.Errorf("repository %s: %w", repoName, err)
	}
	if st, ok := s.syncUpstream(ctx, repoName); ok {
		note("Upstream sync: " + st)
		if st == "diverged" {
			s.post(ctx, a, "The upstream and Magi's copy of "+repoName+" both have new commits. The task starts from Magi's copy; publish needs a manual merge.")
		}
	}
	note("Creating the environment.")
	envr, err := s.Execution.Create(ctx, executionapi.CreateRequest{
		Owner: ptr(a.Owner), WaitReadyMs: ptr(uint64(300000)),
		Spec: executionapi.EnvironmentSpec{
			Image: s.Cfg.EnvImage, CpuMillis: ptr(uint64(2000)), MemoryMb: ptr(uint64(4096)), DiskMb: ptr(uint64(8192)),
			Network: ptr("egress"), TtlSeconds: ptr(uint64(4 * 3600)), Workdir: ptr("/work"),
			Labels: map[string]string{"magi.run": a.Run},
			Env:    map[string]string{"GOFLAGS": "-mod=mod"},
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("create environment: %w", err)
	}
	if envr.Status != "ready" {
		return Result{}, fmt.Errorf("environment %s is %s: %s", envr.Id, envr.Status, deref(envr.Message))
	}
	b, err := s.Source.Bundle(ctx, sourceapi.BundleRequest{Repo: repoName, Refs: []string{repo.DefaultBranch}})
	if err != nil {
		return Result{}, fmt.Errorf("bundle: %w", err)
	}
	if _, err := s.Execution.WriteFile(ctx, executionapi.WriteFileRequest{Environment: envr.Id, Path: "/work/repo.bundle", Body: b.Bundle}); err != nil {
		return Result{}, fmt.Errorf("write bundle: %w", err)
	}
	branch := "task/" + a.Run
	script := fmt.Sprintf(`set -e
git clone -q -b %[1]s /work/repo.bundle /work/repo
cd /work/repo
git config user.name Magi
git config user.email magi@example.local
git checkout -q -b %[2]s
git rev-parse HEAD`, repo.DefaultBranch, branch)
	r, err := s.exec(ctx, envr.Id, "/work", script, 120000)
	if err != nil {
		return Result{}, err
	}
	if r.ExitCode != 0 {
		return Result{}, fmt.Errorf("clone failed: %s", tail(string(r.Stderr), 2000))
	}
	base := strings.TrimSpace(string(r.Stdout))
	note(fmt.Sprintf("Environment %s is ready on branch %s.", envr.Id, branch))
	s.post(ctx, a, fmt.Sprintf("I started work on %q. The agent works on branch %s.", a.Vars["title"], branch))
	return Result{Outcome: "done", Outputs: map[string]string{
		"environment": envr.Id, "workdir": "/work/repo", "branch": branch, "base_commit": base,
		"repo": repoName, "default_branch": repo.DefaultBranch,
	}}, nil
}

func (s *System) check(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	envID, workdir, branch := a.Vars["environment"], a.Vars["workdir"], a.Vars["branch"]
	note("Running " + s.Cfg.CheckCommand)
	r, err := s.exec(ctx, envID, workdir, s.Cfg.CheckCommand+" 2>&1", 20*60000)
	if err != nil {
		return Result{}, err
	}
	output := string(r.Stdout) + string(r.Stderr)
	art, err := s.Artifacts.Put(ctx, artifactsapi.PutRequest{
		MediaType: "text/plain", Body: []byte(output), Owner: ptr(a.Owner),
		Labels: map[string]string{"kind": "check-output", "run": a.Run},
	})
	if err != nil {
		return Result{}, fmt.Errorf("store check output: %w", err)
	}
	attempts, _ := strconv.Atoi(a.Vars["check.attempts"])
	attempts++
	out := map[string]string{"check.output": art.Digest, "check.attempts": strconv.Itoa(attempts)}
	if r.ExitCode != 0 || r.TimedOut {
		out["check_output"] = tail(output, 6000)
		if attempts >= maxCheckAttempts {
			out["task.result"] = "gave-up"
			s.post(ctx, a, fmt.Sprintf("The checks failed %d times. I stopped the task. The last output is in artifact %s.", attempts, art.Digest))
			return Result{Outcome: "give-up", Outputs: out}, nil
		}
		note(fmt.Sprintf("Checks failed (attempt %d). The agent will try again.", attempts))
		s.post(ctx, a, fmt.Sprintf("The checks failed (attempt %d of %d). I sent the output back to the agent.", attempts, maxCheckAttempts))
		return Result{Outcome: "fail-checks", Outputs: out}, nil
	}
	msg := a.Vars["title"]
	if sum := a.Vars["agent.summary"]; sum != "" {
		msg += "\n\n" + sum
	}
	commit := fmt.Sprintf(`set -e
git add -A
if git diff --cached --quiet; then echo NO_CHANGES; exit 0; fi
git commit -q -F - <<'MAGI_MSG'
%s
MAGI_MSG
git bundle create -q /work/out.bundle %s..%s
git rev-parse HEAD`, msg, a.Vars["base_commit"], branch)
	r, err = s.exec(ctx, envID, workdir, commit, 120000)
	if err != nil {
		return Result{}, err
	}
	if r.ExitCode != 0 {
		return Result{}, fmt.Errorf("commit failed: %s", tail(string(r.Stdout)+string(r.Stderr), 2000))
	}
	if strings.Contains(string(r.Stdout), "NO_CHANGES") && a.Vars["head_commit"] == "" {
		out["check_output"] = "The checks passed, but no files changed. Make the requested change."
		if attempts >= maxCheckAttempts {
			out["task.result"] = "gave-up"
			return Result{Outcome: "give-up", Outputs: out}, nil
		}
		return Result{Outcome: "fail-checks", Outputs: out}, nil
	}
	if !strings.Contains(string(r.Stdout), "NO_CHANGES") {
		f, err := s.Execution.ReadFile(ctx, executionapi.ReadFileRequest{Environment: envID, Path: "/work/out.bundle", MaxBytes: ptr(uint64(16 << 20))})
		if err != nil || f.Truncated {
			return Result{}, fmt.Errorf("read bundle: %v (truncated %v)", err, f.Truncated)
		}
		if _, err := s.Source.PushBundle(ctx, sourceapi.PushBundleRequest{Repo: a.Vars["repo"], Bundle: f.Body, Force: ptr(true)}); err != nil {
			return Result{}, fmt.Errorf("push bundle: %w", err)
		}
		out["head_commit"] = strings.TrimSpace(lastLine(string(r.Stdout)))
	}
	out["check_output"] = ""
	out["check.attempts"] = "0"
	note("Checks passed. Waiting for review.")
	s.post(ctx, a, fmt.Sprintf("The checks passed. The change on %s is ready for your review. Decide with the task API: approve, revise with a comment, or reject.", branch))
	return Result{Outcome: "pass", Outputs: out}, nil
}

func (s *System) merge(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	// Merge onto the newest upstream commit, so publish is a fast-forward.
	if st, ok := s.syncUpstream(ctx, a.Vars["repo"]); ok {
		note("Upstream sync: " + st)
	}
	m, err := s.Source.Merge(ctx, sourceapi.MergeRequest{
		Repo: a.Vars["repo"], Source: a.Vars["branch"], Target: a.Vars["default_branch"],
		Message: ptr(fmt.Sprintf("Merge %s: %s", a.Vars["branch"], a.Vars["title"])),
	})
	if err != nil {
		return Result{}, err
	}
	if !m.Merged {
		s.post(ctx, a, fmt.Sprintf("I could not merge %s: %s", a.Vars["branch"], deref(m.Message)))
		return Result{Outcome: "conflict", Outputs: map[string]string{"task.result": "conflict"}}, nil
	}
	note("Merged as " + m.Commit)
	s.post(ctx, a, fmt.Sprintf("I merged the change into %s as commit %.12s.", a.Vars["default_branch"], m.Commit))
	return Result{Outcome: "done", Outputs: map[string]string{"task.result": "merged", "merge.commit": m.Commit}}, nil
}

func (s *System) cleanup(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	if id := a.Vars["environment"]; id != "" {
		if _, err := s.Execution.Destroy(ctx, executionapi.DestroyRequest{Id: id}); err != nil {
			s.Log.Warn("destroy environment", "env", id, "err", err)
		}
	}
	result := a.Vars["task.result"]
	if result == "" {
		result = "rejected"
		s.post(ctx, a, "The change was rejected. I removed the environment. The branch stays for reference.")
	}
	return Result{Outcome: "done", Outputs: map[string]string{"task.result": result}}, nil
}

// syncUpstream updates the repository from its upstream. ok is false when
// the repository has no upstream or the sync failed (the failure is logged).
func (s *System) syncUpstream(ctx context.Context, repo string) (string, bool) {
	r, err := s.Source.Sync(ctx, sourceapi.SyncRequest{Repo: repo})
	if err != nil {
		if rpc.CodeOf(err) != 412 {
			s.Log.Warn("upstream sync", "repo", repo, "err", err)
		}
		return "", false
	}
	return r.Status, true
}

// publish pushes the merged default branch to the upstream with the source
// service's own credentials.
func (s *System) publish(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	p, err := s.Source.Publish(ctx, sourceapi.PublishRequest{Repo: a.Vars["repo"]})
	if rpc.CodeOf(err) == 412 {
		return Result{Outcome: "skipped"}, nil
	}
	if err != nil {
		return Result{}, err
	}
	if !p.Pushed {
		note("Not published: " + deref(p.Message))
		if deref(p.Message) == "already published" {
			return Result{Outcome: "done"}, nil
		}
		s.post(ctx, a, "I could not push the change to the upstream: "+deref(p.Message)+". It is merged in Magi's copy.")
		return Result{Outcome: "rejected", Outputs: map[string]string{"publish.error": deref(p.Message)}}, nil
	}
	note("Published " + p.Commit)
	s.post(ctx, a, fmt.Sprintf("The release stayed healthy, so I pushed commit %.12s to the upstream repository.", p.Commit))
	return Result{Outcome: "done", Outputs: map[string]string{"publish.commit": p.Commit}}, nil
}

// cancelRisk tells why stopping a run at step is risky, or "" when it is
// safe. The work and check steps stop at once and keep the branch.
func cancelRisk(step string) string {
	switch step {
	case "merge":
		return "the change is being merged. If you stop now, the merge can be done but not released."
	case "publish":
		return "the change is being pushed to the upstream repository. If you stop now, it can be pushed or not."
	case "release":
		return "a release is rolling out. Stopping the task does not stop the rollout: the release controller still finishes it or rolls it back, but the task does not report the result."
	}
	return ""
}

// cancelTask stops run for by, removes its environment, and tells the
// chat. A risky step needs confirm.
func (s *System) cancelTask(ctx context.Context, run workflowapi.Run, by, reason string, confirm bool) (workflowapi.Run, error) {
	switch run.Status {
	case "succeeded", "failed", "cancelled":
		return run, rpc.Conflict("the task already ended (status %s)", run.Status)
	}
	if risk := cancelRisk(run.CurrentStep); risk != "" && !confirm {
		return run, rpc.Conflict("confirm: %s", risk)
	}
	step, why := run.CurrentStep, "for "+by
	if reason != "" {
		why += ": " + reason
	}
	run, err := s.Workflow.Cancel(ctx, workflowapi.CancelRequest{Id: run.Id, Reason: &why})
	if err != nil {
		return run, err
	}
	// A runner removes its own environments when it sees the run ended.
	if id := run.Vars["environment"]; id != "" && run.Vars["runner"] == "" {
		if _, err := s.Execution.Destroy(ctx, executionapi.DestroyRequest{Id: id}); err != nil {
			s.Log.Warn("destroy environment", "env", id, "err", err)
		}
	}
	msg := "The task was stopped"
	if reason != "" {
		msg += ": " + reason
	}
	s.post(ctx, workflowapi.Assignment{Run: run.Id, Owner: run.Owner, Vars: run.Vars}, msg+". I removed its environment. The branch stays for reference.")
	s.Log.Info("task cancelled", "run", run.Id, "by", by, "step", step)
	return run, nil
}

// post writes a persona message into the chat that started the run.
func (s *System) post(ctx context.Context, a workflowapi.Assignment, text string) {
	chat := a.Vars["chat"]
	if chat == "" {
		return
	}
	req := conversationapi.PostRequest{
		Conversation: chat, Text: text, Author: ptr(Persona), AuthorKind: ptr("persona"),
		Attachments: []conversationapi.Attachment{{Kind: "workflow-run", Ref: a.Run, Title: ptr(a.Vars["title"])}},
	}
	if sess := a.Vars["session"]; sess != "" {
		req.Sessions = []string{sess}
	}
	if _, err := s.Conversation.Post(ctx, req); err != nil {
		s.Log.Warn("post to chat", "chat", chat, "err", err)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...\n" + s[len(s)-n:]
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
