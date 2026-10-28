package envd

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Main runs envd with command-line flags and exits on failure. The envd
// command and the execution command (when started as "envd") call it.
func Main(args []string) {
	fs := flag.NewFlagSet("envd", flag.ExitOnError)
	listen := fs.String("listen", env("MAGI_ENVD_LISTEN", ":7070"), "listen address")
	tokenFile := fs.String("token-file", env("MAGI_ENVD_TOKEN_FILE", ""), "file that holds the token")
	dir := fs.String("dir", env("MAGI_ENVD_DIR", ""), "working directory (default: current)")
	addrFile := fs.String("addr-file", env("MAGI_ENVD_ADDR_FILE", ""), "write the listen address to this file")
	fs.Parse(args)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(*listen, *tokenFile, *dir, *addrFile, log); err != nil {
		log.Error("envd", "err", err)
		os.Exit(1)
	}
}

func run(listen, tokenFile, dir, addrFile string, log *slog.Logger) error {
	token := os.Getenv("MAGI_ENVD_TOKEN")
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	if len(token) < 16 {
		return errors.New("a token of at least 16 bytes is required (MAGI_ENVD_TOKEN_FILE or MAGI_ENVD_TOKEN)")
	}
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	if addrFile != "" {
		tmp := addrFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(ln.Addr().String()), 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, addrFile); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: &Server{Token: token, Dir: dir}, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Info("envd listening", "addr", ln.Addr().String(), "dir", dir)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
