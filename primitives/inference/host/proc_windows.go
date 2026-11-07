//go:build windows

package host

import (
	"context"
	"encoding/csv"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

// setGroup starts the engine in its own process group, with no console.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow, HideWindow: true}
}

// stopGroup ends the engine and its child processes. A console program on
// Windows has no polite stop signal, and the engine has no state to save.
func stopGroup(pid int, _ bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	c.Run()
}

var getSystemTimes = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemTimes")

var cpuMu sync.Mutex
var cpuIdle, cpuTotal uint64

// cpuLoad is the CPU use (0 to 1) since the last call. Windows has no load
// average. The first call gives 0.
func cpuLoad() float64 {
	var idle, kernel, user syscall.Filetime
	r, _, _ := getSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return 0
	}
	ft := func(f syscall.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	i, total := ft(idle), ft(kernel)+ft(user) // kernel time includes idle time
	cpuMu.Lock()
	defer cpuMu.Unlock()
	di, dt := i-cpuIdle, total-cpuTotal
	first := cpuTotal == 0
	cpuIdle, cpuTotal = i, total
	if first || dt == 0 {
		return 0
	}
	return 1 - float64(di)/float64(dt)
}

// processes gives the image names of all processes, in lower case and
// without ".exe".
func processes(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "tasklist", "/FO", "CSV", "/NH")
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	out, err := c.Output()
	if err != nil {
		return nil
	}
	rows, _ := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	var names []string
	for _, r := range rows {
		if len(r) > 0 {
			names = append(names, strings.TrimSuffix(strings.ToLower(r[0]), ".exe"))
		}
	}
	return names
}

// defaultProcesses: compilers and linkers, and SteamVR. Most games show as
// GPU use (the gpu_util rule).
var defaultProcesses = []string{"cl", "link", "rustc", "compile", "javac", "msbuild",
	"vrserver", "vrcompositor"}
