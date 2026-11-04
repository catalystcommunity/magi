package hostsetup

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const gib = 1 << 30

// GPU is one GPU that the checks found.
type GPU struct {
	Vendor string // nvidia, amd, intel
	Name   string
	Mem    int64 // bytes; 0 when not known
	// Used is the GPU memory that other programs use now (bytes; 0 when
	// not known). A model must fit in Mem - Used.
	Used int64
	// Driver tells if the vendor tool (nvidia-smi or rocm-smi) answers.
	Driver bool
	Vulkan bool
}

// Free is the GPU memory that a model can use now.
func (g GPU) Free() int64 { return max(0, g.Mem-g.Used) }

// Facts are the results of the deterministic checks.
type Facts struct {
	OS, Arch, Distro string
	RAMAvail         int64
	DiskFree         int64
	GPUs             []GPU
	// Python312 and FFmpeg are checked for a speech host only.
	Python312, FFmpeg bool
	// Problems are found by the checks. The model tells the person about them.
	Problems []string
	// Raw has each probe command and its output, for the model.
	Raw string
}

// Exec runs one shell command and returns its output (stdout and stderr).
type Exec func(cmd string) string

// Probes returns the commands of the fact step. dir is the directory for
// downloads; the free disk is measured there.
func Probes(role, dir string) []string {
	p := []string{
		"uname -sm", "cat /etc/os-release", "cat /proc/meminfo", "df -hP " + dir,
		"lspci | grep -i -E 'vga|3d|display'",
		"nvidia-smi --query-gpu=name,memory.total,memory.used --format=csv,noheader",
		"rocm-smi --showmeminfo vram",
		// One line for each card: total and used VRAM, in bytes.
		"for d in /sys/class/drm/card*/device; do [ -r $d/mem_info_vram_total ] && echo $(cat $d/mem_info_vram_total) $(cat $d/mem_info_vram_used 2>/dev/null); done",
		"vulkaninfo --summary",
	}
	if role == "speech" {
		p = append(p, "python3 --version", "python3.12 --version", "command -v ffmpeg")
	}
	return p
}

// ErrNotLinux is the result on a host that does not run Linux.
type ErrNotLinux struct{ OS string }

func (e ErrNotLinux) Error() string {
	return fmt.Sprintf("this host runs %s. Magi host setup supports only Linux hosts (not BSD, macOS, or Windows)", e.OS)
}

// Gather runs the probes and reads the facts from their output. It returns
// an error when the host cannot be a Magi host (not Linux, or an
// architecture with no engine build).
func Gather(exec Exec, role, dir string) (*Facts, error) {
	f := &Facts{}
	out := map[string]string{}
	var raw strings.Builder
	for _, p := range Probes(role, dir) {
		o := strings.TrimSpace(exec(p))
		out[p] = o
		if o == "" {
			o = "(no output)"
		}
		fmt.Fprintf(&raw, "$ %s\n%s\n\n", p, o)
	}
	f.Raw = raw.String()
	ps := Probes(role, dir)
	get := func(i int) string { return out[ps[i]] }

	f.OS, f.Arch = parseUname(get(0))
	if f.OS != "Linux" {
		return f, ErrNotLinux{OS: orUnknown(f.OS)}
	}
	if f.Arch != "x86_64" && f.Arch != "aarch64" {
		return f, fmt.Errorf("the CPU architecture %s has no pinned engine build (x86_64 and aarch64 have)", orUnknown(f.Arch))
	}
	f.Distro = osReleaseID(get(1))
	f.RAMAvail = meminfoAvail(get(2))
	f.DiskFree = dfAvail(get(3))
	if f.RAMAvail == 0 {
		f.Problems = append(f.Problems, "The available RAM is not known (/proc/meminfo has no MemAvailable).")
	}
	if f.DiskFree == 0 {
		f.Problems = append(f.Problems, "The free disk is not known, or the disk is full.")
	}
	f.GPUs = gpus(get(4), get(5), get(6), get(7), get(8))
	for _, g := range f.GPUs {
		switch {
		case g.Vendor == "nvidia" && !g.Driver:
			f.Problems = append(f.Problems, "An NVIDIA GPU is installed, but the NVIDIA driver does not answer (nvidia-smi failed). The CUDA engine is not available until the person installs or repairs the driver.")
		case g.Vendor == "amd" && !g.Driver && !g.Vulkan:
			f.Problems = append(f.Problems, "An AMD GPU is installed, but neither ROCm (rocm-smi) nor Vulkan (vulkaninfo) answers. The GPU engines are not available until the person installs one of them.")
		}
	}
	if role == "speech" {
		f.Python312 = pythonAtLeast(get(9), 3, 12) || pythonAtLeast(get(10), 3, 12)
		f.FFmpeg = strings.HasPrefix(get(11), "/")
		if !f.Python312 {
			f.Problems = append(f.Problems, "Python 3.12 or later is not installed.")
		}
		if !f.FFmpeg {
			f.Problems = append(f.Problems, "ffmpeg is not installed.")
		}
	}
	return f, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown system"
	}
	return s
}

var arches = map[string]string{"x86_64": "x86_64", "amd64": "x86_64", "aarch64": "aarch64", "arm64": "aarch64"}

