package conversation

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

//go:embed schema.sql
var migration string

const policyLock int64 = 427361040

type Store struct {
	Pool        *pgxpool.Pool
	Application w.ApplicationRef
}
type Grant struct {
	Actor   w.ApplicationRef `json:"actor"`
	Owner   w.ScopeRef       `json:"owner"`
	Action  string           `json:"action"`
	Allowed bool             `json:"allowed"`
}

func (s *Store) Initialize(ctx context.Context) error {
	if !validActor(s.Application) {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policyLock); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, migration); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO conversation_service(application,version) VALUES($1,1) ON CONFLICT DO NOTHING`, w.EncodeApplicationRef(s.Application)); e != nil {
		return e
	}
	var a []byte
	var version int
	if e = tx.QueryRow(ctx, `SELECT application,version FROM conversation_service`).Scan(&a, &version); e != nil {
		return e
	}
	if version != 1 || !bytes.Equal(a, w.EncodeApplicationRef(s.Application)) {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
func (s *Store) Check(ctx context.Context) error {
	var a []byte
	var version int
	if e := s.Pool.QueryRow(ctx, `SELECT application,version FROM conversation_service`).Scan(&a, &version); e != nil {
		return e
	}
	if version != 1 || !bytes.Equal(a, w.EncodeApplicationRef(s.Application)) {
		return ErrConflict
	}
	return nil
}
func (s *Store) SetGrant(ctx context.Context, g Grant) error {
	if !validActor(g.Actor) || !validScope(g.Owner) || (g.Action != "read" && g.Action != "write") {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policyLock); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO conversation_grants VALUES($1,$2,$3,$4) ON CONFLICT(actor,owner,action) DO UPDATE SET allowed=excluded.allowed`, w.EncodeApplicationRef(g.Actor), w.EncodeScopeRef(g.Owner), g.Action, g.Allowed); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) authorize(ctx context.Context, tx pgx.Tx, a w.ApplicationRef, owner w.ScopeRef, action string) error {
	if !validActor(a) || !validScope(owner) {
		return ErrDenied
	}
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, policyLock); e != nil {
		return e
	}
	var allowed bool
	e := tx.QueryRow(ctx, `SELECT allowed FROM conversation_grants WHERE actor=$1 AND owner=$2 AND action=$3`, w.EncodeApplicationRef(a), w.EncodeScopeRef(owner), action).Scan(&allowed)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !allowed {
		return ErrDenied
	}
	return e
}
func (s *Store) Append(ctx context.Context, a w.ApplicationRef, raw, proof []byte) (w.ConversationReceipt, error) {
	var out w.ConversationReceipt
	if len(raw) > MaxEntry || len(proof) > MaxEntry+2048 {
		return out, ErrInvalid
	}
	v, e := p.Decode(raw, w.DecodeConversationAppend, w.EncodeConversationAppend)
	if e != nil {
		return out, ErrInvalid
	}
	h, refs, e := validate(a, v)
	if e != nil {
		return out, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, h.Access.ControlScope, "write"); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policyLock+1); e != nil {
		return out, e
	}
	owner, ref := w.EncodeScopeRef(h.Access.ControlScope), w.EncodeRecordRef(h.Ref)
	var previous, receipt, writer []byte
	e = tx.QueryRow(ctx, `SELECT body,receipt,actor FROM conversation_records WHERE owner=$1 AND ref=$2`, owner, ref).Scan(&previous, &receipt, &writer)
	if e == nil {
		if !bytes.Equal(previous, raw) || !bytes.Equal(writer, w.EncodeApplicationRef(a)) {
			return out, ErrConflict
		}
		out, e = w.DecodeConversationReceipt(receipt)
		return out, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if len(refs) > 0 {
		if e = s.authorize(ctx, tx, a, h.Access.ControlScope, "read"); e != nil {
			return out, e
		}
		for _, r := range refs {
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_records WHERE owner=$1 AND ref=$2)`, owner, w.EncodeRecordRef(r)).Scan(&exists); e != nil {
				return out, e
			}
			if !exists {
				return out, ErrDenied
			}
		}
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM conversation_records`).Scan(&count); e != nil {
		return out, e
	}
	if count >= 10000 {
		return out, ErrCapacity
	}
	var sequence uint64
	if e = tx.QueryRow(ctx, `INSERT INTO conversation_records(owner,ref,actor,body,proof,receipt) VALUES($1,$2,$3,$4,$5,''::bytea) RETURNING sequence`, owner, ref, w.EncodeApplicationRef(a), raw, proof).Scan(&sequence); e != nil {
		return out, e
	}
	hash := sha256.Sum256(raw)
	out = w.ConversationReceipt{Ref: h.Ref, Owner: h.Access.ControlScope, Digest: hash[:], Sequence: sequence, AcceptedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if _, e = tx.Exec(ctx, `UPDATE conversation_records SET receipt=$3 WHERE owner=$1 AND ref=$2`, owner, ref, w.EncodeConversationReceipt(out)); e != nil {
		return out, e
	}
	for _, session := range v.Sessions {
		if _, e = tx.Exec(ctx, `INSERT INTO conversation_sessions VALUES($1,$2,$3,$4)`, owner, session, ref, sequence); e != nil {
			return out, e
		}
	}
	for _, r := range refs {
		if _, e = tx.Exec(ctx, `INSERT INTO conversation_edges VALUES($1,$2,$3)`, owner, ref, w.EncodeRecordRef(r)); e != nil {
			return out, e
		}
	}
	return out, tx.Commit(ctx)
}
func decodeStored(body, receipt, proof []byte) (w.ConversationStored, error) {
	var out w.ConversationStored
	var e error
	if out.Entry, e = w.DecodeConversationAppend(body); e != nil {
		return out, e
	}
	if out.Receipt, e = w.DecodeConversationReceipt(receipt); e != nil {
		return out, e
	}
	out.Admission, e = w.DecodeSignedRecord(proof)
	return out, e
}
func (s *Store) Read(ctx context.Context, a w.ApplicationRef, v w.ConversationRead) (w.ConversationStored, error) {
	var out w.ConversationStored
	if !validRef(v.Ref) {
		return out, ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, v.Owner, "read"); e != nil {
		return out, e
	}
	var body, receipt, proof []byte
	e = tx.QueryRow(ctx, `SELECT body,receipt,proof FROM conversation_records WHERE owner=$1 AND ref=$2`, w.EncodeScopeRef(v.Owner), w.EncodeRecordRef(v.Ref)).Scan(&body, &receipt, &proof)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if e != nil {
		return out, e
	}
	return decodeStored(body, receipt, proof)
}
func (s *Store) History(ctx context.Context, a w.ApplicationRef, v w.ConversationHistory) (w.ConversationPage, error) {
	out := w.ConversationPage{Records: []w.ConversationStored{}, NextSequence: v.AfterSequence}
	if !name.MatchString(v.SessionId) || v.Limit < 1 || v.Limit > 4 || v.AfterSequence > p.MaxInteger {
		return out, ErrInvalid
	}
	query := `SELECT r.body,r.receipt,r.proof FROM conversation_sessions s JOIN conversation_records r ON r.sequence=s.sequence WHERE s.owner=$1 AND s.session_id=$2 AND s.sequence>$3 ORDER BY s.sequence LIMIT $4`
	boundary := v.AfterSequence
	if v.BeforeSequence != nil {
		if v.AfterSequence != 0 || *v.BeforeSequence == 0 || *v.BeforeSequence > p.MaxInteger {
			return out, ErrInvalid
		}
		boundary = *v.BeforeSequence
		out.NextSequence = boundary
		query = `SELECT r.body,r.receipt,r.proof FROM conversation_sessions s JOIN conversation_records r ON r.sequence=s.sequence WHERE s.owner=$1 AND s.session_id=$2 AND s.sequence<$3 ORDER BY s.sequence DESC LIMIT $4`
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, v.Owner, "read"); e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, query, w.EncodeScopeRef(v.Owner), v.SessionId, boundary, v.Limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		if len(out.Records) == int(v.Limit) {
			out.More = true
			break
		}
		var body, receipt, proof []byte
		if e = rows.Scan(&body, &receipt, &proof); e != nil {
			return out, e
		}
		record, err := decodeStored(body, receipt, proof)
		if err != nil {
			return out, err
		}
		out.Records = append(out.Records, record)
		out.NextSequence = record.Receipt.Sequence
	}
	return out, rows.Err()
}
