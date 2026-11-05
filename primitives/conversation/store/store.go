// Package store keeps the conversation graph in PostgreSQL.
//
// Each read runs in a transaction that sets the readers that it reads as
// (Access). Row-level security in the database then hides the records
// that those readers cannot read, in addition to the checks of the
// service.
package store

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// Channel is the PostgreSQL NOTIFY channel. The payload is a stream id.
const Channel = "magi_conversation_stream"

// ErrNotFound means no record matches, or the reader cannot read it.
var ErrNotFound = errors.New("not found")

// DefaultThreshold is the review threshold before a person has answered
// reviews.
const DefaultThreshold = 0.75

// Access is the identity that a query reads as. Principals are the
// subjects and groups that the identity is; "*" reads all. Via, when set,
// is a persona that must also be a reader of each record.
type Access struct {
	Principals []string
	Via        string
}

// All reads all records.
var All = Access{Principals: []string{"*"}}

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

// tx runs fn in a transaction that reads as acc.
func (db *DB) tx(ctx context.Context, acc Access, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		p, _ := json.Marshal(acc.Principals)
		if _, err := tx.Exec(ctx, `SELECT set_config('magi.principals', $1, true), set_config('magi.via', $2, true)`, string(p), acc.Via); err != nil {
			return err
		}
		return fn(tx)
	})
}

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

func refsJSON(a []api.Ref) []byte {
	if a == nil {
		a = []api.Ref{}
	}
	b, _ := json.Marshal(a)
	return b
}

func parseRefs(b []byte) ([]api.Ref, error) {
	out := []api.Ref{}
	return out, json.Unmarshal(b, &out)
}

// Personas

const personaCols = "id, name, sharing, members, updated_ms"

func scanPersona(r pgx.Row) (api.Persona, error) {
	var p api.Persona
	var updated int64
	err := r.Scan(&p.Id, &p.Name, &p.Sharing, &p.Members, &updated)
	p.UpdatedMs = uint64(updated)
	if p.Members == nil {
		p.Members = []string{}
	}
	return p, err
}

func (db *DB) PutPersona(ctx context.Context, p api.Persona) (api.Persona, error) {
	if p.Members == nil {
		p.Members = []string{}
	}
	return scanPersona(db.pool.QueryRow(ctx, `INSERT INTO personas (`+personaCols+`) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE SET name = $2, sharing = $3, members = $4, updated_ms = $5 RETURNING `+personaCols,
		p.Id, p.Name, p.Sharing, p.Members, int64(p.UpdatedMs)))
}

func (db *DB) GetPersona(ctx context.Context, id string) (api.Persona, error) {
	return one(scanPersona(db.pool.QueryRow(ctx, `SELECT `+personaCols+` FROM personas WHERE id = $1`, id)))
}

func (db *DB) ListPersonas(ctx context.Context) ([]api.Persona, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+personaCols+` FROM personas ORDER BY id`)
	return many(rows, err, scanPersona)
}

// AudienceGroup is the reader that the audience of a persona has.
func AudienceGroup(persona string) string { return "audience:" + persona }

// Groups returns the groups of subject: the audiences that it is a member of.
func (db *DB) Groups(ctx context.Context, subject string) ([]string, error) {
	rows, err := db.pool.Query(ctx, `SELECT id FROM personas WHERE $1 = ANY(members) AND sharing <> 'never' ORDER BY id`, subject)
	ids, err := many(rows, err, func(r pgx.Row) (string, error) { var s string; return s, r.Scan(&s) })
	for i, id := range ids {
		ids[i] = AudienceGroup(id)
	}
	return ids, err
}

// Streams

const streamCols = "id, person, persona, last_seq, created_ms, updated_ms"

func scanStream(r pgx.Row) (api.Stream, error) {
	var s api.Stream
	var seq, created, updated int64
	err := r.Scan(&s.Id, &s.Person, &s.Persona, &seq, &created, &updated)
	s.LastSeq, s.CreatedMs, s.UpdatedMs = uint64(seq), uint64(created), uint64(updated)
	return s, err
}

// OpenStream returns the stream of person and persona, and makes it with
// id when it is not there.
func (db *DB) OpenStream(ctx context.Context, id, person, persona string, now time.Time) (api.Stream, error) {
	ms := now.UnixMilli()
	_, err := db.pool.Exec(ctx, `INSERT INTO streams (`+streamCols+`) VALUES ($1, $2, $3, 0, $4, $4) ON CONFLICT (person, persona) DO NOTHING`,
		id, person, persona, ms)
	if err != nil {
		return api.Stream{}, err
	}
	return one(scanStream(db.pool.QueryRow(ctx, `SELECT `+streamCols+` FROM streams WHERE person = $1 AND persona = $2`, person, persona)))
}

func (db *DB) GetStream(ctx context.Context, id string) (api.Stream, error) {
	return one(scanStream(db.pool.QueryRow(ctx, `SELECT `+streamCols+` FROM streams WHERE id = $1`, id)))
}

func (db *DB) ListStreams(ctx context.Context, person string) ([]api.Stream, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+streamCols+` FROM streams WHERE person = $1 ORDER BY updated_ms DESC, id`, person)
	return many(rows, err, scanStream)
}

// Utterances

const uttCols = "id, stream, seq, author, author_kind, modality, text, audio, transcript_confidence, attachments, generated_by, visibility, visibility_by, visibility_confidence, created_ms"

func scanUtt(r pgx.Row) (api.Utterance, error) {
	var u api.Utterance
	var seq, created int64
	var att []byte
	if err := r.Scan(&u.Id, &u.Stream, &seq, &u.Author, &u.AuthorKind, &u.Modality, &u.Text, &u.Audio, &u.TranscriptConfidence,
		&att, &u.GeneratedBy, &u.Visibility, &u.VisibilityBy, &u.VisibilityConfidence, &created); err != nil {
		return u, err
	}
	u.Seq, u.CreatedMs = uint64(seq), uint64(created)
	var err error
	u.Attachments, err = parseRefs(att)
	return u, err
}

// Post assigns the next seq of the stream to u, stores it with readers,
// and stores links from it. The stream row lock serializes posts. Other
// replicas learn of the post through NOTIFY on Channel.
func (db *DB) Post(ctx context.Context, u api.Utterance, readers []string, classified bool, links []api.Link) (api.Utterance, error) {
	err := db.tx(ctx, All, func(tx pgx.Tx) error {
		var seq int64
		err := tx.QueryRow(ctx, `UPDATE streams SET last_seq = last_seq + 1, updated_ms = GREATEST(updated_ms, $2)
WHERE id = $1 RETURNING last_seq`, u.Stream, int64(u.CreatedMs)).Scan(&seq)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		u.Seq = uint64(seq)
		if err := insertUtt(ctx, tx, u, readers, classified); err != nil {
			return err
		}
		for _, l := range links {
			if err := insertLink(ctx, tx, l); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, Channel, u.Stream)
		return err
	})
	if u.Attachments == nil {
		u.Attachments = []api.Ref{}
	}
	return u, err
}

func insertUtt(ctx context.Context, tx pgx.Tx, u api.Utterance, readers []string, classified bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO utterances (`+uttCols+`, readers, classified)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		u.Id, u.Stream, int64(u.Seq), u.Author, u.AuthorKind, u.Modality, u.Text, u.Audio, u.TranscriptConfidence,
		refsJSON(u.Attachments), u.GeneratedBy, u.Visibility, u.VisibilityBy, u.VisibilityConfidence, int64(u.CreatedMs), readers, classified)
	return err
}

// ListUtterances returns readable utterances of a stream. With before > 0
// it returns the newest before that seq; else the oldest after after.
// Both are in seq order.
func (db *DB) ListUtterances(ctx context.Context, acc Access, stream string, after, before uint64, limit int) ([]api.Utterance, error) {
	var out []api.Utterance
	err := db.tx(ctx, acc, func(tx pgx.Tx) error {
		var rows pgx.Rows
		var err error
		if before > 0 {
			rows, err = tx.Query(ctx, `SELECT * FROM (SELECT `+uttCols+` FROM utterances WHERE stream = $1 AND seq < $2 ORDER BY seq DESC LIMIT $3) x ORDER BY seq`,
				stream, int64(before), limit)
		} else {
			rows, err = tx.Query(ctx, `SELECT `+uttCols+` FROM utterances WHERE stream = $1 AND seq > $2 ORDER BY seq LIMIT $3`,
				stream, int64(after), limit)
		}
		out, err = many(rows, err, scanUtt)
		return err
	})
	return out, err
}

