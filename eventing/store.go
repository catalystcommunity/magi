// Package eventing provides event delivery without an Magi session or worker.
package eventing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"time"

	w "github.com/catalystcommunity/magi/generated/events/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var Migration string

var (
	ErrInvalid     = errors.New("invalid event request")
	ErrDenied      = errors.New("event operation denied")
	ErrConflict    = errors.New("identifier has different content")
	ErrNotFound    = errors.New("record is not available")
	ErrFence       = errors.New("attempt no longer owns the claim")
	ErrUnavailable = errors.New("identity evidence is unavailable")
	ErrCapacity    = errors.New("event storage capacity reached")
	ErrRecovery    = errors.New("event storage requires reconciliation")
)

const MaxEventBytes = 16384

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)
var domainPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)+$`)

func validIdentity(i w.IdentityRef) bool {
	return domainPattern.MatchString(i.Domain) && namePattern.MatchString(i.AccountId)
}
func validApplication(a w.ApplicationRef) bool {
	return validIdentity(a.Identity) && namePattern.MatchString(a.ApplicationId) && namePattern.MatchString(a.InstanceId)
}
func key(b []byte) string                   { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func identity(i w.IdentityRef) string       { return key(w.EncodeIdentityRef(i)) }
func application(a w.ApplicationRef) string { return key(w.EncodeApplicationRef(a)) }
func digest(b []byte) []byte                { h := sha256.Sum256(b); return h[:] }
func token() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }
func event(raw []byte) (w.Event, error) {
	e, err := p.Decode(raw, w.DecodeEvent, w.EncodeEvent)
	if err != nil || len(raw) > MaxEventBytes || !validIdentity(e.Producer) || e.Ref.Authority != e.Producer || e.Ref.Kind != "event" || !namePattern.MatchString(e.Ref.RecordId) || !namePattern.MatchString(e.EventType) || len(e.Recipients) == 0 || len(e.Recipients) > 64 || len(e.Restrictions.Requirements) > 32 || e.Payload.Schema.Name == "" || e.Payload.Schema.Version == "" || len(e.Payload.Schema.Digest) != 32 {
		return e, ErrInvalid
	}
	for _, r := range e.Recipients {
		if !validIdentity(r) {
			return e, ErrInvalid
		}
	}
	for _, r := range e.Restrictions.Requirements {
		if !validIdentity(r.Owner.Authority) || r.Owner.ScopeId == "" || !validIdentity(r.Policy.Resource.Authority) || r.Policy.Resource.Kind == "" || r.Policy.Resource.ResourceId == "" || r.Policy.Revision == "" || len(r.Policy.Digest) != 32 {
			return e, ErrInvalid
		}
	}
	return e, nil
}
func restrictionKeys(r w.Restrictions) [][]byte {
	out := make([][]byte, 0, len(r.Requirements))
	for _, v := range r.Requirements {
		out = append(out, digest(w.EncodeSourceRestriction(v)))
	}
	return out
}

type Store struct {
	Pool        *pgxpool.Pool
	Application w.ApplicationRef
	// Generation is an operator-managed restore fence, held outside backups.
	Generation string
}

// Initialize is a migration operation. Normal runtime roles need only DML.
func (s *Store) Initialize(ctx context.Context) error {
	if !validApplication(s.Application) {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(427361001)`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, Migration); e != nil {
		return e
	}
	raw := w.EncodeApplicationRef(s.Application)
	if _, e = tx.Exec(ctx, `INSERT INTO event_service(application,version) VALUES($1,2) ON CONFLICT DO NOTHING`, raw); e != nil {
		return e
	}
	var saved []byte
	if e = tx.QueryRow(ctx, `SELECT application FROM event_service`).Scan(&saved); e != nil {
		return e
	}
	if !bytes.Equal(saved, raw) {
		return ErrConflict
	}
	if !namePattern.MatchString(s.generation()) {
		return ErrInvalid
	}
	if _, e = tx.Exec(ctx, `INSERT INTO event_recovery(singleton,generation) VALUES(true,$1) ON CONFLICT DO NOTHING`, s.generation()); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Check(ctx context.Context) error {
	if e := s.CheckBinding(ctx); e != nil {
		return e
	}
	var generation string
	var blocked bool
	if e := s.Pool.QueryRow(ctx, `SELECT generation,blocked FROM event_recovery`).Scan(&generation, &blocked); e != nil {
		return e
	}
	if generation != s.generation() || blocked {
		return ErrRecovery
	}
	return nil
}

