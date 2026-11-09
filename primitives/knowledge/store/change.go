package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// ChangeInput is a validated change request (docs/knowledge.md, "Changes").
type ChangeInput struct {
	ID        string
	Operation string // fix, update, retract
	Set       map[string]string
	At        *uint64
	Erase     bool
	Reason    *string
	Utterance *string
}

// fields that a fix can set, by record type. "property." is a prefix.
var fixable = map[string][]string{
	"entity":    {"label", "kind", "summary", "aliases", "property."},
	"claim":     {"relation", "object", "value", "valid_from_ms", "valid_to_ms", "confidence"},
	"episode":   {"title", "summary", "outcome", "ended_ms"},
	"procedure": {"goal", "steps", "status"},
	"evidence":  {"stance", "note"},
}

func allowed(t, key string) bool {
	for _, f := range fixable[t] {
		if key == f || strings.HasSuffix(f, ".") && strings.HasPrefix(key, f) && len(key) > len(f) {
			return true
		}
	}
	return false
}

// Change does a fix, an update, or a retraction of a record that w owns.
func (s *Store) Change(ctx context.Context, w Who, in ChangeInput) (api.ChangeResponse, error) {
	var out api.ChangeResponse
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		t, err := s.TypeOf(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		owners, _, gone, err := s.ownersOf(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		if gone || !w.owns(owners) {
			return ErrNotFound
		}
		for k := range in.Set {
			if !allowed(t, k) {
				return invalid("%s cannot set %q", t, k)
			}
		}
		var id string
		switch in.Operation {
		case "fix":
			if len(in.Set) == 0 {
				return invalid("fix needs set")
			}
			id, err = s.fix(ctx, tx, w, t, owners, in)
		case "update":
			if t != "claim" {
				return invalid("update is for claims; use fix for a %s", t)
			}
			id, err = s.update(ctx, tx, w, owners, in)
		case "retract":
			id, err = s.retract(ctx, tx, w, t, owners, in)
		default:
			return invalid("operation must be fix, update, or retract")
		}
		if err != nil {
			return err
		}
		out.Change, err = scanChange(tx.QueryRow(ctx, `SELECT `+changeCols+` FROM changes h WHERE id = $1`, id))
		if err != nil || in.Operation == "retract" {
			return err
		}
		all := Who{All: true}
		switch t {
		case "entity":
			e, err := s.Entity(ctx, tx, all, in.ID)
			out.Entity = &e
			return err
		case "claim":
			// After an update, the response is the claim that replaces it.
			id := in.ID
			if in.Operation == "update" {
				tx.QueryRow(ctx, `SELECT replaced_by FROM claims WHERE id = $1`, in.ID).Scan(&id)
			}
			c, err := s.Claim(ctx, tx, all, id)
			out.Claim = &c
			return err
		case "episode":
			e, err := s.Episode(ctx, tx, all, in.ID)
			out.Episode = &e
			return err
		case "procedure":
			p, err := s.Procedure(ctx, tx, all, in.ID)
			out.Procedure = &p
			return err
		}
		return nil
	})
	return out, notFound(err)
}

func columnOf(t string) string {
	return map[string]string{"entity": "entities", "claim": "claims", "episode": "episodes", "procedure": "procedures", "evidence": "evidence"}[t]
}

