package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	regularrp "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
	"github.com/catalystcommunity/magi/conversation"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
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

type localKey struct {
	Actor  w.ApplicationRef `json:"actor"`
	KeyID  string           `json:"key_id"`
	Public []byte           `json:"public_key"`
}
type config struct {
	Application w.ApplicationRef       `json:"application"`
	Domains     []identity.DomainTrust `json:"domains"`
	RP          struct {
		TCPAddress   string   `json:"tcp_address"`
		Fingerprints []string `json:"fingerprints"`
	} `json:"rp"`
	Grants             []conversation.Grant `json:"grants"`
	LocalKeys          []localKey           `json:"local_keys"`
	PublicTestDataOnly bool                 `json:"public_test_data_only"`
}
type testKeys []localKey

func (k testKeys) Resolve(_ context.Context, a w.ApplicationRef, id string) (identity.VerifiedKey, error) {
	for _, v := range k {
		if a == v.Actor && id == v.KeyID && len(v.Public) == 32 {
			return identity.VerifiedKey{Public: v.Public, CreatedAt: time.Unix(0, 0), ExpiresAt: time.Now().Add(time.Hour)}, nil
		}
	}
	return identity.VerifiedKey{}, identity.ErrDenied
}
func main() {
	path := flag.String("config", "", "Conversation configuration")
	migrate := flag.Bool("migrate", false, "Initialize database and exit")
	policy := flag.Bool("apply-policy", false, "Apply configured grants and exit")
	local := flag.Bool("local-test-keys", false, "Synthetic local identities only")
	flag.Parse()
	if *path == "" || *migrate && *policy || flag.NArg() != 0 {
		log.Fatal("Select a configuration and one role")
	}
	raw, e := os.ReadFile(*path)
	if e != nil || len(raw) > 65536 {
		log.Fatal("Configuration unavailable")
	}
	var c config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || dec.Decode(new(any)) != io.EOF || !c.PublicTestDataOnly {
		log.Fatal("Public-test-data configuration is required")
	}
	listen := os.Getenv("CONVERSATIONS_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8095"
	}
	origin := os.Getenv("CONVERSATIONS_ORIGIN")
	if *local {
		host, _, err := net.SplitHostPort(listen)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || origin != "" || c.Application.Identity.Domain != "local.opal.test" || len(c.Domains) != 0 || len(c.LocalKeys) == 0 {
			log.Fatal("Test keys require loopback and local identities")
		}
		for _, v := range c.LocalKeys {
			if v.Actor.Identity.Domain != "local.opal.test" || len(v.Public) != 32 {
				log.Fatal("Invalid test key")
			}
		}
		for _, g := range c.Grants {
			if g.Actor.Identity.Domain != "local.opal.test" || g.Owner.Authority.Domain != "local.opal.test" {
				log.Fatal("Invalid local grant")
			}
		}
	} else if len(c.LocalKeys) > 0 {
		log.Fatal("Test keys require explicit local mode")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("CONVERSATIONS_DATABASE_URL")
	if dsn == "" {
		log.Fatal("CONVERSATIONS_DATABASE_URL is required")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		log.Fatal("Invalid database configuration")
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
	store := &conversation.Store{Pool: pool, Application: c.Application}
	if *migrate {
		if store.Initialize(ctx) != nil {
			log.Fatal("Conversation migration failed")
		}
		return
	}
	if store.Check(ctx) != nil {
		log.Fatal("Conversation database binding failed")
	}
	if *policy {
		for _, g := range c.Grants {
			if store.SetGrant(ctx, g) != nil {
				log.Fatal("Grant update failed")
			}
		}
		return
	}
	var keys identity.KeyResolver
	if *local {
		keys = testKeys(c.LocalKeys)
	} else {
		rp, err := regularrp.NewPinnedRpcTransport(regularrp.PinnedRpcTransportOptions{TCPAddress: c.RP.TCPAddress, Fingerprints: c.RP.Fingerprints, APIKey: os.Getenv("CONVERSATIONS_RP_API_KEY"), RequestTimeout: 8 * time.Second})
		if err != nil {
			log.Fatal("Invalid LinkKeys RP configuration")
		}
		keys = &identity.LinkKeysResolver{RP: lk.NewRpClient(rp), Domains: c.Domains}
	}
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		log.Fatal("Conversation listen address unavailable")
	}
	defer listener.Close()
	if origin == "" {
		origin = "http://" + listener.Addr().String()
	}
	handler, e := protocol.NewRPCHandler(origin, (&conversation.Service{Store: store, Keys: keys}).Dispatch, store.Check)
	if e != nil {
		log.Fatal("Invalid origin")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		handler.Drain()
		bound, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(bound) != nil {
			server.Close()
		}
	}()
	log.Printf("Conversation API ready at %s; public test data only", origin)
	if e = server.Serve(listener); e != nil && e != http.ErrServerClosed {
		log.Fatal("Conversation listener failed")
	}
}