// CheckBinding is also available while recovery has stopped data operations.
func (s *Store) CheckBinding(ctx context.Context) error {
	var raw []byte
	var version int
	if e := s.Pool.QueryRow(ctx, `SELECT application,version FROM event_service`).Scan(&raw, &version); e != nil {
		return e
	}
	if version != 2 || !bytes.Equal(raw, w.EncodeApplicationRef(s.Application)) {
		return ErrConflict
	}
	return nil
}

// Grant is a local administrator operation, not a LinkKeys permission.
type Grant struct {
	Application  w.ApplicationRef `json:"application"`
	Producer     w.IdentityRef    `json:"producer"`
	EventType    string           `json:"event_type"`
	Publish      bool             `json:"publish"`
	Receive      bool             `json:"receive"`
	Restrictions w.Restrictions   `json:"restrictions"`
}

func (s *Store) SetGrant(ctx context.Context, g Grant) error {
	if !validApplication(g.Application) || !validIdentity(g.Producer) || !namePattern.MatchString(g.EventType) {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, application(g.Application)+identity(g.Producer)+g.EventType); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO event_grants VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(application,producer,kind) DO UPDATE SET publish=excluded.publish,receive=excluded.receive,restrictions=excluded.restrictions`, application(g.Application), identity(g.Producer), g.EventType, g.Publish, g.Receive, restrictionKeys(g.Restrictions))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func allowed(ctx context.Context, tx pgx.Tx, a w.ApplicationRef, producer w.IdentityRef, kind string, publish bool, restrictions [][]byte) error {
	var yes bool
	// Row locks require UPDATE rights even for SELECT FOR SHARE. Use an advisory
	// lock so the serving role can have SELECT-only access to policy records.
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, application(a)+identity(producer)+kind); e != nil {
		return e
	}
	e := tx.QueryRow(ctx, `SELECT CASE WHEN $4 THEN publish ELSE receive END AND restrictions @> $5::bytea[] FROM event_grants WHERE application=$1 AND producer=$2 AND kind=$3`, application(a), identity(producer), kind, publish, restrictions).Scan(&yes)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !yes {
		return ErrDenied
	}
	return e
}
func topic(ctx context.Context, tx pgx.Tx, producer w.IdentityRef, kind string) error {
	_, e := tx.Exec(ctx, `INSERT INTO event_topics VALUES($1,$2) ON CONFLICT DO NOTHING`, identity(producer), kind)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `SELECT producer FROM event_topics WHERE producer=$1 AND kind=$2 FOR UPDATE`, identity(producer), kind)
	return e
}

func (s *Store) Subscribe(ctx context.Context, actor w.ApplicationRef, v w.EventSubscription) ([]byte, error) {
	if !validApplication(actor) || !validIdentity(v.Producer) || !namePattern.MatchString(v.SubscriptionId) || !namePattern.MatchString(v.EventType) {
		return nil, ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = s.guard(ctx, tx); e != nil {
		return nil, e
	}
	if e = topic(ctx, tx, v.Producer, v.EventType); e != nil {
		return nil, e
	}
	if e = allowed(ctx, tx, actor, v.Producer, v.EventType, false, [][]byte{}); e != nil {
		return nil, e
	}
	raw := w.EncodeEventSubscription(v)
	var count int
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT count(*),coalesce(bool_or(consumer=$3 AND id=$4),false) FROM event_subscriptions WHERE producer=$1 AND kind=$2`, identity(v.Producer), v.EventType, application(actor), v.SubscriptionId).Scan(&count, &exists); e != nil {
		return nil, e
	}
	if count >= 256 && !exists {
		return nil, ErrInvalid
	}
	receipt := w.EncodeSubscriptionReceipt(w.SubscriptionReceipt{Subscription: v, Consumer: actor, AcceptedAt: now()})
	result, e := tx.Exec(ctx, `INSERT INTO event_subscriptions VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, application(actor), v.SubscriptionId, identity(v.Producer), v.EventType, raw, receipt)
	if e != nil {
		return nil, e
	}
	var saved []byte
	if e = tx.QueryRow(ctx, `SELECT body,receipt FROM event_subscriptions WHERE consumer=$1 AND id=$2`, application(actor), v.SubscriptionId).Scan(&saved, &receipt); e != nil {
		return nil, e
	}
	if !bytes.Equal(raw, saved) {
		return nil, ErrConflict
	}
	if result.RowsAffected() == 1 {
		if e = s.reserve(ctx, tx, 0, 0, 1, 0, nil); e != nil {
			return nil, e
		}
	}
	return receipt, tx.Commit(ctx)
}

func (s *Store) Publish(ctx context.Context, actor w.ApplicationRef, raw []byte) ([]byte, error) {
	return s.publish(ctx, actor, raw, []byte{})
}

func (s *Store) publish(ctx context.Context, actor w.ApplicationRef, raw, proof []byte) ([]byte, error) {
	if len(proof) > p.MaxBlob {
		return nil, ErrInvalid
	}
	v, e := event(raw)
	if e != nil {
		return nil, e
	}
	if actor.Identity != v.Producer || !validApplication(actor) {
		return nil, ErrDenied
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = s.guard(ctx, tx); e != nil {
		return nil, e
	}
	if e = topic(ctx, tx, v.Producer, v.EventType); e != nil {
		return nil, e
	}
	if e = allowed(ctx, tx, actor, v.Producer, v.EventType, true, restrictionKeys(v.Restrictions)); e != nil {
		return nil, e
	}
	receipt := w.EncodePublicationReceipt(w.PublicationReceipt{Event: v.Ref, Publisher: actor, Receiver: s.Application, Digest: digest(raw), AcceptedAt: now(), ReceiptId: token()})
	result, e := tx.Exec(ctx, `INSERT INTO event_publications(producer,id,publisher,body,digest,receipt,proof) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, identity(v.Producer), v.Ref.RecordId, application(actor), raw, digest(raw), receipt, proof)
	if e != nil {
		return nil, e
	}
	if result.RowsAffected() == 0 {
		var saved []byte
		var owner string
		if e = tx.QueryRow(ctx, `SELECT publisher,body,receipt FROM event_publications WHERE producer=$1 AND id=$2`, identity(v.Producer), v.Ref.RecordId).Scan(&owner, &saved, &receipt); e != nil {
			return nil, e
		}
		if owner != application(actor) || !bytes.Equal(saved, raw) {
			return nil, ErrConflict
		}
		return receipt, tx.Commit(ctx)
	}
	rows, e := tx.Query(ctx, `SELECT consumer,id,receipt FROM event_subscriptions WHERE producer=$1 AND kind=$2 ORDER BY consumer,id LIMIT 257`, identity(v.Producer), v.EventType)
	if e != nil {
		return nil, e
	}
	type sub struct {
		owner, id string
		receipt   []byte
	}
	var subscriptions []sub
	for rows.Next() {
		var a sub
		if e = rows.Scan(&a.owner, &a.id, &a.receipt); e != nil {
			rows.Close()
			return nil, e
		}
		subscriptions = append(subscriptions, a)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(subscriptions) > 256 {
		return nil, ErrInvalid
	}
	var deliveryCount, payloadBytes int64
	pending := map[string]int64{}
	for _, a := range subscriptions {
		sub, e := p.Decode(a.receipt, w.DecodeSubscriptionReceipt, w.EncodeSubscriptionReceipt)
		if e != nil {
			return nil, e
		}
		if !slices.Contains(v.Recipients, sub.Consumer.Identity) {
			continue
		}
		// Keep the obligation even if permission is later removed. Pull checks
		// current policy before it releases any payload.
		d := w.Delivery{DeliveryId: token(), Event: bytes.Clone(raw), Recipient: sub.Consumer.Identity, SubscriptionId: a.id}
		deliveryCount++
		payloadBytes += int64(len(w.EncodeDelivery(d)))
		pending[a.owner]++
		_, e = tx.Exec(ctx, `INSERT INTO event_deliveries(id,consumer,subscription,producer,event_id,kind,restrictions,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, d.DeliveryId, a.owner, a.id, identity(v.Producer), v.Ref.RecordId, v.EventType, restrictionKeys(v.Restrictions), w.EncodeDelivery(d))
		if e != nil {
			return nil, e
		}
	}
	if e = s.reserve(ctx, tx, 1, deliveryCount, 0, int64(len(raw))+payloadBytes, pending); e != nil {
		return nil, e
	}
	return receipt, tx.Commit(ctx)
}

func (s *Store) Pull(ctx context.Context, actor w.ApplicationRef, v w.PullEvents) ([]byte, error) {
	if v.Limit < 1 || v.Limit > 8 || !namePattern.MatchString(v.SubscriptionId) || !validApplication(actor) {
		return nil, ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = s.guard(ctx, tx); e != nil {
		return nil, e
	}
	var raw []byte
	if e = tx.QueryRow(ctx, `SELECT body FROM event_subscriptions WHERE consumer=$1 AND id=$2`, application(actor), v.SubscriptionId).Scan(&raw); errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if e != nil {
		return nil, e
	}
	sub, e := w.DecodeEventSubscription(raw)
	if e != nil {
		return nil, e
	}
	if e = allowed(ctx, tx, actor, sub.Producer, sub.EventType, false, [][]byte{}); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT d.body FROM event_deliveries d JOIN event_grants g ON g.application=d.consumer AND g.producer=d.producer AND g.kind=d.kind WHERE d.consumer=$1 AND d.subscription=$2 AND d.receipt IS NULL AND g.receive AND g.restrictions @> d.restrictions ORDER BY d.created_at,d.id LIMIT $3`, application(actor), v.SubscriptionId, int(v.Limit))
	if e != nil {
		return nil, e
	}
	batch := w.DeliveryBatch{Deliveries: []w.Delivery{}}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return nil, e
		}
		d, e := p.Decode(b, w.DecodeDelivery, w.EncodeDelivery)
		if e != nil {
			rows.Close()
			return nil, e
		}
		batch.Deliveries = append(batch.Deliveries, d)
		if len(w.EncodeDeliveryBatch(batch)) > p.MaxBlob-1024 {
			batch.Deliveries = batch.Deliveries[:len(batch.Deliveries)-1]
			break
		}
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return w.EncodeDeliveryBatch(batch), tx.Commit(ctx)
}