func (db *DB) GetUtterances(ctx context.Context, acc Access, ids []string) ([]api.Utterance, error) {
	var out []api.Utterance
	err := db.tx(ctx, acc, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+uttCols+` FROM utterances WHERE id = ANY($1) ORDER BY created_ms, id`, ids)
		out, err = many(rows, err, scanUtt)
		return err
	})
	return out, err
}

func (db *DB) GetUtterance(ctx context.Context, acc Access, id string) (api.Utterance, error) {
	list, err := db.GetUtterances(ctx, acc, []string{id})
	if err != nil {
		return api.Utterance{}, err
	}
	if len(list) == 0 {
		return api.Utterance{}, ErrNotFound
	}
	return list[0], nil
}

// Search returns readable utterances that match any of the words, best
// first.
func (db *DB) Search(ctx context.Context, acc Access, text string, beforeMs uint64, limit int) ([]api.SearchHit, error) {
	words := strings.Fields(strings.Map(func(r rune) rune {
		if r == '\'' || r == '"' || r == '&' || r == '|' || r == '!' || r == ':' || r == '(' || r == ')' || r == '<' || r == '*' || r == '\\' {
			return ' '
		}
		return r
	}, text))
	if len(words) == 0 {
		return []api.SearchHit{}, nil
	}
	if beforeMs == 0 {
		beforeMs = 1 << 62
	}
	var out []api.SearchHit
	err := db.tx(ctx, acc, func(tx pgx.Tx) error {
		// plainto_tsquery of each word drops stop words; the words are
		// joined with OR so that a question finds a partial match.
		rows, err := tx.Query(ctx, `WITH q AS (
  SELECT string_agg(w, ' | ')::tsquery AS q FROM (
    SELECT NULLIF(plainto_tsquery('english', x)::text, '') AS w FROM unnest($1::text[]) x) t WHERE w IS NOT NULL)
SELECT `+uttCols+`, ts_rank_cd(search, q.q) AS rank FROM utterances, q
WHERE q.q IS NOT NULL AND search @@ q.q AND created_ms < $2 ORDER BY rank DESC, created_ms DESC LIMIT $3`, words, int64(beforeMs), limit)
		out, err = many(rows, err, func(r pgx.Row) (api.SearchHit, error) {
			var h api.SearchHit
			var u api.Utterance
			var seq, created int64
			var att []byte
			err := r.Scan(&u.Id, &u.Stream, &seq, &u.Author, &u.AuthorKind, &u.Modality, &u.Text, &u.Audio, &u.TranscriptConfidence,
				&att, &u.GeneratedBy, &u.Visibility, &u.VisibilityBy, &u.VisibilityConfidence, &created, &h.Rank)
			if err != nil {
				return h, err
			}
			u.Seq, u.CreatedMs = uint64(seq), uint64(created)
			u.Attachments, err = parseRefs(att)
			h.Utterance = u
			return h, err
		})
		return err
	})
	return out, err
}

