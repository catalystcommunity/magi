//go:build integration

package eventing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	w "github.com/catalystcommunity/magi/generated/events/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresImage = "postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd"

func docker(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("docker %s: %v: %s", args[0], e, out)
	}
	return strings.TrimSpace(string(out))
}

type database struct {
	id, address string
	admin       *pgxpool.Pool
}

func pg(t *testing.T) *database {
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	must(t, e)
	port := listener.Addr().(*net.TCPAddr).Port
	must(t, listener.Close())
	id := docker(t, "run", "--pull=never", "-d", "--name", "opal-events-"+token(), "--label", "opal.fixture=events", "--memory", "512m", "--cpus", "2", "-p", fmt.Sprintf("127.0.0.1:%d:5432", port), "-e", "POSTGRES_PASSWORD=synthetic-only", postgresImage)
	if len(id) != 64 {
		t.Fatal("invalid fixture ID")
	}
	t.Cleanup(func() {
		if docker(t, "inspect", "-f", `{{index .Config.Labels "opal.fixture"}}`, id) != "events" {
			t.Error("unsafe fixture cleanup")
			return
		}
		docker(t, "rm", "-f", "-v", id)
		t.Log("Removed only the synthetic event database and its disposable volume")
	})
	address := fmt.Sprintf("127.0.0.1:%d", port)
	admin, e := pgxpool.New(context.Background(), "postgres://postgres:synthetic-only@"+address+"/postgres?sslmode=disable")
	must(t, e)
	t.Cleanup(admin.Close)
	db := &database{id, address, admin}
	db.ready(t)
	_, e = admin.Exec(context.Background(), `CREATE ROLE fixture LOGIN PASSWORD 'synthetic-runtime' NOSUPERUSER NOBYPASSRLS`)
	must(t, e)
	return db
}
func (d *database) ready(t testing.TB) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		e := d.admin.Ping(ctx)
		cancel()
		if e == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture readiness failed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
func (d *database) pool(t testing.TB) (*pgxpool.Pool, string) {
	name := "e_" + token()
	_, e := d.admin.Exec(context.Background(), "CREATE DATABASE "+name+" OWNER fixture")
	must(t, e)
	dsn := "postgres://fixture:synthetic-runtime@" + d.address + "/" + name + "?sslmode=disable"
	cfg, e := pgxpool.ParseConfig(dsn)
	must(t, e)
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
	must(t, e)
	t.Cleanup(pool.Close)
	return pool, dsn
}
func signedCall(t testing.TB, private ed25519.PrivateKey, actor w.ApplicationRef, op string, body []byte) w.SignedRecord {
	t.Helper()
	s, e := Sign(op, body, actor, broker, "key", private, now())
	must(t, e)
	return s
}
func grants(t testing.TB, s *Store) {
	t.Helper()
	ctx := context.Background()
	must(t, s.SetGrant(ctx, Grant{Application: producer, Producer: producer.Identity, EventType: "calendar.changed", Publish: true}))
	must(t, s.SetGrant(ctx, Grant{Application: consumer, Producer: producer.Identity, EventType: "calendar.changed", Receive: true}))
}

