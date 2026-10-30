// Command magi-runner runs Magi code-change steps on this machine for one
// Magi installation. It connects to Magi; it never listens on the network.
//
//	magi-runner -config ~/.config/magi-runner/magi.example.com.json
//
// Run one runner for each installation (dev, production), each with its
// own configuration, token, and state directory. Started as
// "magi-runner envd ...", it is the envd inside a sandbox.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

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
	path := flag.String("config", "", "configuration file")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(*path, log); err != nil {
		log.Error("magi-runner", "err", err)
		os.Exit(1)
	}
}

func run(path string, log *slog.Logger) error {
	cfg, err := runner.LoadConfig(path)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("bubblewrap is required: %w", err)
	}
	for _, p := range append([]string{cfg.StateDir}, cfg.Sandbox.Shared...) {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
	}
	token := func() string {
		b, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			log.Error("runner token", "err", err)
		}
		return strings.TrimSpace(string(b))
	}
	r := &runner.Runner{
		Cfg: cfg, Log: log,
		Magi: magiapi.NewRunnerClient(rpc.NewClient(cfg.Server, token)),
		Boxes: &sandbox.Sandboxes{Cfg: sandbox.Config{Root: filepath.Join(cfg.StateDir, "envs"), Envd: self, EnvdArgs: []string{"envd"},
			Bwrap: bwrap, ReadOnly: cfg.Sandbox.ReadOnly, Shared: cfg.Sandbox.Shared, Env: cfg.Sandbox.Env}},
		Source: &sourceserver.Service{Dir: filepath.Join(cfg.StateDir, "repos")},
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return r.Run(ctx)
}
