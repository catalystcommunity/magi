package host

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Sample is one look at the machine. A GPU value below zero is unknown.
type Sample struct {
	CPULoad     float64 // 1-minute load average divided by the CPU count
	GPUUtil     int     // percent
	GPUUsedMiB  int     // all processes, also the engine
	GPUTotalMiB int
	EngineMiB   int      // GPU memory that the engine process uses
	Processes   []string // command names (/proc/<pid>/comm)
}

// Sampler takes samples. enginePID is 0 when the engine does not run.
type Sampler interface {
	Sample(ctx context.Context, enginePID int) Sample
}

// Busy rules. A zero value disables a rule.
type Rules struct {
	CPULoad   float64  `json:"cpu_load"`  // busy above this CPULoad
	GPUUtil   int      `json:"gpu_util"`  // busy above this, only while the engine is idle
	NeedMiB   int      `json:"need_mib"`  // GPU memory that the engine needs to start
	Processes []string `json:"processes"` // busy while one of these runs
	QuietFor  Duration `json:"quiet_for"` // the machine must be quiet this long
}

// DefaultRules suit a desktop that also compiles code and runs games.
func DefaultRules() Rules {
	return Rules{
		CPULoad: 0.6, GPUUtil: 40, NeedMiB: 7000, QuietFor: Duration(60 * time.Second),
		Processes: []string{
			// compilers and linkers
			"cc1", "cc1plus", "rustc", "compile", "ld", "ld.lld", "mold", "javac",
			// games
			"gamescope", "reaper", "wineserver",
		},
	}
}

// busyReason returns why the machine is busy, or "". engineActive is true
// while the engine answers a request, because then the GPU use is its own.
func (r Rules) busyReason(s Sample, engineRunning, engineActive bool) string {
	want := map[string]bool{}
	for _, p := range r.Processes {
		want[p] = true
	}
	for _, p := range s.Processes {
		if want[p] {
			return "process " + p + " is running"
		}
	}
	if r.CPULoad > 0 && s.CPULoad > r.CPULoad {
		return fmt.Sprintf("CPU load is %.2f per CPU", s.CPULoad)
	}
	if r.GPUUtil > 0 && s.GPUUtil >= 0 && !engineActive && s.GPUUtil > r.GPUUtil {
		return fmt.Sprintf("GPU use is %d%%", s.GPUUtil)
	}
	if r.NeedMiB > 0 && s.GPUTotalMiB > 0 && !engineRunning && s.GPUTotalMiB-s.GPUUsedMiB < r.NeedMiB {
		return fmt.Sprintf("only %d MiB of GPU memory is free", s.GPUTotalMiB-s.GPUUsedMiB)
	}
	return ""
}

// System samples Linux /proc and nvidia-smi. It works in a VM with a
// passed-through GPU in the same way. Without nvidia-smi, GPU values are
// unknown.
type System struct{ NvidiaSMI string }

func (sys System) Sample(ctx context.Context, enginePID int) Sample {
	s := Sample{GPUUtil: -1, GPUUsedMiB: -1, GPUTotalMiB: -1}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			l, _ := strconv.ParseFloat(f[0], 64)
			s.CPULoad = l / float64(runtime.NumCPU())
		}
	}
	dirs, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, d := range dirs {
		if b, err := os.ReadFile(d); err == nil {
			s.Processes = append(s.Processes, strings.TrimSpace(string(b)))
		}
	}
	smi := sys.NvidiaSMI
	if smi == "" {
		smi = "nvidia-smi"
	}
	if rows := csvCmd(ctx, smi, "--query-gpu=utilization.gpu,memory.used,memory.total", "--format=csv,noheader,nounits"); len(rows) > 0 && len(rows[0]) == 3 {
		s.GPUUtil, s.GPUUsedMiB, s.GPUTotalMiB = atoi(rows[0][0]), atoi(rows[0][1]), atoi(rows[0][2])
	}
	if enginePID > 0 {
		for _, r := range csvCmd(ctx, smi, "--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits") {
			if len(r) == 2 && atoi(r[0]) == enginePID {
				s.EngineMiB = atoi(r[1])
			}
		}
	}
	return s
}

func csvCmd(ctx context.Context, name string, args ...string) [][]string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil
	}
	r := csv.NewReader(strings.NewReader(string(out)))
	r.TrimLeadingSpace = true
	rows, _ := r.ReadAll()
	return rows
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return -1
	}
	return n
}

// Duration is a time.Duration that reads "90s" or "15m" from JSON.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	v, err := time.ParseDuration(strings.Trim(string(b), `"`))
	*d = Duration(v)
	return err
}
