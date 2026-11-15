package store

// Undo of a change (risk-remediation task 3.3, requirement 9.3).
//
// Undo restores the state that a change replaced, from the before and after
// of its audit row, and writes an audit row of its own with undoes set. The
// service decides who may call it (the person, an admin, or a service that
// holds knowledge:undo); the rules here hold for every caller:
//
//   - A change that later changes depend on is refused, and the answer lists
//     them: undo the later ones first, newest first. A later change counts
//     when it has not been undone itself. Records that depend on it count too
//     (claims of a new entity, claims derived from a claim).
//   - A change is undone once. An undo, an unmerge, an assert, a dispute, and
//     a change of a group are not undone: make the change again.
//   - Undo never raises trust. A fix that lowered trust stays lowered; only
//     promote, by the person, raises it.
//   - A retraction with erase keeps no content, so it cannot be undone.
//   - Undo never reads a model: it reads the stored rows.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

type changeRow struct {
	ID, Record, Operation string
	Before, After         *string
	Owners                []string
	AtMs                  uint64
	Seq                   *int64
	Undoes                *string
}

// Undo undoes the change with id, for a record that w owns.
func (s *Store) Undo(ctx context.Context, w Who, id string, reason, utterance *string) (api.ChangeResponse, error) {
	var out api.ChangeResponse
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var c changeRow
		if err := tx.QueryRow(ctx, `SELECT id, record, operation, before, after, owners, at_ms, seq, undoes FROM changes WHERE id = $1 FOR UPDATE`, id).
			Scan(&c.ID, &c.Record, &c.Operation, &c.Before, &c.After, &c.Owners, &c.AtMs, &c.Seq, &c.Undoes); err != nil {
			return notFound(err)
		}
		if !w.owns(c.Owners) {
			return ErrNotFound
		}
		if c.Undoes != nil || c.Operation == "undo" || c.Operation == "unmerge" {
			return invalid("an undo cannot be undone; make the change again")
		}
		var by string
		if err := tx.QueryRow(ctx, `SELECT id FROM changes WHERE undoes = $1 LIMIT 1`, id).Scan(&by); err == nil {
			return invalid("this change was undone already, by %s", by)
		} else if err != pgx.ErrNoRows {
			return err
		}
		switch c.Operation {
		case "create", "fix", "update", "retract", "promote", "merge", "edit", "visibility":
		default:
			return invalid("a change with operation %s cannot be undone", c.Operation)
		}
		if c.Operation == "retract" && c.After != nil && strings.Contains(*c.After, `"erased":true`) {
			return invalid("the content of this record was erased on purpose; there is nothing to restore")
		}
		if err := s.requireNoLater(ctx, tx, c.Record, c); err != nil {
			return err
		}
		t, err := s.TypeOf(ctx, tx, c.Record)
		if err != nil {
			if err == ErrNotFound {
				return invalid("the record is gone, or it is of a type whose changes cannot be undone (groups)")
			}
			return err
		}
		trustBefore, hasTrust := s.recordTrust(ctx, tx, t, c.Record)
		var undoID string
		switch c.Operation {
		case "create":
			err = s.undoCreate(ctx, tx, t, c)
		case "fix":
			err = s.undoFix(ctx, tx, t, c)
		case "update":
			err = s.undoUpdate(ctx, tx, t, c)
		case "retract":
			err = s.undoRetract(ctx, tx, t, c)
		case "promote":
			err = s.undoPromote(ctx, tx, t, c)
		case "edit", "visibility":
			err = s.undoEdit(ctx, tx, t, c)
		case "merge":
			if t != "entity" {
				return invalid("a merge is of entities")
			}
			undoID, err = s.unmerge(ctx, tx, w, c.ID, reason)
		}
		if err != nil {
			return err
		}
		if hasTrust {
			if after, _ := s.recordTrust(ctx, tx, t, c.Record); TrustRank(after) > TrustRank(trustBefore) {
				return invalid("undo would raise the trust of the record from %s to %s; only the person can promote", trustBefore, after)
			}
		}
		if undoID == "" {
			undoID, _, err = s.changeUndoing(ctx, tx, c.Record, "undo", w, c.Owners, reason, utterance, nil,
				map[string]any{"change": c.ID, "operation": c.Operation}, c.ID)
			if err != nil {
				return err
			}
		}
		out.Change, err = scanChange(tx.QueryRow(ctx, `SELECT `+changeCols+` FROM changes h WHERE id = $1`, undoID))
		if err != nil {
			return err
		}
		all := Who{All: true}
		switch t {
		case "entity":
			if e, err := s.Entity(ctx, tx, all, c.Record); err == nil {
				out.Entity = &e
			}
		case "claim":
			if cl, err := s.Claim(ctx, tx, all, c.Record); err == nil {
				out.Claim = &cl
			}
		case "episode":
			if e, err := s.Episode(ctx, tx, all, c.Record); err == nil {
				out.Episode = &e
			}
		case "procedure":
			if p, err := s.Procedure(ctx, tx, all, c.Record); err == nil {
				out.Procedure = &p
			}
		}
		return nil
	})
	return out, notFound(err)
}

