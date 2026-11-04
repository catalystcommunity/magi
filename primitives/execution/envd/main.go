package envd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	if len(args) > 0 && args[0] == "netns" {
		// "envd netns -- PROGRAM ARGS" (sandbox.bwrapCommand).
		err := netnsExec(args[1:])
		fmt.Fprintln(os.Stderr, "envd netns:", err)
		os.Exit(1)
	}
	if len(args) > 0 && args[0] == "podnet" {
		// "envd podnet -exclude IP:PORT" (the init container of a Pod).
		if err := podnetExec(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "envd podnet:", err)
			os.Exit(1)
		}
		return
	}
	fs := flag.NewFlagSet("envd", flag.ExitOnError)
	listen := fs.String("listen", env("MAGI_ENVD_LISTEN", ":7070"), "listen address: host:port, unix:PATH, or vsock:PORT")
	tokenFile := fs.String("token-file", env("MAGI_ENVD_TOKEN_FILE", ""), "file that holds the token")
	dir := fs.String("dir", env("MAGI_ENVD_DIR", ""), "working directory (default: current)")
	addrFile := fs.String("addr-file", env("MAGI_ENVD_ADDR_FILE", ""), "write the listen address to this file")
	forward := fs.String("forward", "", "ADDR=TARGET: copy each TCP connection to ADDR to TARGET, a unix socket path or vsock:CID:PORT (the proxy port of a sandbox with no network)")
	transparentTo := fs.String("transparent", "", "TARGET: in a task VM, after netns, or after podnet, send each TCP connection through the proxy at TARGET (a unix socket path, vsock:CID:PORT, or tcp:HOST:PORT); the proxy user is in HTTPS_PROXY")
	dns := fs.String("dns", "127.0.0.53:53", "with -transparent: the DNS addresses of envd, separated by commas")
	vm := fs.Bool("vm", false, "run as the init of a task VM; the flags and environment come from "+VMConfig)
	fs.Parse(args)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if *vm {
		a, err := vmInit(log)
		if err != nil {
			log.Error("envd", "err", err)
			os.Exit(1)
		}
		fs.Parse(a)
	}
	if *transparentTo != "" {
		if err := startTransparent(*transparentTo, os.Getenv("HTTPS_PROXY"), strings.Split(*dns, ","), log); err != nil {
			log.Error("envd", "err", err)
			os.Exit(1)
		}
	}
	if *forward != "" {
		if err := startForward(*forward, log); err != nil {
			log.Error("envd", "err", err)
			os.Exit(1)
		}
	}
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
	ln, addr, err := listenSpec(listen)
	if err != nil {
		return err
	}
	if addrFile != "" {
		tmp := addrFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(addr), 0o600); err != nil {
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
	log.Info("envd listening", "addr", addr, "dir", dir)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// startForward listens on the TCP address of spec ("ADDR=TARGET") and
// copies each connection to the target. In a sandbox with no network, the
// target is the only route out, and the runner sends it to the egress
// proxy.
func startForward(spec string, log *slog.Logger) error {
	addr, target, ok := strings.Cut(spec, "=")
	if !ok || addr == "" || target == "" {
		return fmt.Errorf("-forward %q: expected ADDR=TARGET", spec)
	}
	dial, err := dialSpec(target)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Error("forward", "err", err)
				return
			}
			go func() {
				defer c.Close()
				u, err := dial()
				if err != nil {
					log.Warn("forward", "err", err)
					return
				}
				defer u.Close()
				Splice(c, u)
			}()
		}
	}()
	return nil
}

// Splice copies a to b and b to a until both directions end.
func Splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