// Readers returns the readers of an utterance, read as all.
func (db *DB) Readers(ctx context.Context, id string) ([]string, error) {
	var r []string
	err := db.tx(ctx, All, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT readers FROM utterances WHERE id = $1`, id).Scan(&r)
	})
	return one(r, err)
}

// SetVisibility changes the visibility and readers of an utterance and
// records the change.
func (db *DB) SetVisibility(ctx context.Context, id, visibility string, readers []string, by string, conf *float64, note *string, changeID string, now time.Time) (api.Utterance, error) {
	var u api.Utterance
	err := db.tx(ctx, All, func(tx pgx.Tx) error {
		var err error
		u, err = scanUtt(tx.QueryRow(ctx, `UPDATE utterances SET visibility = $2, readers = $3, visibility_by = $4, visibility_confidence = $5
WHERE id = $1 RETURNING `+uttCols, id, visibility, readers, by, conf))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO visibility_changes (id, utterance, visibility, author, confidence, note, created_ms) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			changeID, id, visibility, by, conf, note, now.UnixMilli())
		return err
	})
	return one(u, err)
}

// ClaimUnclassified claims up to limit utterances that are not classified
// and not claimed, until now+lease. SKIP LOCKED lets replicas claim at the
// same time without waiting for each other.
func (db *DB) ClaimUnclassified(ctx context.Context, limit int, now time.Time, lease time.Duration) ([]api.Utterance, error) {
	var out []api.Utterance
	err := db.tx(ctx, All, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE utterances SET claimed_until = $2 WHERE id IN (
  SELECT id FROM utterances WHERE NOT classified AND claimed_until < $1 ORDER BY created_ms, id LIMIT $3 FOR UPDATE SKIP LOCKED)
RETURNING `+uttCols, now.UnixMilli(), now.Add(lease).UnixMilli(), limit)
		out, err = many(rows, err, scanUtt)
		return err
	})
	slices.SortFunc(out, func(a, b api.Utterance) int {
		return cmp.Or(cmp.Compare(a.CreatedMs, b.CreatedMs), cmp.Compare(a.Id, b.Id))
	})
	return out, err
}

// SetAudio sets the audio of an utterance that has none.
func (db *DB) SetAudio(ctx context.Context, id, audio string) (api.Utterance, error) {
	var u api.Utterance
	err := db.tx(ctx, All, func(tx pgx.Tx) error {
		var err error
		u, err = scanUtt(tx.QueryRow(ctx, `UPDATE utterances SET audio = $2 WHERE id = $1 AND (audio IS NULL OR audio = '') RETURNING `+uttCols, id, audio))
		return err
	})
	return one(u, err)
}

func (db *DB) MarkClassified(ctx context.Context, id string) (api.Utterance, error) {
	var u api.Utterance
	err := db.tx(ctx, All, func(tx pgx.Tx) error {
		var err error
		u, err = scanUtt(tx.QueryRow(ctx, `UPDATE utterances SET classified = true, claimed_until = 0 WHERE id = $1 RETURNING `+uttCols, id))
		return err
	})
	return one(u, err)
}

// Links

const linkCols = "id, utterance, span_start, span_end, weight, target_kind, target_ref, target_title, kind, confidence, author, note, state, replaces, created_ms"

func scanLink(r pgx.Row) (api.Link, error) {
	var l api.Link
	var start, end *int64
	var created int64
	err := r.Scan(&l.Id, &l.Utterance, &start, &end, &l.Weight, &l.Target.Kind, &l.Target.Ref, &l.Target.Title, &l.Kind,
		&l.Confidence, &l.Author, &l.Note, &l.State, &l.Replaces, &created)
	if start != nil && end != nil {
		l.Span = &api.Span{Start: uint64(*start), End: uint64(*end)}
	}
	l.CreatedMs = uint64(created)
	return l, err
}

func insertLink(ctx context.Context, tx pgx.Tx, l api.Link) error {
	var start, end *int64
	if l.Span != nil {
		s, e := int64(l.Span.Start), int64(l.Span.End)
		start, end = &s, &e
	}
	_, err := tx.Exec(ctx, `INSERT INTO links (`+linkCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		l.Id, l.Utterance, start, end, l.Weight, l.Target.Kind, l.Target.Ref, l.Target.Title, l.Kind, l.Confidence, l.Author, l.Note,
		l.State, l.Replaces, int64(l.CreatedMs))
	return err
}

// PutLink stores l. When l replaces a link, that link gets state "replaced".
func (db *DB) PutLink(ctx context.Context, l api.Link) error {
	return db.tx(ctx, All, func(tx pgx.Tx) error {
		if l.Replaces != nil {
			if _, err := tx.Exec(ctx, `UPDATE links SET state = 'replaced' WHERE id = $1`, *l.Replaces); err != nil {
				return err
			}
		}
		return insertLink(ctx, tx, l)
	})
}