// recordTrust returns the trust of an entity or a claim.
func (s *Store) recordTrust(ctx context.Context, tx pgx.Tx, t, id string) (string, bool) {
	if t != "entity" && t != "claim" {
		return "", false
	}
	var v string
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT trust FROM %s WHERE id = $1`, columnOf(t)), id).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

// requireNoLater refuses when a change of record that is later than c has
// not been undone. Bookkeeping rows (undo, unmerge), disputes, and asserts
// do not count: they do not change what the record says.
func (s *Store) requireNoLater(ctx context.Context, tx pgx.Tx, record string, c changeRow) error {
	rows, err := tx.Query(ctx, `SELECT l.id || ' (' || l.operation || ')' FROM changes l
		WHERE l.record = $1 AND l.id <> $2 AND l.undoes IS NULL AND l.operation NOT IN ('dispute', 'assert')
		  AND NOT EXISTS (SELECT 1 FROM changes u WHERE u.undoes = l.id)
		  AND (CASE WHEN $3::bigint IS NOT NULL THEN l.seq > $3
		            ELSE (l.seq IS NOT NULL OR l.at_ms > $4 OR (l.at_ms = $4 AND l.id > $2)) END)
		ORDER BY l.seq NULLS FIRST, l.at_ms, l.id`, record, c.ID, c.Seq, c.AtMs)
	if err != nil {
		return err
	}
	later, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(later) > 0 {
		return invalid("later changes depend on this one: %s. Undo them first, newest first", strings.Join(later, ", "))
	}
	return nil
}

func dependents(what string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return invalid("%s depend on it: %s. Undo or retract them first", what, strings.Join(ids, ", "))
}

func (s *Store) undoCreate(ctx context.Context, tx pgx.Tx, t string, c changeRow) error {
	_, _, gone, err := s.ownersOf(ctx, tx, c.Record)
	if err != nil {
		return err
	}
	if gone {
		return invalid("the record is retracted already")
	}
	now := s.now()
	switch t {
	case "entity":
		for _, d := range []struct{ what, sql string }{
			{"claims", `SELECT id FROM claims WHERE (subject = $1 OR object = $1) AND status <> 'retracted'`},
			{"episodes", `SELECT id FROM episodes WHERE $1 = ANY(entities) AND status = 'current'`},
			{"procedures", `SELECT id FROM procedures WHERE $1 = ANY(entities) AND NOT retracted`},
			{"merged entities", `SELECT id FROM entities WHERE merged_into = $1`},
		} {
			found, err := ids(ctx, tx, d.sql, c.Record)
			if err != nil {
				return err
			}
			if err := dependents(d.what, found); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE entities SET status = 'retracted', updated_ms = $2 WHERE id = $1`, c.Record, now)
	case "claim":
		if c.After != nil {
			var a struct {
				Replaces string `json:"replaces"`
			}
			if json.Unmarshal([]byte(*c.After), &a) == nil && a.Replaces != "" {
				return invalid("this claim replaced %s; undo the update of %s instead", a.Replaces, a.Replaces)
			}
		}
		ds, err := ids(ctx, tx, `SELECT id FROM claims WHERE derived_from @> ARRAY[$1]::text[] AND status <> 'retracted'`, c.Record)
		if err != nil {
			return err
		}
		if err := dependents("claims derived from it", ds); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE claims SET status = 'retracted', updated_ms = $2 WHERE id = $1`, c.Record, now)
		return err
	case "episode":
		_, err = tx.Exec(ctx, `UPDATE episodes SET status = 'retracted', updated_ms = $2 WHERE id = $1`, c.Record, now)
	case "procedure":
		ds, err := ids(ctx, tx, `SELECT id FROM episodes WHERE procedure = $1 AND status = 'current'`, c.Record)
		if err != nil {
			return err
		}
		if err := dependents("episodes that used it", ds); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE procedures SET retracted = true, updated_ms = $2 WHERE id = $1`, c.Record, now)
		return err
	default:
		return invalid("the creation of a %s cannot be undone", t)
	}
	return err
}