// parseUname reads the system name (first word) and the architecture
// (a known word) from uname output.
func parseUname(s string) (osName, arch string) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return "", ""
	}
	osName = f[0]
	for _, w := range f[1:] {
		if a, ok := arches[w]; ok {
			arch = a
		}
	}
	if arch == "" && len(f) > 1 {
		arch = f[len(f)-1]
	}
	return osName, arch
}

func osReleaseID(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(l, "ID="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func meminfoAvail(s string) int64 {
	for _, l := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(l, "MemAvailable:"); ok {
			f := strings.Fields(v)
			if n, err := strconv.ParseInt(f[0], 10, 64); err == nil {
				return n << 10
			}
		}
	}
	return 0
}

// dfAvail reads the Avail column of df output, with or without -h.
func dfAvail(s string) int64 {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 2 {
		return 0
	}
	col := -1
	for i, h := range strings.Fields(lines[0]) {
		if strings.HasPrefix(h, "Avail") {
			col = i
		}
	}
	f := strings.Fields(lines[len(lines)-1])
	if col < 0 || col >= len(f) {
		return 0
	}
	return parseSize(f[col], 1<<10)
}

// parseSize reads "120G", "512M", "1.5T", or a plain number of unit bytes.
func parseSize(s string, unit int64) int64 {
	mult := map[byte]float64{'K': 1 << 10, 'M': 1 << 20, 'G': 1 << 30, 'T': 1 << 40, 'P': 1 << 50}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "iB"), "i")
	if s == "" {
		return 0
	}
	if m, ok := mult[s[len(s)-1]]; ok {
		v, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil {
			return 0
		}
		return int64(v * m)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v * unit
}

var (
	nvidiaRow = regexp.MustCompile(`^([^,]+),\s*(\d+)\s*MiB(?:,\s*(\d+)\s*MiB)?`)
	rocmTotal = regexp.MustCompile(`VRAM Total Memory \(B\):\s*(\d+)`)
	rocmUsed  = regexp.MustCompile(`VRAM Total Used Memory \(B\):\s*(\d+)`)
)

// gpus reads the GPUs from lspci, nvidia-smi, rocm-smi, the sysfs VRAM
// size, and vulkaninfo.
func gpus(lspci, nvsmi, rocm, sysVRAM, vulkan string) []GPU {
	var out []GPU
	vk := strings.Contains(vulkan, "deviceName")
	lower := strings.ToLower(lspci)
	var nv []GPU
	for _, l := range strings.Split(nvsmi, "\n") {
		if m := nvidiaRow.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			n, _ := strconv.ParseInt(m[2], 10, 64)
			u, _ := strconv.ParseInt(m[3], 10, 64)
			nv = append(nv, GPU{Vendor: "nvidia", Name: strings.TrimSpace(m[1]), Mem: n << 20, Used: u << 20, Driver: true,
				Vulkan: vk && strings.Contains(strings.ToLower(vulkan), "nvidia")})
		}
	}
	out = append(out, nv...)
	if len(nv) == 0 && strings.Contains(lower, "nvidia") {
		out = append(out, GPU{Vendor: "nvidia", Name: lspciName(lspci, "nvidia"),
			Vulkan: vk && strings.Contains(strings.ToLower(vulkan), "nvidia")})
	}
	if strings.Contains(lower, "amd") || strings.Contains(lower, "ati") && strings.Contains(lower, "radeon") {
		g := GPU{Vendor: "amd", Name: lspciName(lspci, "amd")}
		if m := rocmTotal.FindStringSubmatch(rocm); m != nil {
			g.Mem, _ = strconv.ParseInt(m[1], 10, 64)
			g.Driver = true
			if u := rocmUsed.FindStringSubmatch(rocm); u != nil {
				g.Used, _ = strconv.ParseInt(u[1], 10, 64)
			}
		}
		if g.Mem == 0 {
			// The card with the most VRAM; an integrated GPU has less.
			for _, l := range strings.Split(sysVRAM, "\n") {
				f := strings.Fields(l)
				if len(f) == 0 {
					continue
				}
				if n, err := strconv.ParseInt(f[0], 10, 64); err == nil && n > g.Mem {
					g.Mem, g.Used = n, 0
					if len(f) > 1 {
						g.Used, _ = strconv.ParseInt(f[1], 10, 64)
					}
				}
			}
		}
		vl := strings.ToLower(vulkan)
		g.Vulkan = vk && (strings.Contains(vl, "amd") || strings.Contains(vl, "radeon"))
		out = append(out, g)
	}
	if strings.Contains(lower, "intel") {
		out = append(out, GPU{Vendor: "intel", Name: lspciName(lspci, "intel")})
	}
	return out
}

func lspciName(lspci, vendor string) string {
	for _, l := range strings.Split(lspci, "\n") {
		if strings.Contains(strings.ToLower(l), vendor) {
			if _, after, ok := strings.Cut(l, ": "); ok {
				return strings.TrimSpace(after)
			}
			return strings.TrimSpace(l)
		}
	}
	return ""
}

var pyVersion = regexp.MustCompile(`Python (\d+)\.(\d+)`)

func pythonAtLeast(s string, major, minor int) bool {
	m := pyVersion.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a > major || a == major && b >= minor
}

// GB formats bytes as gigabytes with one decimal.
func GB(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }
