// Package conversation retains source records and their attributed relationships.
package conversation

import (
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"regexp"
	"slices"
)

var ErrInvalid = errors.New("invalid conversation record")
var ErrDenied = errors.New("conversation access denied")
var ErrConflict = errors.New("conversation record conflicts")
var ErrCapacity = errors.New("conversation capacity reached")
var name = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const MaxEntry = 8192

func validIdentity(v w.IdentityRef) bool {
	return v.Domain != "" && len(v.Domain) <= 253 && name.MatchString(v.AccountId)
}
func validScope(v w.ScopeRef) bool { return validIdentity(v.Authority) && name.MatchString(v.ScopeId) }
func validRef(v w.RecordRef) bool {
	return validIdentity(v.Authority) && name.MatchString(v.Kind) && name.MatchString(v.RecordId)
}
func validActor(v w.ApplicationRef) bool {
	return validIdentity(v.Identity) && name.MatchString(v.ApplicationId) && name.MatchString(v.InstanceId)
}
func Header(node w.ConversationNode) (w.EvidenceHeader, error) {
	switch v := node.(type) {
	case w.InteractionRecord:
		return v.Header, nil
	case w.InterpretationRecord:
		return v.Header, nil
	case w.SessionInput:
		return v.Header, nil
	case w.PresentationRecord:
		return v.Header, nil
	case w.ContextSnapshot:
		return v.Header, nil
	case w.SourceContext:
		return v.Header, nil
	}
	return w.EvidenceHeader{}, ErrInvalid
}
func validate(actor w.ApplicationRef, v w.ConversationAppend) (w.EvidenceHeader, []w.RecordRef, error) {
	h, e := Header(v.Node)
	if e != nil {
		return h, nil, e
	}
	if !validRef(h.Ref) || !validScope(h.Access.ControlScope) || h.Producer != actor.Identity || h.Access.Actor != actor.Identity || h.Ref.Authority != actor.Identity || len(h.Restrictions.Requirements) != 0 || len(h.Access.ResultAudience) != 1 || h.Access.ResultAudience[0] != h.Access.ControlScope || h.Capture != "complete" || len(v.Sessions) == 0 || len(v.Sessions) > 8 {
		return h, nil, ErrDenied
	}
	seen := map[string]bool{}
	for _, s := range v.Sessions {
		if !name.MatchString(s) || seen[s] {
			return h, nil, ErrInvalid
		}
		seen[s] = true
	}
	refs := append([]w.RecordRef{}, h.Causes...)
	content := func(c w.CapturedContent) bool { return p.ValidateContent(c) == nil }
	switch n := v.Node.(type) {
	case w.InteractionRecord:
		if !name.MatchString(n.PointId) || !content(n.Content) {
			return h, nil, ErrInvalid
		}
		refs = append(refs, n.ReplyHints...)
		refs = append(refs, n.IdentityEvidence...)
	case w.InterpretationRecord:
		if n.Method == "" || len(n.Destinations) == 0 || len(n.Destinations) > 8 {
			return h, nil, ErrInvalid
		}
		refs = append(refs, n.Sources...)
		refs = append(refs, n.Context...)
		if n.Prior != nil {
			refs = append(refs, *n.Prior)
		}
		for _, d := range n.Destinations {
			if !seen[d.SessionId] || !content(d.Content) {
				return h, nil, ErrInvalid
			}
			refs = append(refs, d.Prompts...)
		}
	case w.SessionInput:
		if len(v.Sessions) != 1 || v.Sessions[0] != n.SessionId || !content(n.Content) {
			return h, nil, ErrInvalid
		}
		refs = append(refs, n.Sources...)
		refs = append(refs, n.Interpretations...)
		// Admission is a foreign service reference, not an implicit graph read.
		if !validRef(n.Admission) {
			return h, nil, ErrInvalid
		}
	case w.PresentationRecord:
		if !name.MatchString(n.PointId) {
			return h, nil, ErrInvalid
		}
		refs = append(refs, n.Outputs...)
		refs = append(refs, n.Prompts...)
	case w.ContextSnapshot:
		if len(v.Sessions) != 1 || v.Sessions[0] != n.SessionId {
			return h, nil, ErrInvalid
		}
		refs = append(refs, n.OrderedInputs...)
	case w.SourceContext:
		if !validIdentity(n.Resource.Authority) || n.Resource.Kind == "" || n.Resource.ResourceId == "" {
			return h, nil, ErrInvalid
		}
	}
	unique := []w.RecordRef{}
	for _, r := range refs {
		if !validRef(r) || r == h.Ref {
			return h, nil, ErrInvalid
		}
		if !slices.Contains(unique, r) {
			unique = append(unique, r)
		}
	}
	if len(unique) > 32 {
		return h, nil, ErrInvalid
	}
	return h, unique, nil
}
