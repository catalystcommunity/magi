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

// The checker is a command of the runner host that checks a change after
// its checks pass: a coding agent of the person who owns the runner (for
// example `codex exec`), with the sign-in that this person made on the host.
// The runner starts the command and reads its result. It does not read,
// keep, or send the credentials of the command.
//
// The checker gets a copy of the tree with the change, outside the sandbox
// of the task, so it cannot change the work. It runs in a view of the host
// that has no home directory of the user (checkerview.go). Its findings about a
// requirement of the task go to the apprentice one time, as the output of a
// failed check. After that round, and for each other finding, the findings
// are a hint in the review request: a checker that always finds something
// must not keep the task from the review of a person.

//go:embed checker-prompt.md
var checkerPrompt string

// checkerSchema is the JSON Schema of the result of the checker.
const checkerSchema = `{"type":"object","additionalProperties":false,"required":["verdict","findings"],
"properties":{"verdict":{"type":"string","enum":["approve","revise"]},
"findings":{"type":"array","items":{"type":"object","additionalProperties":false,
"required":["severity","file","line","sentence","problem","evidence"],
"properties":{"severity":{"type":"string","enum":["requirement","edge"]},
"file":{"type":"string"},"line":{"type":"integer"},"sentence":{"type":"string"},
"problem":{"type":"string"},"evidence":{"type":"string"}}}}}}`

// checkerMaxFindings: the apprentice gets the first findings. A longer list
// hides the important ones.
const checkerMaxFindings = 8

type checkerFinding struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Sentence string `json:"sentence"`
	Problem  string `json:"problem"`
	Evidence string `json:"evidence"`
}

type checkerResult struct {
	Verdict  string           `json:"verdict"`
	Findings []checkerFinding `json:"findings"`
}

// text is the findings of one severity as a numbered list, "" for none.
// The checker finds something in each change, also in a change that a
// person approved: an unusual input, a weak test. Only a finding with the
// severity "requirement" is a reason for a work round.
func (c checkerResult) text(severity string) string {
	var b strings.Builder
	n := 0
	for _, f := range c.Findings {
		if (f.Severity == "requirement") != (severity == "requirement") || n == checkerMaxFindings {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d. %s:%d: %s\n   Task: %s\n   Evidence: %s\n", n, f.File, f.Line, f.Problem, f.Sentence, f.Evidence)
	}
	return b.String()
}

// runChecker runs the checker command on a copy of the tree of the change.
// tree is a gzip tar of the files at the head of the branch; patch is the
// diff from the base.
func (r *Runner) runChecker(ctx context.Context, a magiapi.RunnerAssignment, tree []byte, patch string) (checkerResult, error) {
	var res checkerResult
	dir, err := os.MkdirTemp(r.Cfg.StateDir, "checker-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(dir)
	work, schema, out := filepath.Join(dir, "tree"), filepath.Join(dir, "schema.json"), filepath.Join(dir, "result.json")
	if err := os.Mkdir(work, 0o700); err != nil {
		return res, err
	}
	untar := exec.CommandContext(ctx, "tar", "-xzf", "-", "--no-same-owner", "-C", work)
	untar.Stdin = bytes.NewReader(tree)
	if b, err := untar.CombinedOutput(); err != nil {
		return res, fmt.Errorf("unpack the tree: %v: %s", err, tail(string(b), 300))
	}
	// The diff is a file next to the tree, not in it: the checker reads the
	// change there, and the tree stays the tree of the branch.
	if err := os.WriteFile(filepath.Join(dir, "CHANGE.diff"), []byte(patch), 0o600); err != nil {
		return res, err
	}
	if err := os.WriteFile(schema, []byte(checkerSchema), 0o600); err != nil {
		return res, err
	}
	task := strings.TrimSpace(a.Vars["title"] + "\n\n" + a.Vars["instructions"])
	if c := roundComment(a.Vars); c != "" {
		task += "\n\nThe review comment of the earlier round (each item must be done):\n" + c
	}
	argv := make([]string, len(r.Cfg.Checker.Command))
	for i, arg := range r.Cfg.Checker.Command {
		argv[i] = strings.NewReplacer("{dir}", work, "{schema}", schema, "{out}", out, "{diff}", filepath.Join(dir, "CHANGE.diff")).Replace(arg)
	}
	timeout := time.Duration(r.Cfg.Checker.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv, env, err := r.checkerCommand(dir, argv)
	if err != nil {
		return res, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = work, env
	cmd.Stdin = strings.NewReader(strings.ReplaceAll(checkerPrompt, "{diff}", filepath.Join(dir, "CHANGE.diff")) + "\n" + task + "\n")
	if b, err := cmd.CombinedOutput(); err != nil {
		return res, fmt.Errorf("%s: %v: %s", argv[0], err, tail(string(b), 300))
	}
	body, err := os.ReadFile(out)
	if err != nil {
		return res, fmt.Errorf("the checker wrote no result: %v", err)
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return res, fmt.Errorf("the result of the checker is not JSON: %v", err)
	}
	if res.Verdict != "approve" && res.Verdict != "revise" {
		return res, fmt.Errorf("the checker gave the verdict %q", res.Verdict)
	}
	return res, nil
}
