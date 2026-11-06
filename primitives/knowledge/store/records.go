package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// entitySearch and claimSearch compute search_text in an UPDATE of the
// table (claims with the alias c).
const entitySearch = `concat_ws(' ', kind, label, array_to_string(aliases, ' '), summary,
	(SELECT string_agg(value, ' ') FROM jsonb_each_text(properties)))`
const claimSearch = `concat_ws(' ',
	(SELECT label || ' ' || array_to_string(aliases, ' ') FROM entities WHERE id = c.subject),
	replace(c.relation, '-', ' '),
	(SELECT label || ' ' || array_to_string(aliases, ' ') FROM entities WHERE id = c.object),
	c.value)`

const entityCols = `e.id, e.kind, e.label, e.aliases, e.summary, e.properties, e.ext_system, e.ext_locator, e.ext_context,
	e.owners, e.readers, e.confidence, e.merged_into, e.created_ms, e.updated_ms`
const claimCols = `c.id, c.subject, c.relation, c.object, c.value, c.valid_from_ms, c.valid_to_ms, c.status, c.replaced_by,
	c.confidence, c.authored_by, c.derived_from, c.stale,
	(c.authored_by IS NOT NULL OR EXISTS (SELECT 1 FROM evidence v WHERE v.subject = c.id AND NOT v.retracted AND v.stance = 'supports')),
	c.owners, c.readers, c.created_ms, c.updated_ms`
const episodeCols = `p.id, p.kind, p.title, p.summary, p.started_ms, p.ended_ms, p.participants, p.entities, p.refs, p.procedure,
	p.outcome, p.owners, p.readers, p.created_ms, p.updated_ms`
const procedureCols = `r.id, r.name, r.goal, r.inputs, r.steps, r.definition, r.revision, r.status, r.entities, r.owners, r.readers,
	r.created_ms, r.updated_ms,
	(SELECT coalesce(jsonb_object_agg(o, n), '{}') FROM (SELECT coalesce(outcome, 'unknown') o, count(*) n FROM episodes
		WHERE procedure = r.id AND status = 'current' GROUP BY 1) u)`
const evidenceCols = `v.id, v.subject, v.ref_kind, v.ref, v.ref_title, v.stance, v.note, v.created_ms`

func scanEntity(r pgx.Row) (api.Entity, error) {
	var e api.Entity
	var sys, loc, xctx *string
	err := r.Scan(&e.Id, &e.Kind, &e.Label, &e.Aliases, &e.Summary, &e.Properties, &sys, &loc, &xctx,
		&e.Owners, &e.Readers, &e.Confidence, &e.MergedInto, &e.CreatedMs, &e.UpdatedMs)
	if sys != nil && loc != nil {
		e.External = &api.ExternalRef{System: *sys, Locator: *loc, Context: xctx}
	}
	if e.Properties == nil {
		e.Properties = map[string]string{}
	}
	return e, notFound(err)
}

func scanClaim(r pgx.Row) (api.Claim, error) {
	var c api.Claim
	err := r.Scan(&c.Id, &c.Subject, &c.Relation, &c.Object, &c.Value, &c.ValidFromMs, &c.ValidToMs, &c.Status, &c.ReplacedBy,
		&c.Confidence, &c.AuthoredBy, &c.DerivedFrom, &c.Stale, &c.Supported, &c.Owners, &c.Readers, &c.CreatedMs, &c.UpdatedMs)
	return c, notFound(err)
}

func scanEpisode(r pgx.Row) (api.Episode, error) {
	var p api.Episode
	var refs []byte
	err := r.Scan(&p.Id, &p.Kind, &p.Title, &p.Summary, &p.StartedMs, &p.EndedMs, &p.Participants, &p.Entities, &refs, &p.Procedure,
		&p.Outcome, &p.Owners, &p.Readers, &p.CreatedMs, &p.UpdatedMs)
	p.Refs = []api.Ref{}
	if err == nil {
		err = json.Unmarshal(refs, &p.Refs)
	}
	return p, notFound(err)
}

func scanProcedure(r pgx.Row) (api.Procedure, error) {
	var p api.Procedure
	var uses map[string]uint64
	err := r.Scan(&p.Id, &p.Name, &p.Goal, &p.Inputs, &p.Steps, &p.Definition, &p.Revision, &p.Status, &p.Entities, &p.Owners, &p.Readers,
		&p.CreatedMs, &p.UpdatedMs, &uses)
	p.Uses = uses
	if p.Uses == nil {
		p.Uses = map[string]uint64{}
	}
	if p.Inputs == nil {
		p.Inputs = map[string]string{}
	}
	return p, notFound(err)
}

func scanEvidence(r pgx.Row) (api.Evidence, error) {
	var v api.Evidence
	err := r.Scan(&v.Id, &v.Subject, &v.Ref.Kind, &v.Ref.Ref, &v.Ref.Title, &v.Stance, &v.Note, &v.CreatedMs)
	return v, notFound(err)
}

// ended sets the status "ended" on current claims whose valid_to_ms is past.
func (s *Store) ended(cs []api.Claim) []api.Claim {
	now := s.now()
	for i, c := range cs {
		if c.Status == "current" && c.ValidToMs != nil && *c.ValidToMs <= now {
			cs[i].Status = "ended"
		}
	}
	return cs
}

