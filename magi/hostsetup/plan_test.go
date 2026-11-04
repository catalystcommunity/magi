package hostsetup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/catalystcommunity/magi/magi/model"
)

func planFor(t *testing.T, name string) *Plan {
	t.Helper()
	pins, err := LoadPins()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts() {
		if h.name == name {
			f, err := Gather(func(c string) string { return h.exec(c) }, h.role, "~")
			if err != nil {
				t.Fatal(err)
			}
			return NewPlan(pins, f, h.role)
		}
	}
	t.Fatalf("no host %q", name)
	return nil
}

func TestPinsValid(t *testing.T) {
	p, err := LoadPins()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Engines {
		for _, f := range e.Files {
			if len(f.SHA256) != 64 || f.Size <= 0 || !strings.HasPrefix(f.URL, "https://") {
				t.Errorf("engine %s %s: bad pin %+v", e.Name, e.Arch, f)
			}
		}
	}
	for _, m := range p.Models {
		if len(m.SHA256) != 64 || m.Size <= 0 || !strings.Contains(m.URL, "/resolve/") {
			t.Errorf("model %s: bad pin", m.Name)
		}
	}
	for _, h := range p.Helper {
		if _, ok := p.Model(h); !ok {
			t.Errorf("helper %s is not a pinned model", h)
		}
	}
	if _, ok := p.Engine("llama.cpp-cpu", "x86_64"); !ok {
		t.Error("no CPU engine for x86_64")
	}
}

func TestPlans(t *testing.T) {
	for _, tc := range []struct {
		host, engine, model string
		notAllowed          []string
		problem             string
	}{
		{"nvidia 16 GB", "llama.cpp-cuda", "qwen3.5-9b-q4", []string{"qwen3.5-27b-q4", "llama.cpp-rocm"}, ""},
		{"amd 8 GB", "llama.cpp-rocm", "qwen3.5-9b-q4", []string{"llama.cpp-cuda", "qwen3.5-27b-q4"}, ""},
		{"cpu laptop 16 GB", "llama.cpp-cpu", "qwen3.5-9b-q4", []string{"llama.cpp-cuda", "llama.cpp-vulkan", "qwen3.5-27b-q4"}, ""},
		{"nvidia, 4 GB disk", "llama.cpp-cuda", "qwen3.5-4b-q4", []string{"qwen3.5-9b-q4", "qwen3.5-27b-q4"}, ""},
		{"nvidia, no driver", "llama.cpp-cpu", "qwen3.5-9b-q4", []string{"llama.cpp-cuda", "qwen3.5-27b-q4"}, "NVIDIA driver"},
	} {
		pl := planFor(t, tc.host)
		e, m := pl.Suggested()
		if e != tc.engine || m != tc.model {
			t.Errorf("%s: suggested %s %s, want %s %s", tc.host, e, m, tc.engine, tc.model)
		}
		for _, n := range tc.notAllowed {
			if pl.Allowed(n) {
				t.Errorf("%s: %s is allowed", tc.host, n)
			}
		}
		if tc.problem != "" && !strings.Contains(strings.Join(pl.Facts.Problems, " "), tc.problem) {
			t.Errorf("%s: problems %q, want %q", tc.host, pl.Facts.Problems, tc.problem)
		}
		if p := pl.Prompt(); !strings.Contains(p, tc.model) || strings.Contains(p, "%!") {
			t.Errorf("%s: bad prompt:\n%s", tc.host, p)
		}
	}
	amd := planFor(t, "amd 8 GB")
	if !amd.Allowed("llama.cpp-vulkan") {
		t.Error("amd: vulkan is not allowed")
	}
	sp := planFor(t, "speech, no ffmpeg")
	if !sp.Allowed("kokoro-82m") || !sp.Allowed("magi-speech") || sp.Allowed("qwen3.5-9b-q4") {
		t.Error("speech plan has the wrong items")
	}
	if got := strings.Join(sp.Facts.Problems, " "); !strings.Contains(got, "ffmpeg") || !strings.Contains(got, "Python 3.12") {
		t.Errorf("speech problems: %q", got)
	}
}

func TestGatherRefusesOtherSystems(t *testing.T) {
	for _, u := range []string{"Darwin arm64", "FreeBSD amd64", "MINGW64_NT-10.0 x86_64", ""} {
		_, err := Gather(func(c string) string {
			if strings.HasPrefix(c, "uname") {
				return u
			}
			return ""
		}, "model", "~")
		var nl ErrNotLinux
		if !errors.As(err, &nl) || !strings.Contains(err.Error(), "only Linux") {
			t.Errorf("uname %q: err %v", u, err)
		}
	}
	_, err := Gather(func(c string) string {
		if strings.HasPrefix(c, "uname") {
			return "Linux riscv64"
		}
		return ""
	}, "model", "~")
	if err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("riscv64: err %v", err)
	}
}

