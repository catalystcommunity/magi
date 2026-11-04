//go:build !linux

package sandbox

import (
	"os/exec"
	"syscall"
)

func vmProcAttr(pgid int) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
}

// netns is for bubblewrap, which runs only on Linux.
func netns(*exec.Cmd) {}
