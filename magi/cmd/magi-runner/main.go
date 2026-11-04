// Command magi-runner runs Magi code-change steps on this machine for one
// Magi installation. It connects to Magi; it never listens on the network.
//
//	magi-runner supervise -config FILE   run the current release, roll back failed updates
//	magi-runner -config FILE             run the runner (the supervisor starts this)
//	magi-runner selftest -config FILE    test this release on this machine
//	magi-runner install -config FILE -version V PACKAGE   install a first release
//
// Run one runner for each installation (dev, production), each with its
// own configuration, token, and state directory. Started as
// "magi-runner envd ...", it is the envd inside a sandbox.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/runner"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
	sourceserver "github.com/catalystcommunity/magi/primitives/source/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "envd" {
		envd.Main(os.Args[2:])
		return
	}
	mode := "run"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		mode, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	path := fs.String("config", "", "configuration file")
	version := fs.String("version", "", "release version (install)")
	fs.Parse(args)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := func() error {
		cfg, err := runner.LoadConfig(*path)
		if err != nil {
			return err
		}
		switch mode {
		case "supervise":
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return runner.Supervise(ctx, *path, cfg, log)
		case "install":
			if fs.NArg() != 1 || *version == "" {
				return errors.New("install -config FILE -version V PACKAGE")
			}
			b, err := os.ReadFile(fs.Arg(0))
			if err != nil {
				return err
			}
			return runner.Install(cfg.StateDir, *version, b)
		case "run", "selftest":
			r, err := build(*path, cfg, log)
			if err != nil {
				return err
			}
			if mode == "selftest" {
				// A macOS task VM boots a whole system in the self-test.
				ctx, stop := context.WithTimeout(context.Background(), 5*time.Minute)
				defer stop()
				if err := runner.SelfTest(ctx, r); err != nil {
					return err
				}
				fmt.Println("self-test passed", runner.Version())
				return nil
			}
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return r.Run(ctx)
		}
		return fmt.Errorf("unknown command %q", mode)
	}()
	if errors.Is(err, runner.ErrRestart) {
		log.Info("restarting into the new release")
		os.Exit(runner.ExitRestart)
	}
	if err != nil {
		log.Error("magi-runner", "mode", mode, "err", err)
		os.Exit(1)
	}
}

func build(path string, cfg runner.Config, log *slog.Logger) (*runner.Runner, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	box := sandbox.Config{Root: filepath.Join(cfg.StateDir, "envs"), Envd: self, EnvdArgs: []string{"envd"},
		ReadOnly: cfg.Sandbox.ReadOnly, Shared: cfg.Sandbox.Shared, Env: cfg.Sandbox.Env}
	if runtime.GOOS == "darwin" && cfg.Sandbox.VM.Template != "" {
		home, _ := os.UserHomeDir()
		hv := cfg.Sandbox.VM.Hypervisor
		if hv == "" {
			hv = filepath.Join(home, ".local", "share", "magi-runner-tools", "vm", "magi-vz")
		}
		for _, p := range []string{hv, filepath.Join(cfg.Sandbox.VM.Template, "disk.img")} {
			if _, err := os.Stat(p); err != nil {
				return nil, fmt.Errorf("macOS task VMs: %w", err)
			}
		}
		box.VM = &sandbox.VM{Template: cfg.Sandbox.VM.Template, Hypervisor: hv}
		log.Info("environments are macOS task VMs", "template", cfg.Sandbox.VM.Template)
	} else if runtime.GOOS == "darwin" {
		if box.SandboxExec, err = exec.LookPath("sandbox-exec"); err != nil {
			return nil, fmt.Errorf("sandbox-exec is required: %w", err)
		}
	} else if box.Bwrap, err = exec.LookPath("bwrap"); err != nil {
		return nil, fmt.Errorf("bubblewrap is required: %w", err)
	}
	if v := cfg.Sandbox.VM; v.Kernel != "" && runtime.GOOS == "linux" {
		if box.VM, err = taskVM(v.Kernel, v.Hypervisor, v.Virtiofsd); err != nil {
			return nil, err
		}
		box.VM.CPUs, box.VM.MemoryMB = v.CPUs, v.MemoryMB
		log.Info("environments are task VMs", "kernel", v.Kernel, "hypervisor", box.VM.Hypervisor)
	}
	for _, p := range append([]string{cfg.StateDir}, cfg.Sandbox.Shared...) {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, err
		}
	}
	token := func() string {
		b, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			log.Error("runner token", "err", err)
		}
		return strings.TrimSpace(string(b))
	}
	return runner.New(path, cfg, log, magiapi.NewRunnerClient(rpc.NewClient(cfg.Server, token)), &sandbox.Sandboxes{Cfg: box},
		&sourceserver.Service{Dir: filepath.Join(cfg.StateDir, "repos")}), nil
}

// taskVM checks the files of task VMs and fills in the defaults.
func taskVM(kernel, hypervisor, virtiofsd string) (*sandbox.VM, error) {
	home, _ := os.UserHomeDir()
	if hypervisor == "" {
		hypervisor = filepath.Join(home, ".local", "share", "magi-runner-tools", "vm", "cloud-hypervisor")
	}
	if virtiofsd == "" {
		for _, p := range []string{"/usr/libexec/virtiofsd", "/usr/lib/virtiofsd", "/usr/lib/qemu/virtiofsd",
			filepath.Join(home, ".local", "share", "magi-runner-tools", "vm", "virtiofsd")} {
			if _, err := os.Stat(p); err == nil {
				virtiofsd = p
				break
			}
		}
	}
	for _, p := range []string{kernel, hypervisor, virtiofsd, "/dev/kvm"} {
		if p == "" {
			return nil, errors.New("task VMs need virtiofsd (package virtiofsd)")
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("task VMs: %w", err)
		}
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		return nil, fmt.Errorf("task VMs need read and write access to /dev/kvm (the group kvm): %w", err)
	} else {
		f.Close()
	}
	return &sandbox.VM{Kernel: kernel, Hypervisor: hypervisor, Virtiofsd: virtiofsd}, nil
}
