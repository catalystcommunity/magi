// Package store keeps entities, claims, episodes, procedures, evidence,
// changes, disputes, and groups in PostgreSQL (docs/knowledge.md).
//
// Access is part of each query: a Who gives the principals of the caller
// (the subject and its groups). A record is visible when its owners or
// readers contain a principal, and changeable when its owners do.
package store

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Store is the knowledge database.
type Store struct {
	DB  *pgxpool.Pool
	Now func() time.Time
}

// Who is the caller of an operation. Subject is the actor in the history.
// Principals are the subject and the groups that it is a member of. All
// permits every record (knowledge:admin).
type Who struct {
	Subject    string
	Principals []string
	All        bool
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Open connects to url and applies the schema.
func Open(ctx context.Context, url string) (*Store, error) {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() { s.DB.Close() }

// The first schema had nodes, edges, and evidence. Its tables become
// legacy_*, and their rows become entities, claims, and evidence once.
const legacyRename = `DO $$ BEGIN
IF to_regclass('public.nodes') IS NOT NULL THEN
  ALTER TABLE nodes RENAME TO legacy_nodes;
  ALTER TABLE edges RENAME TO legacy_edges;
  ALTER TABLE evidence RENAME TO legacy_evidence;
  ALTER INDEX evidence_pkey RENAME TO legacy_evidence_pkey;
  ALTER INDEX evidence_subject_ref_kind_ref_key RENAME TO legacy_evidence_subject_ref_kind_ref_key;
END IF; END $$`

const legacyCopy = `DO $$ BEGIN
IF to_regclass('public.legacy_nodes') IS NOT NULL THEN
IF NOT EXISTS (SELECT 1 FROM entities e JOIN legacy_nodes n ON n.id = e.id) THEN
  INSERT INTO entities (id, kind, label, summary, properties, ext_system, ext_locator, ext_context, owners, confidence, created_ms, updated_ms)
    SELECT id, kind, label, summary, properties, ext_system, ext_locator, ext_context, ARRAY[owner], confidence, created_ms, updated_ms FROM legacy_nodes;
  INSERT INTO claims (id, subject, relation, object, confidence, owners, created_ms, updated_ms)
    SELECT id, from_node, relation, to_node, confidence, ARRAY[owner], created_ms, created_ms FROM legacy_edges;
  INSERT INTO evidence (id, subject, ref_kind, ref, stance, note, owners, created_ms)
    SELECT id, subject, ref_kind, ref, stance, note, ARRAY[owner], created_ms FROM legacy_evidence;
  INSERT INTO changes (id, record, operation, actor, reason, owners, at_ms)
    SELECT 'chg_legacy_' || id, id, 'create', owner, 'imported from the first knowledge schema', ARRAY[owner], created_ms FROM legacy_nodes;
END IF;
END IF; END $$`

func (s *Store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7316001)"); err != nil {
			return err
		}
		for _, q := range []string{legacyRename, schema, legacyCopy} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE entities SET search_text = `+entitySearch+` WHERE search_text = ''`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE claims c SET search_text = `+claimSearch+` WHERE c.search_text = ''`)
		return err
	})
}

func (s *Store) now() uint64 {
	if s.Now != nil {
		return uint64(s.Now().UnixMilli())
	}
	return uint64(time.Now().UnixMilli())
}

// NewID returns a random URL-safe ID with prefix.
func NewID(prefix string) string {
	b := make([]byte, 15)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pe.ConstraintName)
	}
	return err
}

func collect[T any](ctx context.Context, db querier, scan func(pgx.Row) (T, error), sql string, args ...any) ([]T, error) {
	rows, err := db.Query(ctx, sql, args...)
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

// Principals returns subject and the groups that have it as a member.
func (s *Store) Principals(ctx context.Context, subject string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT id FROM groups WHERE members @> ARRAY[$1]`, subject)
	if err != nil {
		return nil, err
	}
	gs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return append([]string{subject}, gs...), err
}

// Set returns the sorted, unique, non-empty items of v.
func Set(v []string) []string {
	out := []string{}
	for _, x := range v {
		if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return out
}

// access is an SQL condition on the columns owners and readers of alias t.
// The arguments are appended to args.
func access(w Who, t string, write bool, args *[]any) string {
	if w.All {
		return "true"
	}
	*args = append(*args, w.Principals)
	n := len(*args)
	if write {
		return fmt.Sprintf("%s.owners && $%d", t, n)
	}
	return fmt.Sprintf("(%s.owners && $%d OR %s.readers && $%d)", t, n, t, n)
}

func (w Who) owns(owners []string) bool {
	return w.All || slices.ContainsFunc(owners, func(o string) bool { return slices.Contains(w.Principals, o) })
}

func (w Who) reads(owners, readers []string) bool {
	return w.owns(owners) || slices.ContainsFunc(readers, func(o string) bool { return slices.Contains(w.Principals, o) })
}

func jsonText(v any) *string {
	b, _ := json.Marshal(v)
	s := string(b)
	return &s
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

// change writes one row of the history.
func (s *Store) change(ctx context.Context, tx querier, record, op string, w Who, owners []string, reason, utterance *string, before, after any) (string, uint64, error) {
	id, at := NewID("chg_"), s.now()
	var b, a *string
	if before != nil {
		b = jsonText(before)
	}
	if after != nil {
		a = jsonText(after)
	}
	_, err := tx.Exec(ctx, `INSERT INTO changes (id, record, operation, actor, reason, before, after, utterance, owners, at_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, id, record, op, w.Subject, reason, b, a, utterance, owners, at)
	return id, at, err
}

// TypeOf returns the record type of id: entity, claim, episode, procedure,
// or evidence.
func (s *Store) TypeOf(ctx context.Context, db querier, id string) (string, error) {
	var t string
	err := db.QueryRow(ctx, `SELECT 'entity' FROM entities WHERE id = $1
		UNION ALL SELECT 'claim' FROM claims WHERE id = $1
		UNION ALL SELECT 'episode' FROM episodes WHERE id = $1
		UNION ALL SELECT 'procedure' FROM procedures WHERE id = $1
		UNION ALL SELECT 'evidence' FROM evidence WHERE id = $1 LIMIT 1`, id).Scan(&t)
	return t, notFound(err)
}

// ownersOf returns the owners and readers of a record of any type, and
// whether it is retracted.
func (s *Store) ownersOf(ctx context.Context, db querier, id string) (owners, readers []string, gone bool, err error) {
	err = db.QueryRow(ctx, `SELECT owners, readers, status = 'retracted' FROM entities WHERE id = $1
		UNION ALL SELECT owners, readers, status = 'retracted' FROM claims WHERE id = $1
		UNION ALL SELECT owners, readers, status = 'retracted' FROM episodes WHERE id = $1
		UNION ALL SELECT owners, readers, retracted FROM procedures WHERE id = $1
		UNION ALL SELECT owners, '{}'::text[], retracted FROM evidence WHERE id = $1 LIMIT 1`, id).Scan(&owners, &readers, &gone)
	return owners, readers, gone, notFound(err)
}
