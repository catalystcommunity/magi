// Command magi-inference runs an inference host. It reads one JSON config
// file; see primitives/inference/README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/revconn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/primitives/identity/renew"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
	"github.com/catalystcommunity/magi/primitives/inference/host"
)

type config struct {
	// Listen is the address for callers that connect in. Default
	// 127.0.0.1:8095; with connect and no listen, the host does not listen.
	Listen    *string       `json:"listen"`
	Connect   *connect      `json:"connect"`
	AuthKeys  []string      `json:"auth_keys"`
	Engine    host.Engine   `json:"engine"`
	Rules     *host.Rules   `json:"rules"`
	IdleStop  host.Duration `json:"idle_stop"`
	KeepWarm  bool          `json:"keep_warm"`
	StartWait host.Duration `json:"start_wait"`
	LogFile   string        `json:"engine_log"`
}

// connect: the host connects out to Magi and serves the same API on those
// connections (lib/revconn). Then it needs no open port.
type connect struct {
	URL       string `json:"url"`        // https://<magi>/inference/connect
	TokenFile string `json:"token_file"` // token: subject inference:<name>, scope inference:host
	Idle      int    `json:"idle"`       // idle connections; default 2
	// NoRenew: do not renew the token. By default the host replaces
	// token_file with a renewed token (primitives/identity/renew), so the
	// service account needs write access to its directory.
	NoRenew bool `json:"no_renew"`
}

func main() {
	path := flag.String("config", os.Getenv("MAGI_INFERENCE_CONFIG"), "config file")
	logFile := flag.String("log", "", "log file (default: standard output)")
	flag.Parse()
	out := io.Writer(os.Stdout)
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}
	log := slog.New(slog.NewJSONHandler(out, nil))
	// runService runs as a Windows service when Windows starts the program
	// as one (service_windows.go); else until SIGINT or SIGTERM.
	if err := runService(func(ctx context.Context) error { return run(ctx, *path, log) }); err != nil {
		log.Error("inference", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, path string, log *slog.Logger) error {
	if path == "" {
		return errors.New("-config or MAGI_INFERENCE_CONFIG is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg := config{IdleStop: host.Duration(15 * time.Minute)}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	if len(cfg.AuthKeys) == 0 || len(cfg.Engine.Command) == 0 || cfg.Engine.Port == 0 {
		return errors.New("auth_keys, engine.command, and engine.port are required")
	}
	if cfg.Connect != nil && (cfg.Connect.URL == "" || cfg.Connect.TokenFile == "") {
		return errors.New("connect needs url and token_file")
	}
	listen := ""
	if cfg.Listen != nil {
		listen = *cfg.Listen
	} else if cfg.Connect == nil {
		listen = "127.0.0.1:8095"
	}
	rules := host.DefaultRules()
	if cfg.Rules != nil {
		rules = *cfg.Rules
	}
	ver, err := authn.LoadVerifier(cfg.AuthKeys...)
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	h := host.New(host.Config{
		Engine: cfg.Engine, Rules: rules, IdleStop: time.Duration(cfg.IdleStop), KeepWarm: cfg.KeepWarm,
		StartWait: time.Duration(cfg.StartWait), LogFile: cfg.LogFile,
	}, host.System{}, ver, log)
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	s := &rpc.Server{Verifier: ver, Log: log}
	s.Register("InferenceService", api.InferenceServiceOps(h))
	mux := rpc.Mux(s, nil)
	mux.Handle("/v1/", h.OpenAI())
	if cfg.Connect != nil {
		a := &revconn.Agent{URL: cfg.Connect.URL, Idle: cfg.Connect.Idle, Log: log, Token: func() string {
			b, err := os.ReadFile(cfg.Connect.TokenFile)
			if err != nil {
				log.Warn("inference host token", "err", err)
			}
			return strings.TrimSpace(string(b))
		}}
		log.Info("inference connecting out", "url", cfg.Connect.URL, "model", cfg.Engine.Model)
		go func() { a.Serve(ctx, mux) }()
		if !cfg.Connect.NoRenew {
			u, err := url.Parse(cfg.Connect.URL)
			if err != nil {
				return err
			}
			r := &renew.Renewer{File: cfg.Connect.TokenFile, URL: u.Scheme + "://" + u.Host, Log: log}
			go r.Run(ctx)
		}
	}
	if listen == "" {
		<-ctx.Done()
		<-done
		return nil
	}
	log.Info("inference listening", "addr", listen, "model", cfg.Engine.Model)
	err = rpc.ListenAndServe(ctx, listen, mux)
	stop()
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
