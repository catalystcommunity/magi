package eventing

import (
	"bytes"
	"context"
	"errors"
	"time"

	w "github.com/catalystcommunity/magi/generated/events/go"
	"github.com/jackc/pgx/v5"
)

const recoveryLock = 427361003
const capacityLock = 427361004

func (s *Store) generation() string {
	if s.Generation == "" {
		return "initial"
	}
	return s.Generation
}

// guard holds the restore fence until this transaction finishes. Readiness
// alone cannot protect calls already admitted by another replica.
func (s *Store) guard(ctx context.Context, tx pgx.Tx) error {
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, recoveryLock); e != nil {
		return e
	}
	var generation string
	var blocked bool
	if e := tx.QueryRow(ctx, `SELECT generation,blocked FROM event_recovery`).Scan(&generation, &blocked); e != nil {
		return e
	}
	if blocked || generation != s.generation() {
		return ErrRecovery
	}
	return nil
}

type Limits struct {
	Publications       int64 `json:"publications"`
	Deliveries         int64 `json:"deliveries"`
	Subscriptions      int64 `json:"subscriptions"`
	PendingPerConsumer int64 `json:"pending_per_consumer"`
	PayloadBytes       int64 `json:"payload_bytes"`
}

// SetLimits is an administrator operation. The runtime has SELECT-only access
// to limits. A lower limit does not delete records or prevent acknowledgements.
func (s *Store) SetLimits(ctx context.Context, v Limits) error {
	if v.Publications < 1 || v.Deliveries < 1 || v.Subscriptions < 1 || v.PendingPerConsumer < 1 || v.PayloadBytes < 1 {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, capacityLock); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE event_limits SET publications=$1,deliveries=$2,subscriptions=$3,pending_per_consumer=$4,payload_bytes=$5`, v.Publications, v.Deliveries, v.Subscriptions, v.PendingPerConsumer, v.PayloadBytes); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) reserve(ctx context.Context, tx pgx.Tx, publications, deliveries, subscriptions, payload int64, consumers map[string]int64) error {
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, capacityLock); e != nil {
		return e
	}
	r, e := tx.Exec(ctx, `UPDATE event_usage u SET publications=u.publications+$1,deliveries=u.deliveries+$2,subscriptions=u.subscriptions+$3,payload_bytes=u.payload_bytes+$4 FROM event_limits l WHERE u.publications+$1<=l.publications AND u.deliveries+$2<=l.deliveries AND u.subscriptions+$3<=l.subscriptions AND u.payload_bytes+$4<=l.payload_bytes`, publications, deliveries, subscriptions, payload)
	if e != nil {
		return e
	}
	if r.RowsAffected() != 1 {
		return ErrCapacity
	}
	// New deliveries are already in this transaction. Their count is included.
	// Other publishers cannot reserve until this transaction commits.
	for consumer := range consumers {
		var fits bool
		if e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM event_deliveries WHERE consumer=$1 AND receipt IS NULL)<=pending_per_consumer FROM event_limits`, consumer).Scan(&fits); e != nil {
			return e
		}
		if !fits {
			return ErrCapacity
		}
	}
	return nil
}

type Usage struct {
	Publications  int64 `json:"publications"`
	Deliveries    int64 `json:"deliveries"`
	Subscriptions int64 `json:"subscriptions"`
	PayloadBytes  int64 `json:"payload_bytes"`
}
type QueueSummary struct {
	Consumer      string     `json:"consumer_key"`
	Subscription  string     `json:"subscription_id"`
	Pending       int64      `json:"pending"`
	Acknowledged  int64      `json:"acknowledged"`
	OldestPending *time.Time `json:"oldest_pending,omitempty"`
}
type Inspection struct {
	Generation string         `json:"generation"`
	Blocked    bool           `json:"blocked"`
	Limits     Limits         `json:"limits"`
	Usage      Usage          `json:"usage"`
	Queues     []QueueSummary `json:"queues"`
}

// Inspect is an operator view, not a public metadata endpoint. It returns no
// payloads, keys, signatures, or connection strings. The cursor is exclusive.
func (s *Store) Inspect(ctx context.Context, afterConsumer, afterSubscription string, limit int) (Inspection, error) {
	v := Inspection{Queues: []QueueSummary{}}
	if limit < 1 || limit > 100 || len(afterConsumer) > 64 || len(afterSubscription) > 256 {
		return v, ErrInvalid
	}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `SELECT r.generation,r.blocked,l.publications,l.deliveries,l.subscriptions,l.pending_per_consumer,l.payload_bytes,u.publications,u.deliveries,u.subscriptions,u.payload_bytes FROM event_recovery r CROSS JOIN event_limits l CROSS JOIN event_usage u`).Scan(&v.Generation, &v.Blocked, &v.Limits.Publications, &v.Limits.Deliveries, &v.Limits.Subscriptions, &v.Limits.PendingPerConsumer, &v.Limits.PayloadBytes, &v.Usage.Publications, &v.Usage.Deliveries, &v.Usage.Subscriptions, &v.Usage.PayloadBytes)
	if e != nil {
		return v, e
	}
	rows, e := tx.Query(ctx, `SELECT s.consumer,s.id,count(d.id) FILTER(WHERE d.receipt IS NULL),count(d.id) FILTER(WHERE d.receipt IS NOT NULL),min(d.created_at) FILTER(WHERE d.receipt IS NULL) FROM (SELECT consumer,id FROM event_subscriptions WHERE (consumer,id)>($1,$2) ORDER BY consumer,id LIMIT $3) s LEFT JOIN event_deliveries d ON d.consumer=s.consumer AND d.subscription=s.id GROUP BY s.consumer,s.id ORDER BY s.consumer,s.id`, afterConsumer, afterSubscription, limit)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var q QueueSummary
		if e = rows.Scan(&q.Consumer, &q.Subscription, &q.Pending, &q.Acknowledged, &q.OldestPending); e != nil {
			rows.Close()
			return v, e
		}
		v.Queues = append(v.Queues, q)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}