func (db *DB) GetLink(ctx context.Context, acc Access, id string) (api.Link, error) {
	var l api.Link
	err := db.tx(ctx, acc, func(tx pgx.Tx) error {
		var err error
		l, err = scanLink(tx.QueryRow(ctx, `SELECT `+linkCols+` FROM links WHERE id = $1`, id))
		return err
	})
	return one(l, err)
}

func (db *DB) SetLinkState(ctx context.Context, id, state string, note *string) (api.Link, error) {
	var l api.Link
	err := db.tx(ctx, All, func(tx pgx.Tx) error {
		var err error
		l, err = scanLink(tx.QueryRow(ctx, `UPDATE links SET state = $2, note = COALESCE($3, note) WHERE id = $1 RETURNING `+linkCols, id, state, note))
		return err
	})
	return one(l, err)
}

// LinkFilter selects links. Back selects links from the utterances; Forward
// selects links to them (as utterance targets). States empty means current.
type LinkFilter struct {
	Back, Forward bool
	Kinds         []string
	States        []string
}

// Links returns the readable links of the utterances ids.
func (db *DB) Links(ctx context.Context, acc Access, ids []string, f LinkFilter) ([]api.Link, error) {
	if len(f.States) == 0 {
		f.States = []string{"current"}
	}
	var where []string
	if f.Back {
		where = append(where, "utterance = ANY($1)")
	}
	if f.Forward {
		where = append(where, "(target_kind = 'utterance' AND target_ref = ANY($1))")
	}
	if len(where) == 0 {
		return []api.Link{}, nil
	}
	var out []api.Link
	err := db.tx(ctx, acc, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+linkCols+` FROM links WHERE (`+strings.Join(where, " OR ")+`)
AND state = ANY($2) AND (COALESCE(cardinality($3::text[]), 0) = 0 OR kind = ANY($3)) ORDER BY created_ms, id`, ids, f.States, f.Kinds)
		out, err = many(rows, err, scanLink)
		return err
	})
	return out, err
}

// Reviews

const reviewCols = "id, reviewer, task, utterance, link, question, proposed, choices, confidence, status, answer, note, created_ms, answered_ms"

func scanReview(r pgx.Row) (api.Review, error) {
	var v api.Review
	var created int64
	var answered *int64
	err := r.Scan(&v.Id, &v.Reviewer, &v.Task, &v.Utterance, &v.Link, &v.Question, &v.Proposed, &v.Choices, &v.Confidence,
		&v.Status, &v.Answer, &v.Note, &created, &answered)
	v.CreatedMs = uint64(created)
	if answered != nil {
		a := uint64(*answered)
		v.AnsweredMs = &a
	}
	return v, err
}

func (db *DB) CreateReview(ctx context.Context, v api.Review) error {
	return db.tx(ctx, All, func(tx pgx.Tx) error {
		// A newer question on the same item makes the older one obsolete.
		if _, err := tx.Exec(ctx, `UPDATE reviews SET status = 'dismissed', note = 'replaced by a newer review'
WHERE status = 'open' AND task = $1 AND utterance = $2 AND COALESCE(link, '') = COALESCE($3, '')`, v.Task, v.Utterance, v.Link); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO reviews (`+reviewCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NULL)`,
			v.Id, v.Reviewer, v.Task, v.Utterance, v.Link, v.Question, v.Proposed, v.Choices, v.Confidence, v.Status, v.Answer, v.Note, int64(v.CreatedMs))
		return err
	})
}

func (db *DB) GetReview(ctx context.Context, acc Access, id string) (api.Review, error) {
	var v api.Review
	err := db.tx(ctx, acc, func(tx pgx.Tx) error {
		var err error
		v, err = scanReview(tx.QueryRow(ctx, `SELECT `+reviewCols+` FROM reviews WHERE id = $1`, id))
		return err
	})
	return one(v, err)
}

