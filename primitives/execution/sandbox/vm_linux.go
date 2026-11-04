package sandbox

import (
	"os"
	"os/exec"
	"syscall"
)

// vmProcAttr puts the hypervisor in the process group of virtiofsd. The
// hypervisor stops when the runner stops.
func vmProcAttr(pgid int) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pgid: pgid, Pdeathsig: syscall.SIGKILL}
}

// netns starts cmd ("envd netns") in a new user and network namespace,
// where it is root (the user's IDs mapped to 0).
func netns(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
}