func rawMap(text *string) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if text == nil {
		return m, nil
	}
	if err := json.Unmarshal([]byte(*text), &m); err != nil {
		return nil, invalid("the audit row cannot be read: %v", err)
	}
	return m, nil
}

func (s *Store) undoFix(ctx context.Context, tx pgx.Tx, t string, c changeRow) error {
	before, err := rawMap(c.Before)
	if err != nil {
		return err
	}
	after, err := rawMap(c.After)
	if err != nil {
		return err
	}
	if len(before) == 0 {
		return invalid("the change has no before; there is nothing to restore")
	}
	table := columnOf(t)
	id := c.Record
	if raw, ok := before["trust"]; ok {
		var want *string
		if err := json.Unmarshal(raw, &want); err != nil {
			return err
		}
		cur, _ := s.recordTrust(ctx, tx, t, id)
		if want == nil || TrustRank(*want) > TrustRank(cur) {
			return invalid("this change lowered the trust of the record; undoing it would raise it. Only the person can promote")
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET trust = $2 WHERE id = $1`, table), id, *want); err != nil {
			return err
		}
		delete(before, "trust")
	}
	// An entity that existed only for the old object was retracted by the
	// fix: bring it back first.
	if raw, ok := after["removed"]; ok {
		var removed []string
		if err := json.Unmarshal(raw, &removed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE entities SET status = 'current', updated_ms = $2 WHERE id = ANY($1) AND status = 'retracted'`, nonNil(removed), s.now()); err != nil {
			return err
		}
		for _, e := range removed {
			if err := s.refreshEntity(ctx, tx, e); err != nil {
				return err
			}
		}
	}
	_, hasObject := before["object"]
	_, hasValue := before["value"]
	if t == "claim" && (hasObject || hasValue) {
		var o, v *string
		if raw, ok := before["object"]; ok {
			if err := json.Unmarshal(raw, &o); err != nil {
				return err
			}
		}
		if raw, ok := before["value"]; ok {
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE claims SET object = $2, value = $3 WHERE id = $1`, id, o, v); err != nil {
			return err
		}
		delete(before, "object")
		delete(before, "value")
	}
	for _, k := range slices.Sorted(mapKeys(before)) {
		if !allowed(t, k) {
			return invalid("the change has a field that undo does not know: %s", k)
		}
		var v *string
		if err := json.Unmarshal(before[k], &v); err != nil {
			return invalid("the old value of %s is not text", k)
		}
		var sql string
		args := []any{id, v}
		switch {
		case strings.HasPrefix(k, "property."):
			sql = fmt.Sprintf(`UPDATE %s SET properties = CASE WHEN $3::text IS NULL THEN properties - $2::text
				ELSE properties || jsonb_build_object($2::text, $3::text) END WHERE id = $1`, table)
			args = []any{id, strings.TrimPrefix(k, "property."), v}
		case k == "aliases":
			sql = fmt.Sprintf(`UPDATE %s SET aliases = coalesce($2::text, '{}')::text[] WHERE id = $1`, table)
		case k == "valid_from_ms" || k == "valid_to_ms" || k == "ended_ms":
			sql = fmt.Sprintf(`UPDATE %s SET %s = $2::text::bigint WHERE id = $1`, table, k)
		case k == "confidence":
			sql = fmt.Sprintf(`UPDATE %s SET confidence = $2::text::double precision WHERE id = $1`, table)
		default:
			sql = fmt.Sprintf(`UPDATE %s SET %s = $2::text WHERE id = $1`, table, k)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}
	}
	if t != "evidence" {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET updated_ms = $2 WHERE id = $1`, table), id, s.now()); err != nil {
			return err
		}
	}
	if err := s.reindex(ctx, tx, t, id); err != nil {
		return err
	}
	if t == "claim" {
		return s.markStale(ctx, tx, []string{id})
	}
	return nil
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (s *Store) undoUpdate(ctx context.Context, tx pgx.Tx, t string, c changeRow) error {
	if t != "claim" {
		return invalid("an update is of a claim")
	}
	before, err := rawMap(c.Before)
	if err != nil {
		return err
	}
	var after struct {
		ReplacedBy string `json:"replaced_by"`
	}
	if c.After == nil || json.Unmarshal([]byte(*c.After), &after) != nil || after.ReplacedBy == "" {
		return invalid("the change does not name the claim that replaced it")
	}
	var status string
	var replacedBy, subject, object *string
	if err := tx.QueryRow(ctx, `SELECT status, replaced_by, subject, object FROM claims WHERE id = $1 FOR UPDATE`, c.Record).Scan(&status, &replacedBy, &subject, &object); err != nil {
		return notFound(err)
	}
	if status != "ended" || replacedBy == nil || *replacedBy != after.ReplacedBy {
		return invalid("the claim is not the one that this update ended")
	}
	var nstatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM claims WHERE id = $1 FOR UPDATE`, after.ReplacedBy).Scan(&nstatus); err != nil {
		return notFound(err)
	}
	if nstatus != "current" {
		return invalid("the claim that replaced it is %s, not current", nstatus)
	}
	// What was done to the replacement since: nothing may depend on it.
	var cid string
	var cat uint64
	var cseq *int64
	if err := tx.QueryRow(ctx, `SELECT id, at_ms, seq FROM changes WHERE record = $1 AND operation = 'create' ORDER BY seq NULLS FIRST, at_ms LIMIT 1`, after.ReplacedBy).
		Scan(&cid, &cat, &cseq); err != nil {
		return notFound(err)
	}
	if err := s.requireNoLater(ctx, tx, after.ReplacedBy, changeRow{ID: cid, AtMs: cat, Seq: cseq}); err != nil {
		return err
	}
	ds, err := ids(ctx, tx, `SELECT id FROM claims WHERE derived_from @> ARRAY[$1]::text[] AND status <> 'retracted'`, after.ReplacedBy)
	if err != nil {
		return err
	}
	if err := dependents("claims derived from the new claim", ds); err != nil {
		return err
	}
	for _, e := range []*string{subject, object} {
		if e == nil {
			continue
		}
		var es string
		if err := tx.QueryRow(ctx, `SELECT status FROM entities WHERE id = $1`, *e).Scan(&es); err != nil || es != "current" {
			return invalid("the entity %s of the claim is not current; restore it first", *e)
		}
	}
	var validTo *uint64
	if raw, ok := before["valid_to_ms"]; ok {
		if err := json.Unmarshal(raw, &validTo); err != nil {
			return err
		}
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `UPDATE claims SET status = 'current', valid_to_ms = $2, replaced_by = NULL, updated_ms = $3 WHERE id = $1`, c.Record, validTo, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE claims SET status = 'retracted', updated_ms = $2 WHERE id = $1`, after.ReplacedBy, now); err != nil {
		return err
	}
	if err := s.refreshClaim(ctx, tx, c.Record); err != nil {
		return err
	}
	return s.markStale(ctx, tx, []string{after.ReplacedBy})
}

