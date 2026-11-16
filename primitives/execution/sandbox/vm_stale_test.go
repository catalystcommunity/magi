package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// script writes an executable shell script with body and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	p := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// staleProc starts a stand-in for a process that an earlier launch left,
// in its own process group as virtiofsd and the hypervisor are, and
// reports when it is gone.
func staleProc(t *testing.T, dir string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	gone := make(chan struct{})
	go func() { cmd.Wait(); close(gone) }()
	return cmd, gone
}

func stopped(t *testing.T, gone chan struct{}) {
	t.Helper()
	select {
	case <-gone:
	case <-time.After(10 * time.Second):
		t.Fatal("the process still runs")
	}
}

// Before a launch starts virtiofsd, it stops the processes that an earlier
// launch of the same environment left: the pid files of the environment
// name them. A process of an other environment is never stopped, also when
// a pid file of this environment names it.
func TestVMStaleProcesses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the pid files are read from /proc")
	}
	root := t.TempDir()
	s := &Sandboxes{Cfg: Config{Root: root, VM: &VM{}}}
	id := "run_" + strings.Repeat("s", 26)
	d := s.Dir(id)
	for _, sub := range []string{"work/home", "state", "tmp", "vm"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// An old virtiofsd: the pid file in the vm dir names it, and its
	// working directory is the environment directory.
	fsd, fsdGone := staleProc(t, d)
	if err := os.WriteFile(filepath.Join(d, "vm", "fs.sock.pid"), []byte(strconv.Itoa(fsd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	// A process of an other environment: this environment names it in its
	// own pid file, so the launch must leave it alone.
	otherDir := filepath.Join(root, "run_"+strings.Repeat("o", 26))
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	other, _ := staleProc(t, otherDir)
	defer syscall.Kill(other.Process.Pid, syscall.SIGKILL)
	if err := os.WriteFile(filepath.Join(d, "pid"), []byte(strconv.Itoa(other.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Socket files that an earlier launch left.
	for _, f := range []string{"fs.sock", "vsock.sock", "vsock.sock_3128"} {
		if err := os.WriteFile(filepath.Join(d, "vm", f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s.stopVMProcesses(id)
	s.removeVMSockets(id)
	stopped(t, fsdGone)
	if err := syscall.Kill(other.Process.Pid, 0); err != nil {
		t.Fatalf("the launch stopped a process of an other environment: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d, "vm", "fs.sock.pid")); err == nil {
		t.Fatal("the pid file of the old virtiofsd is still there")
	}
	for _, f := range []string{"fs.sock", "vsock.sock", "vsock.sock_3128"} {
		if _, err := os.Stat(filepath.Join(d, "vm", f)); err == nil {
			t.Fatalf("the stale socket %s is still there", f)
		}
	}
}

// A pid file that names no process of this environment stops nothing: the
// host gives pids of ended processes to new processes.
func TestVMStalePidNotOurs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the pid files are read from /proc")
	}
	root := t.TempDir()
	s := &Sandboxes{Cfg: Config{Root: root, VM: &VM{}}}
	id := "run_" + strings.Repeat("n", 26)
	d := s.Dir(id)
	if err := os.MkdirAll(filepath.Join(d, "vm"), 0o700); err != nil {
		t.Fatal(err)
	}
	mine, _ := staleProc(t, t.TempDir())
	defer syscall.Kill(mine.Process.Pid, syscall.SIGKILL)
	for _, pid := range []string{"1", "not a pid", strconv.Itoa(mine.Process.Pid)} {
		if err := os.WriteFile(filepath.Join(d, "vm", "fs.sock.pid"), []byte(pid), 0o600); err != nil {
			t.Fatal(err)
		}
		s.stopVMProcesses(id)
	}
	if err := syscall.Kill(mine.Process.Pid, 0); err != nil {
		t.Fatalf("the launch stopped a process that does not name the environment: %v", err)
	}
}

// Two resumes of one environment at the same time (a step and the collect
// loop) launch once. The launch stops the processes that the earlier
// launch of the same environment left.
func TestVMResumeOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("task VMs with Cloud Hypervisor need Linux")
	}
	root := t.TempDir()
	count := filepath.Join(root, "launches")
	envs := filepath.Join(root, "envs")
	s := func() *Sandboxes {
		vd := filepath.Join(envs, "run_"+strings.Repeat("r", 26), "vm")
		return &Sandboxes{Cfg: Config{Root: envs, Envd: "envd",
			Bwrap: script(t, "echo launch >> "+count+"\nsleep 1\ntouch "+filepath.Join(vd, "fs.sock")+"\nsleep 30"),
			VM:    &VM{Kernel: "kernel", Hypervisor: script(t, "sleep 30")}}}
	}
	id := "run_" + strings.Repeat("r", 26)
	d := filepath.Join(envs, id)
	for _, sub := range []string{"work/home", "state", "tmp", "vm"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The processes that an earlier launch left: virtiofsd (the pid file in
	// the vm dir) and the hypervisor (the pid file of the sandbox, which
	// names the process group). A runner restart left them behind.
	fsd, fsdGone := staleProc(t, d)
	if err := os.WriteFile(filepath.Join(d, "vm", "fs.sock.pid"), []byte(strconv.Itoa(fsd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	hv, hvGone := staleProc(t, d)
	if err := os.WriteFile(filepath.Join(d, "pid"), []byte(strconv.Itoa(hv.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	// A new Sandboxes after the restart: it knows the environment, but no
	// process of it.
	s2 := s()
	defer s2.Remove(id)
	if s2.Alive(id) {
		t.Fatal("a new Sandboxes must not see the environment alive")
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s2.Resume(id, nil)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("resume %d: %v", i, err)
		}
	}
	b, _ := os.ReadFile(count)
	if got := strings.Count(string(b), "launch"); got != 1 {
		t.Fatalf("the environment launched %d times, want 1", got)
	}
	stopped(t, fsdGone)
	stopped(t, hvGone)
	if !s2.Alive(id) {
		t.Fatal("the environment is not alive after the resumes")
	}
}

// When virtiofsd does not start, the launch runs once more, and the error
// has the last lines of envd.log, not only the path.
func TestVMVirtiofsdError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("task VMs with Cloud Hypervisor need Linux")
	}
	root := t.TempDir()
	s := &Sandboxes{Cfg: Config{Root: root,
		Bwrap: script(t, `echo "Error creating pid file '/run/vfs/fs.sock.pid': Resource temporarily unavailable" >&2; exit 1`),
		VM:    &VM{Kernel: "kernel", Hypervisor: "hypervisor"}}}
	id := "run_" + strings.Repeat("l", 26)
	d := s.Dir(id)
	if err := os.MkdirAll(filepath.Join(d, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := s.launchVM(id, nil)
	if err == nil {
		t.Fatal("the launch must fail: virtiofsd does not start")
	}
	if !strings.Contains(err.Error(), "virtiofsd did not start") {
		t.Fatalf("the error names an other cause: %v", err)
	}
	if !strings.Contains(err.Error(), "Error creating pid file") {
		t.Fatalf("the error has no envd.log tail: %v", err)
	}
	// The launch ran twice: the retry after 2 s.
	b, _ := os.ReadFile(filepath.Join(d, "envd.log"))
	if got := strings.Count(string(b), "Error creating pid file"); got != 2 {
		t.Fatalf("virtiofsd ran %d times, want 2 (one retry)", got)
	}
}
