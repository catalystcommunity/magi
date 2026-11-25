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

// sysDRM is the directory of the graphics cards on Linux.
const sysDRM = "/sys/class/drm"

// gpuMakers has the PCI vendor ids of the GPUs that sysGPUs reports.
// NVIDIA is not in it: nvidia-smi reports those.
var gpuMakers = map[string]string{"0x1002": "AMD", "0x8086": "Intel"}

// sysGPUs reads the GPUs that Linux tells in root (/sys/class/drm): the
// cards of AMD and Intel with the files of their driver. A card with no
// memory file (most Intel cards) has unknown memory. On a system that is
// not Linux the directory is not there, and the result is empty.
func sysGPUs(root string) []GPU {
	cards, _ := filepath.Glob(filepath.Join(root, "card[0-9]*"))
	var out []GPU
	for _, c := range cards {
		// card0-DP-1 is an output of card0, not a card.
		if strings.Contains(filepath.Base(c), "-") {
			continue
		}
		read := func(name string) string {
			b, err := os.ReadFile(filepath.Join(c, "device", name))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(b))
		}
		maker, ok := gpuMakers[read("vendor")]
		if !ok {
			continue
		}
		mib := func(name string) int {
			n, err := strconv.ParseInt(read(name), 10, 64)
			if err != nil {
				return -1
			}
			return int(n >> 20)
		}
		g := GPU{Name: maker + " " + strings.TrimPrefix(read("device"), "0x"), TotalMiB: mib("mem_info_vram_total"), UsedMiB: mib("mem_info_vram_used"), Util: -1}
		if u, err := strconv.Atoi(read("gpu_busy_percent")); err == nil {
			g.Util = u
		}
		out = append(out, g)
	}
	return out
}
