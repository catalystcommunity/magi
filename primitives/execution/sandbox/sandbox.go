// Package sandbox runs envd for one environment in a bubblewrap sandbox on
// this machine.
//
// The sandbox sees only its work directory, read-only system directories,
// and the directories in Config.ReadOnly and Config.Shared. It does not see
// the home directory of the user that runs it, so it cannot read that
// user's credentials. It shares the host network; envd listens on loopback
// only. Nothing here listens on the network: a runner calls envd on
// loopback and connects outward to Magi.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Config is the sandbox configuration.
type Config struct {
	// Root holds one directory per environment.
	Root string
	// Envd is the envd binary. It is mounted read-only in the sandbox.
	// EnvdArgs are put before the envd flags (for example ["envd"] when
	// the binary is a command that also runs as envd).
	Envd     string
	EnvdArgs []string
	// Bwrap is the bubblewrap binary.
	Bwrap string
	// ReadOnly lists host paths that the sandbox sees read-only at the same
	// path, for example a toolchain directory.
	ReadOnly []string
	// Shared lists host paths that the sandbox can write, shared by all
	// environments, for example a Go module cache.
	Shared []string
	// Env is the base environment of envd.
	Env map[string]string
}

// Sandboxes starts and removes environments.
type Sandboxes struct {
	Cfg  Config
	mu   sync.Mutex
	cmds map[string]*exec.Cmd
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Dir is the host directory of an environment.
func (s *Sandboxes) Dir(id string) string { return filepath.Join(s.Cfg.Root, id) }

// WorkDir is the host directory that the sandbox sees as /work.
func (s *Sandboxes) WorkDir(id string) string { return filepath.Join(s.Dir(id), "work") }

// args returns the bubblewrap arguments for one environment.
func (s *Sandboxes) args(d string, env map[string]string) []string {
	a := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--clearenv", "--ro-bind", "/usr", "/usr"}
	// Merged-/usr systems link /bin, /lib, and so on into /usr.
	for _, p := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		if t, err := os.Readlink(p); err == nil {
			a = append(a, "--symlink", t, p)
		} else if _, err := os.Stat(p); err == nil {
			a = append(a, "--ro-bind", p, p)
		}
	}
	// Only the /etc files that programs need for names, users, and TLS.
	for _, p := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/passwd", "/etc/group",
		"/etc/ssl", "/etc/ca-certificates", "/etc/pki", "/etc/localtime"} {
		if _, err := os.Stat(p); err == nil {
			a = append(a, "--ro-bind", p, p)
		}
	}
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		"--bind", filepath.Join(d, "work"), "/work",
		"--ro-bind", filepath.Join(d, "token"), "/run/magi/token",
		"--bind", filepath.Join(d, "state"), "/run/magi/state",
		"--ro-bind", s.Cfg.Envd, "/run/magi/envd")
	for _, p := range s.Cfg.ReadOnly {
		a = append(a, "--ro-bind", p, p)
	}
	for _, p := range s.Cfg.Shared {
		a = append(a, "--bind", p, p)
	}
	all := map[string]string{"HOME": "/work/home", "TMPDIR": "/tmp", "PATH": "/usr/local/bin:/usr/bin:/bin"}
	for k, v := range s.Cfg.Env {
		all[k] = v
	}
	for k, v := range env {
		all[k] = v
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a = append(a, "--setenv", k, all[k])
	}
	a = append(a, "--chdir", "/work", "/run/magi/envd")
	a = append(a, s.Cfg.EnvdArgs...)
	return append(a, "-listen", "127.0.0.1:0", "-token-file", "/run/magi/token", "-dir", "/work", "-addr-file", "/run/magi/state/addr")
}

// Start starts the sandbox of a new environment. token is the secret that
// envd requires. Start does not wait until envd is ready; see Wait.
func (s *Sandboxes) Start(id, token string, env map[string]string) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("bad environment id %q", id)
	}
	if len(token) < 16 {
		return errors.New("the token must have at least 16 bytes")
	}
	d := s.Dir(id)
	if _, err := os.Stat(d); err == nil {
		return fmt.Errorf("environment %s exists", id)
	}
	for _, sub := range []string{"work/home", "state"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(d, "token"), []byte(token), 0o600); err != nil {
		return err
	}
	return s.launch(id, env)
}

// Resume starts envd again in an existing environment, for example after
// the runner restarted. The work directory is kept.
func (s *Sandboxes) Resume(id string, env map[string]string) error {
	if s.Alive(id) {
		return nil
	}
	os.Remove(filepath.Join(s.Dir(id), "state", "addr"))
	os.Remove(filepath.Join(s.Dir(id), "exited"))
	return s.launch(id, env)
}

func (s *Sandboxes) launch(id string, env map[string]string) error {
	d := s.Dir(id)
	cmd := exec.Command(s.Cfg.Bwrap, s.args(d, env)...)
	log, err := os.OpenFile(filepath.Join(d, "envd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.cmds == nil {
		s.cmds = map[string]*exec.Cmd{}
	}
	s.cmds[id] = cmd
	s.mu.Unlock()
	go func() {
		cmd.Wait()
		os.WriteFile(filepath.Join(d, "exited"), nil, 0o600)
	}()
	return nil
}

// Exists reports whether the environment directory exists.
func (s *Sandboxes) Exists(id string) bool {
	_, err := os.Stat(s.Dir(id))
	return idRE.MatchString(id) && err == nil
}

// Alive reports whether envd of the environment runs.
func (s *Sandboxes) Alive(id string) bool {
	s.mu.Lock()
	cmd := s.cmds[id]
	s.mu.Unlock()
	if cmd == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.Dir(id), "exited"))
	return errors.Is(err, os.ErrNotExist)
}

// Token returns the envd token of an environment.
func (s *Sandboxes) Token(id string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "token"))
	return string(b), err
}

// Wait returns the envd base URL once envd listens, or an error.
func (s *Sandboxes) Wait(id string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !s.Alive(id) {
			b, _ := os.ReadFile(filepath.Join(s.Dir(id), "envd.log"))
			return "", fmt.Errorf("envd exited: %s", strings.TrimSpace(string(b)))
		}
		if b, err := os.ReadFile(filepath.Join(s.Dir(id), "state", "addr")); err == nil && len(b) > 0 {
			return "http://" + strings.TrimSpace(string(b)), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("envd of %s did not start in %s", id, timeout)
}

// Remove stops the environment and deletes its directory.
func (s *Sandboxes) Remove(id string) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("bad environment id %q", id)
	}
	s.mu.Lock()
	cmd := s.cmds[id]
	delete(s.cmds, id)
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(filepath.Join(s.Dir(id), "exited")); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return os.RemoveAll(s.Dir(id))
}

// List returns the environment ids on disk.
func (s *Sandboxes) List() []string {
	entries, _ := os.ReadDir(s.Cfg.Root)
	var out []string
	for _, e := range entries {
		if e.IsDir() && idRE.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}
