package question

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DevConfig is deliberately separate from cmd/workflows. It cannot replace
// LinkKeys trust in that executable or bind to a public address.
type DevConfig struct {
	Mode        string           `json:"mode"`
	Application w.ApplicationRef `json:"application"`
	Actor       w.ApplicationRef `json:"actor"`
	Owner       w.ScopeRef       `json:"owner"`
	PublicKey   []byte           `json:"public_key"`
	KeyID       string           `json:"key_id"`
	Listen      string           `json:"listen"`
	DatabaseURL string           `json:"database_url"`
	Corndogs    string           `json:"corndogs"`
	Model       ModelConfig      `json:"model"`
	CallSeconds int              `json:"call_seconds"`
}

func decodeJSON(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return workflow.ErrInvalid
	}
	return nil
}
func localAddress(address string) bool {
	host, _, e := net.SplitHostPort(address)
	return e == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func (c DevConfig) Validate() error {
	u, e := url.Parse(c.DatabaseURL)
	if c.Mode != "local-test-keys-no-linkkeys" || !localAddress(c.Listen) || !localAddress(c.Corndogs) || e != nil || u.Scheme != "postgres" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() ||
		c.Application.Identity.Domain != "local.opal.test" || c.Actor.Identity.Domain != "local.opal.test" || c.Owner.Authority != c.Actor.Identity ||
		len(c.PublicKey) != ed25519.PublicKeySize || c.KeyID != "local-test-key" || c.CallSeconds < 8 || c.CallSeconds > 120 {
		return workflow.ErrInvalid
	}
	if c.Model.Endpoint != "fixture" {
		return c.Model.Validate()
	}
	copy := c.Model
	copy.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	return copy.Validate()
}
func LoadDevConfig(path string) (DevConfig, error) {
	var c DevConfig
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16384 {
		return c, workflow.ErrInvalid
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	if e = decodeJSON(raw, &c); e != nil {
		return c, e
	}
	return c, c.Validate()
}
func InitDev(dir, listen, database, corndogs string, model ModelConfig) error {
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	actor := w.ApplicationRef{Identity: w.IdentityRef{Domain: "local.opal.test", AccountId: "test-human"}, ApplicationId: "qa-cli", InstanceId: "local"}
	application := w.ApplicationRef{Identity: w.IdentityRef{Domain: "local.opal.test", AccountId: "test-assistant"}, ApplicationId: "workflows", InstanceId: "local"}
	c := DevConfig{Mode: "local-test-keys-no-linkkeys", Application: application, Actor: actor, Owner: w.ScopeRef{Authority: actor.Identity, ScopeId: "qa-test"}, PublicKey: pub, KeyID: "local-test-key", Listen: listen, DatabaseURL: database, Corndogs: corndogs, Model: model, CallSeconds: 120}
	if e = c.Validate(); e != nil {
		return e
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("test directory must be private and must not be a symlink")
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	if len(entries) != 0 {
		return errors.New("test directory is not empty; existing files were not changed")
	}
	profile := Profile{Endpoint: "http://" + listen + "/csil/v1/rpc", Actor: actor, Recipient: application, Owner: c.Owner, KeyID: c.KeyID, PrivateKey: key}
	for name, value := range map[string]any{"server.json": c, "client.json": profile} {
		raw, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			return e
		}
		file, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = file.Write(append(raw, '\n'))
		if e == nil {
			e = file.Sync()
		}
		closeErr := file.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

type devKeys struct{ config DevConfig }

func (k devKeys) Resolve(_ context.Context, a w.ApplicationRef, key string) (identity.VerifiedKey, error) {
	if a != k.config.Actor || key != k.config.KeyID {
		return identity.VerifiedKey{}, identity.ErrDenied
	}
	return identity.VerifiedKey{Public: k.config.PublicKey, CreatedAt: time.Unix(0, 0), ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// FixtureHandler proves transport and orchestration only. It is not inference.
func FixtureHandler(wr http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(wr, r)
		return
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 16385))
	if e != nil || len(raw) > 16384 || json.Unmarshal(raw, &req) != nil || len(req.Messages) != 2 {
		wr.WriteHeader(400)
		return
	}
	wr.Header().Set("Content-Type", "application/json")
	json.NewEncoder(wr).Encode(map[string]any{"model": "deterministic-fixture-not-a-model", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": "Test responder received your question. Configure a model endpoint for an actual answer."}}}})
}

// ServeDev keeps durable state in the supplied test database. Ctrl-C stops its
// loops and Python child, not the containers or retained workflow evidence.
func ServeDev(ctx context.Context, c DevConfig, report func(string)) error {
	return ServeDevWithWorker(ctx, c, NewWorker, report)
}

// ServeDevWithWorker uses the same local admission, coordination, and recovery
// loops with an operator-selected worker. It does not add a remote plugin API.
func ServeDevWithWorker(ctx context.Context, c DevConfig, factory func(*workflow.Store, WorkerConfig) (*workflow.Worker, error), report func(string)) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if c.Model.Endpoint == "fixture" {
		fixture := httptest.NewServer(http.HandlerFunc(FixtureHandler))
		defer fixture.Close()
		c.Model.Endpoint = fixture.URL + "/v1/chat/completions"
		report("Model mode: deterministic test responder, not inference")
	} else {
		report("Model mode: configured API; only test questions are permitted")
	}
	config, e := pgxpool.ParseConfig(c.DatabaseURL)
	if e != nil {
		return workflow.ErrInvalid
	}
	config.MaxConns = 12
	config.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	config.ConnConfig.RuntimeParams["statement_timeout"] = "9000"
	config.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	pool, e := pgxpool.NewWithConfig(ctx, config)
	if e != nil {
		return e
	}
	defer pool.Close()
	store := &workflow.Store{Application: c.Application, Journal: &workflow.Journal{Pool: pool, Installation: "local-qa", Transport: &workflow.Transport{Seeds: []string{c.Corndogs}}}}
	bound, cancel := context.WithTimeout(ctx, 30*time.Second)
	e = store.Initialize(bound)
	if e == nil {
		e = store.Check(bound)
	}
	if e == nil {
		for _, action := range []string{"write", "read", "run", "execute"} {
			if e = store.SetGrant(bound, workflow.Grant{Actor: c.Actor, Owner: c.Owner, Action: action, Allowed: true}); e != nil {
				break
			}
		}
	}
	if e == nil {
		_, e = (&workflow.Sweeper{Store: store}).Tick(bound, 16)
	}
	cancel()
	if e != nil {
		return errors.New("test storage initialization or Corndogs check failed")
	}
	worker, e := factory(store, WorkerConfig{Actor: c.Actor, Owner: c.Owner, Model: c.Model, CallSeconds: c.CallSeconds})
	if e != nil {
		return e
	}
	service := &workflow.Service{Store: store, Keys: devKeys{config: c}}
	listener, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return errors.New("test API listen address is unavailable")
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	handler, e := protocol.NewRPCHandler(origin, service.Dispatch, func(check context.Context) error {
		if err := store.Check(check); err != nil {
			return err
		}
		return (&workflow.Sweeper{Store: store}).Check(check)
	})
	if e != nil {
		return e
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	workCtx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	wg.Go(func() {
		(&workflow.Coordinator{Store: store}).Run(workCtx, func(_ workflow.CoordinationReport, e error) {
			if e != nil && workCtx.Err() == nil {
				report("Coordination is delayed; check storage and Corndogs")
			}
		})
	})
	wg.Go(func() {
		(&workflow.Sweeper{Store: store}).Run(workCtx, func(_ workflow.SweepReport, e error) {
			if e != nil && workCtx.Err() == nil {
				report("Timeout checks are delayed; new calls must wait")
			}
		})
	})
	wg.Go(func() {
		worker.Run(workCtx, func(_ int, e error) {
			if e != nil && workCtx.Err() == nil {
				report("Worker is delayed; inspect run evidence")
			}
		})
	})
	serveErr := make(chan error, 1)
	wg.Go(func() { serveErr <- srv.Serve(listener) })
	report("Local test API ready at " + origin + "; authentication uses local test keys, not LinkKeys")
	select {
	case <-ctx.Done():
		e = nil
	case e = <-serveErr:
	}
	handler.Drain()
	stop()
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if srv.Shutdown(shutdown) != nil {
		srv.Close()
	}
	wg.Wait()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
