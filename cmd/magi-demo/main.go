package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"github.com/catalystcommunity/magi/config"
	"github.com/catalystcommunity/magi/conversation"
	"github.com/catalystcommunity/magi/dialogue"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/speech"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/catalystcommunity/magi/workshop"
	"github.com/jackc/pgx/v5/pgxpool"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run(ctx context.Context) error {
	configPath := flag.String("config", ".local/dialogue/server.json", "Private local workflow configuration")
	profilePath := flag.String("profile", ".local/dialogue/client.json", "Private workflow client profile")
	graphPath := flag.String("graph", ".local/conversations/client.json", "Private conversation client profile")
	speechPath := flag.String("speech", config.ProfilePath("speech", "client.json"), "Private speech client profile")
	listen := flag.String("listen", "127.0.0.1:8097", "Loopback browser address")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	host, _, e := net.SplitHostPort(*listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("browser address must use a loopback IP")
	}
	c, e := question.LoadDevConfig(*configPath)
	if e != nil {
		return errors.New("invalid local workflow configuration")
	}
	if endpoint := config.Environment("MAGI_DEMO_MODEL_ENDPOINT"); endpoint != "" {
		c.Model.Endpoint = endpoint
		if c.Validate() != nil {
			return errors.New("invalid model endpoint")
		}
	}
	p, e := question.LoadProfile(*profilePath)
	if e != nil {
		return e
	}
	client, e := dialogue.NewClient(p)
	if e != nil {
		return e
	}
	tasks, e := workshop.NewClient(p)
	if e != nil {
		return e
	}
	pool, e := pgxpool.New(ctx, c.DatabaseURL)
	if e != nil {
		return e
	}
	defer pool.Close()
	g, e := question.LoadProfile(*graphPath)
	if e != nil {
		return e
	}
	graph, e := conversation.NewClient(conversation.Profile(g))
	if e != nil {
		return e
	}
	sp, e := speech.LoadProfile(*speechPath)
	if e != nil {
		return e
	}
	voice, e := speech.NewClient(sp)
	if e != nil {
		return e
	}
	// This interaction point is deliberately not a bridge to production accounts.
	for _, domain := range []string{p.Actor.Identity.Domain, p.Recipient.Identity.Domain, g.Actor.Identity.Domain, g.Recipient.Identity.Domain, sp.Actor.Identity.Domain, sp.Recipient.Identity.Domain} {
		if domain != "local.opal.test" {
			return errors.New("demo browser requires local test profiles")
		}
	}
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, e = graph.History(check, "startup-check", 0)
	cancel()
	if e != nil {
		return errors.New("conversation service is not ready or the profile is denied")
	}
	var token [32]byte
	if _, e = rand.Read(token[:]); e != nil {
		return e
	}
	listener, e := net.Listen("tcp", *listen)
	if e != nil {
		return errors.New("browser address is already in use; no existing process was stopped")
	}
	defer listener.Close()
	handler := &dialogue.Web{Client: client, Graph: graph, Speech: voice, Tasks: tasks, TaskEvidence: &workshop.Store{Pool: pool}, Host: listener.Addr().String(), Token: hex.EncodeToString(token[:])}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 125 * time.Second, WriteTimeout: 125 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	workCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 2)
	go func() {
		done <- question.ServeDevWithWorker(workCtx, c, func(store *workflow.Store, wc question.WorkerConfig) (*workflow.Worker, error) {
			return dialogue.NewWorker(store, wc, graph, voice)
		}, func(message string) { fmt.Fprintln(os.Stderr, message) })
	}()
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("Open http://%s/#token=%s\nPublic test data only. The fragment token grants local demo access. Do not share it.\n", listener.Addr(), handler.Token)
	select {
	case e = <-done:
	case <-ctx.Done():
		e = nil
	}
	stop()
	shutdown, end := context.WithTimeout(context.Background(), 5*time.Second)
	defer end()
	if server.Shutdown(shutdown) != nil {
		server.Close()
	}
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if e := run(ctx); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
