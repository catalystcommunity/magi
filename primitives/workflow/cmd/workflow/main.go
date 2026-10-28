// Command workflow serves WorkflowService.
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
	api "github.com/catalystcommunity/magi/primitives/workflow/api"
	"github.com/catalystcommunity/magi/primitives/workflow/server"
	"github.com/catalystcommunity/magi/primitives/workflow/store"
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
	cdURL := flag.String("corndogs-url", env("MAGI_CORNDOGS_URL", ""), "comma-separated Corndogs addresses (host:port)")
	prefix := flag.String("corndogs-queue-prefix", env("MAGI_CORNDOGS_QUEUE_PREFIX", "magi.workflow."), "prefix of Corndogs queue names")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	fatal := func(msg string, err error) {
		log.Error(msg, "err", err)
		os.Exit(1)
	}
	if *keys == "" || *dbURL == "" || *cdURL == "" {
		fatal("config", errors.New("MAGI_AUTH_KEYS, MAGI_DATABASE_URL, and MAGI_CORNDOGS_URL are required"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	verifier, err := authn.LoadVerifier(strings.Split(*keys, ",")...)
	if err != nil {
		fatal("auth keys", err)
	}
	st, err := store.Open(ctx, *dbURL)
	if err != nil {
		fatal("database", err)
	}
	defer st.Close()
	q, err := server.NewCorndogs(ctx, *cdURL, *prefix)
	if err != nil {
		fatal("corndogs", err)
	}
	svc := &server.Service{Store: st, Queue: q, Log: log}
	go svc.Sweep(ctx)

	rs := &rpc.Server{Verifier: verifier, Log: log}
	rs.Register("WorkflowService", api.WorkflowServiceOps(svc))
	log.Info("listening", "addr", *listen)
	if err := rpc.ListenAndServe(ctx, *listen, rpc.Mux(rs, func() error { return st.DB.Ping(ctx) })); err != nil {
		fatal("serve", err)
	}
}
