// Package store keeps the model catalog in PostgreSQL. Each model and each
// task default is one JSON document.
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	api "github.com/catalystcommunity/magi/primitives/models/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// ErrNotFound means no record matches.
var ErrNotFound = errors.New("not found")

// DB holds the catalog.
type DB struct{ pool *pgxpool.Pool }

// Open connects to url and applies the schema.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	db := &DB{pool: pool}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7311009)"); err != nil {
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

// PutModel adds or replaces a model.
func (db *DB) PutModel(ctx context.Context, m api.Model) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = db.pool.Exec(ctx, `INSERT INTO models (name, doc, updated_ms) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, updated_ms = EXCLUDED.updated_ms`, m.Name, b, int64(m.UpdatedMs))
	return err
}

// GetModel returns the model with the name.
func (db *DB) GetModel(ctx context.Context, name string) (api.Model, error) {
	var m api.Model
	var b []byte
	err := db.pool.QueryRow(ctx, `SELECT doc FROM models WHERE name = $1`, name).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// DeleteModel removes a model. It tells if the model was there.
func (db *DB) DeleteModel(ctx context.Context, name string) (bool, error) {
	t, err := db.pool.Exec(ctx, `DELETE FROM models WHERE name = $1`, name)
	return t.RowsAffected() > 0, err
}

// Models returns all models, in name order.
func (db *DB) Models(ctx context.Context) ([]api.Model, error) {
	rows, err := db.pool.Query(ctx, `SELECT doc FROM models ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.Model
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var m api.Model
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PutTask adds or replaces a task default.
func (db *DB) PutTask(ctx context.Context, t api.TaskDefault) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = db.pool.Exec(ctx, `INSERT INTO tasks (task, doc, updated_ms) VALUES ($1, $2, $3)
		ON CONFLICT (task) DO UPDATE SET doc = EXCLUDED.doc, updated_ms = EXCLUDED.updated_ms`, t.Task, b, int64(t.UpdatedMs))
	return err
}

// GetTask returns the default of a task.
func (db *DB) GetTask(ctx context.Context, task string) (api.TaskDefault, error) {
	var t api.TaskDefault
	var b []byte
	err := db.pool.QueryRow(ctx, `SELECT doc FROM tasks WHERE task = $1`, task).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(b, &t)
}

// Tasks returns all task defaults, in task order.
func (db *DB) Tasks(ctx context.Context) ([]api.TaskDefault, error) {
	rows, err := db.pool.Query(ctx, `SELECT doc FROM tasks ORDER BY task`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.TaskDefault
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var t api.TaskDefault
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
