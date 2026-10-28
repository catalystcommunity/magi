// Package store keeps knowledge nodes, edges, and evidence in PostgreSQL.
package store

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

//go:embed schema.sql
var schema string

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Store is the knowledge database.
type Store struct {
	DB  *pgxpool.Pool
	Now func() time.Time
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

func (s *Store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7316001)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
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

const nodeCols = `id, owner, kind, label, summary, properties, ext_system, ext_locator, ext_context, scope, authored_by, confidence, created_ms, updated_ms`
const edgeCols = `id, owner, from_node, to_node, relation, properties, confidence, created_ms`
const evidenceCols = `id, owner, subject, ref_kind, ref, stance, note, created_ms`

func scanNode(r pgx.Row) (api.Node, error) {
	var n api.Node
	var sys, loc *string
	var ctx *string
	err := r.Scan(&n.Id, &n.Owner, &n.Kind, &n.Label, &n.Summary, &n.Properties, &sys, &loc, &ctx, &n.Scope, &n.AuthoredBy, &n.Confidence, &n.CreatedMs, &n.UpdatedMs)
	if sys != nil && loc != nil {
		n.External = &api.ExternalRef{System: *sys, Locator: *loc, Context: ctx}
	}
	if n.Properties == nil {
		n.Properties = map[string]string{}
	}
	return n, notFound(err)
}

func scanEdge(r pgx.Row) (api.Edge, error) {
	var e api.Edge
	err := r.Scan(&e.Id, &e.Owner, &e.FromNode, &e.ToNode, &e.Relation, &e.Properties, &e.Confidence, &e.CreatedMs)
	if e.Properties == nil {
		e.Properties = map[string]string{}
	}
	return e, notFound(err)
}

func scanEvidence(r pgx.Row) (api.Evidence, error) {
	var v api.Evidence
	err := r.Scan(&v.Id, &v.Owner, &v.Subject, &v.RefKind, &v.Ref, &v.Stance, &v.Note, &v.CreatedMs)
	return v, notFound(err)
}

func collect[T any](ctx context.Context, db *pgxpool.Pool, scan func(pgx.Row) (T, error), sql string, args ...any) ([]T, error) {
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

// searchText is the text that query matches: label, summary, and the
// property values in key order.
func searchText(label, summary string, props map[string]string) string {
	parts := []string{label, summary}
	for _, k := range slices.Sorted(maps.Keys(props)) {
		parts = append(parts, props[k])
	}
	return strings.Join(parts, " ")
}

// NodeInput is a validated put-node request. Nil fields keep stored values
// on update and take defaults on create.
type NodeInput struct {
	ID         string // empty: match by external or create
	Owner      string
	Kind       string
	Label      string
	Summary    *string
	Properties map[string]string
	External   *api.ExternalRef
	Scope      *string
	AuthoredBy *string
	Confidence *float64
}

// PutNode creates or updates a node as the contract describes. With an ID,
// the node must exist. Without one, a node with the same owner and external
// system and locator is updated.
func (s *Store) PutNode(ctx context.Context, in NodeInput) (api.Node, error) {
	now := s.now()
	var sys, loc, xctx *string
	if in.External != nil {
		sys, loc, xctx = &in.External.System, &in.External.Locator, in.External.Context
	}
	var props any
	if in.Properties != nil {
		props = in.Properties
	}
	// Each optional value is passed as NULL when absent. COALESCE keeps the
	// stored value on update.
	set := `kind = $3, label = $4,
		summary = COALESCE($5, nodes.summary),
		properties = COALESCE($6::jsonb, nodes.properties),
		ext_system = COALESCE($7, nodes.ext_system),
		ext_locator = COALESCE($8, nodes.ext_locator),
		ext_context = COALESCE($9, nodes.ext_context),
		scope = COALESCE($10, nodes.scope),
		authored_by = COALESCE($11, nodes.authored_by),
		confidence = COALESCE($12, nodes.confidence),
		updated_ms = $13`
	args := []any{in.ID, in.Owner, in.Kind, in.Label, in.Summary, props, sys, loc, xctx, in.Scope, in.AuthoredBy, in.Confidence, now}
	var q string
	if in.ID != "" {
		q = `UPDATE nodes SET ` + set + ` WHERE id = $1 AND owner = $2 RETURNING ` + nodeCols
	} else {
		args[0] = NewID("nod_")
		q = `INSERT INTO nodes (id, owner, kind, label, summary, properties, ext_system, ext_locator, ext_context, scope, authored_by, confidence, created_ms, updated_ms)
		VALUES ($1, $2, $3, $4, COALESCE($5, ''), COALESCE($6::jsonb, '{}'), $7, $8, $9, COALESCE($10, 'private'), $11, COALESCE($12, 1.0), $13, $13)
		ON CONFLICT (owner, ext_system, ext_locator) WHERE ext_system IS NOT NULL DO UPDATE SET ` + set + `
		RETURNING ` + nodeCols
	}
	var n api.Node
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		if n, err = scanNode(tx.QueryRow(ctx, q, args...)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET search_text = $2 WHERE id = $1`, n.Id, searchText(n.Label, n.Summary, n.Properties))
		return err
	})
	return n, notFound(err)
}

// Node returns one node.
func (s *Store) Node(ctx context.Context, id string) (api.Node, error) {
	return scanNode(s.DB.QueryRow(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = $1`, id))
}

// Edge returns one edge.
func (s *Store) Edge(ctx context.Context, id string) (api.Edge, error) {
	return scanEdge(s.DB.QueryRow(ctx, `SELECT `+edgeCols+` FROM edges WHERE id = $1`, id))
}

// View returns a node, its incident edges, and the evidence for the node
// and for those edges.
func (s *Store) View(ctx context.Context, id string) (api.NodeView, error) {
	n, err := s.Node(ctx, id)
	if err != nil {
		return api.NodeView{}, err
	}
	edges, err := collect(ctx, s.DB, scanEdge, `SELECT `+edgeCols+` FROM edges WHERE from_node = $1 OR to_node = $1 ORDER BY created_ms, id`, id)
	if err != nil {
		return api.NodeView{}, err
	}
	subjects := []string{id}
	for _, e := range edges {
		subjects = append(subjects, e.Id)
	}
	ev, err := collect(ctx, s.DB, scanEvidence, `SELECT `+evidenceCols+` FROM evidence WHERE subject = ANY($1) ORDER BY created_ms, id`, subjects)
	return api.NodeView{Node: n, Edges: edges, Evidence: ev}, err
}

// PutEdge creates or updates the edge with the same owner, nodes, and relation.
// Both nodes must belong to owner.
func (s *Store) PutEdge(ctx context.Context, owner string, req api.PutEdgeRequest) (api.Edge, error) {
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE owner = $1 AND id IN ($2, $3)`, owner, req.FromNode, req.ToNode).Scan(&count); err != nil {
		return api.Edge{}, err
	}
	if want := map[bool]int{true: 1, false: 2}[req.FromNode == req.ToNode]; count != want {
		return api.Edge{}, ErrNotFound
	}
	var props any
	if req.Properties != nil {
		props = req.Properties
	}
	return scanEdge(s.DB.QueryRow(ctx, `INSERT INTO edges (`+edgeCols+`)
		VALUES ($1, $2, $3, $4, $5, COALESCE($6::jsonb, '{}'), COALESCE($7, 1.0), $8)
		ON CONFLICT (owner, from_node, to_node, relation) DO UPDATE SET
			properties = COALESCE($6::jsonb, edges.properties),
			confidence = COALESCE($7, edges.confidence)
		RETURNING `+edgeCols,
		NewID("edg_"), owner, req.FromNode, req.ToNode, req.Relation, props, req.Confidence, s.now()))
}

// LinkEvidence records evidence for subject. The same subject, ref_kind, and
// ref update the stance and note.
func (s *Store) LinkEvidence(ctx context.Context, owner string, req api.LinkEvidenceRequest, stance string) (api.Evidence, error) {
	return scanEvidence(s.DB.QueryRow(ctx, `INSERT INTO evidence (`+evidenceCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (subject, ref_kind, ref) DO UPDATE SET stance = $6, note = COALESCE($7, evidence.note)
		RETURNING `+evidenceCols,
		NewID("evd_"), owner, req.Subject, req.RefKind, req.Ref, stance, req.Note, s.now()))
}

