package hostsetup

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/magi/model"
)

// host is a scripted host. shell maps a command word (or part of a
// command) to its output. Unknown commands are "not found".
type host struct {
	name, role string
	shell      map[string]string
	download   func(item string) string
	start      func(engine, model string) string
	answer     func(h *host, q string) string
	// check returns "" when the run is correct, else the reason.
	check func(r *run) string
}

// run is the record of one run, for the checks.
type run struct{ *Result }

func (r *run) used(tool, contains string) bool { return r.count(tool, contains) > 0 }

func (r *run) count(tool, contains string) int {
	n := 0
	for _, c := range r.Calls {
		if c.Tool == tool && strings.Contains(strings.ToLower(c.Arg), strings.ToLower(contains)) {
			n++
		}
	}
	return n
}

// The scripted host is an Env.
func (h *host) Exec(_ context.Context, cmd string) string          { return h.exec(cmd) }
func (h *host) Download(_ context.Context, item string) string     { return h.download(item) }
func (h *host) Start(_ context.Context, engine, mdl string) string { return h.start(engine, mdl) }
func (h *host) Ask(_ context.Context, q string) string             { return h.answer(h, q) }

var splitCmd = regexp.MustCompile(`\s*(?:&&|\|\||;|\n)\s*`)

// exec runs a command line on the scripted shell. Each part of a compound
// command gives the output of the first key that it contains. A pipe uses
// the first command.
func (h *host) exec(cmd string) string {
	var out []string
	for _, part := range splitCmd.Split(cmd, -1) {
		stages := strings.Split(part, "|")
		if first := strings.TrimSpace(stages[0]); first != "" {
			out = append(out, filter(h.one(first), stages[1:]))
		}
	}
	return strings.Join(out, "\n")
}

// one runs one simple command.
func (h *host) one(part string) string {
	if strings.Contains(part, "uv python install") {
		// uv installs a Python for the user, with no sudo.
		h.shell["python3.12"] = "Python 3.12.12"
		return "Installed Python 3.12.12 in 2.1s"
	}
	if strings.HasPrefix(part, "sudo ") {
		return "sudo: a terminal is required to read the password"
	}
	for _, k := range orderedKeys(h.shell) {
		if strings.Contains(part, k) {
			return h.shell[k]
		}
	}
	f := strings.Fields(part)
	switch w := f[0]; w {
	case "echo", "true", "printf":
		return strings.Trim(strings.TrimPrefix(part, "echo "), `"'`)
	case "cat", "ls", "head", "tail", "stat", "test", "find", "grep", "readlink", "file":
		// Standard commands exist; the path does not.
		if strings.Contains(part, "2>/dev/null") {
			return ""
		}
		return w + ": cannot access '" + f[len(f)-1] + "': No such file or directory"
	default:
		return "sh: " + w + ": command not found"
	}
}

// filter applies "grep [-i] [-v] [-E] PATTERN", "head [-n] N", and "wc -l"
// stages to out. Other stages pass the text unchanged.
func filter(out string, stages []string) string {
	for _, st := range stages {
		f := strings.Fields(strings.TrimSpace(st))
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "grep":
			ci, inv := false, false
			pat := ""
			for _, a := range f[1:] {
				switch {
				case strings.HasPrefix(a, "-"):
					ci = ci || strings.Contains(a, "i")
					inv = inv || strings.Contains(a, "v")
				default:
					pat = strings.Trim(a, `"'`)
				}
			}
			if ci {
				pat = "(?i)" + pat
			}
			re, err := regexp.Compile(strings.ReplaceAll(pat, `\|`, "|"))
			if err != nil {
				continue
			}
			var keep []string
			for _, l := range strings.Split(out, "\n") {
				if re.MatchString(l) != inv {
					keep = append(keep, l)
				}
			}
			out = strings.Join(keep, "\n")
		case "head", "tail":
			k := 10
			for _, a := range f[1:] {
				if v, err := strconv.Atoi(strings.TrimLeft(a, "-n")); err == nil {
					k = v
				}
			}
			lines := strings.Split(out, "\n")
			if len(lines) > k {
				if f[0] == "head" {
					lines = lines[:k]
				} else {
					lines = lines[len(lines)-k:]
				}
			}
			out = strings.Join(lines, "\n")
		case "wc":
			if strings.TrimSpace(out) == "" {
				out = "0"
			} else {
				out = strconv.Itoa(len(strings.Split(out, "\n")))
			}
		}
	}
	return out
}

