// Command magi-server serves the Magi API and runs Magi's background workers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // MAGI_TIME_ZONE in an image with no zone files

	"github.com/catalystcommunity/magi/lib/authn"
	"net/http"

	"github.com/catalystcommunity/magi/lib/revconn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/compose"
	"github.com/catalystcommunity/magi/magi/model"
	"github.com/catalystcommunity/magi/magi/web"
)

func main() {
	// "magi-server preflight": the release controller runs this build once
	// before it changes the Deployment (primitives/release, "Preflight").
	if len(os.Args) > 1 && os.Args[1] == "preflight" {
		log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
		if err := preflight(log); err != nil {
			log.Error("preflight failed", "err", err)
			os.Exit(1)
		}
		log.Info("preflight passed")
		return
	}
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
	// MAGI_LIBRARIAN_MODELS lists the librarian endpoints, usually only a
	// local inference host; without it the librarian uses the persona chain.
	if os.Getenv("MAGI_LIBRARIAN_MODELS") != "" {
		if sys.LibrarianModel, err = model.ChainFromEnv("MAGI_LIBRARIAN_MODELS", m); err != nil {
			return err
		}
	} else {
		sys.LibrarianModel = persona
	}
	// The egress review is background work too: the local model first.
	sys.ReviewModel = sys.LibrarianModel
	// MAGI_DETECTOR_URL: the prompt injection detector (compose/detect.go).
	if sys.Detector, err = compose.DetectorFromEnv(); err != nil {
		return err
	}
	// MAGI_MODEL_EGRESS=true: all model calls of Magi go through the egress
	// proxy, as the proxy user svc_magi. Then the provider key can be a
	// proxy secret, and Magi holds only its placeholder.
	var modelTransport http.RoundTripper
	if os.Getenv("MAGI_MODEL_EGRESS") == "true" {
		if cfg.EgressProxy == nil {
			return errors.New("MAGI_MODEL_EGRESS needs MAGI_EGRESS_PROXY")
		}
		t, err := cfg.EgressProxy.Transport(compose.ServiceUser("magi"))
		if err != nil {
			return err
		}
		modelTransport = t
		log.Info("model calls go through the egress proxy", "proxy", cfg.EgressProxy.Addr)
	}
	// MAGI_SEARCH_URL: the SearXNG service of web_search. Pages that the
	// personas read go through the egress proxy as svc_search when it is
	// set; the proxy policy must permit that user.
	if u := os.Getenv("MAGI_SEARCH_URL"); u != "" {
		web := &http.Client{Timeout: 25 * time.Second}
		if cfg.EgressProxy != nil {
			t, err := cfg.EgressProxy.Transport(compose.ServiceUser("search"))
			if err != nil {
				return err
			}
			web.Transport = t
		}
		sys.Search = &compose.Searcher{URL: u, HTTP: &http.Client{Timeout: 20 * time.Second}, Web: web}
		log.Info("web search on", "url", u, "pages_through_proxy", cfg.EgressProxy != nil)
	}
	// Inference hosts connect out to Magi (/inference/connect) and keep
	// idle connections; a model endpoint http://<host>.revconn/v1 uses
	// them. Other endpoints use modelTransport.
	hosts := &revconn.Pool{Log: log}
	for _, c := range []model.Chatter{model.Chain{m}, persona, sys.LibrarianModel} {
		if ch, ok := c.(model.Chain); ok {
			ch.SetTransport(hosts.Transport(modelTransport))
		}
	}
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
	srv.Register("RunnerService", magiapi.RunnerServiceOps(&compose.Runners{S: sys}))
	srv.Register("EgressAskService", magiapi.EgressAskServiceOps(&compose.Egress{S: sys}))
	mux := rpc.Mux(srv, nil)
	// The release controller checks the services that Magi needs here. The
	// readiness probe stays /healthz.
	mux.Handle("/healthz/deps", sys.DepsHandler())
	// Runners use the coding model through Magi, so they hold no provider key.
	mux.Handle("/runner/v1/chat/completions", compose.ModelProxy(ver, m))
	// Runner sandboxes reach the egress proxy through Magi.
	mux.Handle("/runner/egress", compose.EgressTunnel(ver, sys.EgressProxy))
	mux.Handle("/inference/connect", hosts.Handler(compose.InferenceHostName(ver)))
	context.AfterFunc(ctx, hosts.Close)
	mux.Handle("/", web.Handler())
	log.Info("listening", "addr", listen)
	return rpc.ListenAndServe(ctx, listen, mux)
}

// preflight reads the configuration as the server does, and calls one read
// operation of each service with the clients of this build. It starts no
// workers and changes nothing.
func preflight(log *slog.Logger) error {
	cfg, err := compose.ConfigFromEnv()
	if err != nil {
		return err
	}
	m, err := model.FromEnv()
	if err != nil {
		return err
	}
	for _, v := range []string{"MAGI_PERSONA_MODELS", "MAGI_LIBRARIAN_MODELS"} {
		if os.Getenv(v) != "" {
			if _, err := model.ChainFromEnv(v, m); err != nil {
				return fmt.Errorf("%s: %w", v, err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	checks := compose.New(cfg, m, log).CheckDeps(ctx)
	for _, c := range checks {
		if c.Err != nil {
			log.Error("dependency", "service", c.Name, "ms", c.Ms, "err", c.Err.Error())
		} else {
			log.Info("dependency", "service", c.Name, "ms", c.Ms)
		}
	}
	if bad := compose.DepsFailed(checks); bad != nil {
		return fmt.Errorf("%d of %d dependency checks failed: %s", len(bad), len(checks), strings.Join(bad, "; "))
	}
	return nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
