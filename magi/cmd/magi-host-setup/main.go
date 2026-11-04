// Command magi-host-setup makes this machine into a Magi model host. It
// runs on the host as the person who sets it up (not root):
//
//	magi-host-setup [-dir DIR] [-confirm]         check, then set up with the local setup model
//	magi-host-setup -check                        show the checks and the plan only
//	magi-host-setup -model-url URL -model NAME    use an OpenAI-compatible endpoint as the setup model
//
// Deterministic checks run first (Linux, architecture, RAM, disk, GPU and
// driver). They remove the engines and models that do not fit. A local
// model (Qwen3.5 9B on the CPU, pinned in hostsetup/pins.json) then does
// the setup with a shell, pinned downloads, and questions to the person.
// The person sees each command and its output. The result is
// DIR/inference-engine.json, the "engine" part of the inference host
// configuration (primitives/inference).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/magi/hostsetup"
	"github.com/catalystcommunity/magi/magi/model"
)

func main() {
	if runtime.GOOS != "linux" {
		fmt.Fprintf(os.Stderr, "magi-host-setup: this host runs %s. Magi host setup supports only Linux hosts (not BSD, macOS, or Windows).\n", runtime.GOOS)
		os.Exit(2)
	}
	home, _ := os.UserHomeDir()
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	dir := flag.String("dir", filepath.Join(data, "magi", "host-setup"), "directory for engines, models, logs, and the result")
	role := flag.String("role", "model", "host role: model (speech is not built yet)")
	check := flag.Bool("check", false, "show the checks and the plan, then stop")
	confirm := flag.Bool("confirm", false, "ask before each command of the setup model")
	port := flag.Int("port", 18091, "port of the engine in the result configuration")
	modelURL := flag.String("model-url", "", "OpenAI-compatible base URL of the setup model (default: start the local setup model)")
	modelName := flag.String("model", "", "model name at -model-url")
	keyFile := flag.String("model-key-file", "", "file with the API key for -model-url")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	code, err := run(ctx, *dir, *role, *check, *confirm, *port, *modelURL, *modelName, *keyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "magi-host-setup:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func run(ctx context.Context, dir, role string, check, confirm bool, port int, modelURL, modelName, keyFile string) (int, error) {
	if role == "speech" {
		return 2, errors.New("the speech role is not built yet; use primitives/speech/engine/install.sh")
	}
	if role != "model" {
		return 2, fmt.Errorf("unknown role %q", role)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 1, err
	}
	pins, err := hostsetup.LoadPins()
	if err != nil {
		return 1, err
	}
	facts, err := hostsetup.Gather(quiet, role, dir)
	if err != nil {
		return 2, err
	}
	plan := hostsetup.NewPlan(pins, facts, role)
	show(plan)
	if len(plan.Choices) == 0 {
		return 1, errors.New("no pinned model fits this host")
	}
	if check {
		return 0, nil
	}
	local := &hostsetup.Local{Dir: dir, Pins: pins, Arch: facts.Arch, Out: os.Stdout, In: bufio.NewReader(os.Stdin),
		Confirm: confirm, RAMAvail: hostsetup.ReadRAMAvail, GPUOf: plan.GPUOf}

	var chat model.Chatter
	var helper *hostsetup.Helper
	if modelURL != "" {
		c := &model.Client{BaseURL: strings.TrimRight(modelURL, "/"), Model: modelName, HTTP: &http.Client{Timeout: 10 * time.Minute}}
		if keyFile != "" {
			b, err := os.ReadFile(keyFile)
			if err != nil {
				return 1, err
			}
			c.APIKey = strings.TrimSpace(string(b))
		}
		chat = c
	} else {
		helper, err = local.StartHelper(ctx, facts.RAMAvail)
		if err != nil {
			return 1, err
		}
		defer helper.Stop()
		chat = helper.Client
	}

	fmt.Println("\nThe setup model starts. You see each command and its output.")
	r, err := hostsetup.Run(ctx, chat, local, plan, func(c hostsetup.Call) {
		switch c.Tool {
		case "run_command", "ask_person":
			// Local prints these when they run.
		case "finish":
		default:
			fmt.Printf("\n[%s %s] %s\n", c.Tool, c.Arg, c.Out)
		}
	})
	helper.Stop()
	if err != nil {
		return 1, err
	}
	fmt.Printf("\nSetup model: %s\n%s\n", r.Finish["status"], r.Finish["summary"])
	switch r.Finish["status"] {
	case "ok":
	case "needs-person":
		return 3, nil
	default:
		return 1, nil
	}
	if r.Engine == "" {
		return 1, errors.New("the setup model said ok, but no engine start was healthy")
	}
	fmt.Printf("\nFull start check of %s with %s ...\n", r.Engine, r.Model)
	p, err := local.Finalize(ctx, r.Engine, r.Model, port)
	if err != nil {
		return 1, err
	}
	fmt.Printf("The engine is healthy. The engine configuration is in %s.\nPut its \"engine\" part in the inference host configuration (primitives/inference/README.md).\n", p)
	return 0, nil
}

// quiet runs a probe command. Probes only read the host.
func quiet(cmd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, _ := exec.CommandContext(ctx, "/bin/sh", "-c", cmd).CombinedOutput()
	return string(b)
}

func show(pl *hostsetup.Plan) {
	f := pl.Facts
	fmt.Printf("Host: %s %s, %s of available RAM, %s of free disk.\n", f.Distro, f.Arch, hostsetup.GB(f.RAMAvail), hostsetup.GB(f.DiskFree))
	for _, g := range f.GPUs {
		fmt.Printf("GPU: %s %s, %s (%s free), driver answers: %v, Vulkan: %v\n", g.Vendor, g.Name, hostsetup.GB(g.Mem), hostsetup.GB(g.Free()), g.Driver, g.Vulkan)
	}
	for _, p := range f.Problems {
		fmt.Println("Problem:", p)
	}
	for _, r := range pl.Removed {
		fmt.Println("Removed:", r)
	}
	for _, c := range pl.Choices {
		fmt.Printf("Fits: %s (%s) on %s\n", c.Model.Name, hostsetup.GB(c.Model.Size), strings.Join(c.Engines, ", "))
	}
	if e, m := pl.Suggested(); e != "" {
		fmt.Printf("Suggested: %s with %s\n", e, m)
	}
}
