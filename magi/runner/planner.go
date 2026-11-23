package runner

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// The planner is a command of the runner host that reads the repository
// before the first work round: a coding agent of the person who owns the
// runner, as the checker is (checker.go). It writes acceptance checks with
// exact values and a plan with the real names of the repository. A small
// coding model follows exact instructions well, and does not find these
// things out by itself.
//
// The planner gets a copy of the tree, in the view of the checker, so it
// cannot change the work. Its text is a help for the apprentice: a planner
// that fails does not stop the task.

//go:embed planner-prompt.md
var plannerPrompt string

// plannerMaxChars is the most text of a plan that the apprentice gets.
const plannerMaxChars = 12000

// planNote is the text between the task and the plan in the instructions.
const planNote = "A planner read the repository before your work and wrote the text below. " +
	"The task above decides when the two do not agree. Use the names and the helpers that the text gives, " +
	"and make each acceptance check pass.\n\n"

// runPlanner runs the planner command on a copy of the tree. tree is a
// gzip tar of the files of the work directory.
func (r *Runner) runPlanner(ctx context.Context, a magiapi.RunnerAssignment, tree []byte) (string, error) {
	dir, err := os.MkdirTemp(r.Cfg.StateDir, "planner-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	work, out := filepath.Join(dir, "tree"), filepath.Join(dir, "plan.md")
	if err := os.Mkdir(work, 0o700); err != nil {
		return "", err
	}
	untar := exec.CommandContext(ctx, "tar", "-xzf", "-", "--no-same-owner", "-C", work)
	untar.Stdin = bytes.NewReader(tree)
	if b, err := untar.CombinedOutput(); err != nil {
		return "", fmt.Errorf("unpack the tree: %v: %s", err, tail(string(b), 300))
	}
	argv := make([]string, len(r.Cfg.Planner.Command))
	for i, arg := range r.Cfg.Planner.Command {
		argv[i] = strings.NewReplacer("{dir}", work, "{out}", out).Replace(arg)
	}
	timeout := time.Duration(r.Cfg.Planner.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv, env, err := agentCommand(r.Cfg.Planner, dir, argv)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = work, env
	cmd.Stdin = strings.NewReader(plannerPrompt + "\n" + strings.TrimSpace(a.Vars["title"]+"\n\n"+a.Vars["instructions"]) + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", argv[0], err, tail(stderr.String()+stdout.String(), 300))
	}
	body, err := os.ReadFile(out)
	if os.IsNotExist(err) {
		// A command with no option for a result file prints the text.
		body, err = stdout.Bytes(), nil
	}
	if err != nil {
		return "", err
	}
	return parsePlan(body)
}

// parsePlan reads the text of the planner: plain text, or a JSON object
// that has the text in the field "result" (the JSON output of Claude Code).
func parsePlan(body []byte) (string, error) {
	text := string(body)
	var wrap struct {
		Result  *string `json:"result"`
		IsError bool    `json:"is_error"`
	}
	if json.Unmarshal(body, &wrap) == nil && wrap.Result != nil {
		if wrap.IsError {
			return "", fmt.Errorf("the planner reported an error: %s", tail(*wrap.Result, 300))
		}
		text = *wrap.Result
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("the planner wrote no text")
	}
	if len(text) > plannerMaxChars {
		text = text[:plannerMaxChars] + "\n[The plan is cut here.]"
	}
	return text, nil
}

// plan gives the plan of the task: the plan of an earlier round, or the
// text of a new planner run. "" is no plan. tried reports a planner run,
// also a failed one, so that the next round does not run it again.
func (r *Runner) plan(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (text string, tried bool) {
	if len(r.Cfg.Planner.Command) == 0 || a.Vars["plan.tried"] == "true" {
		return a.Vars["plan"], false
	}
	r.note(ctx, st, "The planner started.")
	env, w := a.Vars["environment"], r.Boxes.Work(a.Vars["environment"])
	text, err := func() (string, error) {
		// The tree as it is, not a commit: prepare can leave files that are
		// not in a commit. The .git directory is not a part of the plan.
		res, err := r.exec(ctx, env, a.Vars["workdir"], "tar -czf "+shellQuote(w+"/plan-tree.tgz")+" --exclude=./.git .", 120000)
		if err != nil {
			return "", err
		}
		if res.ExitCode != 0 {
			return "", fmt.Errorf("archive of the tree: %s", tail(string(res.Stdout)+string(res.Stderr), 300))
		}
		tree, err := r.readFile(ctx, env, w+"/plan-tree.tgz", 64<<20)
		if err != nil {
			return "", err
		}
		return r.runPlanner(ctx, a, tree)
	}()
	if err != nil {
		// The planner is a help: the work starts without a plan.
		r.Log.Warn("planner", "run", a.Run, "err", err)
		r.note(ctx, st, "The planner failed. The work starts without a plan: "+tail(err.Error(), 300))
		return "", true
	}
	r.note(ctx, st, fmt.Sprintf("The planner wrote a plan (%d characters).", len(text)))
	return text, true
}
