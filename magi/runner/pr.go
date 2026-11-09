package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	sourceapi "github.com/catalystcommunity/magi/primitives/source/api"
)

// Pull requests (Repo.PullRequests). The publish step pushes the merged
// commit to the branch magi/<run> and opens a pull request with gh. The
// workflow then waits (pr-wait) and this runner checks the pull request
// (pr-check) until a person merges or closes it.

// ghRepo is OWNER/NAME of a GitHub upstream, or "".
var ghRepo = regexp.MustCompile(`^(?:git@github\.com:|ssh://git@github\.com/|https://github\.com/)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

func githubRepo(upstream string) string {
	if m := ghRepo.FindStringSubmatch(strings.TrimSpace(upstream)); m != nil {
		return m[1]
	}
	return ""
}

// gh runs the gh command of this machine.
var gh = func(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (r *Runner) proposePR(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep, repo Repo) (Result, error) {
	name, commit := a.Vars["repo"], a.Vars["merge.commit"]
	full := githubRepo(repo.Upstream)
	if full == "" {
		return Result{}, fmt.Errorf("pull requests need a GitHub upstream; %s is not one", repo.Upstream)
	}
	mb, err := r.Magi.GetArtifact(ctx, magiapi.RunnerGetArtifactRequest{Step: st, Digest: a.Vars["merge.bundle"]})
	if err != nil {
		return Result{}, fmt.Errorf("get merge bundle: %w", err)
	}
	if _, err := r.Source.PushBundle(sourceCtx(ctx), sourceapi.PushBundleRequest{Repo: name, Bundle: mb.Body, Force: ptr(true)}); err != nil {
		return Result{}, fmt.Errorf("store merge: %w", err)
	}
	branch := "magi/" + st.Run
	p, err := r.Source.Publish(sourceCtx(ctx), sourceapi.PublishRequest{Repo: name, Branch: &branch, Commit: &commit})
	if err != nil {
		return Result{}, err
	}
	if !p.Pushed {
		r.report(ctx, st, "normal", "Push failed", "I could not push the branch "+branch+": "+deref(p.Message))
		return Result{Outcome: "rejected", Outputs: map[string]string{"publish.error": deref(p.Message)}}, nil
	}
	url := a.Vars["pr.url"]
	if url == "" {
		// An earlier attempt can have opened it already.
		if out, err := gh(ctx, "pr", "list", "--repo", full, "--head", branch, "--state", "open", "--json", "url", "--jq", ".[0].url"); err == nil {
			url = strings.TrimSpace(string(out))
		}
	}
	if url == "" {
		out, err := gh(ctx, "pr", "create", "--repo", full, "--head", branch, "--base", repo.DefaultBranch,
			"--title", prTitle(a.Vars), "--body", prBody(a.Vars, st.Run, commit))
		if err != nil {
			return Result{}, err
		}
		url = strings.TrimSpace(string(out))
		if i := strings.LastIndex(url, "\n"); i >= 0 {
			url = strings.TrimSpace(url[i+1:])
		}
	}
	r.report(ctx, st, "normal", "Pull request", fmt.Sprintf("The release stayed healthy, so I opened a pull request: %s", url))
	return Result{Outcome: "proposed", Outputs: map[string]string{"pr.url": url, "pr.branch": branch, "publish.commit": commit, "task.result": "proposed"}}, nil
}

// prCheck reads the state of the pull request.
func (r *Runner) prCheck(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error) {
	url := a.Vars["pr.url"]
	if url == "" {
		return Result{}, fmt.Errorf("the task has no pull request")
	}
	res, err := r.prCheckState(ctx, url)
	if err != nil {
		return res, err
	}
	switch res.Outcome {
	case "merged":
		r.report(ctx, st, "normal", "Merged", "The pull request is merged: "+url)
	case "closed":
		r.report(ctx, st, "normal", "Closed", "The pull request was closed without a merge: "+url)
	}
	return res, nil
}

// prCheckState gives the outcome of pr-check for the state on GitHub.
func (r *Runner) prCheckState(ctx context.Context, url string) (Result, error) {
	out, err := gh(ctx, "pr", "view", url, "--json", "state,mergeCommit")
	if err != nil {
		return Result{}, err
	}
	var v struct {
		State       string `json:"state"`
		MergeCommit *struct {
			Oid string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return Result{}, fmt.Errorf("gh pr view: %w", err)
	}
	switch v.State {
	case "MERGED":
		res := Result{Outcome: "merged", Outputs: map[string]string{"task.result": "published"}}
		if v.MergeCommit != nil {
			res.Outputs["publish.commit"] = v.MergeCommit.Oid
		}
		return res, nil
	case "CLOSED":
		return Result{Outcome: "closed", Outputs: map[string]string{"task.result": "pr-closed"}}, nil
	}
	return Result{Outcome: "open"}, nil
}

func prTitle(vars map[string]string) string {
	t := strings.TrimSpace(vars["title"])
	if t == "" {
		t = "Change by Magi"
	}
	if len([]rune(t)) > 100 {
		t = string([]rune(t)[:100])
	}
	return t
}

func prBody(vars map[string]string, run, commit string) string {
	var b strings.Builder
	b.WriteString("A change by a Magi apprentice. A person reviewed it in Magi, it passed the checks, and its release stayed healthy.\n\n")
	if v := strings.TrimSpace(vars["instructions"]); v != "" {
		fmt.Fprintf(&b, "## Instructions\n\n%s\n\n", v)
	}
	if v := strings.TrimSpace(vars["apprentice.summary"]); v != "" {
		fmt.Fprintf(&b, "## Summary of the apprentice\n\n%s\n\n", v)
	}
	fmt.Fprintf(&b, "Magi task: `%s`. Commit: `%.12s`.\n", run, commit)
	return b.String()
}
