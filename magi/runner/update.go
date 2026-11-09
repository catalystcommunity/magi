package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
)

// Self-update. A supervised runner keeps its releases in
// <state>/releases/<version>/ and runs <state>/current/magi-runner, where
// current links to one release:
//
//  1. Magi answers a hello with the release that the runner must run.
//  2. The runner stops claiming, waits until no step runs, downloads the
//     package, checks its digest, unpacks it, and runs its self-test.
//  3. It writes a trial record, points current at the new release, and
//     exits with ExitRestart.
//  4. The supervisor starts the new release. The new release marks itself
//     healthy after its first hello. If it exits twice, or is not healthy
//     in TrialTimeout, the supervisor points current back at the old
//     release and records the error; the runner reports it in its hello,
//     and Magi does not offer that release to it again.

// ExitRestart is the exit code that asks the supervisor to start current.
const ExitRestart = 75

// TrialTimeout is how long a new release has to become healthy.
var TrialTimeout = 3 * time.Minute

var restartDelay = 5 * time.Second

// ErrRestart stops Run so that the supervisor starts the new release.
var ErrRestart = errors.New("restart into the new release")

const envSupervised = "MAGI_RUNNER_SUPERVISED"

var versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type trial struct {
	Version  string `json:"version"`
	Previous string `json:"previous"`
	Failures int    `json:"failures"`
}

func releasesDir(state string) string { return filepath.Join(state, "releases") }
func currentLink(state string) string { return filepath.Join(state, "current") }

// Version returns the release that this binary belongs to: the VERSION
// file next to the executable, or "dev".
func Version() string {
	exe, err := os.Executable()
	if err != nil {
		return "dev"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "VERSION"))
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(b))
}

// Install unpacks a package as release version and makes it current. It
// is for a first install; updates go through the trial.
func Install(state, version string, pkg []byte) error {
	if err := unpackRelease(state, version, pkg); err != nil {
		return err
	}
	return switchCurrent(state, version)
}

