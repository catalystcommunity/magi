// Package runner runs Magi code-change steps on this machine. It connects
// to Magi's public API with a runner token and claims steps from its own
// queues (runner.<name>.<step>). It never listens on the network.
//
// The runner keeps a mirror of each repository with an upstream (for
// example GitHub) and uses this machine's Git credentials for it. Each task
// gets a bubblewrap sandbox; the apprentice runs on this machine and uses
// the sandbox through a loopback execution API. Model calls go through
// Magi, so the machine holds no provider key.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/catalystcommunity/magi/primitives/identity/renew"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	// Build makes the merge step build magi-server for the release and the
	// runner packages for a runner release.
	Build bool `json:"build"`
	// Publish lets this runner push merged changes to the upstream with
	// this machine's Git credentials. Magi sends publish steps only to a
	// runner that publishes the repository.
	Publish bool `json:"publish"`
	// PullRequests: publish pushes the change to the branch magi/<run> and
	// opens a pull request on GitHub with the gh command of this machine,
	// in place of a push to the default branch. The change lands when a
	// person merges it.
	PullRequests bool `json:"pull_requests"`
}

// Config is the runner configuration file.
type Config struct {
	// Server is the Magi base URL, for example https://magi.example.com.
	Server string `json:"server"`
	// Name is the runner name; the token subject is runner:<name>.
	Name      string `json:"name"`
	TokenFile string `json:"token_file"`
	// NoRenew: do not renew the token. By default the runner replaces
	// token_file with a renewed token before it expires
	// (primitives/identity/renew).
	NoRenew  bool            `json:"no_renew"`
	StateDir string          `json:"state_dir"`
	Repos    map[string]Repo `json:"repos"`
	// Author is the Git author of the commits that the runner makes.
	Author struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
	Apprentice struct {
		Python string `json:"python"`
		// Path is the PYTHONPATH with magi_apprentice and the Python
		// clients. Default: apprentice/ next to the magi-runner binary.
		Path     string `json:"path"`
		MaxSteps int    `json:"max_steps"`
	} `json:"apprentice"`
	Sandbox struct {
		ReadOnly []string          `json:"read_only"`
		Shared   []string          `json:"shared"`
		Env      map[string]string `json:"env"`
		// VM makes each environment a task VM (Cloud Hypervisor, Linux)
		// when Kernel is set. Hypervisor defaults to cloud-hypervisor in
		// the runner tools (tools/install-runner-tools.sh), Virtiofsd to
		// the virtiofsd of the distribution.
		VM struct {
			// Template is a macOS template directory (tools/vm/macos);
			// on a Mac it makes each environment a macOS VM (magi-vz).
			Template   string `json:"template"`
			Kernel     string `json:"kernel"`
			Hypervisor string `json:"hypervisor"`
			Virtiofsd  string `json:"virtiofsd"`
			CPUs       int    `json:"cpus"`
			MemoryMB   int    `json:"memory_mb"`
		} `json:"vm"`
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
	fix(&c.Apprentice.Path)
	for i := range c.Sandbox.ReadOnly {
		fix(&c.Sandbox.ReadOnly[i])
	}
	for i := range c.Sandbox.Shared {
		fix(&c.Sandbox.Shared[i])
	}
	fix(&c.Sandbox.VM.Kernel)
	fix(&c.Sandbox.VM.Template)
	fix(&c.Sandbox.VM.Hypervisor)
	fix(&c.Sandbox.VM.Virtiofsd)
	for k, v := range c.Sandbox.Env {
		fix(&v)
		c.Sandbox.Env[k] = v
	}
	if c.Server == "" || c.Name == "" || c.TokenFile == "" || c.StateDir == "" {
		return c, fmt.Errorf("%s: server, name, token_file, and state_dir are required", path)
	}
	if c.Author.Name == "" {
		c.Author.Name = "Magi"
	}
	if c.Author.Email == "" {
		c.Author.Email = "magi@example.local"
	}
	if c.Apprentice.Python == "" {
		c.Apprentice.Python = "python3"
	}
	fix(&c.Apprentice.Python)
	if c.Apprentice.Path == "" {
		// The apprentice of this release, or of the current release when
		// the launcher runs without the supervisor.
		c.Apprentice.Path = filepath.Join(c.StateDir, "current", "apprentice")
		if exe, err := os.Executable(); err == nil {
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			if p := filepath.Join(filepath.Dir(exe), "apprentice"); dirExists(p) {
				c.Apprentice.Path = p
			}
		}
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
var Steps = []string{"prepare", "work", "check", "merge", "publish", "pr-check", "cleanup"}

const lease = 2 * time.Minute

// Runner runs steps.
type Runner struct {
	Cfg    Config
	Log    *slog.Logger
	Magi   *magiapi.RunnerClient
	Boxes  *sandbox.Sandboxes
	Source *sourceserver.Service
	// exec is the loopback execution API for the apprentice.
	execURL, execTokenFile string
	// cfgPath is the configuration file; a new release gets it too.
	cfgPath  string
	version  string
	busy     atomic.Int64 // steps that run now
	updating atomic.Bool  // no new claims during an update
	// proxies are the proxy sockets of the environments (egress.go).
	mu      sync.Mutex
	proxies map[string]net.Listener
}

// New returns a runner for the configuration file at cfgPath.
func New(cfgPath string, cfg Config, log *slog.Logger, m *magiapi.RunnerClient, boxes *sandbox.Sandboxes, src *sourceserver.Service) *Runner {
	return &Runner{Cfg: cfg, Log: log, Magi: m, Boxes: boxes, Source: src, cfgPath: cfgPath, version: Version()}
}

// Result is the outcome of one step.
type Result struct {
	Outcome string
	Outputs map[string]string
}

type handler func(ctx context.Context, a magiapi.RunnerAssignment, st magiapi.RunnerStep) (Result, error)

// Run claims and runs steps until ctx ends. It returns ErrRestart when an
// update is installed and the supervisor must start the new release.
func (r *Runner) Run(ctx context.Context) error {
	if r.version == "" {
		r.version = Version()
	}
	if err := r.ensureRepos(ctx); err != nil {
		return err
	}
	ctx, stopAll := context.WithCancel(ctx)
	defer stopAll()
	if !r.Cfg.NoRenew {
		go (&renew.Renewer{File: r.Cfg.TokenFile, URL: r.Cfg.Server, Log: r.Log}).Run(ctx)
	}
	restart := make(chan struct{})
	go r.helloLoop(ctx, func() { close(restart) })
	stopExec, err := r.startExec()
	if err != nil {
		return err
	}
	defer stopExec()
	handlers := map[string]handler{"prepare": r.prepare, "work": r.work, "check": r.check,
		"merge": r.merge, "publish": r.publish, "pr-check": r.prCheck, "cleanup": r.cleanup}
	for _, step := range Steps {
		go r.loop(ctx, step, handlers[step])
	}
	go r.collect(ctx)
	r.Log.Info("runner started", "server", r.Cfg.Server, "name", r.Cfg.Name, "version", r.version)
	select {
	case <-ctx.Done():
		return nil
	case <-restart:
		return ErrRestart
	}
}

func (r *Runner) loop(ctx context.Context, step string, h handler) {
	queue := "runner." + r.Cfg.Name + "." + step
	for ctx.Err() == nil {
		if r.updating.Load() {
			sleep(ctx, 5*time.Second)
			continue
		}
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
	r.busy.Add(1)
	defer r.busy.Add(-1)
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

// report posts text as a notice with title to the devices of the person
// (urgency "low" shows it, "normal" says it at the next pause).
func (r *Runner) report(ctx context.Context, st magiapi.RunnerStep, urgency, title, text string) {
	if _, err := r.Magi.Post(ctx, magiapi.RunnerPostRequest{Step: st, Text: text, Notice: &title, Urgency: &urgency}); err != nil {
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
				r.removeBox(id)
			case err == nil && !r.Boxes.Alive(id):
				// The runner restarted while the run waits (for example
				// for review): start envd again on the kept work.
				if err := r.resumeBox(id); err != nil {
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

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