// fix changes the fields in place and writes the old values to the history.
func (s *Store) fix(ctx context.Context, tx pgx.Tx, w Who, t string, owners []string, in ChangeInput) (string, error) {
	table := columnOf(t)
	before, after := map[string]any{}, map[string]any{}
	props := map[string]any{}
	for _, k := range sortedKeys(in.Set) {
		v := in.Set[k]
		if name, ok := strings.CutPrefix(k, "property."); ok {
			var old *string
			tx.QueryRow(ctx, `SELECT properties->>$2 FROM entities WHERE id = $1`, in.ID, name).Scan(&old)
			before[k], after[k] = old, v
			if v == "" {
				props[name] = nil
			} else {
				props[name] = v
			}
			continue
		}
		var old *string
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s::text FROM %s WHERE id = $1`, k, table), in.ID).Scan(&old); err != nil {
			return "", err
		}
		before[k], after[k] = old, v
		var val any = v
		switch k {
		case "aliases":
			val = Set(strings.Split(v, ","))
		case "valid_from_ms", "valid_to_ms", "ended_ms":
			if v == "" {
				val = nil
			} else if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				val = n
			} else {
				return "", invalid("%s must be a number", k)
			}
		case "confidence":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f > 1 {
				return "", invalid("confidence must be from 0 to 1")
			}
			val = f
		case "object", "value":
			// A claim has an object or a value: setting one clears the other.
			other := map[string]string{"object": "value", "value": "object"}[k]
			var o *string
			tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM claims WHERE id = $1`, other), in.ID).Scan(&o)
			if o != nil {
				before[other], after[other] = o, nil
			}
			if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE claims SET %s = NULL WHERE id = $1`, other), in.ID); err != nil {
				return "", err
			}
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $2 WHERE id = $1`, table, k), in.ID, val); err != nil {
			return "", err
		}
	}
	if len(props) > 0 {
		b, _ := json.Marshal(props)
		if _, err := tx.Exec(ctx, `UPDATE entities SET properties = jsonb_strip_nulls(properties || $2::jsonb) WHERE id = $1`, in.ID, b); err != nil {
			return "", err
		}
	}
	if t != "evidence" {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET updated_ms = $2 WHERE id = $1`, table), in.ID, s.now()); err != nil {
			return "", err
		}
	}
	if err := s.reindex(ctx, tx, t, in.ID); err != nil {
		return "", err
	}
	if t == "claim" {
		if err := s.markStale(ctx, tx, []string{in.ID}); err != nil {
			return "", err
		}
		// The wrong object can be an entity that exists only because of
		// this claim ("Tucson" of "Dana lives in Tucson"). Remove it too:
		// else the error stays in the store as a thing with no facts.
		if old, ok := before["object"].(*string); ok && old != nil {
			if gone, err := s.dropOrphan(ctx, tx, *old, owners); err != nil {
				return "", err
			} else if gone {
				after["removed"] = []string{*old}
			}
		}
	}
	id, _, err := s.change(ctx, tx, in.ID, "fix", w, owners, in.Reason, in.Utterance, before, after)
	return id, err
}

// dropOrphan retracts entity id when it has the same owners, no external
// reference, no merged entity, no summary, no current claim, and no episode
// or procedure. It reports whether it retracted the entity.
func (s *Store) dropOrphan(ctx context.Context, tx pgx.Tx, id string, owners []string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE entities e SET status = 'retracted', updated_ms = $3
		WHERE e.id = $1 AND e.status = 'current' AND e.owners = $2 AND e.ext_system IS NULL AND e.summary = ''
		  AND NOT EXISTS (SELECT 1 FROM claims c WHERE (c.subject = e.id OR c.object = e.id) AND c.status <> 'retracted')
		  AND NOT EXISTS (SELECT 1 FROM entities m WHERE m.merged_into = e.id)
		  AND NOT EXISTS (SELECT 1 FROM episodes p WHERE e.id = ANY(p.entities))
		  AND NOT EXISTS (SELECT 1 FROM procedures p WHERE e.id = ANY(p.entities))`, id, owners, s.now())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) reindex(ctx context.Context, tx pgx.Tx, t, id string) error {
	var err error
	switch t {
	case "entity":
		err = s.refreshEntity(ctx, tx, id)
	case "claim":
		err = s.refreshClaim(ctx, tx, id)
	case "episode":
		_, err = tx.Exec(ctx, `UPDATE episodes SET search_text = concat_ws(' ', kind, title, summary) WHERE id = $1`, id)
	case "procedure":
		_, err = tx.Exec(ctx, `UPDATE procedures SET search_text = concat_ws(' ', replace(name, '-', ' '), goal, steps) WHERE id = $1`, id)
	}
	return err
}

// markStale marks the current claims derived from ids.
func (s *Store) markStale(ctx context.Context, tx pgx.Tx, ids []string) error {
	_, err := tx.Exec(ctx, `UPDATE claims SET stale = true WHERE derived_from && $1 AND status = 'current'`, ids)
	return err
}

