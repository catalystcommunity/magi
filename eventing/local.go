package eventing

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	w "github.com/catalystcommunity/magi/generated/events/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed local.sql
var LocalMigration string

// Local belongs to a producer or consumer database, not the event service.
// Tx methods join the caller's business transaction. They do not commit it.
type Local struct {
	Pool        *pgxpool.Pool
	Application w.ApplicationRef
}

func (l *Local) Initialize(ctx context.Context) error {
	if !validApplication(l.Application) {
		return ErrInvalid
	}
	tx, e := l.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(427361002)`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, LocalMigration); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (l *Local) EnqueueTx(ctx context.Context, tx pgx.Tx, target w.ApplicationRef, raw []byte) error {
	v, e := event(raw)
	if e != nil {
		return e
	}
	if v.Producer != l.Application.Identity || !validApplication(target) {
		return ErrDenied
	}
	_, e = tx.Exec(ctx, `INSERT INTO event_local_outbox(owner,destination,id,target,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, application(l.Application), application(target), v.Ref.RecordId, w.EncodeApplicationRef(target), raw)
	if e != nil {
		return e
	}
	var saved []byte
	if e = tx.QueryRow(ctx, `SELECT body FROM event_local_outbox WHERE owner=$1 AND destination=$2 AND id=$3`, application(l.Application), application(target), v.Ref.RecordId).Scan(&saved); e != nil {
		return e
	}
	if !bytes.Equal(saved, raw) {
		return ErrConflict
	}
	return nil
}

type Claim struct {
	Target   w.ApplicationRef
	ID       string
	Body     []byte
	Attempt  string
	Attempts int
}

func (l *Local) Claim(ctx context.Context, lease time.Duration) (*Claim, error) {
	if lease < time.Second || lease > 5*time.Minute {
		return nil, ErrInvalid
	}
	// Recovery uses the database clock. An expired publisher can never record
	// success for a claim that another publisher now owns.
	var target []byte
	c := &Claim{Attempt: token()}
	e := l.Pool.QueryRow(ctx, `WITH exhausted AS (UPDATE event_local_outbox SET state='attention',last_error='lease-exhausted' WHERE owner=$1 AND state='attempting' AND attempts>=20 AND due<=clock_timestamp() RETURNING id), candidate AS (SELECT owner,destination,id FROM event_local_outbox WHERE owner=$1 AND (state IN ('pending','retry_due') OR state='attempting' AND attempts<20) AND due<=clock_timestamp() ORDER BY due,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE event_local_outbox o SET state='attempting',attempt=$2,attempts=o.attempts+1,due=clock_timestamp()+$3::interval FROM candidate c WHERE (o.owner,o.destination,o.id)=(c.owner,c.destination,c.id) RETURNING o.target,o.id,o.body,o.attempts`, application(l.Application), c.Attempt, fmt.Sprintf("%d milliseconds", lease.Milliseconds())).Scan(&target, &c.ID, &c.Body, &c.Attempts)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	c.Target, e = p.Decode(target, w.DecodeApplicationRef, w.EncodeApplicationRef)
	return c, e
}
func (l *Local) Complete(ctx context.Context, c Claim, receipt w.PublicationReceipt) error {
	ev, e := event(c.Body)
	if e != nil {
		return e
	}
	if receipt.Publisher != l.Application || receipt.Receiver != c.Target || receipt.Event != ev.Ref || !bytes.Equal(receipt.Digest, digest(c.Body)) || !namePattern.MatchString(receipt.ReceiptId) {
		return ErrConflict
	}
	result, e := l.Pool.Exec(ctx, `UPDATE event_local_outbox SET state='accepted',receipt=$5,last_error=NULL WHERE owner=$1 AND destination=$2 AND id=$3 AND attempt=$4 AND state='attempting' AND due>clock_timestamp() AND body=$6`, application(l.Application), application(c.Target), c.ID, c.Attempt, w.EncodePublicationReceipt(receipt), c.Body)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return ErrFence
	}
	return nil
}
func (l *Local) Retry(ctx context.Context, c Claim, delay time.Duration, code string, attention bool) error {
	if delay < 0 || delay > 24*time.Hour || !namePattern.MatchString(code) {
		return ErrInvalid
	}
	state := "retry_due"
	if attention || c.Attempts >= 20 {
		state = "attention"
	}
	result, e := l.Pool.Exec(ctx, `UPDATE event_local_outbox SET state=$5,due=clock_timestamp()+$6::interval,last_error=$7 WHERE owner=$1 AND destination=$2 AND id=$3 AND attempt=$4 AND state='attempting' AND due>clock_timestamp()`, application(l.Application), application(c.Target), c.ID, c.Attempt, state, fmt.Sprintf("%d milliseconds", delay.Milliseconds()), code)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return ErrFence
	}
	return nil
}