func (s *Store) refreshEntity(ctx context.Context, tx querier, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE entities SET search_text = `+entitySearch+` WHERE id = $1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE claims c SET search_text = `+claimSearch+` WHERE c.subject = $1 OR c.object = $1`, id)
	return err
}

func (s *Store) refreshClaim(ctx context.Context, tx querier, id string) error {
	_, err := tx.Exec(ctx, `UPDATE claims c SET search_text = `+claimSearch+` WHERE c.id = $1`, id)
	return err
}

// Entity returns a visible entity. A merged entity gives the entity that it
// was merged into.
func (s *Store) Entity(ctx context.Context, db querier, w Who, id string) (api.Entity, error) {
	for range 4 {
		args := []any{id}
		e, err := scanEntity(db.QueryRow(ctx, `SELECT `+entityCols+` FROM entities e WHERE e.id = $1 AND e.status IN ('current', 'merged') AND `+access(w, "e", false, &args), args...))
		if err != nil || e.MergedInto == nil {
			return e, err
		}
		id = *e.MergedInto
	}
	return api.Entity{}, ErrNotFound
}

// Entities returns the visible current entities of ids, in no order.
func (s *Store) Entities(ctx context.Context, db querier, w Who, ids []string) ([]api.Entity, error) {
	args := []any{Set(ids)}
	return collect(ctx, db, scanEntity, `SELECT `+entityCols+` FROM entities e WHERE e.id = ANY($1) AND e.status = 'current' AND `+access(w, "e", false, &args), args...)
}

// Claim returns a visible claim that is not retracted.
func (s *Store) Claim(ctx context.Context, db querier, w Who, id string) (api.Claim, error) {
	args := []any{id}
	c, err := scanClaim(db.QueryRow(ctx, `SELECT `+claimCols+` FROM claims c WHERE c.id = $1 AND c.status <> 'retracted' AND `+access(w, "c", false, &args), args...))
	if err == nil {
		c = s.ended([]api.Claim{c})[0]
	}
	return c, err
}

func (s *Store) Episode(ctx context.Context, db querier, w Who, id string) (api.Episode, error) {
	args := []any{id}
	return scanEpisode(db.QueryRow(ctx, `SELECT `+episodeCols+` FROM episodes p WHERE p.id = $1 AND p.status = 'current' AND `+access(w, "p", false, &args), args...))
}

func (s *Store) Procedure(ctx context.Context, db querier, w Who, id string) (api.Procedure, error) {
	args := []any{id}
	return scanProcedure(db.QueryRow(ctx, `SELECT `+procedureCols+` FROM procedures r WHERE r.id = $1 AND NOT r.retracted AND `+access(w, "r", false, &args), args...))
}

func (s *Store) EvidenceRecord(ctx context.Context, db querier, w Who, id string) (api.Evidence, error) {
	args := []any{id}
	return scanEvidence(db.QueryRow(ctx, `SELECT `+evidenceCols+` FROM evidence v WHERE v.id = $1 AND NOT v.retracted AND `+access(w, "v", true, &args),
		args...))
}

// EntityInput is a validated put-entity request.
type EntityInput struct {
	Kind, Label string
	Aliases     []string
	Summary     *string
	Properties  map[string]string
	External    *api.ExternalRef
	Owners      []string
	Readers     []string
	Confidence  *float64
	Utterance   *string
}

// PutEntity returns the entity with the same owners and external reference,
// or with the same owners, kind, and label or alias (ignoring case), and
// adds the absent fields and the new aliases. Otherwise it makes one. A
// matching entity of kind "thing" takes the new kind.
func (s *Store) PutEntity(ctx context.Context, w Who, in EntityInput) (api.Entity, error) {
	in.Owners, in.Readers = Set(in.Owners), Set(in.Readers)
	var out api.Entity
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var id string
		var err error
		if x := in.External; x != nil {
			err = tx.QueryRow(ctx, `SELECT id FROM entities WHERE owners = $1 AND ext_system = $2 AND ext_locator = $3 AND status = 'current' LIMIT 1 FOR UPDATE`,
				in.Owners, x.System, x.Locator).Scan(&id)
		}
		if id == "" {
			err = tx.QueryRow(ctx, `SELECT id FROM entities WHERE owners = $1 AND (kind = $2 OR kind = 'thing' OR $2 = 'thing') AND status = 'current'
				AND (lower(label) = lower($3) OR lower($3) = ANY(SELECT lower(a) FROM unnest(aliases) a))
				ORDER BY (kind = $2) DESC, created_ms LIMIT 1 FOR UPDATE`, in.Owners, in.Kind, in.Label).Scan(&id)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		now := s.now()
		var props any
		if in.Properties != nil {
			props = in.Properties
		}
		var sys, loc, xctx *string
		if in.External != nil {
			sys, loc, xctx = &in.External.System, &in.External.Locator, in.External.Context
		}
		if id != "" {
			_, err = tx.Exec(ctx, `UPDATE entities SET
				kind = CASE WHEN kind = 'thing' THEN $2 ELSE kind END,
				aliases = ARRAY(SELECT DISTINCT a FROM unnest(aliases || $3::text[]) a WHERE lower(a) <> lower(label)),
				summary = CASE WHEN summary = '' THEN coalesce($4, '') ELSE summary END,
				properties = coalesce($5::jsonb, '{}') || properties,
				ext_system = coalesce(ext_system, $6), ext_locator = coalesce(ext_locator, $7), ext_context = coalesce(ext_context, $8),
				readers = ARRAY(SELECT DISTINCT r FROM unnest(readers || $9::text[]) r ORDER BY r),
				updated_ms = $10 WHERE id = $1`, id, in.Kind, nonNil(in.Aliases), in.Summary, props, sys, loc, xctx, in.Readers, now)
		} else {
			id = NewID("ent_")
			conf := 1.0
			if in.Confidence != nil {
				conf = *in.Confidence
			}
			_, err = tx.Exec(ctx, `INSERT INTO entities (id, kind, label, aliases, summary, properties, ext_system, ext_locator, ext_context, owners, readers, confidence, created_ms, updated_ms)
				VALUES ($1, $2, $3, $4, coalesce($5, ''), coalesce($6::jsonb, '{}'), $7, $8, $9, $10, $11, $12, $13, $13)`,
				id, in.Kind, in.Label, Set(in.Aliases), in.Summary, props, sys, loc, xctx, in.Owners, in.Readers, conf, now)
			if err == nil {
				_, _, err = s.change(ctx, tx, id, "create", w, in.Owners, nil, in.Utterance, nil, map[string]any{"kind": in.Kind, "label": in.Label})
			}
			if err == nil && in.Utterance != nil {
				err = s.addEvidence(ctx, tx, id, in.Owners, api.Ref{Kind: "utterance", Ref: *in.Utterance}, "supports", nil)
			}
		}
		if err != nil {
			return err
		}
		if err := s.refreshEntity(ctx, tx, id); err != nil {
			return err
		}
		out, err = s.Entity(ctx, tx, Who{All: true}, id)
		return err
	})
	return out, notFound(err)
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Self returns the entity of kind person with the alias "me" that only
// subject owns, and makes it when there is none.
func (s *Store) Self(ctx context.Context, w Who, subject, label string) (api.Entity, error) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id FROM entities WHERE owners = ARRAY[$1] AND kind = 'person' AND 'me' = ANY(aliases) AND status = 'current' ORDER BY created_ms LIMIT 1`, subject).Scan(&id)
	if err == nil {
		return s.Entity(ctx, s.DB, Who{All: true}, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return api.Entity{}, err
	}
	if label == "" {
		label = strings.TrimPrefix(subject, "user:")
	}
	id = NewID("ent_")
	now := s.now()
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO entities (id, kind, label, aliases, owners, confidence, created_ms, updated_ms)
			VALUES ($1, 'person', $2, ARRAY['me', 'myself'], ARRAY[$3], 1, $4, $4)`, id, label, subject, now); err != nil {
			return err
		}
		if _, _, err := s.change(ctx, tx, id, "create", w, []string{subject}, nil, nil, nil, map[string]any{"kind": "person", "label": label, "self": true}); err != nil {
			return err
		}
		return s.refreshEntity(ctx, tx, id)
	})
	if err != nil {
		return api.Entity{}, err
	}
	return s.Entity(ctx, s.DB, Who{All: true}, id)
}

// ClaimInput is a validated assert request. Subject and Object are visible
// entities.
type ClaimInput struct {
	Subject, Relation  string
	Object, Value      *string
	ValidFrom, ValidTo *uint64
	Confidence         *float64
	AuthoredBy         *string
	DerivedFrom        []string
	Evidence           []api.Ref
	Owners, Readers    []string
	Utterance          *string
}

// Assert returns the current claim with the same owners, subject, relation,
// and object or value (ignoring case), with the new evidence. Otherwise it
// makes one.
func (s *Store) Assert(ctx context.Context, w Who, in ClaimInput) (api.Claim, error) {
	in.Owners, in.Readers = Set(in.Owners), Set(in.Readers)
	var id string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id FROM claims WHERE owners = $1 AND subject = $2 AND relation = $3 AND status = 'current'
			AND (object = $4 OR lower(value) = lower($5)) LIMIT 1 FOR UPDATE`, in.Owners, in.Subject, in.Relation, in.Object, in.Value).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if id == "" {
			id = NewID("clm_")
			conf := 1.0
			if in.Confidence != nil {
				conf = *in.Confidence
			}
			now := s.now()
			if _, err := tx.Exec(ctx, `INSERT INTO claims (id, subject, relation, object, value, valid_from_ms, valid_to_ms, confidence, authored_by, derived_from, owners, readers, created_ms, updated_ms)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)`,
				id, in.Subject, in.Relation, in.Object, in.Value, in.ValidFrom, in.ValidTo, conf, in.AuthoredBy, Set(in.DerivedFrom), in.Owners, in.Readers, now); err != nil {
				return err
			}
			after := map[string]any{"subject": in.Subject, "relation": in.Relation, "object": in.Object, "value": in.Value}
			if _, _, err := s.change(ctx, tx, id, "create", w, in.Owners, nil, in.Utterance, nil, after); err != nil {
				return err
			}
			if err := s.refreshClaim(ctx, tx, id); err != nil {
				return err
			}
		}
		refs := in.Evidence
		if in.Utterance != nil {
			refs = append(refs, api.Ref{Kind: "utterance", Ref: *in.Utterance})
		}
		for _, r := range refs {
			if err := s.addEvidence(ctx, tx, id, in.Owners, r, "supports", nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return api.Claim{}, notFound(err)
	}
	return s.Claim(ctx, s.DB, Who{All: true}, id)
}

