// Package runner runs Magi code-change steps on this machine. It connects
// to Magi's public API with a runner token and claims steps from its own
// queues (runner.<name>.<step>). It never listens on the network.
//
// The runner keeps a mirror of each repository with an upstream (for
// example GitHub) and uses this machine's Git credentials for it. Each task
// gets a bubblewrap sandbox; the coding agent runs on this machine and uses
// the sandbox through a loopback execution API. Model calls go through
// Magi, so the machine holds no provider key.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
	sourceserver "github.com/catalystcommunity/magi/primitives/source/server"
)

// Repo is one repository that the runner works on.
type Repo struct {
	Upstream      string `json:"upstream"`
	DefaultBranch string `json:"default_branch"`
	// Build makes the merge step build magi-server for the release.
	Build bool `json:"build"`
}

// Config is the runner configuration file.
type Config struct {
	// Server is the Magi base URL, for example https://magi.example.com.
	Server string `json:"server"`
	// Name is the runner name; the token subject is runner:<name>.
	Name      string          `json:"name"`
	TokenFile string          `json:"token_file"`
	StateDir  string          `json:"state_dir"`
	Repos     map[string]Repo `json:"repos"`
	Agent     struct {
		Python   string `json:"python"`
		Path     string `json:"path"` // PYTHONPATH with magi_agent and the Python clients
		MaxSteps int    `json:"max_steps"`
	} `json:"agent"`
	Sandbox struct {
		ReadOnly []string          `json:"read_only"`
		Shared   []string          `json:"shared"`
		Env      map[string]string `json:"env"`
	} `json:"sandbox"`
}

// LoadConfig reads a configuration file. "~/" in paths means the home
// directory.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	home, _ := os.UserHomeDir()
	fix := func(p *string) {
		if rest, ok := strings.CutPrefix(*p, "~/"); ok {
			*p = filepath.Join(home, rest)
		}
	}
	fix(&c.TokenFile)
	fix(&c.StateDir)
	fix(&c.Agent.Path)
	for i := range c.Sandbox.ReadOnly {
		fix(&c.Sandbox.ReadOnly[i])
	}
	for i := range c.Sandbox.Shared {
		fix(&c.Sandbox.Shared[i])
	}
	for k, v := range c.Sandbox.Env {
		fix(&v)
		c.Sandbox.Env[k] = v
	}
	if c.Server == "" || c.Name == "" || c.TokenFile == "" || c.StateDir == "" {
		return c, fmt.Errorf("%s: server, name, token_file, and state_dir are required", path)
	}
	if c.Agent.Python == "" {
		c.Agent.Python = "python3"
	}
	for name, r := range c.Repos {
		if r.DefaultBranch == "" {
			r.DefaultBranch = "main"
			c.Repos[name] = r
		}
	}
	return c, nil
}

// Steps are the code-change steps that a runner runs.
var Steps = []string{"prepare", "work", "check", "merge", "publish", "cleanup"}

const lease = 2 * time.Minute

// Runner runs steps.
type Runner struct {
	Cfg    Config
	Log    *slog.Logger
	Magi   *magiapi.RunnerClient
	Boxes  *sandbox.Sandboxes
	Source *sourceserver.Service
	// exec is the loopback execution API for the agent.
	execURL, execTokenFile string
}

// Result is the outcome of one step.
type Result struct {
	Outcome string
	Outputs map[string]string
}

type handler func(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error)

// Run claims and runs steps until ctx ends.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.ensureRepos(ctx); err != nil {
		return err
	}
	stopExec, err := r.startExec()
	if err != nil {
		return err
	}
	defer stopExec()
	handlers := map[string]handler{"prepare": r.prepare, "work": r.work, "check": r.check,
		"merge": r.merge, "publish": r.publish, "cleanup": r.cleanup}
	for _, step := range Steps {
		go r.loop(ctx, step, handlers[step])
	}
	go r.collect(ctx)
	r.Log.Info("runner started", "server", r.Cfg.Server, "name", r.Cfg.Name)
	<-ctx.Done()
	return nil
}

func (r *Runner) loop(ctx context.Context, step string, h handler) {
	queue := "runner." + r.Cfg.Name + "." + step
	for ctx.Err() == nil {
		res, err := r.Magi.Claim(ctx, magiapi.RunnerClaimRequest{Queue: queue, WaitMs: ptr(uint64(20000))})
		if err != nil {
			if ctx.Err() == nil {
				r.Log.Warn("claim failed", "queue", queue, "err", err)
				sleep(ctx, 5*time.Second)
			}
			continue
		}
		if res.Assignment != nil {
			r.handle(ctx, *res.Assignment, h)
		}
	}
}

// handle runs one step with heartbeats. A lost claim (for example a
// cancelled run) stops the step.
func (r *Runner) handle(ctx context.Context, a magiapi.RunnerAssignment, h handler) {
	log := r.Log.With("run", a.Run, "step", a.Step, "attempt", a.Attempt)
	st := magiapi.RunnerStep{Run: a.Run, Step: a.Step, Claim: a.Claim}
	hctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(lease / 3)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				if _, err := r.Magi.Heartbeat(ctx, st); err != nil {
					log.Warn("heartbeat failed", "err", err)
					if c := rpc.CodeOf(err); c == 409 || c == 404 {
						stop()
						return
					}
				}
			}
		}
	}()
	log.Info("step started")
	res, err := h(hctx, a, st)
	if ctx.Err() != nil {
		log.Warn("stopped before the step finished; the lease will expire")
		return
	}
	if hctx.Err() != nil {
		log.Warn("claim lost; not completing")
		return
	}
	req := magiapi.RunnerCompleteRequest{Step: st, Outcome: res.Outcome, Outputs: res.Outputs}
	if err != nil {
		log.Error("step failed", "err", err)
		req.Outcome, req.Error = "fail", ptr(err.Error())
	}
	if _, err := r.Magi.Complete(ctx, req); err != nil {
		log.Error("complete failed", "err", err)
		return
	}
	log.Info("step finished", "outcome", req.Outcome)
}

func (r *Runner) note(ctx context.Context, st magiapi.RunnerStep, msg string) {
	if _, err := r.Magi.Progress(ctx, magiapi.RunnerProgressRequest{Step: st, Note: msg}); err != nil {
		r.Log.Warn("progress failed", "err", err)
	}
}

func (r *Runner) post(ctx context.Context, st magiapi.RunnerStep, text string) {
	if _, err := r.Magi.Post(ctx, magiapi.RunnerPostRequest{Step: st, Text: text}); err != nil {
		r.Log.Warn("post failed", "err", err)
	}
}

// collect removes the environments of runs that ended, also after a
// cancel, when no cleanup step runs.
func (r *Runner) collect(ctx context.Context) {
	for ctx.Err() == nil {
		for _, id := range r.Boxes.List() {
			st, err := r.Magi.RunStatus(ctx, magiapi.RunnerRunStatusRequest{Run: id})
			switch {
			case rpc.CodeOf(err) == 404, err == nil && (st.Status == "succeeded" || st.Status == "failed" || st.Status == "cancelled"):
				r.Log.Info("removing environment of an ended run", "run", id)
				r.Boxes.Remove(id)
			case err == nil && !r.Boxes.Alive(id):
				// The runner restarted while the run waits (for example
				// for review): start envd again on the kept work.
				if err := r.Boxes.Resume(id, r.sandboxEnv()); err != nil {
					r.Log.Warn("resume environment", "run", id, "err", err)
				}
			}
		}
		sleep(ctx, 2*time.Minute)
	}
}

func ptr[T any](v T) *T { return &v }

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