// Resume requires a local operator decision. It does not discard the attempt count.
func (l *Local) Resume(ctx context.Context, target w.ApplicationRef, id string) error {
	result, e := l.Pool.Exec(ctx, `UPDATE event_local_outbox SET state='retry_due',due=clock_timestamp() WHERE owner=$1 AND destination=$2 AND id=$3 AND state='attention'`, application(l.Application), application(target), id)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// PublishOne is safe to retry because publication has an identity-bound key.
// A network timeout here is not permission to repeat a business effect.
func (l *Local) PublishOne(ctx context.Context, publish func(context.Context, w.ApplicationRef, []byte) (w.PublicationReceipt, error)) (bool, error) {
	c, e := l.Claim(ctx, 30*time.Second)
	if e != nil || c == nil {
		return false, e
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	r, e := publish(callCtx, c.Target, c.Body)
	cancel()
	if e == nil {
		e = l.Complete(ctx, *c, r)
		return true, e
	}
	delay := time.Second * time.Duration(1<<min(c.Attempts, 10))
	attention := errors.Is(e, ErrConflict) || errors.Is(e, ErrInvalid) || errors.Is(e, ErrDenied)
	var remote *p.RemoteError
	if errors.As(e, &remote) && !remote.Detail.Retryable {
		attention = true
	}
	if retryErr := l.Retry(ctx, *c, delay, "publication-failed", attention); retryErr != nil {
		return true, retryErr
	}
	return true, e
}

func (l *Local) AcceptTx(ctx context.Context, tx pgx.Tx, sender w.ApplicationRef, d w.Delivery) (w.DeliveryReceipt, error) {
	var empty w.DeliveryReceipt
	ev, e := event(d.Event)
	if e != nil {
		return empty, e
	}
	if !validApplication(sender) || d.Recipient != l.Application.Identity || !namePattern.MatchString(d.DeliveryId) || !namePattern.MatchString(d.SubscriptionId) {
		return empty, ErrDenied
	}
	if !containsIdentity(ev.Recipients, l.Application.Identity) {
		return empty, ErrDenied
	}
	raw := w.EncodeDelivery(d)
	receipt := w.EncodeDeliveryReceipt(w.DeliveryReceipt{DeliveryId: d.DeliveryId, Event: ev.Ref, Recipient: l.Application.Identity, Digest: digest(d.Event), AcceptedAt: now(), Receipt: w.RecordRef{Authority: l.Application.Identity, Kind: "delivery-receipt", RecordId: token()}})
	_, e = tx.Exec(ctx, `INSERT INTO event_local_inbox(owner,sender,id,body,receipt) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, application(l.Application), application(sender), d.DeliveryId, raw, receipt)
	if e != nil {
		return empty, e
	}
	var saved []byte
	e = tx.QueryRow(ctx, `SELECT body,receipt FROM event_local_inbox WHERE owner=$1 AND sender=$2 AND id=$3`, application(l.Application), application(sender), d.DeliveryId).Scan(&saved, &receipt)
	if e != nil {
		return empty, e
	}
	if !bytes.Equal(saved, raw) {
		return empty, ErrConflict
	}
	return p.Decode(receipt, w.DecodeDeliveryReceipt, w.EncodeDeliveryReceipt)
}
func containsIdentity(ids []w.IdentityRef, i w.IdentityRef) bool {
	for _, v := range ids {
		if v == i {
			return true
		}
	}
	return false
}

// Accept returns only after durable commit. Authenticate the source and check
// consumer admission policy before calling it. It is not a public HTTP handler.
func (l *Local) Accept(ctx context.Context, sender w.ApplicationRef, d w.Delivery) (w.DeliveryReceipt, error) {
	tx, e := l.Pool.Begin(ctx)
	if e != nil {
		return w.DeliveryReceipt{}, e
	}
	defer tx.Rollback(ctx)
	r, e := l.AcceptTx(ctx, tx, sender, d)
	if e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}

// ApplyTx may perform ONLY work in tx. External effects must use the explicit
// unknown-outcome path below. A duplicate cannot run the callback twice.
func (l *Local) ApplyTx(ctx context.Context, tx pgx.Tx, sender w.ApplicationRef, id string, work func(pgx.Tx, w.Event) error) (bool, error) {
	var raw []byte
	var state string
	e := tx.QueryRow(ctx, `SELECT body,state FROM event_local_inbox WHERE owner=$1 AND sender=$2 AND id=$3 FOR UPDATE`, application(l.Application), application(sender), id).Scan(&raw, &state)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if e != nil {
		return false, e
	}
	if state == "completed" {
		return false, nil
	}
	if state != "accepted" {
		return false, ErrConflict
	}
	d, e := w.DecodeDelivery(raw)
	if e != nil {
		return false, e
	}
	ev, e := event(d.Event)
	if e != nil {
		return false, e
	}
	if e = work(tx, ev); e != nil {
		return false, e
	}
	_, e = tx.Exec(ctx, `UPDATE event_local_inbox SET state='completed' WHERE owner=$1 AND sender=$2 AND id=$3`, application(l.Application), application(sender), id)
	return true, e
}

// BeginExternal commits before the caller invokes a remote effect. A crash at
// any later point leaves outcome_unknown. Do not retry without reconciliation.
func (l *Local) BeginExternal(ctx context.Context, sender w.ApplicationRef, id string) error {
	result, e := l.Pool.Exec(ctx, `UPDATE event_local_inbox SET state='outcome_unknown' WHERE owner=$1 AND sender=$2 AND id=$3 AND state='accepted'`, application(l.Application), application(sender), id)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// ResolveExternal records the result of a trusted local reconciliation step.
// notPerformed permits another attempt only after evidence proves no effect.
func (l *Local) ResolveExternal(ctx context.Context, sender w.ApplicationRef, id, evidence string, notPerformed bool) error {
	if !namePattern.MatchString(evidence) {
		return ErrInvalid
	}
	state := "completed"
	if notPerformed {
		state = "accepted"
	}
	result, e := l.Pool.Exec(ctx, `UPDATE event_local_inbox SET state=$4,reconciliation=$5 WHERE owner=$1 AND sender=$2 AND id=$3 AND state='outcome_unknown'`, application(l.Application), application(sender), id, state, evidence)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
