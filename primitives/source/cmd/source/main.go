// Command source serves SourceService and Git smart HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/primitives/source/server"
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
	dir := flag.String("source-dir", env("MAGI_SOURCE_DIR", "/var/lib/magi/source"), "directory of bare repositories")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*listen, *keys, *dir, log); err != nil {
		log.Error("source", "err", err)
		os.Exit(1)
	}
}

func run(listen, keys, dir string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if keys == "" {
		return errors.New("MAGI_AUTH_KEYS is required")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is not installed")
	}
	v, err := authn.LoadVerifier(strings.Split(keys, ",")...)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	s := &rpc.Server{Verifier: v, MaxBody: server.MaxRequest, Log: log}
	h := server.Handler(s, &server.Service{Dir: dir})
	log.Info("source listening", "addr", listen, "dir", dir)
	return rpc.ListenAndServe(ctx, listen, h)
}
