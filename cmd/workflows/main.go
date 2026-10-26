package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	regularrp "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	Application  w.ApplicationRef       `json:"application"`
	Installation string                 `json:"installation"`
	Seeds        []string               `json:"corndogs_seeds"`
	CAFile       string                 `json:"corndogs_ca_file"`
	ServerName   string                 `json:"corndogs_server_name"`
	Domains      []identity.DomainTrust `json:"domains"`
	RP           struct {
		TCPAddress   string   `json:"tcp_address"`
		Fingerprints []string `json:"fingerprints"`
	} `json:"rp"`
	Grants         []workflow.Grant       `json:"grants"`
	QuestionWorker *question.WorkerConfig `json:"question_worker"`
}

func main() {
	path := flag.String("config", "", "Workflow configuration file")
	migrate := flag.Bool("migrate", false, "Initialize the dedicated workflow database and exit")
	policy := flag.Bool("apply-policy", false, "Apply the listed grants with an administrator database role")
	advance := flag.String("advance", "", "Advance one run without dispatching external calls")
	coordinate := flag.Bool("coordinate", false, "Recover admitted runs continuously without dispatching external calls")
	sweep := flag.Bool("sweep", false, "Evaluate timeouts for admitted workflow call queues")
	sweepCheck := flag.Bool("sweep-check", false, "Check timeout evaluator readiness and exit")
	questionWorker := flag.Bool("question-worker", false, "Execute the configured question workflow through its Python model adapter")
	domain := flag.String("owner-domain", "", "Run owner domain")
	account := flag.String("owner-account", "", "Run owner account")
	scope := flag.String("scope", "", "Run owner scope")
	flag.Parse()
	selected := 0
	for _, v := range []bool{*migrate, *policy, *advance != "", *coordinate, *sweep, *sweepCheck, *questionWorker} {
		if v {
			selected++
		}
	}
	if selected > 1 || *path == "" || flag.NArg() != 0 {
		log.Fatal("Select one operation and a configuration file")
	}
	raw, e := os.ReadFile(*path)
	if e != nil || len(raw) > 65536 {
		log.Fatal("Configuration is not available")
	}
	var c config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF {
		log.Fatal("Configuration is invalid")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	dsn := os.Getenv("WORKFLOWS_DATABASE_URL")
	if dsn == "" {
		log.Fatal("WORKFLOWS_DATABASE_URL is required")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		log.Fatal("Database configuration is invalid")
	}
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "9000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		log.Fatal("Database connection failed")
	}
	defer pool.Close()
	carrier := &workflow.Transport{Seeds: c.Seeds}
	if c.ServerName != "" {
		roots, e := x509.SystemCertPool()
		if e != nil {
			log.Fatal("TLS trust store is unavailable")
		}
		if c.CAFile != "" {
			pem, e := os.ReadFile(c.CAFile)
			if e != nil || !roots.AppendCertsFromPEM(pem) {
				log.Fatal("Corndogs CA file is invalid")
			}
		}
		carrier.TLS = &tls.Config{RootCAs: roots, ServerName: c.ServerName, MinVersion: tls.VersionTLS12}
	} else if c.CAFile != "" {
		log.Fatal("Corndogs TLS server name is required")
	}
	store := &workflow.Store{Application: c.Application, Journal: &workflow.Journal{Pool: pool, Installation: c.Installation, Transport: carrier}}
	if *migrate {
		if err := store.Initialize(ctx); err != nil {
			log.Fatal("Workflow migration failed: ", startupError(err, cfg.ConnConfig.Password, dsn))
		}
		log.Print("Workflow database is initialized")
		return
	}
	if store.Check(ctx) != nil {
		log.Fatal("Workflow database or Corndogs check failed")
	}
	if *policy {
		for _, grant := range c.Grants {
			if store.SetGrant(ctx, grant) != nil {
				log.Fatal("Policy update failed")
			}
		}
		log.Print("Workflow grants are applied")
		return
	}
	if *advance != "" {
		owner := w.ScopeRef{Authority: w.IdentityRef{Domain: *domain, AccountId: *account}, ScopeId: *scope}
		bound, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if store.Advance(bound, owner, *advance) != nil {
			log.Fatal("Run did not advance; inspect its journal")
		}
		log.Print("Run transitions were evaluated")
		return
	}
	if *sweepCheck {
		if (&workflow.Sweeper{Store: store}).Check(ctx) != nil {
			log.Fatal("Timeout evaluator is not ready; new tool calls must wait")
		}
		log.Print("Timeout evaluator is ready")
		return
	}
	if *sweep {
		log.Print("Workflow timeout evaluator is starting; only admitted call queues are in scope")
		e = (&workflow.Sweeper{Store: store}).Run(ctx, func(report workflow.SweepReport, err error) {
			if err != nil {
				log.Print("Timeout evaluation failed; readiness must be checked")
			} else if report.Checked > 0 {
				log.Printf("Timeout checks=%d closed=%d deferred=%d", report.Checked, report.Closed, report.Deferred)
			}
		})
		if e != nil && !errors.Is(e, context.Canceled) {
			log.Fatal("Timeout evaluator stopped")
		}
		return
	}
	if *coordinate {
		coordinator := &workflow.Coordinator{Store: store}
		log.Print("Workflow coordinator is ready; tool execution is disabled")
		e = coordinator.Run(ctx, func(report workflow.CoordinationReport, err error) {
			if err != nil {
				log.Print("Coordination pass failed; recovery will retry")
			} else if report.Evaluated > 0 {
				log.Printf("Coordination evaluated=%d completed=%d deferred=%d", report.Evaluated, report.Completed, report.Deferred)
			}
		})
		if e != nil && !errors.Is(e, context.Canceled) {
			log.Fatal("Workflow coordinator stopped")
		}
		return
	}
	if *questionWorker {
		if c.QuestionWorker == nil {
			log.Fatal("question_worker configuration is required")
		}
		worker, e := question.NewWorker(store, *c.QuestionWorker)
		if e != nil {
			log.Fatal("Question worker configuration is invalid")
		}
		log.Print("Question worker is starting; only its configured actor, scope, and definition are admitted")
		e = worker.Run(ctx, func(n int, e error) {
			if e != nil {
				log.Print("Question worker pass failed; inspect run evidence")
			} else if n > 0 {
				log.Printf("Question worker results=%d", n)
			}
		})
		if e != nil && !errors.Is(e, context.Canceled) {
			log.Fatal("Question worker stopped")
		}
		return
	}
	rp, e := regularrp.NewPinnedRpcTransport(regularrp.PinnedRpcTransportOptions{TCPAddress: c.RP.TCPAddress, Fingerprints: c.RP.Fingerprints, APIKey: os.Getenv("WORKFLOWS_RP_API_KEY"), RequestTimeout: 8 * time.Second})
	if e != nil {
		log.Fatal("LinkKeys RP configuration is invalid")
	}
	service := &workflow.Service{Store: store, Keys: &identity.LinkKeysResolver{RP: lk.NewRpClient(rp), Domains: c.Domains}}
	listen := os.Getenv("WORKFLOWS_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8082"
	}
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		log.Fatal("Workflow listener failed")
	}
	defer listener.Close()
	origin := os.Getenv("WORKFLOWS_ORIGIN")
	if origin == "" {
		origin = "http://" + listener.Addr().String()
	}
	handler, e := protocol.NewRPCHandler(origin, service.Dispatch, store.Check)
	if e != nil {
		log.Fatal("Workflow origin is invalid")
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		handler.Drain()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if srv.Shutdown(shutdown) != nil {
			srv.Close()
		}
		close(done)
	}()
	log.Printf("Workflow API is ready at %s", origin)
	if e = srv.Serve(listener); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal("Workflow listener failed")
	}
	<-done
}

// Startup operations contain no task data. Remove connection credentials
// before reporting a failure so an operator can distinguish TLS and SQL errors.
func startupError(err error, password, dsn string) string {
	message := err.Error()
	for _, value := range []string{dsn, password} {
		if value != "" {
			message = strings.ReplaceAll(message, value, "<database credential>")
		}
	}
	return message
}