// update ends a claim and makes the claim that replaces it.
func (s *Store) update(ctx context.Context, tx pgx.Tx, w Who, owners []string, in ChangeInput) (string, error) {
	at := s.now()
	if in.At != nil {
		at = *in.At
	}
	var c api.Claim
	c, err := scanClaim(tx.QueryRow(ctx, `SELECT `+claimCols+` FROM claims c WHERE id = $1 FOR UPDATE`, in.ID))
	if err != nil {
		return "", err
	}
	if c.Status != "current" {
		return "", invalid("only a current claim can have an update")
	}
	n := c
	n.Id, n.ValidFromMs, n.ValidToMs = NewID("clm_"), &at, nil
	for k, v := range in.Set {
		switch k {
		case "relation":
			n.Relation = v
		case "object":
			n.Object, n.Value = &v, nil
		case "value":
			n.Value, n.Object = &v, nil
		case "valid_from_ms", "valid_to_ms":
			x, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return "", invalid("%s must be a number", k)
			}
			if k == "valid_from_ms" {
				n.ValidFromMs = &x
			} else {
				n.ValidToMs = &x
			}
		case "confidence":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f > 1 {
				return "", invalid("confidence must be from 0 to 1")
			}
			n.Confidence = f
		}
	}
	if n.Object != nil && *n.Object == c.Subject {
		return "", invalid("a claim cannot have its subject as object")
	}
	if n.Relation == c.Relation && ptrEq(n.Object, c.Object) && ptrEq(n.Value, c.Value) {
		return "", invalid("the update does not change the claim; use fix to change only the time or the confidence")
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `INSERT INTO claims (id, subject, relation, object, value, valid_from_ms, valid_to_ms, confidence, authored_by, owners, readers, created_ms, updated_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)`,
		n.Id, n.Subject, n.Relation, n.Object, n.Value, n.ValidFromMs, n.ValidToMs, n.Confidence, c.AuthoredBy, c.Owners, c.Readers, now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE claims SET status = 'ended', valid_to_ms = $2, replaced_by = $3, updated_ms = $4 WHERE id = $1`, c.Id, at, n.Id, now); err != nil {
		return "", err
	}
	if err := s.refreshClaim(ctx, tx, n.Id); err != nil {
		return "", err
	}
	if in.Utterance != nil {
		if err := s.addEvidence(ctx, tx, n.Id, owners, api.Ref{Kind: "utterance", Ref: *in.Utterance}, "supports", nil); err != nil {
			return "", err
		}
	}
	if err := s.markStale(ctx, tx, []string{c.Id}); err != nil {
		return "", err
	}
	if _, _, err := s.change(ctx, tx, n.Id, "create", w, owners, in.Reason, in.Utterance, nil, map[string]any{"replaces": c.Id}); err != nil {
		return "", err
	}
	id, _, err := s.change(ctx, tx, c.Id, "update", w, owners, in.Reason, in.Utterance,
		map[string]any{"relation": c.Relation, "object": c.Object, "value": c.Value},
		map[string]any{"replaced_by": n.Id, "valid_to_ms": at, "relation": n.Relation, "object": n.Object, "value": n.Value})
	return id, err
}

func ptrEq(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || strings.EqualFold(*a, *b))
}

// retract removes a record from all queries. An entity takes its claims
// with it. With erase, the rows are deleted and the history keeps no
// content of them.
func (s *Store) retract(ctx context.Context, tx pgx.Tx, w Who, t string, owners []string, in ChangeInput) (string, error) {
	ids := []string{in.ID}
	if t == "entity" {
		rows, err := tx.Query(ctx, `SELECT id FROM claims WHERE (subject = $1 OR object = $1) AND status <> 'retracted'`, in.ID)
		if err != nil {
			return "", err
		}
		cs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return "", err
		}
		ids = append(ids, cs...)
	}
	now := s.now()
	claims := ids
	if t == "entity" {
		claims = ids[1:]
	}
	if t == "claim" || t == "entity" {
		if err := s.markStale(ctx, tx, claims); err != nil {
			return "", err
		}
	}
	after := map[string]any{"records": ids, "erased": in.Erase}
	if in.Erase {
		for _, q := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM evidence WHERE subject = ANY($1)`, ids},
			{`DELETE FROM claims WHERE id = ANY($1)`, ids},
			{`UPDATE changes SET before = NULL, after = NULL WHERE record = ANY($1)`, ids},
			{`UPDATE episodes SET entities = array_remove(entities, $1) WHERE $1 = ANY(entities)`, in.ID},
			{`UPDATE procedures SET entities = array_remove(entities, $1) WHERE $1 = ANY(entities)`, in.ID},
			{`DELETE FROM ` + columnOf(t) + ` WHERE id = $1`, in.ID},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.arg); err != nil {
				return "", err
			}
		}
	} else {
		var err error
		switch t {
		case "procedure", "evidence":
			_, err = tx.Exec(ctx, `UPDATE `+columnOf(t)+` SET retracted = true WHERE id = $1`, in.ID)
		default:
			_, err = tx.Exec(ctx, `UPDATE `+columnOf(t)+` SET status = 'retracted', updated_ms = $2 WHERE id = $1`, in.ID, now)
		}
		if err != nil {
			return "", err
		}
		if len(claims) > 0 && t == "entity" {
			if _, err := tx.Exec(ctx, `UPDATE claims SET status = 'retracted', updated_ms = $2 WHERE id = ANY($1)`, claims, now); err != nil {
				return "", err
			}
		}
		// The object of a retracted claim can exist only because of it
		// ("Arcadia Climbing Gym" of "me member-of Arcadia Climbing Gym").
		// Remove it too, as a fix does.
		if t == "claim" {
			var obj *string
			if err := tx.QueryRow(ctx, `SELECT object FROM claims WHERE id = $1`, in.ID).Scan(&obj); err != nil {
				return "", err
			}
			if obj != nil {
				if gone, err := s.dropOrphan(ctx, tx, *obj, owners); err != nil {
					return "", err
				} else if gone {
					after["removed"] = []string{*obj}
				}
			}
		}
	}
	id, _, err := s.change(ctx, tx, in.ID, "retract", w, owners, in.Reason, in.Utterance, nil, after)
	return id, err
}