func (s *Store) Acknowledge(ctx context.Context, actor w.ApplicationRef, v w.AcknowledgeDelivery) ([]byte, error) {
	return s.acknowledge(ctx, actor, v, []byte{})
}
func (s *Store) acknowledge(ctx context.Context, actor w.ApplicationRef, v w.AcknowledgeDelivery, proof []byte) ([]byte, error) {
	if len(proof) > p.MaxBlob || len(w.EncodeDeliveryReceipt(v.Receipt)) > p.MaxText {
		return nil, ErrInvalid
	}
	if !validApplication(actor) || v.Receipt.Recipient != actor.Identity || v.Receipt.Receipt.Authority != actor.Identity || v.Receipt.Receipt.Kind != "delivery-receipt" || !namePattern.MatchString(v.Receipt.Receipt.RecordId) || !namePattern.MatchString(v.SubscriptionId) || !namePattern.MatchString(v.Receipt.DeliveryId) {
		return nil, ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = s.guard(ctx, tx); e != nil {
		return nil, e
	}
	var raw, receipt []byte
	e = tx.QueryRow(ctx, `SELECT body,receipt FROM event_deliveries WHERE id=$1 AND consumer=$2 AND subscription=$3 FOR UPDATE`, v.Receipt.DeliveryId, application(actor), v.SubscriptionId).Scan(&raw, &receipt)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	d, e := w.DecodeDelivery(raw)
	if e != nil {
		return nil, e
	}
	ev, e := event(d.Event)
	if e != nil {
		return nil, e
	}
	if v.Receipt.Event != ev.Ref || !bytes.Equal(v.Receipt.Digest, digest(d.Event)) {
		return nil, ErrConflict
	}
	incoming := w.EncodeDeliveryReceipt(v.Receipt)
	if receipt != nil {
		if !bytes.Equal(receipt, incoming) {
			return nil, ErrConflict
		}
		return receipt, tx.Commit(ctx)
	}
	// Acknowledgement releases no event content and remains valid after a read
	// grant is withdrawn. The authenticated subscription owner must still match.
	_, e = tx.Exec(ctx, `UPDATE event_deliveries SET receipt=$2,acknowledgement_proof=$3 WHERE id=$1`, d.DeliveryId, incoming, proof)
	if e != nil {
		return nil, e
	}
	return incoming, tx.Commit(ctx)
}

func (s *Store) ReadPublication(ctx context.Context, actor w.ApplicationRef, v w.ReadPublication) ([]byte, error) {
	if v.Event.Authority != actor.Identity || v.Event.Kind != "event" || !namePattern.MatchString(v.Event.RecordId) {
		return nil, ErrDenied
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = s.guard(ctx, tx); e != nil {
		return nil, e
	}
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT receipt FROM event_publications WHERE producer=$1 AND id=$2 AND publisher=$3`, identity(actor.Identity), v.Event.RecordId, application(actor)).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	return raw, tx.Commit(ctx)
}
