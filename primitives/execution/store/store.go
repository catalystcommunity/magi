// Package store keeps environment records in PostgreSQL.
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// ErrNotFound means no record matches.
var ErrNotFound = errors.New("not found")

// Record is an environment and the values that only the service sees.
type Record struct {
	Env      api.Environment
	Backend  string
	Endpoint string
	Token    string
}

type DB struct{ pool *pgxpool.Pool }

// Open connects to url and applies the schema.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7311005)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (db *DB) Close()                         { db.pool.Close() }
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

const cols = "id, owner, backend, spec, status, message, held, created_ms, expires_ms, endpoint, token"

func scan(row pgx.Row) (Record, error) {
	var r Record
	var spec []byte
	var msg string
	var created, expires int64
	err := row.Scan(&r.Env.Id, &r.Env.Owner, &r.Backend, &spec, &r.Env.Status, &msg, &r.Env.Held, &created, &expires, &r.Endpoint, &r.Token)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if msg != "" {
		r.Env.Message = &msg
	}
	r.Env.CreatedMs, r.Env.ExpiresMs = uint64(created), uint64(expires)
	return r, json.Unmarshal(spec, &r.Env.Spec)
}

func (db *DB) Insert(ctx context.Context, r Record) error {
	spec, err := json.Marshal(r.Env.Spec)
	if err != nil {
		return err
	}
	_, err = db.pool.Exec(ctx, "INSERT INTO environments ("+cols+") VALUES ($1,$2,$3,$4,$5,'',$6,$7,$8,$9,$10)",
		r.Env.Id, r.Env.Owner, r.Backend, spec, r.Env.Status, r.Env.Held, int64(r.Env.CreatedMs), int64(r.Env.ExpiresMs), r.Endpoint, r.Token)
	return err
}

func (db *DB) Get(ctx context.Context, id string) (Record, error) {
	return scan(db.pool.QueryRow(ctx, "SELECT "+cols+" FROM environments WHERE id = $1", id))
}

func (db *DB) query(ctx context.Context, sql string, args ...any) ([]Record, error) {
	rows, err := db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// List returns the newest records. An empty owner matches every owner. An
// empty status matches every status except "destroyed".
func (db *DB) List(ctx context.Context, owner, status string, limit int) ([]Record, error) {
	return db.query(ctx, "SELECT "+cols+` FROM environments
		WHERE ($1 = '' OR owner = $1) AND (($2 = '' AND status <> 'destroyed') OR status = $2)
		ORDER BY created_ms DESC, id LIMIT $3`, owner, status, limit)
}

// SetState records a backend state. It does not change a destroyed record.
func (db *DB) SetState(ctx context.Context, id, status, message, endpoint string, now uint64) (Record, error) {
	destroyed := int64(0)
	if status == "destroyed" {
		destroyed = int64(now)
	}
	return scan(db.pool.QueryRow(ctx, `UPDATE environments SET status = $2, message = $3, endpoint = $4,
		destroyed_ms = $5 WHERE id = $1 AND status <> 'destroyed' RETURNING `+cols, id, status, message, endpoint, destroyed))
}

func (db *DB) SetHeld(ctx context.Context, id string, held bool) (Record, error) {
	return scan(db.pool.QueryRow(ctx, "UPDATE environments SET held = $2 WHERE id = $1 RETURNING "+cols, id, held))
}

func (db *DB) SetExpiry(ctx context.Context, id string, expiresMs uint64) (Record, error) {
	return scan(db.pool.QueryRow(ctx, "UPDATE environments SET expires_ms = $2 WHERE id = $1 RETURNING "+cols, id, int64(expiresMs)))
}

// Expired returns live, unheld records whose TTL ended before now.
func (db *DB) Expired(ctx context.Context, now uint64) ([]Record, error) {
	return db.query(ctx, "SELECT "+cols+" FROM environments WHERE status <> 'destroyed' AND NOT held AND expires_ms < $1", int64(now))
}

// Creating returns records that wait for their backend.
func (db *DB) Creating(ctx context.Context) ([]Record, error) {
	return db.query(ctx, "SELECT "+cols+" FROM environments WHERE status = 'creating'")
}
