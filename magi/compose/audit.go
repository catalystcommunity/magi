package compose

// Audit log and undo of memory changes (risk-remediation task 3.3,
// requirement 9.3).
//
// The knowledge service keeps the audit log (docs/knowledge.md, "Audit and
// undo"). Magi adds what only it knows about a write: which persona, which
// tool, which turn and run, and whether the person confirmed it. That goes to
// the service as the origin of the call (JSON text), next to the person it
// acts for. The service records the caller (via) itself.
//
// Undo is the act of the person. A persona or a model has no tool for it:
// change_knowledge does not take "undo", and changeRecord refuses it without
// the mark of the person (withPersonConfirmed), which only the API call of
// the person sets (Service.ChangeKnowledge, for a user token). The knowledge
// service checks again: it lets only the person, an admin, or a service with
// knowledge:undo undo a change.

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// auditOrigin is the origin of a write, as the audit log keeps it.
type auditOrigin struct {
	Persona   string `json:"persona,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Turn      string `json:"turn,omitempty"`
	Run       string `json:"run,omitempty"`
	By        string `json:"by,omitempty"`        // "note-taker" or "api" when it is not a persona tool
	Confirmed bool   `json:"confirmed,omitempty"` // the person confirmed this exact action
}

type auditOriginKey struct{}

func withAuditOrigin(ctx context.Context, o auditOrigin) context.Context {
	return context.WithValue(ctx, auditOriginKey{}, o)
}

func auditOriginOf(ctx context.Context) (auditOrigin, bool) {
	o, ok := ctx.Value(auditOriginKey{}).(auditOrigin)
	return o, ok
}

// originText is the origin of the writes of ctx, or nil when there is none.
func originText(ctx context.Context) *string {
	o, ok := auditOriginOf(ctx)
	if !ok {
		return nil
	}
	b, err := json.Marshal(o)
	if err != nil {
		return nil
	}
	return ptr(string(b))
}

// undoRecord undoes the change with id for the person. ctx must be the act
// of the person (withPersonConfirmed).
func (s *System) undoRecord(ctx context.Context, owner, id, reason string, utt *string) (knowledgeapi.ChangeResponse, error) {
	if !personConfirmed(ctx) {
		return knowledgeapi.ChangeResponse{}, fmt.Errorf("only the person can undo a change")
	}
	return s.Knowledge.Change(ctx, knowledgeapi.ChangeRequest{Id: id, Operation: "undo", Reason: optional(reason), ActFor: &owner, Utterance: utt, Origin: originText(ctx)})
}
