// Package store keeps conversation records in PostgreSQL.
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// Channel is the PostgreSQL NOTIFY channel. The payload is a conversation id.
const Channel = "magi_conversation_utterance"

// ErrNotFound means no record matches.
var ErrNotFound = errors.New("not found")

type DB struct{ pool *pgxpool.Pool }

// Open connects to url and applies the schema.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	db := &DB{pool: pool}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// The lock keeps replicas that start together from racing on DDL.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7311002)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

func (db *DB) Close()                         { db.pool.Close() }
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

func one[T any](v T, err error) (T, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

func many[T any](rows pgx.Rows, err error, scan func(pgx.Row) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func refsJSON(a []api.Attachment) []byte {
	if a == nil {
		a = []api.Attachment{}
	}
	b, _ := json.Marshal(a)
	return b
}

func parseRefs(b []byte) ([]api.Attachment, error) {
	out := []api.Attachment{}
	return out, json.Unmarshal(b, &out)
}

// Conversations

const convCols = "id, title, owner, participants, created_ms, updated_ms, last_seq"

func scanConv(r pgx.Row) (api.Conversation, error) {
	var c api.Conversation
	var created, updated, seq int64
	err := r.Scan(&c.Id, &c.Title, &c.Owner, &c.Participants, &created, &updated, &seq)
	c.CreatedMs, c.UpdatedMs, c.LastSeq = uint64(created), uint64(updated), uint64(seq)
	if c.Participants == nil {
		c.Participants = []string{}
	}
	return c, err
}

func (db *DB) CreateConversation(ctx context.Context, c api.Conversation) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO conversations (`+convCols+`) VALUES ($1, $2, $3, $4, $5, $6, 0)`,
		c.Id, c.Title, c.Owner, c.Participants, int64(c.CreatedMs), int64(c.UpdatedMs))
	return err
}

func (db *DB) GetConversation(ctx context.Context, id string) (api.Conversation, error) {
	return one(scanConv(db.pool.QueryRow(ctx, `SELECT `+convCols+` FROM conversations WHERE id = $1`, id)))
}

// ListConversations returns conversations that subject owns or takes part
// in, most recently updated first.
func (db *DB) ListConversations(ctx context.Context, subject string, beforeMs uint64, limit int) ([]api.Conversation, error) {
	if beforeMs == 0 {
		beforeMs = 1 << 62
	}
	rows, err := db.pool.Query(ctx, `SELECT `+convCols+` FROM conversations
WHERE (owner = $1 OR participants @> ARRAY[$1]) AND updated_ms < $2
ORDER BY updated_ms DESC, id LIMIT $3`, subject, int64(beforeMs), limit)
	return many(rows, err, scanConv)
}

// Utterances

const uttCols = "id, conversation, seq, author, author_kind, modality, text, audio, transcript_confidence, reply_to, sessions, attachments, generated_by, created_ms"

func scanUtt(r pgx.Row) (api.Utterance, error) {
	var u api.Utterance
	var seq, created int64
	var att []byte
	if err := r.Scan(&u.Id, &u.Conversation, &seq, &u.Author, &u.AuthorKind, &u.Modality, &u.Text,
		&u.Audio, &u.TranscriptConfidence, &u.ReplyTo, &u.Sessions, &att, &u.GeneratedBy, &created); err != nil {
		return u, err
	}
	u.Seq, u.CreatedMs = uint64(seq), uint64(created)
	if u.Sessions == nil {
		u.Sessions = []string{}
	}
	var err error
	u.Attachments, err = parseRefs(att)
	return u, err
}

// Post assigns the next seq of the conversation to u and stores it. The
// conversation row lock serializes concurrent posts. Other replicas learn
// of the post through NOTIFY on Channel.
func (db *DB) Post(ctx context.Context, u api.Utterance) (api.Utterance, error) {
	if u.Sessions == nil {
		u.Sessions = []string{}
	}
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		var seq int64
		err := tx.QueryRow(ctx, `UPDATE conversations SET last_seq = last_seq + 1, updated_ms = GREATEST(updated_ms, $2)
WHERE id = $1 RETURNING last_seq`, u.Conversation, int64(u.CreatedMs)).Scan(&seq)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		u.Seq = uint64(seq)
		if _, err := tx.Exec(ctx, `INSERT INTO utterances (`+uttCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			u.Id, u.Conversation, seq, u.Author, u.AuthorKind, u.Modality, u.Text, u.Audio, u.TranscriptConfidence,
			u.ReplyTo, u.Sessions, refsJSON(u.Attachments), u.GeneratedBy, int64(u.CreatedMs)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, Channel, u.Conversation)
		return err
	})
	if u.Attachments == nil {
		u.Attachments = []api.Attachment{}
	}
	return u, err
}

// ListUtterances returns utterances with seq greater than after, oldest first.
func (db *DB) ListUtterances(ctx context.Context, conv string, after uint64, limit int) ([]api.Utterance, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+uttCols+` FROM utterances WHERE conversation = $1 AND seq > $2 ORDER BY seq LIMIT $3`,
		conv, int64(after), limit)
	return many(rows, err, scanUtt)
}

func (db *DB) GetUtterance(ctx context.Context, id string) (api.Utterance, error) {
	return one(scanUtt(db.pool.QueryRow(ctx, `SELECT `+uttCols+` FROM utterances WHERE id = $1`, id)))
}

// Sessions

const sesCols = "id, conversation, title, status, refs, created_ms, updated_ms"

func scanSes(r pgx.Row) (api.Session, error) {
	var s api.Session
	var created, updated int64
	var refs []byte
	if err := r.Scan(&s.Id, &s.Conversation, &s.Title, &s.Status, &refs, &created, &updated); err != nil {
		return s, err
	}
	s.CreatedMs, s.UpdatedMs = uint64(created), uint64(updated)
	var err error
	s.Refs, err = parseRefs(refs)
	return s, err
}

func (db *DB) OpenSession(ctx context.Context, s api.Session) (api.Session, error) {
	return scanSes(db.pool.QueryRow(ctx, `INSERT INTO sessions (`+sesCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+sesCols,
		s.Id, s.Conversation, s.Title, s.Status, refsJSON(s.Refs), int64(s.CreatedMs), int64(s.UpdatedMs)))
}

func (db *DB) GetSession(ctx context.Context, id string) (api.Session, error) {
	return one(scanSes(db.pool.QueryRow(ctx, `SELECT `+sesCols+` FROM sessions WHERE id = $1`, id)))
}

// UpdateSession sets title and status when they are not nil and appends refs.
func (db *DB) UpdateSession(ctx context.Context, id string, title, status *string, addRefs []api.Attachment, now time.Time) (api.Session, error) {
	return one(scanSes(db.pool.QueryRow(ctx, `UPDATE sessions SET
title = COALESCE($2, title), status = COALESCE($3, status), refs = refs || $4::jsonb, updated_ms = $5
WHERE id = $1 RETURNING `+sesCols, id, title, status, refsJSON(addRefs), now.UnixMilli())))
}

func (db *DB) ListSessions(ctx context.Context, conv string) ([]api.Session, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+sesCols+` FROM sessions WHERE conversation = $1 ORDER BY created_ms, id`, conv)
	return many(rows, err, scanSes)
}

// Annotations

const annCols = "id, utterance, kind, author, body, created_ms"

func scanAnn(r pgx.Row) (api.Annotation, error) {
	var a api.Annotation
	var created int64
	err := r.Scan(&a.Id, &a.Utterance, &a.Kind, &a.Author, &a.Body, &created)
	a.CreatedMs = uint64(created)
	return a, err
}

func (db *DB) Annotate(ctx context.Context, a api.Annotation) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO annotations (`+annCols+`) VALUES ($1, $2, $3, $4, $5, $6)`,
		a.Id, a.Utterance, a.Kind, a.Author, a.Body, int64(a.CreatedMs))
	return err
}

func (db *DB) ListAnnotations(ctx context.Context, utterance string) ([]api.Annotation, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+annCols+` FROM annotations WHERE utterance = $1 ORDER BY created_ms, id`, utterance)
	return many(rows, err, scanAnn)
}

// Listen calls fn with the conversation id of each post on any replica. It
// reconnects after errors and returns when ctx ends.
func (db *DB) Listen(ctx context.Context, fn func(conv string), onError func(error)) {
	for ctx.Err() == nil {
		err := db.listen(ctx, fn)
		if ctx.Err() != nil {
			return
		}
		if onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func (db *DB) listen(ctx context.Context, fn func(string)) error {
	pc, err := db.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// The connection stays in LISTEN state, so take it out of the pool.
	conn := pc.Hijack()
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		fn(n.Payload)
	}
}
