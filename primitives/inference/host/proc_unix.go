//go:build !windows

package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"context"
)

// setGroup starts the engine in its own process group.
func setGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// stopGroup sends SIGTERM (or SIGKILL when kill) to the process group.
func stopGroup(pid int, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	syscall.Kill(-pid, sig)
}

func cpuLoad() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	l, _ := strconv.ParseFloat(f[0], 64)
	return l / float64(runtime.NumCPU())
}

func processes(context.Context) []string {
	var out []string
	dirs, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, d := range dirs {
		if b, err := os.ReadFile(d); err == nil {
			out = append(out, strings.TrimSpace(string(b)))
		}
	}
	return out
}

// defaultProcesses: compilers and linkers, and game processes.
var defaultProcesses = []string{"cc1", "cc1plus", "rustc", "compile", "ld", "ld.lld", "mold", "javac",
	"gamescope", "reaper", "wineserver"}
