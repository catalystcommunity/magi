// Command execution serves ExecutionService and the envd builds that
// environments download. Started under the file name "envd", it runs envd,
// so an image without separate envd builds can serve itself.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/primitives/execution/backend"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	"github.com/catalystcommunity/magi/primitives/execution/server"
	"github.com/catalystcommunity/magi/primitives/execution/store"
	"k8s.io/client-go/kubernetes"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil {
		return d
	}
	return def
}

type config struct {
	listen, keys, dbURL              string
	backends, envdDir, localRoot     string
	namespace, controlNS, publicURL  string
	delivery, connect, fetchImage    string
	pullSecret, defaultImage         string
	kubeconfig, kubeContext          string
	defaultTTL, maxTTL, reapInterval time.Duration
}

func main() {
	if filepath.Base(os.Args[0]) == "envd" {
		envd.Main(os.Args[1:])
		return
	}
	var c config
	flag.StringVar(&c.listen, "listen", env("MAGI_LISTEN", ":8080"), "listen address")
	flag.StringVar(&c.keys, "auth-keys", env("MAGI_AUTH_KEYS", ""), "comma-separated issuer key files")
	flag.StringVar(&c.dbURL, "database-url", env("MAGI_DATABASE_URL", ""), "PostgreSQL URL")
	flag.StringVar(&c.backends, "backend", env("MAGI_EXEC_BACKEND", "kubernetes"), "comma-separated backends (kubernetes, local); the first is the default")
	flag.StringVar(&c.envdDir, "envd-dir", env("MAGI_EXEC_ENVD_DIR", "/usr/local/lib/magi/envd"), "directory with envd-linux-amd64 and envd-linux-arm64")
	flag.StringVar(&c.localRoot, "local-root", env("MAGI_EXEC_LOCAL_ROOT", filepath.Join(os.TempDir(), "magi-envs")), "local backend directory")
	flag.StringVar(&c.namespace, "namespace", env("MAGI_EXEC_NAMESPACE", "magi-envs"), "Kubernetes namespace for environments")
	flag.StringVar(&c.controlNS, "control-namespace", env("MAGI_EXEC_CONTROL_NAMESPACE", "magi"), "namespace of this service (network none + fetch)")
	flag.StringVar(&c.publicURL, "public-url", env("MAGI_EXEC_PUBLIC_URL", ""), "base URL of this service as seen from Pods (fetch delivery)")
	flag.StringVar(&c.delivery, "envd-delivery", env("MAGI_EXEC_ENVD_DELIVERY", ""), "fetch or push (default: fetch in a cluster, push outside)")
	flag.StringVar(&c.connect, "connect", env("MAGI_EXEC_CONNECT", ""), "direct or port-forward (default: direct in a cluster, port-forward outside)")
	flag.StringVar(&c.fetchImage, "fetch-image", env("MAGI_ENVD_FETCH_IMAGE", "busybox:1.37"), "init container image (needs sh, wget, sha256sum)")
	flag.StringVar(&c.pullSecret, "pull-secret", env("MAGI_EXEC_PULL_SECRET", "magi-registry"), "image pull secret in the environment namespace (empty for none)")
	flag.StringVar(&c.defaultImage, "default-image", env("MAGI_EXEC_DEFAULT_IMAGE", "golang:1.26-bookworm"), "image for specs without one")
	flag.StringVar(&c.kubeconfig, "kubeconfig", env("KUBECONFIG", ""), "kubeconfig path (default: in-cluster, then ~/.kube/config)")
	flag.StringVar(&c.kubeContext, "kube-context", env("MAGI_EXEC_KUBE_CONTEXT", ""), "kubeconfig context")
	flag.DurationVar(&c.defaultTTL, "default-ttl", envDuration("MAGI_EXEC_DEFAULT_TTL", time.Hour), "TTL of a spec without one")
	flag.DurationVar(&c.maxTTL, "max-ttl", envDuration("MAGI_EXEC_MAX_TTL", 7*24*time.Hour), "largest TTL")
	flag.DurationVar(&c.reapInterval, "reap-interval", envDuration("MAGI_EXEC_REAP_INTERVAL", 15*time.Second), "reaper interval")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(c, log); err != nil {
		log.Error("execution", "err", err)
		os.Exit(1)
	}
}

func run(c config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if c.keys == "" || c.dbURL == "" {
		return errors.New("MAGI_AUTH_KEYS and MAGI_DATABASE_URL are required")
	}
	v, err := authn.LoadVerifier(strings.Split(c.keys, ",")...)
	if err != nil {
		return err
	}
	bins := &backend.Binaries{Dir: c.envdDir}
	if self, err := os.Executable(); err == nil {
		bins.Self = self
	}
	svc := &server.Service{Backends: map[string]backend.Backend{}, DefaultImage: c.defaultImage,
		DefaultTTL: c.defaultTTL, MaxTTL: c.maxTTL, Log: log}
	for i, name := range strings.Split(c.backends, ",") {
		name = strings.TrimSpace(name)
		if i == 0 {
			svc.Default = name
		}
		switch name {
		case "local":
			svc.Backends[name] = &backend.Local{Root: c.localRoot, Envd: bins}
		case "kubernetes":
			k, err := kube(c, bins)
			if err != nil {
				return fmt.Errorf("kubernetes backend: %w", err)
			}
			defer k.Close()
			log.Info("kubernetes backend", "namespace", k.Namespace, "delivery", k.Delivery, "connect", k.Connect)
			svc.Backends[name] = k
		default:
			return fmt.Errorf("unknown backend %q", name)
		}
	}
	for _, p := range backend.Platforms {
		if _, err := bins.Sum(p); err != nil {
			log.Warn("envd build missing", "platform", p, "err", err)
		}
	}
	db, err := store.Open(ctx, c.dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	svc.DB = db
	go svc.RunReaper(ctx, c.reapInterval)

	s := &rpc.Server{Verifier: v, MaxBody: server.MaxRequest, Log: log}
	server.Register(s, svc)
	mux := rpc.Mux(s, func() error { return db.Ping(ctx) })
	mux.Handle("/envd/", bins)
	log.Info("execution listening", "addr", c.listen)
	return rpc.ListenAndServe(ctx, c.listen, mux)
}

func kube(c config, bins *backend.Binaries) (*backend.Kubernetes, error) {
	rc, inCluster, err := backend.RestConfig(c.kubeconfig, c.kubeContext)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	k := &backend.Kubernetes{Client: cs, Config: rc, Namespace: c.namespace, Delivery: c.delivery, Connect: c.connect,
		PublicURL: c.publicURL, FetchImage: c.fetchImage, PullSecret: c.pullSecret, ControlNamespace: c.controlNS, Envd: bins}
	if k.Delivery == "" {
		k.Delivery = backend.DeliverPush
		if inCluster {
			k.Delivery = backend.DeliverFetch
		}
	}
	if k.Connect == "" {
		k.Connect = backend.ConnectPortForward
		if inCluster {
			k.Connect = backend.ConnectDirect
		}
	}
	if k.Delivery == backend.DeliverFetch && k.PublicURL == "" {
		return nil, errors.New("fetch delivery needs MAGI_EXEC_PUBLIC_URL")
	}
	return k, nil
}
