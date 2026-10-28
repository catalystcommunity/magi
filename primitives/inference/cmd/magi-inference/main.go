// Command magi-inference runs an inference host. It reads one JSON config
// file; see primitives/inference/README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
	"github.com/catalystcommunity/magi/primitives/inference/host"
)

type config struct {
	Listen    string        `json:"listen"`
	AuthKeys  []string      `json:"auth_keys"`
	Engine    host.Engine   `json:"engine"`
	Rules     *host.Rules   `json:"rules"`
	IdleStop  host.Duration `json:"idle_stop"`
	StartWait host.Duration `json:"start_wait"`
	LogFile   string        `json:"engine_log"`
}

func main() {
	path := flag.String("config", os.Getenv("MAGI_INFERENCE_CONFIG"), "config file")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*path, log); err != nil {
		log.Error("inference", "err", err)
		os.Exit(1)
	}
}

func run(path string, log *slog.Logger) error {
	if path == "" {
		return errors.New("-config or MAGI_INFERENCE_CONFIG is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg := config{Listen: "127.0.0.1:8095", IdleStop: host.Duration(15 * time.Minute)}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	if len(cfg.AuthKeys) == 0 || len(cfg.Engine.Command) == 0 || cfg.Engine.Port == 0 {
		return errors.New("auth_keys, engine.command, and engine.port are required")
	}
	rules := host.DefaultRules()
	if cfg.Rules != nil {
		rules = *cfg.Rules
	}
	ver, err := authn.LoadVerifier(cfg.AuthKeys...)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h := host.New(host.Config{
		Engine: cfg.Engine, Rules: rules, IdleStop: time.Duration(cfg.IdleStop),
		StartWait: time.Duration(cfg.StartWait), LogFile: cfg.LogFile,
	}, host.System{}, ver, log)
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	s := &rpc.Server{Verifier: ver, Log: log}
	s.Register("InferenceService", api.InferenceServiceOps(h))
	mux := rpc.Mux(s, nil)
	mux.Handle("/v1/", h.OpenAI())
	log.Info("inference listening", "addr", cfg.Listen, "model", cfg.Engine.Model)
	err = rpc.ListenAndServe(ctx, cfg.Listen, mux)
	stop()
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