const changeCols = `h.id, h.record, h.operation, h.actor, h.reason, h.before, h.after, h.utterance, h.at_ms`

func scanChange(r pgx.Row) (api.Change, error) {
	var c api.Change
	err := r.Scan(&c.Id, &c.Record, &c.Operation, &c.Actor, &c.Reason, &c.Before, &c.After, &c.Utterance, &c.AtMs)
	return c, notFound(err)
}

// merged is the "after" of a merge change: what moved, so that unmerge can
// move it back.
type merged struct {
	Keep          string   `json:"keep"`
	Merge         string   `json:"merge"`
	ClaimsSubject []string `json:"claims_subject"`
	ClaimsObject  []string `json:"claims_object"`
	Episodes      []string `json:"episodes"`
	Procedures    []string `json:"procedures"`
	Evidence      []string `json:"evidence"`
	Aliases       []string `json:"aliases"`
	Summary       bool     `json:"summary"`
	Properties    []string `json:"properties"`
}

func ids(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Merge moves the claims, episodes, procedures, and evidence of entity
// merge to entity keep. Both must be current, owned by w, and have the
// same owners.
func (s *Store) Merge(ctx context.Context, w Who, keep, merge string, reason, utterance *string) (api.Change, error) {
	var out api.Change
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if keep == merge {
			return invalid("keep and merge are the same entity")
		}
		var ko, mo []string
		var klabel, mlabel, ksum, msum string
		var kal, mal []string
		var kprops, mprops map[string]string
		if err := tx.QueryRow(ctx, `SELECT owners, label, aliases, summary, properties FROM entities WHERE id = $1 AND status = 'current' FOR UPDATE`, keep).Scan(&ko, &klabel, &kal, &ksum, &kprops); err != nil {
			return notFound(err)
		}
		if err := tx.QueryRow(ctx, `SELECT owners, label, aliases, summary, properties FROM entities WHERE id = $1 AND status = 'current' FOR UPDATE`, merge).Scan(&mo, &mlabel, &mal, &msum, &mprops); err != nil {
			return notFound(err)
		}
		if !w.owns(ko) || !w.owns(mo) {
			return ErrNotFound
		}
		if !slices.Equal(ko, mo) {
			return invalid("the two entities have different owners")
		}
		m := merged{Keep: keep, Merge: merge}
		var err error
		if m.ClaimsSubject, err = ids(ctx, tx, `UPDATE claims SET subject = $2 WHERE subject = $1 AND object IS DISTINCT FROM $2 RETURNING id`, merge, keep); err != nil {
			return err
		}
		if m.ClaimsObject, err = ids(ctx, tx, `UPDATE claims SET object = $2 WHERE object = $1 AND subject <> $2 RETURNING id`, merge, keep); err != nil {
			return err
		}
		if m.Episodes, err = ids(ctx, tx, `UPDATE episodes SET entities = array_append(array_remove(entities, $1), $2) WHERE $1 = ANY(entities) RETURNING id`, merge, keep); err != nil {
			return err
		}
		if m.Procedures, err = ids(ctx, tx, `UPDATE procedures SET entities = array_append(array_remove(entities, $1), $2) WHERE $1 = ANY(entities) RETURNING id`, merge, keep); err != nil {
			return err
		}
		if m.Evidence, err = ids(ctx, tx, `UPDATE evidence v SET subject = $2 WHERE subject = $1
			AND NOT EXISTS (SELECT 1 FROM evidence x WHERE x.subject = $2 AND x.ref_kind = v.ref_kind AND x.ref = v.ref) RETURNING id`, merge, keep); err != nil {
			return err
		}
		for _, a := range append([]string{mlabel}, mal...) {
			if !strings.EqualFold(a, klabel) && !slices.ContainsFunc(kal, func(x string) bool { return strings.EqualFold(x, a) }) {
				m.Aliases = append(m.Aliases, a)
				kal = append(kal, a)
			}
		}
		if ksum == "" && msum != "" {
			m.Summary, ksum = true, msum
		}
		for k, v := range mprops {
			if _, ok := kprops[k]; !ok {
				m.Properties, kprops[k] = append(m.Properties, k), v
			}
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE entities SET aliases = $2, summary = $3, properties = $4, updated_ms = $5 WHERE id = $1`, keep, Set(kal), ksum, kprops, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE entities SET status = 'merged', merged_into = $2, updated_ms = $3 WHERE id = $1`, merge, keep, now); err != nil {
			return err
		}
		if err := s.refreshEntity(ctx, tx, keep); err != nil {
			return err
		}
		id, _, err := s.change(ctx, tx, merge, "merge", w, ko, reason, utterance, nil, m)
		if err != nil {
			return err
		}
		out, err = scanChange(tx.QueryRow(ctx, `SELECT `+changeCols+` FROM changes h WHERE id = $1`, id))
		return err
	})
	return out, notFound(err)
}