func TestDurableIndependentParticipants(t *testing.T) {
	ctx := context.Background()
	db := pg(t)
	sp, _ := db.pool(t)
	pp, pdsn := db.pool(t)
	cp, cdsn := db.pool(t)
	store := &Store{Pool: sp, Application: broker}
	must(t, store.Initialize(ctx))
	grants(t, store)
	prod := &Local{Pool: pp, Application: producer}
	cons := &Local{Pool: cp, Application: consumer}
	must(t, prod.Initialize(ctx))
	must(t, cons.Initialize(ctx))
	_, e := pp.Exec(ctx, `CREATE TABLE business(id text PRIMARY KEY)`)
	must(t, e)
	_, e = cp.Exec(ctx, `CREATE TABLE business(id text PRIMARY KEY)`)
	must(t, e)
	pub, private, e := ed25519.GenerateKey(rand.Reader)
	must(t, e)
	service := &Service{Store: store, Keys: testKeys{pub}}
	start := func() *httptest.Server {
		srv := httptest.NewUnstartedServer(nil)
		handler, e := p.NewRPCHandler("http://"+srv.Listener.Addr().String(), service.Dispatch, store.Check)
		must(t, e)
		srv.Config.Handler = handler
		srv.Start()
		t.Cleanup(srv.Close)
		return srv
	}
	a, b := start(), start()
	carrier, e := NewHTTPTransport(a.URL + "/csil/v1/rpc")
	must(t, e)
	client := w.NewEventsClient(carrier)
	carrier2, e := NewHTTPTransport(b.URL + "/csil/v1/rpc")
	must(t, e)
	client2 := w.NewEventsClient(carrier2)
	sub := w.EventSubscription{SubscriptionId: "external-calendar-report", Producer: producer.Identity, EventType: "calendar.changed"}
	subscribe := func(c *w.EventsClient) (w.SubscriptionReceipt, error) {
		return c.Subscribe(ctx, signedCall(t, private, consumer, "subscribe", w.EncodeEventSubscription(sub)))
	}
	_, e = subscribe(client)
	must(t, e)
	pull := func() (w.DeliveryBatch, error) {
		return client2.Pull(ctx, signedCall(t, private, consumer, "pull", w.EncodePullEvents(w.PullEvents{SubscriptionId: sub.SubscriptionId, Limit: 8})))
	}
	publish := func(_ context.Context, target w.ApplicationRef, raw []byte) (w.PublicationReceipt, error) {
		return client.Publish(ctx, signedCall(t, private, producer, "publish", raw))
	}
	t.Run("business-and-outbox-rollback-together", func(t *testing.T) {
		tx, e := pp.Begin(ctx)
		must(t, e)
		_, e = tx.Exec(ctx, `INSERT INTO business VALUES('rollback')`)
		must(t, e)
		must(t, prod.EnqueueTx(ctx, tx, broker, sample("rollback")))
		must(t, tx.Rollback(ctx))
		var count int
		must(t, pp.QueryRow(ctx, `SELECT (SELECT count(*) FROM business)+(SELECT count(*) FROM event_local_outbox)`).Scan(&count))
		if count != 0 {
			t.Fatal("partial commit")
		}
	})
	t.Run("T04-producer-killed-after-commit", func(t *testing.T) {
		crash(t, "producer", pdsn)
		var count int
		must(t, pp.QueryRow(ctx, `SELECT count(*) FROM business WHERE id='crash'`).Scan(&count))
		if count != 1 {
			t.Fatal("business commit missing")
		}
		worked, e := prod.PublishOne(ctx, publish)
		must(t, e)
		if !worked {
			t.Fatal("committed outbox lost")
		}
	})
	var delivery w.Delivery
	t.Run("two-replicas-return-same-pending-delivery", func(t *testing.T) {
		batch, e := pull()
		must(t, e)
		if len(batch.Deliveries) != 1 {
			t.Fatalf("deliveries %d", len(batch.Deliveries))
		}
		delivery = batch.Deliveries[0]
		again, e := pull()
		must(t, e)
		if !bytes.Equal(w.EncodeDelivery(batch.Deliveries[0]), w.EncodeDelivery(again.Deliveries[0])) {
			t.Fatal("delivery changed")
		}
	})
	var receipt w.DeliveryReceipt
	t.Run("T05-receiver-killed-after-inbox-commit-before-reply", func(t *testing.T) {
		crash(t, "consumer", cdsn, hexBytes(w.EncodeDelivery(delivery)))
		r, e := cons.Accept(ctx, broker, delivery)
		must(t, e)
		receipt = r
		r2, e := cons.Accept(ctx, broker, delivery)
		must(t, e)
		if !bytes.Equal(w.EncodeDeliveryReceipt(r), w.EncodeDeliveryReceipt(r2)) {
			t.Fatal("receipt changed")
		}
	})
	t.Run("T02-concurrent-duplicate-local-effect-once", func(t *testing.T) {
		var group sync.WaitGroup
		errs := make(chan error, 12)
		for i := 0; i < 12; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				tx, e := cp.Begin(ctx)
				if e != nil {
					errs <- e
					return
				}
				defer tx.Rollback(ctx)
				_, e = cons.ApplyTx(ctx, tx, broker, delivery.DeliveryId, func(tx pgx.Tx, ev w.Event) error {
					_, e := tx.Exec(ctx, `INSERT INTO business VALUES($1)`, ev.Ref.RecordId)
					return e
				})
				if e == nil {
					e = tx.Commit(ctx)
				}
				errs <- e
			}()
		}
		group.Wait()
		close(errs)
		for e := range errs {
			must(t, e)
		}
		var count int
		must(t, cp.QueryRow(ctx, `SELECT count(*) FROM business`).Scan(&count))
		if count != 1 {
			t.Fatal("effect count", count)
		}
	})
	t.Run("lost-delivery-ack-returns-identical-receipt", func(t *testing.T) {
		q := signedCall(t, private, consumer, "acknowledge", w.EncodeAcknowledgeDelivery(w.AcknowledgeDelivery{SubscriptionId: sub.SubscriptionId, Receipt: receipt}))
		r, e := client.Acknowledge(ctx, q)
		must(t, e)
		r2, e := client2.Acknowledge(ctx, q)
		must(t, e)
		if !bytes.Equal(w.EncodeDeliveryReceipt(r), w.EncodeDeliveryReceipt(r2)) {
			t.Fatal("ack changed")
		}
		batch, e := pull()
		must(t, e)
		if len(batch.Deliveries) != 0 {
			t.Fatal("ack did not clear obligation")
		}
	})
	t.Run("T03-same-identity-key-different-content-conflicts", func(t *testing.T) {
		var raw []byte
		must(t, pp.QueryRow(ctx, `SELECT body FROM event_local_outbox WHERE id='crash'`).Scan(&raw))
		_, e := publish(ctx, broker, raw)
		must(t, e)
		v, e := w.DecodeEvent(raw)
		must(t, e)
		v.Payload.Data = []byte("changed")
		if _, e = publish(ctx, broker, w.EncodeEvent(v)); e == nil {
			t.Fatal("accepted changed content")
		}
		changed := delivery
		changed.Event = w.EncodeEvent(v)
		if _, e = cons.Accept(ctx, broker, changed); !errors.Is(e, ErrConflict) {
			t.Fatal("inbox conflict", e)
		}
	})
	t.Run("wrong-consumer-cannot-read-or-ack", func(t *testing.T) {
		_, e := client.Pull(ctx, signedCall(t, private, producer, "pull", w.EncodePullEvents(w.PullEvents{SubscriptionId: sub.SubscriptionId, Limit: 1})))
		if e == nil {
			t.Fatal("foreign subscription accepted")
		}
		bad := consumer
		bad.InstanceId = "another"
		if _, e := store.Pull(ctx, bad, w.PullEvents{SubscriptionId: sub.SubscriptionId, Limit: 1}); !errors.Is(e, ErrNotFound) {
			t.Fatal("instance isolation", e)
		}
	})
	t.Run("T07-old-event-fresh-attempt-after-two-hours", func(t *testing.T) {
		v, e := w.DecodeEvent(sample("old"))
		must(t, e)
		v.OccurredAt = now().Add(-2 * time.Hour)
		v.RecordedAt = v.OccurredAt
		_, e = publish(ctx, broker, w.EncodeEvent(v))
		must(t, e)
		batch, e := pull()
		must(t, e)
		if len(batch.Deliveries) != 1 {
			t.Fatal("old retained event rejected")
		}
	})
	t.Run("revoke-receive-policy-before-content-release", func(t *testing.T) {
		must(t, store.SetGrant(ctx, Grant{Application: consumer, Producer: producer.Identity, EventType: sub.EventType}))
		if _, e := pull(); e == nil {
			t.Fatal("released revoked content")
		}
		grants(t, store)
		batch, e := pull()
		must(t, e)
		if len(batch.Deliveries) != 1 {
			t.Fatal("obligation was discarded")
		}
	})
	t.Run("restrictions-are-intersected-at-release", func(t *testing.T) {
		v, e := w.DecodeEvent(sample("private"))
		must(t, e)
		v.Restrictions = w.Restrictions{Requirements: []w.SourceRestriction{{Owner: w.ScopeRef{Authority: producer.Identity, ScopeId: "private"}, Policy: w.RevisionRef{Resource: w.ResourceRef{Authority: producer.Identity, Kind: "policy", ResourceId: "private"}, Revision: "1", Digest: make([]byte, 32)}}}}
		raw := w.EncodeEvent(v)
		must(t, store.SetGrant(ctx, Grant{Application: producer, Producer: producer.Identity, EventType: sub.EventType, Publish: true, Restrictions: v.Restrictions}))
		_, e = publish(ctx, broker, raw)
		must(t, e)
		batch, e := pull()
		must(t, e)
		if len(batch.Deliveries) != 1 {
			t.Fatal("private metadata released")
		}
		must(t, store.SetGrant(ctx, Grant{Application: consumer, Producer: producer.Identity, EventType: sub.EventType, Receive: true, Restrictions: v.Restrictions}))
		batch, e = pull()
		must(t, e)
		if len(batch.Deliveries) != 2 {
			t.Fatal("permitted private record absent")
		}
	})
	t.Run("T06-external-timeout-stays-unknown-until-reconciled", func(t *testing.T) {
		batch, e := pull()
		must(t, e)
		d := batch.Deliveries[0]
		_, e = cons.Accept(ctx, broker, d)
		must(t, e)
		must(t, cons.BeginExternal(ctx, broker, d.DeliveryId))
		if e := cons.BeginExternal(ctx, broker, d.DeliveryId); !errors.Is(e, ErrConflict) {
			t.Fatal("blind external retry")
		}
		tx, e := cp.Begin(ctx)
		must(t, e)
		defer tx.Rollback(ctx)
		called := false
		_, e = cons.ApplyTx(ctx, tx, broker, d.DeliveryId, func(pgx.Tx, w.Event) error { called = true; return nil })
		if !errors.Is(e, ErrConflict) || called {
			t.Fatal("unknown effect repeated")
		}
		must(t, tx.Rollback(ctx))
		must(t, cons.ResolveExternal(ctx, broker, d.DeliveryId, "receipt:external-1", false))
	})
	t.Run("expired-publisher-fence-and-lost-publication-reply", func(t *testing.T) {
		tx, e := pp.Begin(ctx)
		must(t, e)
		raw := sample("lease")
		must(t, prod.EnqueueTx(ctx, tx, broker, raw))
		must(t, tx.Commit(ctx))
		first, e := prod.Claim(ctx, time.Second)
		must(t, e)
		if first == nil {
			t.Fatal("missing claim")
		}
		r, e := publish(ctx, broker, first.Body)
		must(t, e)
		_, e = pp.Exec(ctx, `UPDATE event_local_outbox SET due=clock_timestamp()-interval '1 second' WHERE id='lease'`)
		must(t, e)
		second, e := prod.Claim(ctx, time.Second*30)
		must(t, e)
		if second == nil || first.Attempt == second.Attempt {
			t.Fatal("lease not fenced")
		}
		if e = prod.Complete(ctx, *first, r); !errors.Is(e, ErrFence) {
			t.Fatal("old publisher accepted", e)
		}
		r2, e := publish(ctx, broker, second.Body)
		must(t, e)
		if !bytes.Equal(w.EncodePublicationReceipt(r), w.EncodePublicationReceipt(r2)) {
			t.Fatal("lost reply changed receipt")
		}
		must(t, prod.Complete(ctx, *second, r2))
	})
	t.Run("retry-exhaustion-retains-attention-record", func(t *testing.T) {
		tx, e := pp.Begin(ctx)
		must(t, e)
		must(t, prod.EnqueueTx(ctx, tx, broker, sample("attention")))
		must(t, tx.Commit(ctx))
		claim, e := prod.Claim(ctx, 30*time.Second)
		must(t, e)
		must(t, prod.Retry(ctx, *claim, time.Second, "permission-denied", true))
		next, e := prod.Claim(ctx, time.Second)
		must(t, e)
		if next != nil {
			t.Fatal("attention auto-retried")
		}
		must(t, prod.Resume(ctx, broker, "attention"))
		next, e = prod.Claim(ctx, 30*time.Second)
		must(t, e)
		if next == nil || next.ID != "attention" {
			t.Fatal("operator resume lost record")
		}
	})
	t.Run("postgres-kill-restart-preserves-receipts-and-obligations", func(t *testing.T) {
		before, e := pull()
		must(t, e)
		docker(t, "kill", "--signal=KILL", db.id)
		docker(t, "start", db.id)
		db.ready(t)
		after, e := pull()
		must(t, e)
		if !bytes.Equal(w.EncodeDeliveryBatch(before), w.EncodeDeliveryBatch(after)) {
			t.Fatal("pending history changed after database recovery")
		}
		r, e := cons.Accept(ctx, broker, delivery)
		must(t, e)
		if !bytes.Equal(w.EncodeDeliveryReceipt(r), w.EncodeDeliveryReceipt(receipt)) {
			t.Fatal("receipt lost on database recovery")
		}
	})
	t.Run("service-store-cannot-change-authority", func(t *testing.T) {
		other := &Store{Pool: sp, Application: consumer}
		if e := other.Initialize(ctx); !errors.Is(e, ErrConflict) {
			t.Fatal("store identity changed", e)
		}
	})
	t.Run("concurrent-publication-has-one-receipt", func(t *testing.T) {
		raw := sample("concurrent")
		type result struct {
			raw []byte
			err error
		}
		ch := make(chan result, 12)
		for i := 0; i < 12; i++ {
			go func() { r, e := store.Publish(ctx, producer, raw); ch <- result{r, e} }()
		}
		var first []byte
		for i := 0; i < 12; i++ {
			r := <-ch
			must(t, r.err)
			if first == nil {
				first = r.raw
			} else if !bytes.Equal(first, r.raw) {
				t.Fatal("multiple publication receipts")
			}
		}
	})
	t.Run("publication-and-acknowledgement-proofs-retained", func(t *testing.T) {
		var proof []byte
		must(t, sp.QueryRow(ctx, `SELECT proof FROM event_publications WHERE id='crash'`).Scan(&proof))
		packet, e := w.DecodeSignedRecord(proof)
		must(t, e)
		v, e := authenticate(ctx, testKeys{pub}, broker, "publish", packet)
		must(t, e)
		ev, e := event(v.Body)
		must(t, e)
		if ev.Ref.RecordId != "crash" {
			t.Fatal("wrong publication proof")
		}
		must(t, sp.QueryRow(ctx, `SELECT acknowledgement_proof FROM event_deliveries WHERE id=$1`, delivery.DeliveryId).Scan(&proof))
		packet, e = w.DecodeSignedRecord(proof)
		must(t, e)
		_, e = authenticate(ctx, testKeys{pub}, broker, "acknowledge", packet)
		must(t, e)
	})
	t.Run("large-pull-stays-within-carrier-budget", func(t *testing.T) {
		bigSub := w.EventSubscription{SubscriptionId: "large", Producer: producer.Identity, EventType: "calendar.changed"}
		_, e := store.Subscribe(ctx, consumer, bigSub)
		must(t, e)
		for i := 0; i < 8; i++ {
			v, e := w.DecodeEvent(sample(fmt.Sprintf("large-%d", i)))
			must(t, e)
			v.Payload.Data = make([]byte, MaxEventBytes-800)
			_, e = store.Publish(ctx, producer, w.EncodeEvent(v))
			must(t, e)
		}
		raw, e := store.Pull(ctx, consumer, w.PullEvents{SubscriptionId: "large", Limit: 8})
		must(t, e)
		must(t, p.CheckFrame(raw))
		if len(raw) > p.MaxBlob-1024 {
			t.Fatal("response exceeds carrier budget")
		}
		batch, e := w.DecodeDeliveryBatch(raw)
		must(t, e)
		if len(batch.Deliveries) < 1 {
			t.Fatal("no progress")
		}
	})
	t.Run("repeated-publisher-crashes-require-attention", func(t *testing.T) {
		_, e := pp.Exec(ctx, `UPDATE event_local_outbox SET attempts=20,state='attempting',due=clock_timestamp()-interval '1 second' WHERE id='attention'`)
		must(t, e)
		c, e := prod.Claim(ctx, time.Second)
		must(t, e)
		if c != nil {
			t.Fatal("exhausted claim was retried")
		}
		var state string
		must(t, pp.QueryRow(ctx, `SELECT state FROM event_local_outbox WHERE id='attention'`).Scan(&state))
		if state != "attention" {
			t.Fatal("crash exhaustion was lost")
		}
	})
}

