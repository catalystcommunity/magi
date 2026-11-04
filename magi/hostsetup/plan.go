package hostsetup

import (
	"fmt"
	"sort"
	"strings"
)

// Fit rules. The checks apply them in code, before the model sees the
// lists, because every measured model sometimes broke them.
const (
	// GPUHeadroom is the GPU memory that the KV cache and the engine need
	// on top of the model file.
	GPUHeadroom = 2 * gib
	// RAMHeadroom is the same for the CPU engine.
	RAMHeadroom = 2 * gib
	// DiskHeadroom stays free on the disk after the download.
	DiskHeadroom = 1 * gib
	// CPUMaxModel is the largest model file for the CPU engine. A larger
	// model is too slow on a CPU to be useful.
	CPUMaxModel = 8_000_000_000
)

// kindOrder is the preference of engine kinds, fastest first.
var kindOrder = map[string]int{"cuda": 0, "rocm": 1, "vulkan": 2, "cpu": 3}

// Choice is a model that fits, with the engines that can run it.
type Choice struct {
	Model   Model
	Engines []string
}

// Plan is what the model gets: the facts, and the engines and models that
// passed the checks.
type Plan struct {
	Role    string
	Facts   *Facts
	Engines []Engine
	// gpuOf has the GPU of each GPU engine.
	gpuOf   map[string]GPU
	Choices []Choice
	// Removed has a reason for each pinned item that did not pass.
	Removed []string
	Speech  []SpeechItem
	SpeechE []SpeechItem
}

// NewPlan applies the fit rules to the pins.
func NewPlan(p *Pins, f *Facts, role string) *Plan {
	pl := &Plan{Role: role, Facts: f, gpuOf: map[string]GPU{}}
	if role == "speech" {
		pl.SpeechE = p.Speech.Engines
		for _, m := range p.Speech.Models {
			if m.Size+DiskHeadroom > f.DiskFree {
				pl.Removed = append(pl.Removed, fmt.Sprintf("%s: the free disk (%s) is too small", m.Name, GB(f.DiskFree)))
				continue
			}
			pl.Speech = append(pl.Speech, m)
		}
		return pl
	}
	for _, e := range p.Engines {
		if e.Arch != f.Arch {
			continue
		}
		if !e.GPU() {
			pl.Engines = append(pl.Engines, e)
			continue
		}
		g, why := gpuFor(e.Kind, f.GPUs)
		if why != "" {
			pl.Removed = append(pl.Removed, e.Name+": "+why)
			continue
		}
		pl.gpuOf[e.Name] = g
		pl.Engines = append(pl.Engines, e)
	}
	sort.SliceStable(pl.Engines, func(i, j int) bool { return kindOrder[pl.Engines[i].Kind] < kindOrder[pl.Engines[j].Kind] })
	models := append([]Model{}, p.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].Size > models[j].Size })
	for _, m := range models {
		if m.Size+DiskHeadroom > f.DiskFree {
			pl.Removed = append(pl.Removed, fmt.Sprintf("%s: the file (%s) does not fit the free disk (%s)", m.Name, GB(m.Size), GB(f.DiskFree)))
			continue
		}
		var es, why []string
		for _, e := range pl.Engines {
			if r := pl.fits(m, e); r == "" {
				es = append(es, e.Name)
			} else {
				why = append(why, e.Name+": "+r)
			}
		}
		if len(es) == 0 {
			pl.Removed = append(pl.Removed, m.Name+": "+strings.Join(why, "; "))
			continue
		}
		pl.Choices = append(pl.Choices, Choice{Model: m, Engines: es})
	}
	return pl
}

// GPUOf returns the GPU of a GPU engine.
func (pl *Plan) GPUOf(engine string) (GPU, bool) {
	g, ok := pl.gpuOf[engine]
	return g, ok
}

// fits returns "" when m fits on e, else the reason.
func (pl *Plan) fits(m Model, e Engine) string {
	if e.GPU() {
		g := pl.gpuOf[e.Name]
		if m.Size+GPUHeadroom > g.Free() {
			return fmt.Sprintf("needs %s of GPU memory, the GPU has %s free (%s in use)", GB(m.Size+GPUHeadroom), GB(g.Free()), GB(g.Used))
		}
		return ""
	}
	if m.Size > CPUMaxModel {
		return fmt.Sprintf("larger than %s, too slow on CPU", GB(CPUMaxModel))
	}
	if m.Size+RAMHeadroom > pl.Facts.RAMAvail {
		return fmt.Sprintf("needs %s of available RAM, the host has %s", GB(m.Size+RAMHeadroom), GB(pl.Facts.RAMAvail))
	}
	return ""
}