// Unmerge reverts the merge of change id.
func (s *Store) Unmerge(ctx context.Context, w Who, change string) (api.Change, error) {
	var out api.Change
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var op string
		var after *string
		var owners []string
		if err := tx.QueryRow(ctx, `SELECT operation, after, owners FROM changes WHERE id = $1 FOR UPDATE`, change).Scan(&op, &after, &owners); err != nil {
			return notFound(err)
		}
		if op != "merge" || after == nil || !w.owns(owners) {
			return ErrNotFound
		}
		var m merged
		if err := json.Unmarshal([]byte(*after), &m); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM entities WHERE id = $1 FOR UPDATE`, m.Merge).Scan(&status); err != nil {
			return notFound(err)
		}
		if status != "merged" {
			return invalid("the entity is not merged")
		}
		for _, q := range []struct {
			sql string
			ids []string
		}{
			{`UPDATE claims SET subject = $2 WHERE id = ANY($1) AND subject = $3`, m.ClaimsSubject},
			{`UPDATE claims SET object = $2 WHERE id = ANY($1) AND object = $3`, m.ClaimsObject},
			{`UPDATE episodes SET entities = array_append(array_remove(entities, $3), $2) WHERE id = ANY($1)`, m.Episodes},
			{`UPDATE procedures SET entities = array_append(array_remove(entities, $3), $2) WHERE id = ANY($1)`, m.Procedures},
			{`UPDATE evidence SET subject = $2 WHERE id = ANY($1) AND subject = $3`, m.Evidence},
		} {
			if _, err := tx.Exec(ctx, q.sql, nonNil(q.ids), m.Merge, m.Keep); err != nil {
				return err
			}
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE entities SET aliases = ARRAY(SELECT a FROM unnest(aliases) a WHERE NOT a = ANY($2)),
			summary = CASE WHEN $3 THEN '' ELSE summary END, properties = properties - $4::text[], updated_ms = $5 WHERE id = $1`,
			m.Keep, nonNil(m.Aliases), m.Summary, nonNil(m.Properties), now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE entities SET status = 'current', merged_into = NULL, updated_ms = $2 WHERE id = $1`, m.Merge, now); err != nil {
			return err
		}
		for _, e := range []string{m.Keep, m.Merge} {
			if err := s.refreshEntity(ctx, tx, e); err != nil {
				return err
			}
		}
		id, _, err := s.change(ctx, tx, m.Merge, "unmerge", w, owners, nil, nil, nil, map[string]any{"merge_change": change})
		if err != nil {
			return err
		}
		out, err = scanChange(tx.QueryRow(ctx, `SELECT `+changeCols+` FROM changes h WHERE id = $1`, id))
		return err
	})
	return out, notFound(err)
}
