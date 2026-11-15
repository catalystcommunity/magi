package store

// Audit log of memory and knowledge changes, and undo (risk-remediation
// task 3.3, requirement 9.3).
//
// The changes table is the audit log. Each change of a record writes one row:
// the actor (the person whose record it is), via (the caller, when it is
// another subject such as service:magi), origin (what the caller says of the
// write: for Magi the persona, tool, turn, run, and whether the person
// confirmed it), the trust label of the record, the reason, the time, and
// before and after: the changed fields, enough to undo the change.
//
// Operations that write rows: create, assert (the same fact again, or
// evidence), fix, update, retract, promote, merge, unmerge, edit (an entity
// or an episode changed in place), visibility (readers of an entity, or
// members of a group), dispute, and undo.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// entitySnapshot returns the fields of an entity that a put changes in place.
func (s *Store) entitySnapshot(ctx context.Context, db querier, id string) (map[string]any, error) {
	var kind, summary string
	var aliases, readers []string
	var props map[string]string
	var sys, loc, xctx *string
	if err := db.QueryRow(ctx, `SELECT kind, aliases, summary, properties, ext_system, ext_locator, ext_context, readers FROM entities WHERE id = $1`, id).
		Scan(&kind, &aliases, &summary, &props, &sys, &loc, &xctx, &readers); err != nil {
		return nil, notFound(err)
	}
	if props == nil {
		props = map[string]string{}
	}
	return roundTrip(map[string]any{"kind": kind, "aliases": nonNil(aliases), "summary": summary, "properties": props,
		"ext_system": sys, "ext_locator": loc, "ext_context": xctx, "readers": nonNil(readers)}), nil
}

// episodeSnapshot returns the episode that w owns, locked, and its owners.
func (s *Store) episodeSnapshot(ctx context.Context, tx pgx.Tx, w Who, id string) (map[string]any, []string, error) {
	args := []any{id}
	var title, summary string
	var ended *uint64
	var outcome, procedure *string
	var participants, entities, owners []string
	var refs []byte
	if err := tx.QueryRow(ctx, `SELECT title, summary, ended_ms, outcome, procedure, participants, entities, refs, owners FROM episodes p
		WHERE p.id = $1 AND p.status = 'current' AND `+access(w, "p", true, &args)+` FOR UPDATE`, args...).
		Scan(&title, &summary, &ended, &outcome, &procedure, &participants, &entities, &refs, &owners); err != nil {
		return nil, nil, notFound(err)
	}
	var rv any
	if err := json.Unmarshal(refs, &rv); err != nil {
		return nil, nil, err
	}
	return roundTrip(map[string]any{"title": title, "summary": summary, "ended_ms": ended, "outcome": outcome, "procedure": procedure,
		"participants": nonNil(participants), "entities": nonNil(entities), "refs": rv}), owners, nil
}

// roundTrip makes v plain JSON values, so that two snapshots compare.
func roundTrip(v map[string]any) map[string]any {
	b, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// logEdit writes a change for the fields of record id of type t that differ
// from prev (a snapshot taken before). It writes nothing when none differs.
func (s *Store) logEdit(ctx context.Context, tx pgx.Tx, t, id string, w Who, owners []string, prev map[string]any, reason *string) error {
	var cur map[string]any
	var err error
	switch t {
	case "entity":
		cur, err = s.entitySnapshot(ctx, tx, id)
	case "episode":
		cur, _, err = s.episodeSnapshot(ctx, tx, Who{All: true}, id)
	default:
		return invalid("no edit of a %s", t)
	}
	if err != nil {
		return err
	}
	before, after := map[string]any{}, map[string]any{}
	for k, v := range cur {
		if string(jsonBytes(v)) != string(jsonBytes(prev[k])) {
			before[k], after[k] = prev[k], v
		}
	}
	if len(after) == 0 {
		return nil
	}
	op := "edit"
	if len(after) == 1 && after["readers"] != nil {
		op = "visibility"
	}
	_, _, err = s.change(ctx, tx, id, op, w, owners, reason, nil, before, after)
	return err
}

func jsonBytes(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// statuses returns the status of each of ids that has one (entities, claims,
// episodes). Procedures and evidence have a flag, not a status, so they are
// not in the result.
func (s *Store) statuses(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id, status FROM entities WHERE id = ANY($1)
		UNION ALL SELECT id, status FROM claims WHERE id = ANY($1)
		UNION ALL SELECT id, status FROM episodes WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// editColumns are the fields that undo restores for an edit, with the type
// of the column.
var editColumns = map[string]map[string]string{
	"entity": {"kind": "text", "aliases": "text[]", "summary": "text", "properties": "jsonb", "ext_system": "text", "ext_locator": "text",
		"ext_context": "text", "readers": "text[]"},
	"episode": {"title": "text", "summary": "text", "ended_ms": "bigint", "outcome": "text", "procedure": "text", "participants": "text[]",
		"entities": "text[]", "refs": "jsonb"},
}

// restoreEdit sets the fields of before back.
func (s *Store) restoreEdit(ctx context.Context, tx pgx.Tx, t, id string, before map[string]json.RawMessage) error {
	cols := editColumns[t]
	for k, raw := range before {
		typ, ok := cols[k]
		if !ok {
			return invalid("the change of %s has a field that undo does not know: %s", t, k)
		}
		var arg any
		null := strings.TrimSpace(string(raw)) == "null"
		switch typ {
		case "text":
			var v *string
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			arg = v
		case "text[]":
			var v []string
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			arg = nonNil(v)
		case "jsonb":
			if null {
				arg = nil
			} else {
				arg = string(raw)
			}
			if arg == nil {
				arg = map[string]string{}
			}
		case "bigint":
			if null {
				arg = nil
			} else {
				arg = string(raw)
			}
		}
		table := columnOf(t)
		sql := fmt.Sprintf(`UPDATE %s SET %s = $2 WHERE id = $1`, table, k)
		switch typ {
		case "jsonb":
			sql = fmt.Sprintf(`UPDATE %s SET %s = $2::text::jsonb WHERE id = $1`, table, k)
			if _, isMap := arg.(map[string]string); isMap {
				arg = "{}"
				if k == "refs" {
					arg = "[]"
				}
			}
		case "bigint":
			sql = fmt.Sprintf(`UPDATE %s SET %s = $2::text::bigint WHERE id = $1`, table, k)
		}
		if _, err := tx.Exec(ctx, sql, id, arg); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET updated_ms = $2 WHERE id = $1`, columnOf(t)), id, s.now()); err != nil {
		return err
	}
	return s.reindex(ctx, tx, t, id)
}
