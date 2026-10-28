// Command magi-server serves the Magi API and runs Magi's background workers.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/compose"
	"github.com/catalystcommunity/magi/magi/model"
	"github.com/catalystcommunity/magi/magi/web"
)

func main() {
	listen := flag.String("listen", envOr("MAGI_LISTEN", ":8080"), "listen address")
	keys := flag.String("auth-keys", os.Getenv("MAGI_AUTH_KEYS"), "comma-separated issuer key files")
	workers := flag.Bool("workers", envOr("MAGI_WORKERS", "true") == "true", "run background workers")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*listen, *keys, *workers, log); err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(listen, keys string, workers bool, log *slog.Logger) error {
	ver, err := authn.LoadVerifier(strings.Split(keys, ",")...)
	if err != nil {
		return err
	}
	cfg, err := compose.ConfigFromEnv()
	if err != nil {
		return err
	}
	m, err := model.FromEnv()
	if err != nil {
		return err
	}
	// MAGI_PERSONA_MODELS lists the persona endpoints in order of
	// preference, for example a local inference host and then a provider.
	persona, err := model.ChainFromEnv("MAGI_PERSONA_MODELS", m)
	if err != nil {
		return err
	}
	sys := compose.New(cfg, persona, log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if workers {
		for {
			if err := sys.InstallDefinitions(ctx); err == nil {
				break
			} else {
				log.Warn("install definitions", "err", err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
		}
		sys.RunWorkers(ctx)
	}
	srv := &rpc.Server{Verifier: ver, Log: log, MaxBody: 20 << 20}
	srv.Register("MagiService", magiapi.MagiServiceOps(&compose.Service{S: sys}))
	mux := rpc.Mux(srv, nil)
	mux.Handle("/", web.Handler())
	log.Info("listening", "addr", listen)
	return rpc.ListenAndServe(ctx, listen, mux)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