func hexBytes(b []byte) string { return fmt.Sprintf("%x", b) }
func crash(t testing.TB, mode, dsn string, extra ...string) {
	t.Helper()
	args := []string{"-test.run=^TestCrashProcess$"}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "EVENT_FIXTURE_CRASH="+mode, "EVENT_FIXTURE_DSN="+dsn)
	if len(extra) > 0 {
		cmd.Env = append(cmd.Env, "EVENT_FIXTURE_DELIVERY="+extra[0])
	}
	out, e := cmd.CombinedOutput()
	if e == nil {
		t.Fatal("child did not crash")
	}
	var exit *exec.ExitError
	if !errors.As(e, &exit) {
		t.Fatalf("unexpected child failure: %v %s", e, out)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child was not killed: %v %s", e, out)
	}
}
func TestCrashProcess(t *testing.T) {
	mode := os.Getenv("EVENT_FIXTURE_CRASH")
	if mode == "" {
		return
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, os.Getenv("EVENT_FIXTURE_DSN"))
	must(t, e)
	if mode == "producer" {
		l := &Local{Pool: pool, Application: producer}
		tx, e := pool.Begin(ctx)
		must(t, e)
		_, e = tx.Exec(ctx, `INSERT INTO business VALUES('crash')`)
		must(t, e)
		must(t, l.EnqueueTx(ctx, tx, broker, sample("crash")))
		must(t, tx.Commit(ctx))
	} else {
		var raw []byte
		_, e := fmt.Sscanf(os.Getenv("EVENT_FIXTURE_DELIVERY"), "%x", &raw)
		must(t, e)
		d, e := w.DecodeDelivery(raw)
		must(t, e)
		l := &Local{Pool: pool, Application: consumer}
		_, e = l.Accept(ctx, broker, d)
		must(t, e)
	}
	must(t, syscall.Kill(os.Getpid(), syscall.SIGKILL))
	panic("SIGKILL did not terminate the fixture")
}
