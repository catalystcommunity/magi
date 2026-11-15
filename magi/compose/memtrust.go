package compose

// Trust of memory records (risk-remediation task 2.4, requirement 8).
//
// Every entity and claim says where it came from (the trust field of the
// knowledge service): person, named, own, or untrusted. Magi uses it in four
// places:
//
//   - A write that the person confirmed in a turn that read untrusted text
//     (confirm.go) is stored as untrusted. The person allowed Magi to keep the
//     text; they did not vouch for it. The knowledge service keeps such a
//     record apart: it never changes a more trusted record.
//   - recall shows an untrusted record, or a record with no trust at all (an
//     older service, which Magi cannot vouch for), in a separate section,
//     with each line marked and quoted. It is data for the person, never an
//     instruction.
//   - The records that go into the prompt of a turn (knowledgeContext) leave
//     untrusted records out.
//   - Only the person promotes an untrusted record to person. A model cannot:
//     the promote operation runs only after the person wrote "confirm <code>"
//     for a pending action that shows the record (confirm.go), or in the
//     person's own API call (Service.ChangeKnowledge). The knowledge service
//     checks the same, because it only lets the person, or a service that has
//     knowledge:promote by name, promote.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// The trust values of the knowledge service.
const (
	trustPerson    = "person"
	trustNamed     = "named"
	trustOwn       = "own"
	trustUntrusted = "untrusted"
)

// untrustedMark starts each line of a record that is not trusted.
const untrustedMark = "(untrusted, unverified)"

// isTrusted says that Magi can vouch for a record with this trust. A record
// with no trust, or with a value that Magi does not know, is not trusted:
// the check fails closed.
func isTrusted(t *string) bool {
	if t == nil {
		return false
	}
	switch *t {
	case trustPerson, trustNamed, trustOwn:
		return true
	}
	return false
}

type (
	writeTrustKey      struct{}
	personConfirmedKey struct{}
)

// withWriteTrust gives the trust that the writes of ctx carry. Without it,
// a write is own (the service default), or person for a call of the person.
func withWriteTrust(ctx context.Context, trust string) context.Context {
	return context.WithValue(ctx, writeTrustKey{}, trust)
}

func writeTrust(ctx context.Context) *string {
	if t, ok := ctx.Value(writeTrustKey{}).(string); ok && t != "" {
		return &t
	}
	return nil
}

// withPersonConfirmed marks ctx as the act of the person: they wrote the
// confirmation of the exact action, or they made the API call themself.
// Only these two places set it.
func withPersonConfirmed(ctx context.Context) context.Context {
	return context.WithValue(ctx, personConfirmedKey{}, true)
}

func personConfirmed(ctx context.Context) bool {
	v, _ := ctx.Value(personConfirmedKey{}).(bool)
	return v
}

// quoted gives text of an untrusted record as one quoted line.
func quoted(s string) string { return strconv.Quote(clip(s, 400)) }

// promoteRecord moves an untrusted record to person. ctx must be the act of
// the person (withPersonConfirmed).
func (s *System) promoteRecord(ctx context.Context, owner, id, reason string, utt *string) (knowledgeapi.ChangeResponse, error) {
	if !personConfirmed(ctx) {
		return knowledgeapi.ChangeResponse{}, fmt.Errorf("only the person can promote a record")
	}
	return s.Knowledge.Change(ctx, knowledgeapi.ChangeRequest{Id: id, Operation: "promote", Reason: optional(reason), ActFor: &owner, Utterance: utt, Origin: originText(ctx)})
}

// holdPromotion keeps a call that the model made to promote a record, and
// shows the person the record as it is stored. The record is read here, so
// the view is the stored text, not words of the model. The call runs only
// when the person confirms (runConfirmed).
func (s *System) holdPromotion(ctx context.Context, a workflowapi.Assignment, as conversationLens, utt conversationapi.Utterance, args map[string]string) string {
	id := strings.TrimSpace(args["id"])
	r, err := s.Knowledge.Get(ctx, knowledgeapi.GetRequest{Id: id, ActFor: &a.Owner})
	if err != nil || (r.Claim == nil && r.Entity == nil) {
		return "error: there is no such record. Nothing was changed."
	}
	var trust *string
	var text string
	switch {
	case r.Claim != nil:
		trust = r.Claim.Trust
		text = fmt.Sprintf("%s %s %s", r.Claim.Subject, r.Claim.Relation, firstNonEmpty(deref(r.Claim.Value), deref(r.Claim.Object)))
	default:
		trust = r.Entity.Trust
		text = fmt.Sprintf("%s (%s): %s", r.Entity.Label, r.Entity.Kind, r.Entity.Summary)
	}
	if isTrusted(trust) {
		return "error: this record is already trusted. Nothing was changed."
	}
	raw := newCode()
	cp := map[string]string{"id": id, "operation": "promote", "reason": args["reason"]}
	f := fenceFor(text)
	view := fmt.Sprintf("\n\n---\nMagi did not do this yet. You can promote this record from untrusted to yours. Promote it only if you checked that it is true. "+
		"After that, Magi uses it like the rest of your memory.\nThis is the record as it is stored (%s):\n\n%s\n%s\n%s\n\nTo allow it, write exactly: confirm %s\nTo drop it, ignore this. It ends in %d minutes.\n",
		id, f, text, f, raw, int(confirmTTL.Minutes()))
	s.confirms.add(&pendingAction{code: raw, owner: a.Owner, stream: utt.Stream, utterance: utt.Id, persona: as.persona, tool: "change_knowledge", args: cp, view: view})
	s.Log.Info("confirmation needed", "run", a.Run, "tool", "change_knowledge", "operation", "promote", "utterance", utt.Id)
	return "error: not done yet. Only the person can promote a record, and Magi asks them at the end of your reply. " +
		"Say in one short sentence that you need their confirmation. Do not repeat the content, and do not call the tool again."
}