// gpuFor returns the GPU that an engine kind can use, or the reason that
// there is none.
func gpuFor(kind string, gs []GPU) (GPU, string) {
	var why string
	for _, g := range gs {
		switch kind {
		case "cuda":
			if g.Vendor != "nvidia" {
				continue
			}
			if !g.Driver {
				why = "the NVIDIA driver does not answer"
				continue
			}
		case "rocm":
			if g.Vendor != "amd" {
				continue
			}
			if !g.Driver {
				why = "ROCm (rocm-smi) does not answer"
				continue
			}
		case "vulkan":
			if g.Vendor != "amd" && g.Vendor != "nvidia" {
				continue
			}
			if !g.Vulkan {
				why = "Vulkan (vulkaninfo) does not answer for this GPU"
				continue
			}
		default:
			continue
		}
		if g.Mem == 0 {
			why = "the GPU memory is not known"
			continue
		}
		return g, ""
	}
	if why == "" {
		why = "no GPU for this engine"
	}
	return GPU{}, why
}

// Allowed tells if a download or start of item is in the plan.
func (pl *Plan) Allowed(item string) bool {
	item = strings.TrimSpace(item)
	for _, e := range pl.Engines {
		if e.Name == item {
			return true
		}
	}
	for _, c := range pl.Choices {
		if c.Model.Name == item {
			return true
		}
	}
	for _, s := range append(append([]SpeechItem{}, pl.Speech...), pl.SpeechE...) {
		if s.Name == item {
			return true
		}
	}
	return false
}

// Suggested is the largest model on the most preferred engine.
func (pl *Plan) Suggested() (engine, model string) {
	best := -1
	for _, c := range pl.Choices {
		for _, e := range c.Engines {
			k := kindOrder[pl.kind(e)]
			if best < 0 || k < best {
				best, engine, model = k, e, c.Model.Name
			}
		}
	}
	return engine, model
}

func (pl *Plan) kind(name string) string {
	for _, e := range pl.Engines {
		if e.Name == name {
			return e.Kind
		}
	}
	return ""
}

// setupPrompt is the system prompt of the setup model. The deterministic
// checks run before it; the model does the work that needs judgement.
const setupPrompt = `You set up this computer as a Magi %s host. The setup program already checked that the host runs Linux, collected the facts below, and removed the engine builds and models that do not fit this host.

Use the tools: choose an engine build and a model from the lists, download them, start the engine, and finish. Run commands only to learn what the facts do not tell, or to find the cause of a failure.

Engine builds for this host:
%s
Models for this host:
%s
Rules:
- Use the facts: GPU (vendor and memory), RAM (available), free disk. Do not guess them.
- Use only items from the lists above. Choose the largest model that fits, on the fastest engine that can run it.
- Do not run commands that need sudo or a password, and do not install drivers or packages yourself. Ask the person with ask_person, and give the exact command for this distribution.
- When a download fails its hash check, stop: do not try again and do not skip the check. Finish with status "failed".
- When something fails, find the cause before you change the plan.
- Tell the person about each problem that the checks found.
- Finish with the finish tool. Use status "ok" only when the engine is healthy.

Facts:
%s`

// Prompt is the system prompt for the plan.
func (pl *Plan) Prompt() string {
	var eng, mdl, facts strings.Builder
	f := pl.Facts
	if pl.Role == "speech" {
		for _, e := range pl.SpeechE {
			fmt.Fprintf(&eng, "- %s: needs %s.\n", e.Name, e.Needs)
		}
		for _, m := range pl.Speech {
			fmt.Fprintf(&mdl, "- %s (%s): file %s.\n", m.Name, m.Use, GB(m.Size))
		}
		mdl.WriteString("All of them are necessary. Give the names separated by a comma to start_engine.\n")
	} else {
		for _, e := range pl.Engines {
			if g, ok := pl.gpuOf[e.Name]; ok {
				fmt.Fprintf(&eng, "- %s: GPU %s, %s of free GPU memory.\n", e.Name, g.Name, GB(g.Free()))
			} else {
				fmt.Fprintf(&eng, "- %s: the CPU, %s of available RAM.\n", e.Name, GB(f.RAMAvail))
			}
		}
		for _, c := range pl.Choices {
			fmt.Fprintf(&mdl, "- %s: file %s. Fits on: %s.\n", c.Model.Name, GB(c.Model.Size), strings.Join(c.Engines, ", "))
		}
		if len(pl.Choices) == 0 {
			mdl.WriteString("(none: no model fits this host)\n")
		}
	}
	fmt.Fprintf(&facts, "Distribution: %s. Architecture: %s. Available RAM: %s. Free disk: %s.\n", f.Distro, f.Arch, GB(f.RAMAvail), GB(f.DiskFree))
	if e, m := pl.Suggested(); e != "" {
		fmt.Fprintf(&facts, "Suggested by the checks: %s with %s.\n", e, m)
	}
	for _, p := range f.Problems {
		fmt.Fprintf(&facts, "Problem: %s\n", p)
	}
	for _, r := range pl.Removed {
		fmt.Fprintf(&facts, "Removed by the checks: %s.\n", r)
	}
	facts.WriteString("\nProbe outputs:\n\n")
	facts.WriteString(f.Raw)
	return fmt.Sprintf(setupPrompt, pl.Role, eng.String(), mdl.String(), facts.String())
}
