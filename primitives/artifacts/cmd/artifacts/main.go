// Command artifacts serves ArtifactService.
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
	"github.com/catalystcommunity/magi/primitives/artifacts/server"
	"github.com/catalystcommunity/magi/primitives/artifacts/store"
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
	dir := flag.String("artifact-dir", env("MAGI_ARTIFACT_DIR", "/var/lib/magi/artifacts"), "body storage directory")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*listen, *keys, *dbURL, *dir, log); err != nil {
		log.Error("artifacts", "err", err)
		os.Exit(1)
	}
}

func run(listen, keys, dbURL, dir string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if keys == "" || dbURL == "" {
		return errors.New("MAGI_AUTH_KEYS and MAGI_DATABASE_URL are required")
	}
	v, err := authn.LoadVerifier(strings.Split(keys, ",")...)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	s := &rpc.Server{Verifier: v, MaxBody: server.MaxRequest, Log: log}
	server.Register(s, &server.Service{DB: db, Blobs: store.Blobs{Dir: dir}})
	log.Info("artifacts listening", "addr", listen)
	return rpc.ListenAndServe(ctx, listen, rpc.Mux(s, func() error { return db.Ping(ctx) }))
}