func TestParsers(t *testing.T) {
	if os, arch := parseUname("Linux aarch64"); os != "Linux" || arch != "aarch64" {
		t.Errorf("uname: %s %s", os, arch)
	}
	real := "Filesystem      Size  Used Avail Use% Mounted on\n/dev/nvme0n1p2  1.9T  1.2T  622G  66% /home"
	if got := dfAvail(real); got != 622<<30 {
		t.Errorf("df -hP: %d", got)
	}
	blocks := "Filesystem     1024-blocks      Used Available Capacity Mounted on\n/dev/sda1        1000000    400000    600000      40% /"
	if got := dfAvail(blocks); got != 600000<<10 {
		t.Errorf("df -P: %d", got)
	}
	if got := meminfoAvail("MemTotal: 100 kB\nMemAvailable:   2048 kB\n"); got != 2<<20 {
		t.Errorf("meminfo: %d", got)
	}
	g := gpus("01:00.0 VGA compatible controller: NVIDIA Corporation AD103", "NVIDIA GeForce RTX 4080, 16376 MiB, 412 MiB", "", "", "")
	if len(g) != 1 || !g[0].Driver || g[0].Mem != 16376<<20 || g[0].Used != 412<<20 {
		t.Errorf("gpus: %+v", g)
	}
	// sysfs: the discrete card has the most VRAM; another program uses 2 GB.
	g = gpus("03:00.0 VGA compatible controller: Advanced Micro Devices, Inc. [AMD/ATI] Navi 33", "", "", "8573157376 2114019328\n2147483648 25276416", "")
	if len(g) != 1 || g[0].Mem != 8573157376 || g[0].Free() != 8573157376-2114019328 {
		t.Errorf("amd gpus: %+v", g)
	}
	if !pythonAtLeast("Python 3.13.1", 3, 12) || pythonAtLeast("Python 3.11.2", 3, 12) {
		t.Error("python version")
	}
}

// script is a model that returns fixed tool calls, one turn each.
type script [][]model.ToolCall

func (s *script) Chat(context.Context, []model.Message, []model.Tool, int) (model.Message, error) {
	if len(*s) == 0 {
		return model.Message{}, errors.New("script ended")
	}
	m := model.Message{Role: "assistant", ToolCalls: (*s)[0]}
	*s = (*s)[1:]
	return m, nil
}

func tc(name, args string) model.ToolCall {
	var c model.ToolCall
	c.ID, c.Type = name, "function"
	c.Function.Name, c.Function.Arguments = name, args
	return c
}

func TestRunEnforcesPlan(t *testing.T) {
	var h *host
	for _, x := range hosts() {
		if x.name == "nvidia, 4 GB disk" {
			h = x
		}
	}
	pl := planFor(t, "nvidia, 4 GB disk")
	s := &script{
		{tc("download", `{"item":"qwen3.5-9b-q4"}`)},
		{tc("run_command", `{"command":"sudo apt install foo"}`)},
		{tc("start_engine", `{"engine":"llama.cpp-cuda","model":"qwen3.5-4b-q4"}`)},
		{tc("download", `{"item":"llama.cpp-cuda"}`), tc("download", `{"item":"qwen3.5-4b-q4"}`)},
		{tc("start_engine", `{"engine":"llama.cpp-cuda","model":"qwen3.5-4b-q4"}`)},
		{tc("finish", `{"status":"ok","engine":"llama.cpp-cuda","model":"qwen3.5-4b-q4","summary":"done"}`)},
	}
	r, err := Run(context.Background(), s, h, pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"not in the lists", "not permitted", "not downloaded", "ok", "ok", "ok"}
	for i, w := range want {
		if !strings.Contains(r.Calls[i].Out, w) {
			t.Errorf("call %d %s: %q, want %q", i, r.Calls[i].Tool, r.Calls[i].Out, w)
		}
	}
	if r.Downloaded["qwen3.5-9b-q4"] || r.Engine != "llama.cpp-cuda" || r.Finish["status"] != "ok" {
		t.Errorf("result: %+v", r)
	}
}

// On lab-nas, Vulkan0 is the integrated GPU, which shows the system RAM.
func TestPickDevice(t *testing.T) {
	out := `ggml_vulkan: Found 2 Vulkan devices:
Available devices:
  Vulkan0: AMD Radeon Graphics (RADV RAPHAEL_MENDOCINO) (49125 MiB, 49088 MiB free)
  Vulkan1: AMD Radeon RX 7600 (RADV NAVI33) (8176 MiB, 6159 MiB free)
`
	ds := ParseDevices(out)
	if len(ds) != 2 || ds[1].Free != 6159<<20 {
		t.Fatalf("devices: %+v", ds)
	}
	g := GPU{Vendor: "amd", Name: "Advanced Micro Devices, Inc. [AMD/ATI] Navi 33 [Radeon RX 7600/7600 XT/7600M XT/7600S/7700S / PRO W7600] (rev cf)", Mem: 8573157376}
	if d := PickDevice(ds, g); d != "Vulkan1" {
		t.Fatalf("picked %s", d)
	}
	cuda := ParseDevices("  CUDA0: NVIDIA GeForce RTX 4080 (16376 MiB, 15800 MiB free)\n")
	if d := PickDevice(cuda, GPU{Name: "NVIDIA GeForce RTX 4080", Mem: 16376 << 20}); d != "CUDA0" {
		t.Fatalf("cuda picked %s", d)
	}
	if PickDevice(nil, g) != "" {
		t.Fatal("no devices")
	}
}