// Evidence returns one evidence record.
func (s *Store) Evidence(ctx context.Context, id string) (api.Evidence, error) {
	return scanEvidence(s.DB.QueryRow(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE id = $1`, id))
}

// Delete removes a node, edge, or evidence record by ID. A node takes its
// edges and the evidence of both with it.
func (s *Store) Delete(ctx context.Context, id string) (bool, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// Evidence has no foreign key because its subject is a node or an edge.
		if _, err := tx.Exec(ctx, `DELETE FROM evidence WHERE subject = $1 OR subject IN (SELECT id FROM edges WHERE from_node = $1 OR to_node = $1)`, id); err != nil {
			return err
		}
		for _, t := range []string{"nodes", "edges", "evidence"} {
			tag, err := tx.Exec(ctx, `DELETE FROM `+t+` WHERE id = $1`, id)
			if err != nil {
				return err
			}
			n += tag.RowsAffected()
		}
		return nil
	})
	return n > 0, err
}

// Query is a validated query.
type Query struct {
	Owner string
	Text  string
	Kind  string
	Near  string
	Depth int
	Limit int
}

// Find returns the owner's nodes that match q and the edges between them.
func (s *Store) Find(ctx context.Context, q Query) (api.QueryResponse, error) {
	args := []any{q.Owner}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	var with, from = "", "nodes n"
	where := []string{"n.owner = $1"}
	order := []string{}
	if q.Near != "" {
		near, depth := arg(q.Near), arg(q.Depth)
		with = `WITH RECURSIVE hood(id, d) AS (
			SELECT id, 0 FROM nodes WHERE id = ` + near + ` AND owner = $1
			UNION
			SELECT CASE WHEN e.from_node = h.id THEN e.to_node ELSE e.from_node END, h.d + 1
			FROM hood h JOIN edges e ON (e.from_node = h.id OR e.to_node = h.id) AND e.owner = $1
			WHERE h.d < ` + depth + `
		), dist AS (SELECT id, min(d) AS d FROM hood GROUP BY id) `
		from = "nodes n JOIN dist ON dist.id = n.id"
		order = append(order, "dist.d")
	}
	if q.Kind != "" {
		where = append(where, "n.kind = "+arg(q.Kind))
	}
	if q.Text != "" {
		// Any word can match, so a question in natural words finds records
		// that hold only some of them. More matched words rank higher. The
		// word patterns also match inside longer words ("vet" in
		// "veterinary").
		words := searchWords(q.Text)
		ts := arg(strings.Join(words, " or "))
		likes := []string{"%" + escapeLike(q.Text) + "%"}
		for _, w := range words {
			if len([]rune(w)) >= 3 && !stopWords[w] {
				likes = append(likes, "%"+escapeLike(w)+"%")
			}
		}
		like := arg(likes)
		where = append(where, "(n.search @@ websearch_to_tsquery('english', "+ts+") OR n.search_text ILIKE ANY("+like+"))")
		order = append(order, "ts_rank(n.search, websearch_to_tsquery('english', "+ts+")) DESC")
	}
	order = append(order, "n.updated_ms DESC", "n.id")
	sql := with + `SELECT ` + prefixed("n.", nodeCols) + ` FROM ` + from + ` WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + strings.Join(order, ", ") + ` LIMIT ` + arg(q.Limit)
	nodes, err := collect(ctx, s.DB, scanNode, sql, args...)
	if err != nil {
		return api.QueryResponse{}, err
	}
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.Id
	}
	edges, err := collect(ctx, s.DB, scanEdge, `SELECT `+edgeCols+` FROM edges WHERE owner = $1 AND from_node = ANY($2) AND to_node = ANY($2) ORDER BY created_ms, id`, q.Owner, ids)
	return api.QueryResponse{Nodes: nodes, Edges: edges}, err
}

func prefixed(p, cols string) string {
	parts := strings.Split(cols, ", ")
	for i := range parts {
		parts[i] = p + parts[i]
	}
	return strings.Join(parts, ", ")
}

// searchWords returns the lower-case words of text without punctuation.
func searchWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// stopWords are not used as substring patterns. Full-text search drops them
// by itself.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "how": true, "what": true, "who": true, "where": true,
	"when": true, "which": true, "with": true, "you": true, "your": true, "are": true, "was": true,
	"has": true, "have": true, "this": true, "that": true, "from": true, "about": true, "does": true,
	"can": true, "not": true, "any": true, "all": true, "out": true, "get": true, "use": true,
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// OwnerOf returns the owner of the node, edge, or evidence record id.
// kinds limits the tables searched: "nodes", "edges", "evidence".
func (s *Store) OwnerOf(ctx context.Context, id string, kinds ...string) (string, error) {
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		if k != "nodes" && k != "edges" && k != "evidence" {
			return "", fmt.Errorf("unknown table %q", k)
		}
		parts[i] = `SELECT owner FROM ` + k + ` WHERE id = $1`
	}
	var o string
	err := s.DB.QueryRow(ctx, strings.Join(parts, " UNION ALL ")+" LIMIT 1", id).Scan(&o)
	return o, notFound(err)
}
