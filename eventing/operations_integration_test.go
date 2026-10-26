//go:build integration

package eventing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"testing"

	w "github.com/catalystcommunity/magi/generated/events/go"
	"github.com/jackc/pgx/v5"
)

func TestOperationalRecovery(t *testing.T) {
	ctx := context.Background()
	db := pg(t)
	fresh := func(t *testing.T) (*Store, w.EventSubscription) {
		pool, _ := db.pool(t)
		s := &Store{Pool: pool, Application: broker}
		must(t, s.Initialize(ctx))
		grants(t, s)
		sub := w.EventSubscription{SubscriptionId: "operational", Producer: producer.Identity, EventType: "calendar.changed"}
		_, e := s.Subscribe(ctx, consumer, sub)
		must(t, e)
		return s, sub
	}
	limits := Limits{Publications: 100, Deliveries: 100, Subscriptions: 100, PendingPerConsumer: 100, PayloadBytes: 1 << 20}
	t.Run("concurrent-admission-is-atomic-at-capacity", func(t *testing.T) {
		s, _ := fresh(t)
		v := limits
		v.Publications = 3
		must(t, s.SetLimits(ctx, v))
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := s.Publish(ctx, producer, sample(fmt.Sprintf("quota-%d", i)))
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		accepted := 0
		for e := range errs {
			if e == nil {
				accepted++
			} else if !errors.Is(e, ErrCapacity) {
				t.Fatal(e)
			}
		}
		if accepted != 3 {
			t.Fatalf("accepted %d", accepted)
		}
		v.Publications = 1
		must(t, s.SetLimits(ctx, v))
		inspection, e := s.Inspect(ctx, "", "", 100)
		must(t, e)
		if inspection.Usage.Publications != 3 || inspection.Usage.Deliveries != 3 || inspection.Queues[0].Pending != 3 {
			t.Fatalf("partial quota commit: %+v", inspection)
		}
		var raw []byte
		must(t, s.Pool.QueryRow(ctx, `SELECT body FROM event_publications LIMIT 1`).Scan(&raw))
		_, e = s.Publish(ctx, producer, raw)
		must(t, e) // Duplicate remains usable above a lowered limit.
		_, e = s.Subscribe(ctx, consumer, w.EventSubscription{SubscriptionId: "operational", Producer: producer.Identity, EventType: "calendar.changed"})
		must(t, e)
	})
	t.Run("pending-capacity-recovers-after-acknowledgement", func(t *testing.T) {
		s, sub := fresh(t)
		v := limits
		v.PendingPerConsumer = 1
		must(t, s.SetLimits(ctx, v))
		_, e := s.Publish(ctx, producer, sample("first"))
		must(t, e)
		_, e = s.Publish(ctx, producer, sample("next"))
		if !errors.Is(e, ErrCapacity) {
			t.Fatal(e)
		}
		cp, _ := db.pool(t)
		local := &Local{Pool: cp, Application: consumer}
		must(t, local.Initialize(ctx))
		raw, e := s.Pull(ctx, consumer, w.PullEvents{SubscriptionId: sub.SubscriptionId, Limit: 8})
		must(t, e)
		batch, e := w.DecodeDeliveryBatch(raw)
		must(t, e)
		r, e := local.Accept(ctx, broker, batch.Deliveries[0])
		must(t, e)
		_, e = s.Acknowledge(ctx, consumer, w.AcknowledgeDelivery{SubscriptionId: sub.SubscriptionId, Receipt: r})
		must(t, e)
		_, e = s.Publish(ctx, producer, sample("next"))
		must(t, e)
	})
	for _, name := range []string{"deliveries", "payload", "subscriptions"} {
		t.Run("bound-"+name, func(t *testing.T) {
			s, _ := fresh(t)
			v := limits
			switch name {
			case "deliveries":
				v.Deliveries = 1
			case "payload":
				v.PayloadBytes = 1
			case "subscriptions":
				v.Subscriptions = 1
			}
			must(t, s.SetLimits(ctx, v))
			var e error
			if name == "subscriptions" {
				_, e = s.Subscribe(ctx, consumer, w.EventSubscription{SubscriptionId: "extra", Producer: producer.Identity, EventType: "calendar.changed"})
			} else {
				if name == "deliveries" {
					_, e = s.Publish(ctx, producer, sample("one"))
					must(t, e)
				}
				_, e = s.Publish(ctx, producer, sample("too-many"))
			}
			if !errors.Is(e, ErrCapacity) {
				t.Fatal(e)
			}
		})
	}
	t.Run("inspection-paginates-without-payloads", func(t *testing.T) {
		s, _ := fresh(t)
		_, e := s.Subscribe(ctx, consumer, w.EventSubscription{SubscriptionId: "zz", Producer: producer.Identity, EventType: "calendar.changed"})
		must(t, e)
		a, e := s.Inspect(ctx, "", "", 1)
		must(t, e)
		if len(a.Queues) != 1 {
			t.Fatal(a)
		}
		b, e := s.Inspect(ctx, a.Queues[0].Consumer, a.Queues[0].Subscription, 1)
		must(t, e)
		if len(b.Queues) != 1 || b.Queues[0].Subscription != "zz" {
			t.Fatal(b)
		}
		_, e = s.Inspect(ctx, "", "", 101)
		if !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	})
	t.Run("real-backup-restore-fences-replicas-and-exposes-gaps", func(t *testing.T) {
		s, sub := fresh(t)
		raw, e := s.Publish(ctx, producer, sample("before-backup"))
		must(t, e)
		before, e := w.DecodePublicationReceipt(raw)
		must(t, e)
		backup, e := exec.Command("docker", "exec", db.id, "pg_dump", "-U", "postgres", "-Fc", "--no-owner", s.Pool.Config().ConnConfig.Database).Output()
		must(t, e)
		raw, e = s.Publish(ctx, producer, sample("after-backup"))
		must(t, e)
		after, e := w.DecodePublicationReceipt(raw)
		must(t, e)
		cp, _ := db.pool(t)
		consumerStore := &Local{Pool: cp, Application: consumer}
		must(t, consumerStore.Initialize(ctx))
		raw, e = s.Pull(ctx, consumer, w.PullEvents{SubscriptionId: sub.SubscriptionId, Limit: 8})
		must(t, e)
		batch, e := w.DecodeDeliveryBatch(raw)
		must(t, e)
		delivery := batch.Deliveries[0]
		ack, e := consumerStore.Accept(ctx, broker, delivery)
		must(t, e)
		_, e = s.Acknowledge(ctx, consumer, w.AcknowledgeDelivery{SubscriptionId: sub.SubscriptionId, Receipt: ack})
		must(t, e)
		restoredPool, _ := db.pool(t)
		cmd := exec.Command("docker", "exec", "-i", db.id, "pg_restore", "-U", "postgres", "--no-owner", "--role=fixture", "--exit-on-error", "-d", restoredPool.Config().ConnConfig.Database)
		cmd.Stdin = bytes.NewReader(backup)
		output, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("restore: %v %s", e, output)
		}
		r := &Store{Pool: restoredPool, Application: broker, Generation: "restore-1"}
		if !errors.Is(r.Check(ctx), ErrRecovery) {
			t.Fatal("old backup admitted new generation")
		}
		if _, e = r.Pull(ctx, consumer, w.PullEvents{SubscriptionId: sub.SubscriptionId, Limit: 8}); !errors.Is(e, ErrRecovery) {
			t.Fatal(e)
		}
		must(t, r.BeginRecovery(ctx))
		must(t, r.BeginRecovery(ctx))
		oldReplica := &Store{Pool: restoredPool, Application: broker}
		if _, e = oldReplica.Publish(ctx, producer, sample("blocked")); !errors.Is(e, ErrRecovery) {
			t.Fatal(e)
		}
		match, e := r.ComparePublication(ctx, before)
		must(t, e)
		if match != "match" {
			t.Fatal(match)
		}
		match, e = r.ComparePublication(ctx, after)
		must(t, e)
		if match != "missing" {
			t.Fatal(match)
		}
		changed := before
		changed.ReceiptId = "changed"
		match, e = r.ComparePublication(ctx, changed)
		must(t, e)
		if match != "conflict" {
			t.Fatal(match)
		}
		match, e = r.CompareAcknowledgement(ctx, consumer, sub.SubscriptionId, ack)
		must(t, e)
		if match != "pending" {
			t.Fatal(match)
		}
		// The operator records a known gap. Completion does not invent its data.
		must(t, r.CompleteRecovery(ctx, "evidence:known-gap-fixture"))
		must(t, r.CompleteRecovery(ctx, "evidence:known-gap-fixture"))
		if !errors.Is(r.CompleteRecovery(ctx, "evidence:changed"), ErrConflict) {
			t.Fatal("changed confirmation accepted")
		}
		if !errors.Is(oldReplica.Check(ctx), ErrRecovery) {
			t.Fatal("old replica resumed")
		}
		must(t, r.Check(ctx))
		raw, e = r.Pull(ctx, consumer, w.PullEvents{SubscriptionId: sub.SubscriptionId, Limit: 8})
		must(t, e)
		redelivery, e := w.DecodeDeliveryBatch(raw)
		must(t, e)
		if len(redelivery.Deliveries) != 1 {
			t.Fatal("restore invented lost publications")
		}
		again, e := consumerStore.Accept(ctx, broker, redelivery.Deliveries[0])
		must(t, e)
		if !bytes.Equal(w.EncodeDeliveryReceipt(ack), w.EncodeDeliveryReceipt(again)) {
			t.Fatal("durable consumer lost deduplication")
		}
		_, e = r.Acknowledge(ctx, consumer, w.AcknowledgeDelivery{SubscriptionId: sub.SubscriptionId, Receipt: again})
		must(t, e)
		match, e = r.CompareAcknowledgement(ctx, consumer, sub.SubscriptionId, ack)
		must(t, e)
		if match != "match" {
			t.Fatal(match)
		}
	})
	t.Run("recovery-waits-for-in-flight-transaction", func(t *testing.T) {
		s, _ := fresh(t)
		tx, e := s.Pool.Begin(ctx)
		must(t, e)
		must(t, s.guard(ctx, tx))
		// A failed NOWAIT probe proves the transaction holds the shared fence.
		other, e := s.Pool.Begin(ctx)
		must(t, e)
		var acquired bool
		must(t, other.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, recoveryLock).Scan(&acquired))
		if acquired {
			t.Fatal("restore fence not held")
		}
		must(t, other.Rollback(ctx))
		must(t, tx.Commit(ctx))
		s.Generation = "next"
		must(t, s.BeginRecovery(ctx))
	})
	t.Run("repeat-migration-preserves-usage", func(t *testing.T) {
		s, _ := fresh(t)
		_, e := s.Publish(ctx, producer, sample("retained"))
		must(t, e)
		must(t, s.Initialize(ctx))
		var n int
		must(t, s.Pool.QueryRow(ctx, `SELECT publications FROM event_usage`).Scan(&n))
		if n != 1 {
			t.Fatal(n)
		}
		// A read-only role cannot change recovery state or limits.
		tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		must(t, e)
		_, e = tx.Exec(ctx, `UPDATE event_recovery SET blocked=false`)
		if e == nil {
			t.Fatal("read-only transaction changed fence")
		}
		must(t, tx.Rollback(ctx))
	})
	t.Run("version-one-upgrade-preserves-records-and-backfills-limits", func(t *testing.T) {
		s, _ := fresh(t)
		raw := sample("upgrade")
		before, e := s.Publish(ctx, producer, raw)
		must(t, e)
		// Recreate the previous schema boundary in this disposable database.
		_, e = s.Pool.Exec(ctx, `DROP TABLE event_usage,event_limits,event_recovery_history,event_recovery;
ALTER TABLE event_service DROP CONSTRAINT event_service_version_check;
UPDATE event_service SET version=1;
ALTER TABLE event_service ADD CONSTRAINT event_service_version_check CHECK(version=1);`)
		must(t, e)
		if !errors.Is(s.CheckBinding(ctx), ErrConflict) {
			t.Fatal("old schema admitted")
		}
		must(t, s.Initialize(ctx))
		must(t, s.Check(ctx))
		after, e := s.Publish(ctx, producer, raw)
		must(t, e)
		if !bytes.Equal(before, after) {
			t.Fatal("upgrade changed receipt")
		}
		usage, e := s.Inspect(ctx, "", "", 100)
		must(t, e)
		if usage.Usage.Publications != 1 || usage.Usage.Deliveries != 1 || usage.Usage.Subscriptions != 1 || usage.Usage.PayloadBytes == 0 {
			t.Fatal("usage backfill failed")
		}
	})
}
