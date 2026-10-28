// Package store keeps artifact metadata in PostgreSQL and bodies on disk.
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	api "github.com/catalystcommunity/magi/primitives/artifacts/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// ErrNotFound means no record matches.
var ErrNotFound = errors.New("not found")

// DB holds artifact records. One record exists per digest and owner.
type DB struct{ pool *pgxpool.Pool }

// Open connects to url and applies the schema.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	db := &DB{pool: pool}
	if err := db.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

// migrate applies the schema under an advisory lock, so replicas that start
// together do not race on CREATE IF NOT EXISTS.
func (db *DB) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7311001)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
}

func (db *DB) Close()                         { db.pool.Close() }
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

const cols = "digest, owner, media_type, size, labels, created_ms"

func scan(row pgx.Row) (api.Artifact, error) {
	var a api.Artifact
	var labels []byte
	var size, created int64
	err := row.Scan(&a.Digest, &a.Owner, &a.MediaType, &size, &labels, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.Size, a.CreatedMs = uint64(size), uint64(created)
	if err := json.Unmarshal(labels, &a.Labels); err != nil {
		return a, err
	}
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	return a, nil
}

func labelsJSON(l map[string]string) []byte {
	if l == nil {
		l = map[string]string{}
	}
	b, _ := json.Marshal(l)
	return b
}

// Put inserts a record. If the owner already has a record for the digest,
// the new labels are merged into it and the other fields are kept.
func (db *DB) Put(ctx context.Context, a api.Artifact) (api.Artifact, error) {
	return scan(db.pool.QueryRow(ctx, `
INSERT INTO artifacts (`+cols+`) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (digest, owner) DO UPDATE SET labels = artifacts.labels || EXCLUDED.labels
RETURNING `+cols,
		a.Digest, a.Owner, a.MediaType, int64(a.Size), labelsJSON(a.Labels), int64(a.CreatedMs)))
}

// Get returns the record of owner for digest.
func (db *DB) Get(ctx context.Context, digest, owner string) (api.Artifact, error) {
	return scan(db.pool.QueryRow(ctx, `SELECT `+cols+` FROM artifacts WHERE digest = $1 AND owner = $2`, digest, owner))
}

// First returns the oldest record for digest, of any owner.
func (db *DB) First(ctx context.Context, digest string) (api.Artifact, error) {
	return scan(db.pool.QueryRow(ctx, `SELECT `+cols+` FROM artifacts WHERE digest = $1 ORDER BY created_ms, owner LIMIT 1`, digest))
}

// Query selects records for List.
type Query struct {
	Owner      string
	LabelKey   string
	LabelValue *string
	BeforeMs   uint64
	Limit      int
}

// List returns records of q.Owner, newest first.
func (db *DB) List(ctx context.Context, q Query) ([]api.Artifact, error) {
	where := []string{"owner = $1"}
	args := []any{q.Owner}
	if q.LabelKey != "" && q.LabelValue != nil {
		args = append(args, labelsJSON(map[string]string{q.LabelKey: *q.LabelValue}))
		where = append(where, fmt.Sprintf("labels @> $%d", len(args)))
	} else if q.LabelKey != "" {
		args = append(args, q.LabelKey)
		where = append(where, fmt.Sprintf("labels ? $%d", len(args)))
	}
	if q.BeforeMs > 0 {
		args = append(args, int64(q.BeforeMs))
		where = append(where, fmt.Sprintf("created_ms < $%d", len(args)))
	}
	args = append(args, q.Limit)
	rows, err := db.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM artifacts WHERE %s ORDER BY created_ms DESC, digest LIMIT $%d`,
		cols, strings.Join(where, " AND "), len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Artifact{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Label merges labels into the record of owner for digest.
func (db *DB) Label(ctx context.Context, digest, owner string, labels map[string]string) (api.Artifact, error) {
	return scan(db.pool.QueryRow(ctx, `UPDATE artifacts SET labels = labels || $3 WHERE digest = $1 AND owner = $2 RETURNING `+cols,
		digest, owner, labelsJSON(labels)))
}