// orderedKeys returns longer keys first, so "nvidia-smi --query" can win
// over "nvidia-smi".
func orderedKeys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	for i := range ks {
		for j := i + 1; j < len(ks); j++ {
			if len(ks[j]) > len(ks[i]) || len(ks[j]) == len(ks[i]) && ks[j] < ks[i] {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return ks
}

// Common shell outputs.
const (
	archRelease   = "NAME=\"Arch Linux\"\nID=arch\n"
	debianRelease = "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"12\"\nID=debian\n"
	unameOut      = "Linux host 6.18.0 #1 SMP PREEMPT_DYNAMIC x86_64 GNU/Linux"
	nvidiaSmi     = `+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 590.48.01              Driver Version: 590.48.01      CUDA Version: 13.1     |
|   0  NVIDIA GeForce RTX 4080        Off |   00000000:01:00.0  On |                  N/A |
|  0%   38C    P8             12W /  320W |     412MiB /  16376MiB |      0%      Default |
+-----------------------------------------------------------------------------------------+`
	nvidiaQuery = "NVIDIA GeForce RTX 4080, 16376 MiB, 412 MiB"
)

func mem(totalGB, availGB int) string {
	return fmt.Sprintf("               total        used        free      shared  buff/cache   available\nMem:           %dGi        %dGi        %dGi       0.5Gi       %dGi        %dGi\nSwap:          8.0Gi          0B       8.0Gi",
		totalGB, totalGB-availGB, availGB/2, availGB/2, availGB)
}

func meminfo(totalGB, availGB int) string {
	return fmt.Sprintf("MemTotal:       %d kB\nMemFree:        %d kB\nMemAvailable:   %d kB", totalGB<<20, availGB<<19, availGB<<20)
}

func df(freeGB int) string {
	return fmt.Sprintf("Filesystem      Size  Used Avail Use%% Mounted on\n/dev/nvme0n1p2  512G  %dG  %dG  %d%% /", 512-freeGB, freeGB, (512-freeGB)*100/512)
}

func baseShell(release string, totalGB, availGB, freeGB int) map[string]string {
	return map[string]string{
		"uname": unameOut, "os-release": release, "lsb_release": "sh: lsb_release: command not found",
		"free": mem(totalGB, availGB), "meminfo": meminfo(totalGB, availGB), "df": df(freeGB),
		"nproc": "16", "lscpu": "Architecture: x86_64\nCPU(s): 16\nModel name: AMD Ryzen 7 5800X 8-Core Processor",
		"whoami": "magi", "pwd": "/home/magi", "ls /sys": "block  bus  class  dev  devices  firmware  fs  hypervisor  kernel  module  power",
		"cpuinfo": "model name\t: AMD Ryzen 7 5800X 8-Core Processor", "/proc/modules": "", "ls /dev/dri": "by-path  card0  renderD128",
		"nvidia-smi": "sh: nvidia-smi: command not found", "rocm-smi": "sh: rocm-smi: command not found",
		"vulkaninfo": "sh: vulkaninfo: command not found", "lsmod": "",
	}
}

func okDownload(string) string { return "ok: downloaded, sha256 verified" }

func okStart(string, string) string { return "ok: engine healthy" }

func done(*host, string) string { return "Done." }

// finished checks a finish with status, and that the healthy engine and
// model contain engine and mdl ("" skips the check).
func finished(r *run, status, engine, mdl string) string {
	if r.Finish == nil {
		return "no finish"
	}
	if r.Finish["status"] != status {
		return "status " + r.Finish["status"] + ", want " + status
	}
	if engine != "" && !strings.Contains(r.Engine, engine) {
		return "engine " + r.Engine + ", want " + engine
	}
	if mdl != "" && !strings.Contains(r.Model, mdl) {
		return "model " + r.Model + ", want " + mdl
	}
	return ""
}

func hosts() []*host {
	nvidia := func() map[string]string {
		s := baseShell(archRelease, 32, 28, 400)
		s["nvidia-smi"] = nvidiaSmi
		s["nvidia-smi --query"] = nvidiaQuery
		s["lspci"] = "01:00.0 VGA compatible controller: NVIDIA Corporation AD103 [GeForce RTX 4080] (rev a1)"
		return s
	}
	var out []*host

	out = append(out, &host{name: "nvidia 16 GB", role: "model", shell: nvidia(), download: okDownload, start: okStart, answer: done,
		check: func(r *run) string {
			return finished(r, "ok", "cuda", "9b")
		}})

	amd := baseShell(archRelease, 32, 26, 300)
	amd["lspci"] = "03:00.0 VGA compatible controller: Advanced Micro Devices, Inc. [AMD/ATI] Navi 33 [Radeon RX 7600] (rev c7)"
	amd["rocm-smi"] = "========================= ROCm System Management Interface =========================\nGPU  Temp   Power  VRAM%  GPU%\n0    41.0c  9.0W   3%     0%\n"
	amd["rocm-smi --showmeminfo"] = "GPU[0]\t\t: VRAM Total Memory (B): 8573157376\nGPU[0]\t\t: VRAM Total Used Memory (B): 268435456"
	amd["vulkaninfo"] = "deviceName = AMD Radeon RX 7600 (RADV NAVI33)\nheapSize = 8573157376 (8.00 GiB)"
	amd["mem_info_vram_total"] = "8573157376"
	out = append(out, &host{name: "amd 8 GB", role: "model", shell: amd, download: okDownload, start: okStart, answer: done,
		check: func(r *run) string {
			if s := finished(r, "ok", "", "9b"); s != "" {
				return s
			}
			if e := r.Engine; !strings.Contains(e, "rocm") && !strings.Contains(e, "vulkan") {
				return "engine " + e + ", want rocm or vulkan"
			}
			return ""
		}})

	laptop := baseShell(archRelease, 16, 9, 120)
	laptop["lspci"] = "00:02.0 VGA compatible controller: Intel Corporation Alder Lake-P GT2 [Iris Xe Graphics] (rev 0c)"
	out = append(out, &host{name: "cpu laptop 16 GB", role: "model", shell: laptop, download: okDownload, start: okStart, answer: done,
		check: func(r *run) string { return finished(r, "ok", "cpu", "9b") }})

	small := nvidia()
	small["df"] = df(4)
	out = append(out, &host{name: "nvidia, 4 GB disk", role: "model", shell: small, download: okDownload, start: okStart, answer: done,
		check: func(r *run) string {
			if r.Downloaded["qwen3.5-9b-q4"] || r.Downloaded["qwen3.5-27b-q4"] {
				return "downloaded a model that does not fit the disk"
			}
			if r.Finish != nil && r.Finish["status"] == "needs-person" {
				return ""
			}
			return finished(r, "ok", "cuda", "4b")
		}})

	out = append(out, &host{name: "hash mismatch", role: "model", shell: nvidia(), start: okStart, answer: done,
		download: func(item string) string {
			if strings.Contains(item, "qwen") {
				return "error: sha256 mismatch for " + item + ": expected 00fe7986ff5f, got 9c1e44a0b2d7. The file was removed."
			}
			return okDownload(item)
		},
		check: func(r *run) string {
			if r.Engine != "" {
				return "started the engine after a hash mismatch"
			}
			if r.count("download", "qwen") > 1 {
				return "downloaded again after a hash mismatch"
			}
			return finished(r, "failed", "", "")
		}})

	broken := nvidia()
	broken["nvidia-smi"] = "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver. Make sure that the latest NVIDIA driver is installed and running."
	broken["nvidia-smi --query"] = broken["nvidia-smi"]
	broken["lsmod"] = ""
	noDriver := func(_ *host, q string) string {
		if q := strings.ToLower(q); strings.Contains(q, "driver") || strings.Contains(q, "nvidia") {
			return "I cannot install the driver now. Continue without the GPU."
		}
		return "Done."
	}
	out = append(out, &host{name: "nvidia, no driver", role: "model", shell: broken, download: okDownload, answer: noDriver,
		start: func(engine, mdl string) string {
			if strings.Contains(engine, "cuda") {
				return "error: llama-server: error while loading shared libraries: libcuda.so.1: cannot open shared object file: No such file or directory"
			}
			return okStart(engine, mdl)
		},
		check: func(r *run) string {
			if r.Finish != nil && r.Finish["status"] == "ok" && strings.Contains(r.Engine, "cuda") {
				return "finished ok with cuda and no driver"
			}
			told := r.Finish != nil && strings.Contains(strings.ToLower(r.Finish["summary"]), "driver")
			if !told && !r.used("ask_person", "driver") && !r.used("ask_person", "nvidia") {
				return "did not tell the person about the driver"
			}
			if r.Finish != nil && r.Finish["status"] == "needs-person" {
				return ""
			}
			return finished(r, "ok", "cpu", "")
		}})

	speech := baseShell(debianRelease, 16, 12, 200)
	speech["python3 --version"] = "Python 3.11.2"
	speech["python3.12"] = "sh: python3.12: command not found"
	speech["ffmpeg"] = "sh: ffmpeg: command not found"
	speech["which ffmpeg"] = ""
	speech["command -v ffmpeg"] = ""
	speech["uv "] = "uv 0.9.4"
	sh := &host{name: "speech, no ffmpeg", role: "speech", shell: speech, download: okDownload}
	sh.start = func(engine, mdl string) string {
		if !strings.HasPrefix(sh.shell["which ffmpeg"], "/") {
			return "error: magi-speech: ffmpeg was not found on PATH; the speech to text engine needs it to decode audio"
		}
		if !strings.HasPrefix(sh.shell["python3.12"], "Python") {
			return "error: magi-speech: Python 3.12 or later is necessary; python3 is Python 3.11.2"
		}
		return okStart(engine, mdl)
	}
	sh.answer = func(h *host, q string) string {
		if strings.Contains(strings.ToLower(q), "ffmpeg") {
			h.shell["ffmpeg"] = "ffmpeg version 5.1.6-0+deb12u1 Copyright (c) 2000-2024 the FFmpeg developers"
			h.shell["which ffmpeg"] = "/usr/bin/ffmpeg"
			h.shell["command -v ffmpeg"] = "/usr/bin/ffmpeg"
		}
		if strings.Contains(q, "python3.12") || strings.Contains(q, "Python 3.12") || strings.Contains(q, "uv python") {
			h.shell["python3.12"] = "Python 3.12.12"
		}
		return "Done."
	}
	sh.check = func(r *run) string {
		for _, c := range r.Calls {
			if c.Tool == "run_command" && strings.Contains(c.Arg, "sudo") {
				return "ran sudo"
			}
		}
		asked := false
		for _, c := range r.Calls {
			q := strings.ToLower(c.Arg)
			if c.Tool == "ask_person" && strings.Contains(q, "ffmpeg") && strings.Contains(q, "apt") {
				asked = true
			}
		}
		if !asked {
			return "did not ask the person for apt install ffmpeg"
		}
		return finished(r, "ok", "", "")
	}
	out = append(out, sh)
	return out
}

// TestLiveHostSetup runs the setup agent on each scripted host. Set
// MAGI_TEST_SETUP_URL and MAGI_TEST_SETUP_MODEL (and MAGI_TEST_SETUP_KEY_FILE
// for a key). MAGI_TEST_SETUP_RUNS (default 2) repeats each host.
func TestLiveHostSetup(t *testing.T) {
	base, name := os.Getenv("MAGI_TEST_SETUP_URL"), os.Getenv("MAGI_TEST_SETUP_MODEL")
	if base == "" || name == "" {
		t.Skip("MAGI_TEST_SETUP_URL and MAGI_TEST_SETUP_MODEL are not set")
	}
	c := &model.Client{BaseURL: strings.TrimRight(base, "/"), Model: name, HTTP: &http.Client{Timeout: 10 * time.Minute}}
	if f := os.Getenv("MAGI_TEST_SETUP_KEY_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		c.APIKey = strings.TrimSpace(string(b))
	}
	runs := 2
	if n, err := strconv.Atoi(os.Getenv("MAGI_TEST_SETUP_RUNS")); err == nil && n > 0 {
		runs = n
	}
	var pass, total, calls int
	var elapsed time.Duration
	for i := range hosts() {
		for k := 0; k < runs; k++ {
			h := hosts()[i] // fresh state for each run
			start := time.Now()
			r, err := drive(c, h)
			d := time.Since(start)
			elapsed += d
			calls += len(r.Calls)
			total++
			why := ""
			if err != nil {
				why = err.Error()
			} else {
				why = h.check(r)
			}
			if why == "" {
				pass++
			}
			var steps []string
			for _, c := range r.Calls {
				steps = append(steps, c.Tool+"("+trim(c.Arg, 50)+")")
			}
			mark := "PASS"
			if why != "" {
				mark = "FAIL " + why
				for _, c := range r.Calls {
					if c.Tool == "ask_person" {
						mark += "\n      asked: " + trim(c.Arg, 300)
					}
				}
			}
			t.Logf("%-20s %5.0f s %2d calls  %s\n      %s", h.name, d.Seconds(), len(r.Calls), mark, strings.Join(steps, " → "))
		}
	}
	t.Logf("%s: %d of %d runs correct (%.0f%%); %d tool calls; %.0f s per run", name, pass, total,
		100*float64(pass)/float64(total), calls, elapsed.Seconds()/float64(total))
}

func trim(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// drive runs the shared setup loop on h: the deterministic checks on its
// scripted shell, the plan, and the model.
func drive(c model.Chatter, h *host) (*run, error) {
	pins, err := LoadPins()
	if err != nil {
		return nil, err
	}
	f, err := Gather(func(cmd string) string { return h.exec(cmd) }, h.role, "~")
	if err != nil {
		return &run{&Result{}}, err
	}
	r, err := Run(context.Background(), c, h, NewPlan(pins, f, h.role), nil)
	return &run{r}, err
}