func unpackRelease(state, version string, pkg []byte) error {
	if !versionRE.MatchString(version) {
		return fmt.Errorf("bad version %q", version)
	}
	dir := filepath.Join(releasesDir(state), version)
	tmp := dir + ".new"
	os.RemoveAll(tmp)
	if err := Unpack(pkg, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

// switchCurrent points current at a release, atomically.
func switchCurrent(state, version string) error {
	tmp := currentLink(state) + ".new"
	os.Remove(tmp)
	if err := os.Symlink(filepath.Join("releases", version), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, currentLink(state))
}

func currentVersion(state string) string {
	t, err := os.Readlink(currentLink(state))
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

// prune removes releases other than keep.
func prune(state string, keep ...string) {
	entries, _ := os.ReadDir(releasesDir(state))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		used := false
		for _, k := range keep {
			used = used || e.Name() == k
		}
		if !used {
			os.RemoveAll(filepath.Join(releasesDir(state), e.Name()))
		}
	}
}

func readJSON(p string, v any) bool {
	b, err := os.ReadFile(p)
	return err == nil && json.Unmarshal(b, v) == nil
}

func writeJSON(p string, v any) error {
	b, _ := json.Marshal(v)
	tmp := p + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (r *Runner) updateError() string {
	b, _ := os.ReadFile(filepath.Join(r.Cfg.StateDir, "update-error"))
	return strings.TrimSpace(string(b))
}

func (r *Runner) setUpdateError(version, reason string) {
	msg := fmt.Sprintf("update to %s failed: %s", version, reason)
	r.Log.Error("update failed", "version", version, "reason", reason)
	os.WriteFile(filepath.Join(r.Cfg.StateDir, "update-error"), []byte(msg), 0o600)
}

func (r *Runner) hello(ctx context.Context) (*magiapi.RunnerUpdate, error) {
	var repos, pubs []string
	for name, repo := range r.Cfg.Repos {
		repos = append(repos, name)
		if repo.Publish {
			pubs = append(pubs, name)
		}
	}
	sort.Strings(repos)
	sort.Strings(pubs)
	req := magiapi.RunnerHelloRequest{Version: r.version, Os: runtime.GOOS, Arch: runtime.GOARCH,
		Repos: nonNil(repos), Publishes: nonNil(pubs), Busy: r.busy.Load() > 0}
	if e := r.updateError(); e != "" {
		req.UpdateError = &e
	}
	res, err := r.Magi.Hello(ctx, req)
	if err != nil {
		return nil, err
	}
	// The first hello of a new release makes it healthy.
	os.WriteFile(filepath.Join(r.Cfg.StateDir, "healthy"), []byte(r.version), 0o600)
	return res.Update, nil
}

// helloLoop says hello once a minute and starts an update when Magi asks.
func (r *Runner) helloLoop(ctx context.Context, restart func()) {
	for ctx.Err() == nil {
		u, err := r.hello(ctx)
		if err != nil {
			r.Log.Warn("hello failed", "err", err)
		} else if u != nil && !r.updating.Load() {
			if os.Getenv(envSupervised) == "" {
				r.Log.Warn("an update is available, but the runner is not supervised", "version", u.Version)
			} else {
				r.updating.Store(true)
				go func(u magiapi.RunnerUpdate) {
					if err := r.update(ctx, u); err != nil {
						r.setUpdateError(u.Version, err.Error())
						r.updating.Store(false)
						return
					}
					restart()
				}(*u)
			}
		}
		sleep(ctx, time.Minute)
	}
}

// update installs release u as a trial. On success the caller restarts.
func (r *Runner) update(ctx context.Context, u magiapi.RunnerUpdate) error {
	r.Log.Info("update: waiting until no step runs", "version", u.Version)
	for r.busy.Load() > 0 {
		sleep(ctx, 5*time.Second)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	pkg, err := r.Magi.GetPackage(ctx, magiapi.RunnerGetPackageRequest{Version: u.Version, Os: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	sum := sha256.Sum256(pkg.Body)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != u.Digest {
		return fmt.Errorf("the package digest is %s, not %s", got, u.Digest)
	}
	state := r.Cfg.StateDir
	if err := unpackRelease(state, u.Version, pkg.Body); err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	exe := filepath.Join(releasesDir(state), u.Version, "magi-runner")
	tctx, cancel := context.WithTimeout(ctx, 6*time.Minute) // a macOS task VM boots in the self-test
	defer cancel()
	cmd := exec.CommandContext(tctx, exe, "selftest", "-config", r.cfgPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("self-test: %v: %s", err, tail(string(out), 1500))
	}
	prev := currentVersion(state)
	if err := writeJSON(filepath.Join(state, "trial"), trial{Version: u.Version, Previous: prev}); err != nil {
		return err
	}
	if err := switchCurrent(state, u.Version); err != nil {
		os.Remove(filepath.Join(state, "trial"))
		return err
	}
	os.Remove(filepath.Join(state, "update-error"))
	prune(state, u.Version, prev)
	r.Log.Info("update: switching to the new release", "version", u.Version, "previous", prev)
	return nil
}

// SelfTest checks that this release works on this machine: the
// configuration loads, a sandbox starts and runs a command, the
// apprentice imports, and Magi accepts the runner token. The sandbox has a
// proxy when this sandbox kind can have one, as tasks do (the proxy is not
// used: the command needs no network).
func SelfTest(ctx context.Context, r *Runner) error {
	root, err := os.MkdirTemp(r.Cfg.StateDir, "selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	cfg := r.Boxes.Cfg
	cfg.Root = root
	boxes := &sandbox.Sandboxes{Cfg: cfg}
	env, proxy := r.sandboxEnv(), (*sandbox.Proxy)(nil)
	if boxes.CanProxy() {
		proxy = &sandbox.Proxy{CA: []byte("self-test")}
		env = r.proxyEnv("selftest", "self-test", nil)
	}
	if err := boxes.Start("selftest", strings.Repeat("s", 32), env, proxy); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	defer boxes.Remove("selftest")
	r.Boxes = boxes
	res, err := r.exec(ctx, "selftest", boxes.Work("selftest"), "git --version && echo ok", 30000)
	if err != nil || res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "ok") {
		return fmt.Errorf("sandbox command: %v %s %s", err, res.Stdout, res.Stderr)
	}
	py := exec.CommandContext(ctx, r.Cfg.Apprentice.Python, "-c", "import magi_apprentice.__main__")
	py.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONPATH=" + r.Cfg.Apprentice.Path}
	if out, err := py.CombinedOutput(); err != nil {
		return fmt.Errorf("apprentice import: %v %s", err, out)
	}
	_, err = r.Magi.RunStatus(ctx, magiapi.RunnerRunStatusRequest{Run: "run_selftest"})
	if c := rpc.CodeOf(err); err != nil && c != 404 {
		return fmt.Errorf("magi: %w", err)
	}
	return nil
}

// Supervise runs <state>/current/magi-runner -config cfgPath and keeps it
// running. It rolls a trial release back when the release fails. When ctx
// ends, it stops the runner with SIGTERM and returns.
func Supervise(ctx context.Context, cfgPath string, cfg Config, log *slog.Logger) error {
	state := cfg.StateDir
	healthy := func(v string) bool {
		b, _ := os.ReadFile(filepath.Join(state, "healthy"))
		return strings.TrimSpace(string(b)) == v
	}
	// clearTrial removes the trial record only when it is still about
	// version v: a healthy release can write a new trial for its own update.
	clearTrial := func(v string) {
		var cur trial
		if readJSON(filepath.Join(state, "trial"), &cur) && cur.Version == v {
			os.Remove(filepath.Join(state, "trial"))
		}
	}
	rollback := func(t trial, reason string) {
		log.Error("rolling back the update", "version", t.Version, "previous", t.Previous, "reason", reason)
		if t.Previous != "" {
			if err := switchCurrent(state, t.Previous); err != nil {
				log.Error("rollback failed", "err", err)
			}
		}
		os.WriteFile(filepath.Join(state, "update-error"), []byte(fmt.Sprintf("update to %s failed: %s; back on %s", t.Version, reason, t.Previous)), 0o600)
		clearTrial(t.Version)
	}
	for {
		exe := filepath.Join(currentLink(state), "magi-runner")
		if _, err := os.Stat(exe); err != nil {
			return fmt.Errorf("no current release: %w (run tools/install-runner.sh)", err)
		}
		var t trial
		inTrial := readJSON(filepath.Join(state, "trial"), &t)
		cmd := exec.Command(exe, "-config", cfgPath)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), envSupervised+"=1")
		log.Info("starting runner", "version", currentVersion(state), "trial", inTrial)
		started := time.Now()
		if err := cmd.Start(); err != nil {
			if inTrial {
				rollback(t, "start: "+err.Error())
				continue
			}
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var timeout <-chan time.Time
		if inTrial {
			timeout = time.After(TrialTimeout)
		}
		tick := time.NewTicker(time.Second)
		var err error
		exited, rolledBack := false, false
		for !exited && !rolledBack {
			select {
			case <-tick.C:
				// The trial ends when the release is healthy.
				if inTrial && healthy(t.Version) {
					clearTrial(t.Version)
					inTrial, timeout = false, nil
					log.Info("the new release is healthy", "version", t.Version)
				}
			case <-ctx.Done():
				cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(60 * time.Second):
					cmd.Process.Kill()
				}
				return nil
			case <-timeout:
				timeout = nil
				if inTrial && !healthy(t.Version) {
					cmd.Process.Kill()
					<-done
					rollback(t, fmt.Sprintf("not healthy after %s", TrialTimeout))
					rolledBack = true
				}
			case err = <-done:
				exited = true
			}
		}
		tick.Stop()
		if rolledBack {
			continue
		}
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		if inTrial {
			if healthy(t.Version) {
				clearTrial(t.Version)
			} else if code != ExitRestart {
				t.Failures++
				if t.Failures >= 2 {
					rollback(t, fmt.Sprintf("exited with code %d before its first hello", code))
					continue
				}
				writeJSON(filepath.Join(state, "trial"), t)
			}
		}
		if code == ExitRestart {
			continue
		}
		log.Warn("runner exited", "code", code, "after", time.Since(started).Round(time.Second))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(restartDelay):
		}
	}
}
