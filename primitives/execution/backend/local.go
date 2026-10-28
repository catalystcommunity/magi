package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
)

// Local runs envd as a child process on the control plane host, in a
// directory under Root. It gives no isolation. It is for tests and is the
// model for a host backend. The image field is ignored.
type Local struct {
	Root string
	Envd *Binaries
	mu   sync.Mutex
	cmds map[string]*exec.Cmd
}

var _ Backend = (*Local)(nil)

func (l *Local) dir(id string) string { return filepath.Join(l.Root, id) }

func (l *Local) Create(_ context.Context, id string, spec api.EnvironmentSpec, token string) error {
	bin, err := l.Envd.Path("linux-" + runtime.GOARCH)
	if err != nil {
		return err
	}
	d := l.dir(id)
	if err := os.MkdirAll(filepath.Join(d, "work"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d, "token"), []byte(token), 0o600); err != nil {
		return err
	}
	cmd := exec.Command(bin, "-listen", "127.0.0.1:0", "-token-file", filepath.Join(d, "token"),
		"-dir", filepath.Join(d, "work"), "-addr-file", filepath.Join(d, "addr"))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(d, "work")}
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	log, err := os.Create(filepath.Join(d, "envd.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return err
	}
	l.mu.Lock()
	if l.cmds == nil {
		l.cmds = map[string]*exec.Cmd{}
	}
	l.cmds[id] = cmd
	l.mu.Unlock()
	go func() {
		cmd.Wait()
		os.WriteFile(filepath.Join(d, "exited"), nil, 0o600)
	}()
	return nil
}

func (l *Local) pid(id string) (int, error) {
	b, err := os.ReadFile(filepath.Join(l.dir(id), "pid"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func (l *Local) alive(id string) bool {
	if _, err := os.Stat(filepath.Join(l.dir(id), "exited")); err == nil {
		return false
	}
	pid, err := l.pid(id)
	return err == nil && syscall.Kill(pid, 0) == nil
}

func (l *Local) Status(_ context.Context, id string) (Status, error) {
	if _, err := os.Stat(l.dir(id)); errors.Is(err, os.ErrNotExist) {
		return Status{State: Gone, Message: "environment directory is gone"}, nil
	}
	if !l.alive(id) {
		return Status{State: Failed, Message: "envd exited"}, nil
	}
	addr, err := os.ReadFile(filepath.Join(l.dir(id), "addr"))
	if err != nil {
		return Status{State: Creating}, nil
	}
	return Status{State: Ready, Endpoint: "http://" + string(addr)}, nil
}

func (l *Local) Destroy(_ context.Context, id string) error {
	if pid, err := l.pid(id); err == nil {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
	l.mu.Lock()
	cmd := l.cmds[id]
	delete(l.cmds, id)
	l.mu.Unlock()
	if cmd != nil {
		for i := 0; i < 50 && l.alive(id); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := os.RemoveAll(l.dir(id)); err != nil {
		return fmt.Errorf("remove environment directory: %w", err)
	}
	return nil
}

func (l *Local) HTTP() *http.Client { return &http.Client{} }