// BeginRecovery closes admission and fences old replicas. Before restoring a
// backup, stop all replicas and change Generation in external configuration.
// A database cannot detect a rollback of itself and its only fence together.
func (s *Store) BeginRecovery(ctx context.Context) error {
	if !namePattern.MatchString(s.generation()) {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, recoveryLock); e != nil {
		return e
	}
	var old string
	var blocked, used bool
	if e = tx.QueryRow(ctx, `SELECT generation,blocked,EXISTS(SELECT 1 FROM event_recovery_history WHERE generation=$1) FROM event_recovery`, s.generation()).Scan(&old, &blocked, &used); e != nil {
		return e
	}
	if old == s.generation() && blocked {
		return tx.Commit(ctx)
	}
	if old == s.generation() || used {
		return ErrConflict
	}
	if _, e = tx.Exec(ctx, `UPDATE event_recovery SET generation=$1,blocked=true,evidence=NULL`, s.generation()); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// CompleteRecovery records the operator's reconciliation evidence. It cannot
// infer which lost publications, subscriptions, or business effects to rebuild.
func (s *Store) CompleteRecovery(ctx context.Context, evidence string) error {
	if !namePattern.MatchString(evidence) {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, recoveryLock); e != nil {
		return e
	}
	var generation string
	var blocked bool
	var saved *string
	if e = tx.QueryRow(ctx, `SELECT generation,blocked,evidence FROM event_recovery`).Scan(&generation, &blocked, &saved); e != nil {
		return e
	}
	if generation != s.generation() {
		return ErrConflict
	}
	if !blocked {
		if saved != nil && *saved == evidence {
			return tx.Commit(ctx)
		}
		return ErrConflict
	}
	if _, e = tx.Exec(ctx, `INSERT INTO event_recovery_history(generation,evidence) VALUES($1,$2)`, generation, evidence); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE event_recovery SET blocked=false,evidence=$1`, evidence); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// ComparePublication is a read-only operator reconciliation check. It remains
// available while the public data operations are blocked. A missing receipt is
// not evidence that an external effect did not occur.
func (s *Store) ComparePublication(ctx context.Context, expected w.PublicationReceipt) (string, error) {
	if expected.Receiver != s.Application || expected.Event.Authority != expected.Publisher.Identity || expected.Event.Kind != "event" || !validApplication(expected.Publisher) || len(expected.Digest) != 32 || !namePattern.MatchString(expected.Event.RecordId) {
		return "", ErrInvalid
	}
	var saved []byte
	e := s.Pool.QueryRow(ctx, `SELECT receipt FROM event_publications WHERE producer=$1 AND id=$2 AND publisher=$3`, identity(expected.Publisher.Identity), expected.Event.RecordId, application(expected.Publisher)).Scan(&saved)
	if errors.Is(e, pgx.ErrNoRows) {
		return "missing", nil
	}
	if e != nil {
		return "", e
	}
	if !bytes.Equal(saved, w.EncodePublicationReceipt(expected)) {
		return "conflict", nil
	}
	return "match", nil
}

// CompareAcknowledgement distinguishes a restored unacknowledged obligation
// from one absent from the restored store. It does not reset or replay effects.
func (s *Store) CompareAcknowledgement(ctx context.Context, consumer w.ApplicationRef, subscription string, expected w.DeliveryReceipt) (string, error) {
	if !validApplication(consumer) || expected.Recipient != consumer.Identity || !namePattern.MatchString(subscription) {
		return "", ErrInvalid
	}
	var saved, raw []byte
	e := s.Pool.QueryRow(ctx, `SELECT body,receipt FROM event_deliveries WHERE consumer=$1 AND subscription=$2 AND id=$3`, application(consumer), subscription, expected.DeliveryId).Scan(&raw, &saved)
	if errors.Is(e, pgx.ErrNoRows) {
		return "missing", nil
	}
	if e != nil {
		return "", e
	}
	d, e := w.DecodeDelivery(raw)
	if e != nil {
		return "", e
	}
	ev, e := event(d.Event)
	if e != nil {
		return "", e
	}
	if expected.Event != ev.Ref || !bytes.Equal(expected.Digest, digest(d.Event)) {
		return "conflict", nil
	}
	if saved == nil {
		return "pending", nil
	}
	if !bytes.Equal(saved, w.EncodeDeliveryReceipt(expected)) {
		return "conflict", nil
	}
	return "match", nil
}
