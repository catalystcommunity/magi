package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/question"
)

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: magi-qa init|serve|ask|read|evidence [options]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "init":
		dir := fs.String("dir", ".local/qa", "Private test configuration directory")
		listen := fs.String("listen", "127.0.0.1:8093", "Loopback API address")
		database := fs.String("database", "postgres://postgres:local-qa-only@127.0.0.1:25432/postgres?sslmode=disable", "Dedicated local test database")
		corndogs := fs.String("corndogs", "127.0.0.1:25080", "Dedicated loopback Corndogs address")
		endpoint := fs.String("model-url", "http://127.0.0.1:8088/v1/chat/completions", "Complete model URL, or fixture for transport tests")
		model := fs.String("model", "opal-test-small", "Model name")
		python := fs.String("python", "", "Absolute Python 3 path")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected argument")
		}
		if *python == "" {
			path, e := exec.LookPath("python3")
			if e != nil {
				return errors.New("Python 3 is required")
			}
			*python, _ = filepath.Abs(path)
		}
		return question.InitDev(*dir, *listen, *database, *corndogs, question.ModelConfig{Endpoint: *endpoint, Model: *model, Python: *python, MaxTokens: 256})
	case "serve":
		config := fs.String("config", ".local/qa/server.json", "Local test server configuration")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected argument")
		}
		c, e := question.LoadDevConfig(*config)
		if e != nil {
			return errors.New("local test configuration is invalid")
		}
		return question.ServeDev(ctx, c, func(message string) { fmt.Fprintln(os.Stderr, message) })
	case "ask", "read", "evidence":
		profilePath := fs.String("profile", ".local/qa/client.json", "Private application signing profile")
		id := fs.String("run", "", "Stable run ID; reuse it with identical input after an uncertain reply")
		wait := fs.Duration("wait", 180*time.Second, "Maximum answer wait; expiry does not cancel the run")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if fs.NArg() != 0 || *wait <= 0 || *wait > 10*time.Minute {
			return errors.New("invalid arguments; supply a question on standard input")
		}
		profile, e := question.LoadProfile(*profilePath)
		if e != nil {
			return e
		}
		client, e := question.NewClient(profile)
		if e != nil {
			return errors.New("client profile is invalid")
		}
		if args[0] == "ask" {
			raw, e := io.ReadAll(io.LimitReader(os.Stdin, 3001))
			if e != nil {
				return errors.New("question input failed")
			}
			// A terminal's final newline is not part of the submitted question.
			text := strings.TrimSuffix(string(raw), "\n")
			if len(raw) > 3000 {
				return errors.New("question exceeds 3000 bytes")
			}
			if *id == "" {
				var b [12]byte
				if _, e = rand.Read(b[:]); e != nil {
					return e
				}
				*id = "q-" + hex.EncodeToString(b[:])
			}
			fmt.Fprintln(os.Stderr, "Run:", *id)
			if _, e = client.Submit(ctx, *id, text); e != nil {
				return e
			}
			bound, cancel := context.WithTimeout(ctx, *wait)
			defer cancel()
			answer, e := client.Wait(bound, *id)
			if e != nil {
				return fmt.Errorf("answer unavailable; use read or evidence with the printed run ID: %w", e)
			}
			fmt.Println(question.TerminalText(answer))
			return nil
		}
		if *id == "" {
			return errors.New("-run is required")
		}
		if args[0] == "read" {
			v, e := client.Read(ctx, *id)
			if e != nil {
				return e
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(v)
		}
		v, e := client.Evidence(ctx, *id)
		if e != nil {
			return e
		}
		raw, e := question.DisplayEvidence(v)
		if e != nil {
			return e
		}
		fmt.Println(string(raw))
		return nil
	default:
		return errors.New("unknown command")
	}
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if e := run(ctx, os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