func (s *Store) addEvidence(ctx context.Context, tx querier, subject string, owners []string, r api.Ref, stance string, note *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO evidence (id, subject, ref_kind, ref, ref_title, stance, note, owners, created_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (subject, ref_kind, ref) DO UPDATE SET stance = $6, note = coalesce($7, evidence.note), retracted = false`,
		NewID("evd_"), subject, r.Kind, r.Ref, r.Title, stance, note, owners, s.now())
	return err
}

// LinkEvidence adds or changes evidence of a record that w owns.
func (s *Store) LinkEvidence(ctx context.Context, w Who, subject string, r api.Ref, stance string, note *string) (api.Evidence, error) {
	owners, _, gone, err := s.ownersOf(ctx, s.DB, subject)
	if err != nil || gone || !w.owns(owners) {
		return api.Evidence{}, ErrNotFound
	}
	if err := s.addEvidence(ctx, s.DB, subject, owners, r, stance, note); err != nil {
		return api.Evidence{}, err
	}
	return scanEvidence(s.DB.QueryRow(ctx, `SELECT `+evidenceCols+` FROM evidence v WHERE subject = $1 AND ref_kind = $2 AND ref = $3`, subject, r.Kind, r.Ref))
}

// EpisodeInput is a validated record-episode request. With ID, the episode
// is changed: the given fields replace the stored ones, and lists are
// added.
type EpisodeInput struct {
	ID                     string
	Kind, Title            string
	Summary                *string
	Started, Ended         *uint64
	Participants, Entities []string
	Refs                   []api.Ref
	Procedure, Outcome     *string
	Owners, Readers        []string
}

func (s *Store) RecordEpisode(ctx context.Context, w Who, in EpisodeInput) (api.Episode, error) {
	in.Owners, in.Readers = Set(in.Owners), Set(in.Readers)
	refs := in.Refs
	if refs == nil {
		refs = []api.Ref{}
	}
	rb, _ := json.Marshal(refs)
	now := s.now()
	id := in.ID
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if id != "" {
			args := []any{id, in.Title, in.Summary, in.Ended, in.Outcome, in.Procedure, nonNil(in.Participants), nonNil(in.Entities), rb, now}
			tag, err := tx.Exec(ctx, `UPDATE episodes p SET title = coalesce(nullif($2::text, ''), title), summary = coalesce($3::text, summary),
				ended_ms = coalesce($4, ended_ms), outcome = coalesce($5, outcome), procedure = coalesce($6, procedure),
				participants = ARRAY(SELECT DISTINCT x FROM unnest(participants || $7::text[]) x),
				entities = ARRAY(SELECT DISTINCT x FROM unnest(entities || $8::text[]) x),
				refs = refs || $9::jsonb, search_text = concat_ws(' ', kind, coalesce(nullif($2, ''), title), coalesce($3, summary)), updated_ms = $10
				WHERE id = $1 AND status = 'current' AND `+access(w, "p", true, &args), args...)
			if err == nil && tag.RowsAffected() == 0 {
				return ErrNotFound
			}
			return err
		}
		id = NewID("epi_")
		started := now
		if in.Started != nil {
			started = *in.Started
		}
		summary := ""
		if in.Summary != nil {
			summary = *in.Summary
		}
		if _, err := tx.Exec(ctx, `INSERT INTO episodes (id, kind, title, summary, started_ms, ended_ms, participants, entities, refs, procedure, outcome, owners, readers, created_ms, updated_ms, search_text)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14, concat_ws(' ', $2::text, $3::text, $4::text))`,
			id, in.Kind, in.Title, summary, started, in.Ended, Set(in.Participants), Set(in.Entities), rb, in.Procedure, in.Outcome, in.Owners, in.Readers, now); err != nil {
			return err
		}
		_, _, err := s.change(ctx, tx, id, "create", w, in.Owners, nil, nil, nil, map[string]any{"kind": in.Kind, "title": in.Title})
		return err
	})
	if err != nil {
		return api.Episode{}, notFound(err)
	}
	return s.Episode(ctx, s.DB, Who{All: true}, id)
}

// ProcedureInput is a validated put-procedure request.
type ProcedureInput struct {
	Name, Goal, Steps string
	Inputs            map[string]string
	Definition        *string
	Status            string
	Entities          []string
	Owners, Readers   []string
	Reason            *string
}

// PutProcedure adds the next revision of the procedure with the same name
// and owners.
func (s *Store) PutProcedure(ctx context.Context, w Who, in ProcedureInput) (api.Procedure, error) {
	in.Owners, in.Readers = Set(in.Owners), Set(in.Readers)
	id, now := NewID("prc_"), s.now()
	inputs := in.Inputs
	if inputs == nil {
		inputs = map[string]string{}
	}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, in.Name); err != nil {
			return err
		}
		var rev uint64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision), 0) + 1 FROM procedures WHERE name = $1 AND owners = $2`, in.Name, in.Owners).Scan(&rev); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procedures (id, name, goal, inputs, steps, definition, revision, status, entities, owners, readers, created_ms, updated_ms, search_text)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12, concat_ws(' ', replace($2::text, '-', ' '), $3::text, $5::text))`,
			id, in.Name, in.Goal, inputs, in.Steps, in.Definition, rev, in.Status, Set(in.Entities), in.Owners, in.Readers, now); err != nil {
			return err
		}
		_, _, err := s.change(ctx, tx, id, "create", w, in.Owners, in.Reason, nil, nil, map[string]any{"name": in.Name, "revision": rev})
		return err
	})
	if err != nil {
		return api.Procedure{}, notFound(err)
	}
	return s.Procedure(ctx, s.DB, Who{All: true}, id)
}

// PutGroup makes a group, or changes one that w owns.
func (s *Store) PutGroup(ctx context.Context, w Who, id, label string, members, owners []string) (api.Group, error) {
	var g api.Group
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var cur []string
		err := tx.QueryRow(ctx, `SELECT owners FROM groups WHERE id = $1 FOR UPDATE`, id).Scan(&cur)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case !w.All && !slices.Contains(cur, w.Subject):
			return ErrNotFound
		}
		return tx.QueryRow(ctx, `INSERT INTO groups (id, label, members, owners, updated_ms) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (id) DO UPDATE SET label = $2, members = $3, owners = $4, updated_ms = $5
			RETURNING id, label, members, owners, updated_ms`, id, label, Set(members), Set(owners), s.now()).
			Scan(&g.Id, &g.Label, &g.Members, &g.Owners, &g.UpdatedMs)
	})
	return g, notFound(err)
}

func scanDispute(r pgx.Row) (api.Dispute, error) {
	var d api.Dispute
	err := r.Scan(&d.Id, &d.Record, &d.By, &d.Reason, &d.Proposed, &d.State, &d.Resolution, &d.Owners, &d.CreatedMs, &d.UpdatedMs)
	return d, notFound(err)
}

const disputeCols = `d.id, d.record, d.by_subject, d.reason, d.proposed, d.state, d.resolution, d.owners, d.created_ms, d.updated_ms`

// Dispute records that w does not agree with a record that w can read.
func (s *Store) Dispute(ctx context.Context, w Who, record, reason string, proposed *string) (api.Dispute, error) {
	owners, readers, gone, err := s.ownersOf(ctx, s.DB, record)
	if err != nil || gone || !w.reads(owners, readers) {
		return api.Dispute{}, ErrNotFound
	}
	now := s.now()
	var d api.Dispute
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		d, err = scanDispute(tx.QueryRow(ctx, `INSERT INTO disputes AS d (id, record, by_subject, reason, proposed, state, owners, created_ms, updated_ms)
			VALUES ($1, $2, $3, $4, $5, 'open', $6, $7, $7) RETURNING `+disputeCols, NewID("dsp_"), record, w.Subject, reason, proposed, owners, now))
		if err != nil {
			return err
		}
		_, _, err = s.change(ctx, tx, record, "dispute", w, owners, &reason, nil, nil, map[string]any{"dispute": d.Id, "proposed": proposed})
		return err
	})
	return d, err
}

// ResolveDispute closes an open dispute of a record that w owns.
func (s *Store) ResolveDispute(ctx context.Context, w Who, id string, accept bool, resolution *string) (api.Dispute, error) {
	state := map[bool]string{true: "accepted", false: "rejected"}[accept]
	args := []any{id, state, resolution, s.now()}
	return scanDispute(s.DB.QueryRow(ctx, `UPDATE disputes d SET state = $2, resolution = $3, updated_ms = $4
		WHERE id = $1 AND state = 'open' AND `+access(w, "d", true, &args)+` RETURNING `+disputeCols, args...))
}

// Disputes returns the disputes of records that w owns, and those that w made.
func (s *Store) Disputes(ctx context.Context, w Who, state string) ([]api.Dispute, error) {
	args := []any{state, w.Subject}
	return collect(ctx, s.DB, scanDispute, `SELECT `+disputeCols+` FROM disputes d WHERE ($1 = '' OR state = $1)
		AND (by_subject = $2 OR `+access(w, "d", true, &args)+`) ORDER BY created_ms DESC LIMIT 200`, args...)
}