func (db *DB) ListReviews(ctx context.Context, acc Access, reviewer, status string, limit int) ([]api.Review, error) {
	var out []api.Review
	err := db.tx(ctx, acc, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+reviewCols+` FROM reviews WHERE reviewer = $1 AND ($2 = '' OR status = $2) ORDER BY created_ms, id LIMIT $3`,
			reviewer, status, limit)
		out, err = many(rows, err, scanReview)
		return err
	})
	return out, err
}

// CloseReview records the answer, and closes open reviews of the same item.
func (db *DB) CloseReview(ctx context.Context, id, status, answer string, note *string, now time.Time) (api.Review, error) {
	var v api.Review
	err := db.tx(ctx, All, func(tx pgx.Tx) error {
		var err error
		v, err = scanReview(tx.QueryRow(ctx, `UPDATE reviews SET status = $2, answer = $3, note = $4, answered_ms = $5 WHERE id = $1 RETURNING `+reviewCols,
			id, status, answer, note, now.UnixMilli()))
		return err
	})
	return one(v, err)
}

// CloseOpenReviews dismisses the open reviews of an item, for example when
// the person corrects it directly.
func (db *DB) CloseOpenReviews(ctx context.Context, task, utterance string, link *string, now time.Time) error {
	return db.tx(ctx, All, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'dismissed', note = 'the person corrected it', answered_ms = $4
WHERE status = 'open' AND task = $1 AND utterance = $2 AND ($3::text IS NULL OR link = $3)`, task, utterance, link, now.UnixMilli())
		return err
	})
}

// Examples and calibration

func (db *DB) AddExample(ctx context.Context, reviewer string, e api.Example) error {
	return db.tx(ctx, All, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO examples (reviewer, task, utterance, proposed, label, note, created_ms) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			reviewer, e.Task, e.Utterance, e.Proposed, e.Label, e.Note, int64(e.CreatedMs))
		return err
	})
}

func (db *DB) ListExamples(ctx context.Context, acc Access, reviewer, task string, limit int) ([]api.Example, error) {
	var out []api.Example
	err := db.tx(ctx, acc, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT task, utterance, proposed, label, note, created_ms FROM examples
WHERE reviewer = $1 AND task = $2 ORDER BY created_ms DESC LIMIT $3`, reviewer, task, limit)
		out, err = many(rows, err, func(r pgx.Row) (api.Example, error) {
			var e api.Example
			var created int64
			err := r.Scan(&e.Task, &e.Utterance, &e.Proposed, &e.Label, &e.Note, &created)
			e.CreatedMs = uint64(created)
			return e, err
		})
		return err
	})
	return out, err
}

func (db *DB) Calibration(ctx context.Context, reviewer, task string) (api.Calibration, error) {
	c := api.Calibration{Reviewer: reviewer, Task: task, Threshold: DefaultThreshold}
	var acc, cor int64
	err := db.pool.QueryRow(ctx, `SELECT threshold, accepted, corrected FROM calibration WHERE reviewer = $1 AND task = $2`, reviewer, task).
		Scan(&c.Threshold, &acc, &cor)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	c.Accepted, c.Corrected = uint64(acc), uint64(cor)
	return c, err
}

// Calibrate moves the threshold of reviewer for task: down a little when
// the person accepted a proposal, up more when the person corrected one.
func (db *DB) Calibrate(ctx context.Context, reviewer, task string, accepted bool) error {
	step, a, c := 0.05, 0, 1
	if accepted {
		step, a, c = -0.01, 1, 0
	}
	_, err := db.pool.Exec(ctx, `INSERT INTO calibration (reviewer, task, threshold, accepted, corrected) VALUES ($1, $2, LEAST(0.99, GREATEST(0.5, $3::float8 + $4)), $5, $6)
ON CONFLICT (reviewer, task) DO UPDATE SET threshold = LEAST(0.99, GREATEST(0.5, calibration.threshold + $4)),
accepted = calibration.accepted + $5, corrected = calibration.corrected + $6`, reviewer, task, DefaultThreshold, step, a, c)
	return err
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

// Listen calls fn with the stream id of each post on any replica. It
// reconnects after errors and returns when ctx ends.
func (db *DB) Listen(ctx context.Context, fn func(stream string), onError func(error)) {
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
