// Package store keeps the household lexicon in PostgreSQL.
package store

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	api "github.com/catalystcommunity/magi/primitives/speech/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type DB struct{ pool *pgxpool.Pool }

// Open connects to url and applies the schema.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// The lock keeps replicas that start together from racing on DDL.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7311003)"); err != nil {
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

// List returns all entries, ordered by written form.
func (db *DB) List(ctx context.Context) ([]api.LexiconEntry, error) {
	rows, err := db.pool.Query(ctx, `SELECT written, spoken, ipa, heard_as, note, updated_by, updated_ms FROM lexicon ORDER BY key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (api.LexiconEntry, error) {
		var e api.LexiconEntry
		var ms int64
		err := r.Scan(&e.Written, &e.Spoken, &e.Ipa, &e.HeardAs, &e.Note, &e.UpdatedBy, &ms)
		e.UpdatedMs = uint64(ms)
		return e, err
	})
}

// Put adds or replaces the entry for e.Written (not case-sensitive).
func (db *DB) Put(ctx context.Context, e api.LexiconEntry) error {
	if e.HeardAs == nil {
		e.HeardAs = []string{}
	}
	_, err := db.pool.Exec(ctx, `INSERT INTO lexicon (key, written, spoken, ipa, heard_as, note, updated_by, updated_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (key) DO UPDATE SET written = $2, spoken = $3, ipa = $4, heard_as = $5, note = $6, updated_by = $7, updated_ms = $8`,
		strings.ToLower(e.Written), e.Written, e.Spoken, e.Ipa, e.HeardAs, e.Note, e.UpdatedBy, int64(e.UpdatedMs))
	return err
}

// Delete removes the entry for written. It reports whether one was there.
func (db *DB) Delete(ctx context.Context, written string) (bool, error) {
	t, err := db.pool.Exec(ctx, `DELETE FROM lexicon WHERE key = $1`, strings.ToLower(written))
	return t.RowsAffected() > 0, err
}
