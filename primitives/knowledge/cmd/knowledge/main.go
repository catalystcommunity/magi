// Command knowledge serves KnowledgeService.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/primitives/knowledge/server"
	"github.com/catalystcommunity/magi/primitives/knowledge/store"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	listen := flag.String("listen", env("MAGI_LISTEN", ":8080"), "listen address")
	keys := flag.String("auth-keys", env("MAGI_AUTH_KEYS", ""), "comma-separated issuer key files")
	dbURL := flag.String("database-url", env("MAGI_DATABASE_URL", ""), "PostgreSQL URL")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*listen, *keys, *dbURL, log); err != nil {
		log.Error("knowledge", "err", err)
		os.Exit(1)
	}
}

func run(listen, keys, dbURL string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if keys == "" || dbURL == "" {
		return errors.New("MAGI_AUTH_KEYS and MAGI_DATABASE_URL are required")
	}
	v, err := authn.LoadVerifier(strings.Split(keys, ",")...)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	s := &rpc.Server{Verifier: v, Log: log}
	server.Register(s, &server.Service{Store: st})
	log.Info("knowledge listening", "addr", listen)
	return rpc.ListenAndServe(ctx, listen, rpc.Mux(s, func() error { return st.DB.Ping(ctx) }))
}