func (s *Store) undoRetract(ctx context.Context, tx pgx.Tx, t string, c changeRow) error {
	var after struct {
		Records []string `json:"records"`
		Erased  bool     `json:"erased"`
		Removed []string `json:"removed"`
	}
	if c.After == nil || json.Unmarshal([]byte(*c.After), &after) != nil || len(after.Records) == 0 {
		return invalid("the change does not say which records it retracted")
	}
	if after.Erased {
		return invalid("the content of this record was erased on purpose; there is nothing to restore")
	}
	var before struct {
		Status map[string]string `json:"status"`
	}
	if c.Before != nil {
		_ = json.Unmarshal([]byte(*c.Before), &before)
	}
	now := s.now()
	if _, _, gone, err := s.ownersOf(ctx, tx, c.Record); err != nil {
		return err
	} else if !gone {
		return invalid("the record is not retracted")
	}
	restored := []string{}
	for _, id := range slices.Concat(after.Records, after.Removed) {
		rt, err := s.TypeOf(ctx, tx, id)
		if err != nil {
			continue // erased since, or never there
		}
		switch rt {
		case "procedure", "evidence":
			_, err = tx.Exec(ctx, `UPDATE `+columnOf(rt)+` SET retracted = false WHERE id = $1`, id)
		case "entity", "claim", "episode":
			prior := before.Status[id]
			if prior == "" || prior == "retracted" {
				prior = "current"
				if rt == "claim" {
					var rb *string
					_ = tx.QueryRow(ctx, `SELECT replaced_by FROM claims WHERE id = $1`, id).Scan(&rb)
					if rb != nil {
						prior = "ended"
					}
				}
			}
			_, err = tx.Exec(ctx, `UPDATE `+columnOf(rt)+` SET status = $2, updated_ms = $3 WHERE id = $1 AND status = 'retracted'`, id, prior, now)
		}
		if err != nil {
			return err
		}
		restored = append(restored, id)
	}
	for _, id := range restored {
		switch rt, _ := s.TypeOf(ctx, tx, id); rt {
		case "entity":
			err := s.refreshEntity(ctx, tx, id)
			if err != nil {
				return err
			}
		case "claim":
			if err := s.refreshClaim(ctx, tx, id); err != nil {
				return err
			}
		}
	}
	orphans, err := ids(ctx, tx, `SELECT c.id FROM claims c WHERE c.id = ANY($1) AND c.status <> 'retracted' AND (
		EXISTS (SELECT 1 FROM entities e WHERE e.id = c.subject AND e.status = 'retracted')
		OR EXISTS (SELECT 1 FROM entities e WHERE e.id = c.object AND e.status = 'retracted'))`, restored)
	if err != nil {
		return err
	}
	if len(orphans) > 0 {
		return invalid("the claim %s is about an entity that is retracted; restore the entity first", strings.Join(orphans, ", "))
	}
	return nil
}

func (s *Store) undoPromote(ctx context.Context, tx pgx.Tx, t string, c changeRow) error {
	before, err := rawMap(c.Before)
	if err != nil {
		return err
	}
	var want string
	if raw, ok := before["trust"]; !ok || json.Unmarshal(raw, &want) != nil || NormalizeTrustValue(want) == "" {
		return invalid("the change does not say the trust before")
	}
	cur, ok := s.recordTrust(ctx, tx, t, c.Record)
	if !ok {
		return invalid("a promotion is of an entity or a claim")
	}
	if TrustRank(want) > TrustRank(cur) {
		return invalid("undo would raise trust; only the person can promote")
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET trust = $2, updated_ms = $3 WHERE id = $1`, columnOf(t)), c.Record, want, s.now())
	return err
}

func (s *Store) undoEdit(ctx context.Context, tx pgx.Tx, t string, c changeRow) error {
	if _, ok := editColumns[t]; !ok {
		return invalid("an edit of a %s cannot be undone", t)
	}
	before, err := rawMap(c.Before)
	if err != nil {
		return err
	}
	if len(before) == 0 {
		return invalid("the change has no before; there is nothing to restore")
	}
	return s.restoreEdit(ctx, tx, t, c.Record, before)
}
