package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	regularrp "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
	"github.com/catalystcommunity/magi/eventing"
	w "github.com/catalystcommunity/magi/generated/events/go"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type config struct {
	Application w.ApplicationRef       `json:"application"`
	Domains     []eventing.DomainTrust `json:"domains"`
	RP          struct {
		TCPAddress   string   `json:"tcp_address"`
		Fingerprints []string `json:"fingerprints"`
	} `json:"rp"`
	Grants            []eventing.Grant `json:"grants"`
	StorageGeneration string           `json:"storage_generation"`
	Limits            *eventing.Limits `json:"limits,omitempty"`
}

func main() {
	path := flag.String("config", "", "Event service configuration file")
	migrate := flag.Bool("migrate", false, "Initialize this service database and exit")
	policy := flag.Bool("apply-policy", false, "Apply listed grants with an administrator database role and exit")
	limits := flag.Bool("apply-limits", false, "Apply configured storage limits and exit")
	inspect := flag.Bool("inspect", false, "Print a bounded operator queue view and exit")
	beginRecovery := flag.Bool("begin-recovery", false, "Block delivery under a new storage generation and exit")
	completeRecovery := flag.Bool("complete-recovery", false, "Record reconciliation evidence and resume this generation")
	evidence := flag.String("evidence", "", "Reconciliation evidence reference")
	afterConsumer := flag.String("after-consumer", "", "Queue inspection cursor consumer key")
	afterSubscription := flag.String("after-subscription", "", "Queue inspection cursor subscription ID")
	pageSize := flag.Int("limit", 100, "Maximum inspection rows (1 to 100)")
	flag.Parse()
	selected := 0
	for _, v := range []bool{*migrate, *policy, *limits, *inspect, *beginRecovery, *completeRecovery} {
		if v {
			selected++
		}
	}
	if selected > 1 || flag.NArg() != 0 {
		log.Fatal("Select one operation")
	}
	if *path == "" {
		log.Fatal("A configuration file is required")
	}
	raw, e := os.ReadFile(*path)
	if e != nil || len(raw) > 65536 {
		log.Fatal("Configuration is not available")
	}
	var c config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || dec.Decode(new(any)) != io.EOF {
		log.Fatal("Configuration is invalid")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("EVENTS_DATABASE_URL")
	if dsn == "" {
		log.Fatal("EVENTS_DATABASE_URL is required")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		log.Fatal("Database configuration is invalid")
	}
	cfg.MaxConns = 8
	// A session cannot weaken commit durability through its inherited settings.
	cfg.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "9000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		log.Fatal("Database connection failed")
	}
	defer pool.Close()
	store := &eventing.Store{Pool: pool, Application: c.Application, Generation: c.StorageGeneration}
	if *migrate {
		if store.Initialize(ctx) != nil {
			log.Fatal("Event database migration failed")
		}
		log.Print("Event database is initialized")
		return
	}
	if store.CheckBinding(ctx) != nil {
		log.Fatal("Event database identity or version check failed")
	}
	if *limits {
		if c.Limits == nil || store.SetLimits(ctx, *c.Limits) != nil {
			log.Fatal("Storage limit update failed")
		}
		log.Print("Storage limits are applied")
		return
	}
	if *inspect {
		v, e := store.Inspect(ctx, *afterConsumer, *afterSubscription, *pageSize)
		if e != nil {
			log.Fatal("Queue inspection failed")
		}
		if json.NewEncoder(os.Stdout).Encode(v) != nil {
			log.Fatal("Queue output failed")
		}
		return
	}
	if *beginRecovery {
		if store.BeginRecovery(ctx) != nil {
			log.Fatal("Recovery fence update failed")
		}
		log.Print("Event operations are blocked for reconciliation")
		return
	}
	if *completeRecovery {
		if store.CompleteRecovery(ctx, *evidence) != nil {
			log.Fatal("Recovery confirmation failed")
		}
		log.Print("Reconciliation evidence is recorded")
		return
	}
	if *policy {
		for _, g := range c.Grants {
			if store.SetGrant(ctx, g) != nil {
				log.Fatal("Policy update failed")
			}
		}
		log.Print("Event grants are applied")
		return
	}
	if store.Check(ctx) != nil {
		log.Fatal("Event storage generation is not ready")
	}
	rp, e := regularrp.NewPinnedRpcTransport(regularrp.PinnedRpcTransportOptions{TCPAddress: c.RP.TCPAddress, Fingerprints: c.RP.Fingerprints, APIKey: os.Getenv("EVENTS_RP_API_KEY"), RequestTimeout: 8 * time.Second})
	if e != nil {
		log.Fatal("LinkKeys RP configuration is invalid")
	}
	service := &eventing.Service{Store: store, Keys: &eventing.LinkKeysResolver{RP: lk.NewRpClient(rp), Domains: c.Domains}}
	listen := os.Getenv("EVENTS_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8081"
	}
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		log.Fatal("Event listener failed")
	}
	defer listener.Close()
	origin := os.Getenv("EVENTS_ORIGIN")
	if origin == "" {
		origin = "http://" + listener.Addr().String()
	}
	handler, e := protocol.NewRPCHandler(origin, service.Dispatch, store.Check)
	if e != nil {
		log.Fatal("Event origin is invalid")
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
	log.Printf("Event service is ready at %s", origin)
	if e = srv.Serve(listener); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal("Event listener failed")
	}
	<-done
}
