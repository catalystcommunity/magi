package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	regularrp "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/speech"
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
type configuration struct {
	Application w.ApplicationRef       `json:"application"`
	Domains     []identity.DomainTrust `json:"domains"`
	RP          struct {
		TCPAddress   string   `json:"tcp_address"`
		Fingerprints []string `json:"fingerprints"`
	} `json:"rp"`
	Grants             []speech.Grant                 `json:"grants"`
	Backends           map[string]speech.PythonConfig `json:"backends"`
	LocalKeys          []localKey                     `json:"local_keys"`
	PublicTestDataOnly bool                           `json:"public_test_data_only"`
}
type testKeys []localKey

func (k testKeys) Resolve(_ context.Context, a w.ApplicationRef, id string) (identity.VerifiedKey, error) {
	for _, v := range k {
		if v.Actor == a && v.KeyID == id && len(v.Public) == 32 {
			return identity.VerifiedKey{Public: v.Public, CreatedAt: time.Unix(0, 0), ExpiresAt: time.Now().Add(time.Hour)}, nil
		}
	}
	return identity.VerifiedKey{}, identity.ErrDenied
}
func validLocal(c configuration, listen, origin string) bool {
	host, _, e := net.SplitHostPort(listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || origin != "" || c.Application.Identity.Domain != "local.opal.test" || len(c.Domains) != 0 || len(c.LocalKeys) == 0 {
		return false
	}
	for _, key := range c.LocalKeys {
		if key.Actor.Identity.Domain != "local.opal.test" || len(key.Public) != 32 {
			return false
		}
	}
	for _, g := range c.Grants {
		if g.Actor.Identity.Domain != "local.opal.test" || g.Owner.Authority.Domain != "local.opal.test" {
			return false
		}
	}
	return true
}
func main() {
	configPath := flag.String("config", "", "Speech configuration file")
	migrate := flag.Bool("migrate", false, "Initialize the speech database and exit")
	policy := flag.Bool("apply-policy", false, "Apply configured grants and exit")
	worker := flag.String("worker", "", "Run the tts or stt worker instead of the API")
	local := flag.Bool("local-test-keys", false, "Use synthetic identities on loopback only")
	flag.Parse()
	if *configPath == "" || flag.NArg() != 0 || *migrate && *policy || (*migrate || *policy) && *worker != "" {
		log.Fatal("Select a configuration and one role")
	}
	raw, e := os.ReadFile(*configPath)
	if e != nil || len(raw) > 65536 {
		log.Fatal("Configuration unavailable")
	}
	var c configuration
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || dec.Decode(new(any)) != io.EOF || !c.PublicTestDataOnly {
		log.Fatal("This prototype requires explicit public-test-data configuration")
	}
	listen := os.Getenv("SPEECH_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8094"
	}
	origin := os.Getenv("SPEECH_ORIGIN")
	if *local {
		if !validLocal(c, listen, origin) {
			log.Fatal("Local test keys require synthetic identities and a loopback listener")
		}
	} else if len(c.LocalKeys) > 0 {
		log.Fatal("Local keys require explicit local mode")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("SPEECH_DATABASE_URL")
	if dsn == "" {
		log.Fatal("SPEECH_DATABASE_URL is required")
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
	store := &speech.Store{Pool: pool, Application: c.Application}
	if *migrate {
		if store.Initialize(ctx) != nil {
			log.Fatal("Speech migration failed")
		}
		log.Print("Speech database initialized")
		return
	}
	if store.Check(ctx) != nil {
		log.Fatal("Speech database identity or schema check failed")
	}
	if *policy {
		for _, g := range c.Grants {
			if store.SetGrant(ctx, g) != nil {
				log.Fatal("Speech grant update failed")
			}
		}
		log.Print("Speech grants applied")
		return
	}
	if *worker != "" {
		config, ok := c.Backends[*worker]
		if !ok || config.Kind != *worker || (*worker != "tts" && *worker != "stt") {
			log.Fatal("Unknown worker profile")
		}
		backend := &speech.PythonBackend{Config: config}
		defer backend.Close()
		runner := &speech.Worker{Store: store, Kind: w.SpeechKind(*worker), Backend: backend}
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, e = runner.Tick(ctx); e != nil && ctx.Err() == nil {
					log.Print("Speech worker operation failed; retained request state must be checked")
				}
			}
		}
	}
	var keys identity.KeyResolver
	if *local {
		keys = testKeys(c.LocalKeys)
	} else {
		rp, err := regularrp.NewPinnedRpcTransport(regularrp.PinnedRpcTransportOptions{TCPAddress: c.RP.TCPAddress, Fingerprints: c.RP.Fingerprints, APIKey: os.Getenv("SPEECH_RP_API_KEY"), RequestTimeout: 8 * time.Second})
		if err != nil {
			log.Fatal("Invalid LinkKeys RP configuration")
		}
		keys = &identity.LinkKeysResolver{RP: lk.NewRpClient(rp), Domains: c.Domains}
	}
	service := &speech.Service{Store: store, Keys: keys}
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		log.Fatal("Speech listen address unavailable")
	}
	defer listener.Close()
	if origin == "" {
		origin = "http://" + listener.Addr().String()
	}
	handler, e := protocol.NewRPCHandler(origin, service.Dispatch, store.Check)
	if e != nil {
		log.Fatal("Invalid speech origin")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bound, cancel := context.WithTimeout(ctx, 5*time.Second)
				store.Sweep(bound)
				cancel()
			}
		}
	}()
	go func() {
		<-ctx.Done()
		handler.Drain()
		bound, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(bound) != nil {
			server.Close()
		}
	}()
	log.Printf("Speech API ready at %s; public test data only", origin)
	if e = server.Serve(listener); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal("Speech listener failed")
	}
}
