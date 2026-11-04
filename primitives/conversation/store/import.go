package store

import (
	"context"
	"encoding/json"
	"time"

	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/jackc/pgx/v5"
)

// ImportResult counts what ImportLegacy copied.
type ImportResult struct{ Streams, Utterances, Links int }

// ImportLegacy copies the chats of the earlier model (legacy_ tables) into
// streams of persona: one stream for each conversation owner, in time
// order. Utterance ids stay the same. reply_to becomes a reply-to link and
// the workflow runs of sessions become reports-on links. The imported
// utterances are private and not classified. It copies only utterances
// that are not there, so it is safe to run again. newID makes record ids.
func (db *DB) ImportLegacy(ctx context.Context, persona string, readers func(person string) []string, newID func(string) string, now time.Time) (ImportResult, error) {
	var res ImportResult
	var exists bool
	if err := db.pool.QueryRow(ctx, `SELECT to_regclass('legacy_utterances') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return res, err
	}
	type row struct {
		id, conv, owner, author, kind, modality, text string
		audio, replyTo, generatedBy                   *string
		confidence                                    *float64
		sessions                                      []string
		att                                           []byte
		created                                       int64
	}
	rows, err := db.pool.Query(ctx, `SELECT u.id, u.conversation, c.owner, u.author, u.author_kind, u.modality, u.text, u.audio, u.reply_to,
u.generated_by, u.transcript_confidence, u.sessions, u.attachments, u.created_ms
FROM legacy_utterances u JOIN legacy_conversations c ON c.id = u.conversation
ORDER BY c.owner, u.created_ms, u.conversation, u.seq`)
	if err != nil {
		return res, err
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.conv, &r.owner, &r.author, &r.kind, &r.modality, &r.text, &r.audio, &r.replyTo,
			&r.generatedBy, &r.confidence, &r.sessions, &r.att, &r.created); err != nil {
			rows.Close()
			return res, err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	// Workflow runs of each legacy session.
	runs := map[string][]api.Ref{}
	srows, err := db.pool.Query(ctx, `SELECT id, refs FROM legacy_sessions`)
	if err != nil {
		return res, err
	}
	for srows.Next() {
		var id string
		var refs []byte
		if err := srows.Scan(&id, &refs); err != nil {
			srows.Close()
			return res, err
		}
		var rr []api.Ref
		json.Unmarshal(refs, &rr)
		for _, r := range rr {
			if r.Kind == "workflow-run" {
				runs[id] = append(runs[id], r)
			}
		}
	}
	srows.Close()

	streams := map[string]api.Stream{}
	for _, r := range list {
		st, ok := streams[r.owner]
		if !ok {
			var err error
			st, err = db.OpenStream(ctx, newID("str_"), r.owner, persona, time.UnixMilli(r.created))
			if err != nil {
				return res, err
			}
			streams[r.owner] = st
			res.Streams++
		}
		err := db.tx(ctx, All, func(tx pgx.Tx) error {
			var have bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM utterances WHERE id = $1)`, r.id).Scan(&have); err != nil || have {
				return err
			}
			var seq int64
			if err := tx.QueryRow(ctx, `UPDATE streams SET last_seq = last_seq + 1, updated_ms = GREATEST(updated_ms, $2) WHERE id = $1 RETURNING last_seq`,
				st.Id, r.created).Scan(&seq); err != nil {
				return err
			}
			att, _ := parseRefs(r.att)
			u := api.Utterance{Id: r.id, Stream: st.Id, Seq: uint64(seq), Author: r.author, AuthorKind: r.kind, Modality: r.modality, Text: r.text,
				Audio: r.audio, TranscriptConfidence: r.confidence, Attachments: att, GeneratedBy: r.generatedBy, Visibility: "private",
				CreatedMs: uint64(r.created)}
			if err := insertUtt(ctx, tx, u, readers(r.owner), false); err != nil {
				return err
			}
			res.Utterances++
			add := func(kind string, target api.Ref) error {
				res.Links++
				return insertLink(ctx, tx, api.Link{Id: newID("lnk_"), Utterance: r.id, Weight: 1, Target: target, Kind: kind,
					Confidence: 1, Author: "system:import", State: "current", CreatedMs: uint64(now.UnixMilli())})
			}
			if r.replyTo != nil && *r.replyTo != "" {
				if err := add("reply-to", api.Ref{Kind: "utterance", Ref: *r.replyTo}); err != nil {
					return err
				}
			}
			for _, s := range r.sessions {
				for _, run := range runs[s] {
					if err := add("reports-on", run); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return res, err
		}
	}
	return res, nil
}
