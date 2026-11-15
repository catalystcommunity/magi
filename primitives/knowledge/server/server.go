// Package server implements KnowledgeService (docs/knowledge.md).
//
// Access: each record has owners and readers (subjects and groups). The
// caller acts as its own subject, or as act_for with knowledge:act-as. A
// caller sets owners only to the self and to its groups; knowledge:act-as
// permits all owners. knowledge:admin permits all operations. A record that
// the caller cannot see gives "not found".
package server

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
	"github.com/catalystcommunity/magi/primitives/knowledge/store"
)

const (
	ScopeActAs = "knowledge:act-as"
	ScopeAdmin = "knowledge:admin"
	// ScopePromote lets a service promote untrusted records for a person. It
	// is matched exactly: "*", "knowledge:*", and knowledge:admin do not give
	// it. The person never needs it, because a person promotes as themself.
	ScopePromote = "knowledge:promote"
	// ScopeUndo lets a service undo changes for a person, matched by name like
	// ScopePromote. Magi holds it and calls undo only in the API call of the
	// person. The person and an admin need no scope.
	ScopeUndo = "knowledge:undo"

	defaultAudit = 100
	maxAudit     = 1000
	defaultLimit = 50
	maxLimit     = 500
	maxDepth     = 3
	maxText      = 4096
	maxLong      = 64 << 10
	maxProps     = 256
	maxList      = 256
)

// Service implements api.KnowledgeService.
type Service struct{ Store *store.Store }

var _ api.KnowledgeService = (*Service)(nil)

// Register adds the service to s.
func Register(s *rpc.Server, impl *Service) {
	s.Register("KnowledgeService", api.KnowledgeServiceOps(impl))
}

// who returns the caller as a store.Who, acting for actFor when it is set.
func (s *Service) who(ctx context.Context, actFor *string) (store.Who, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return store.Who{}, err
	}
	subject := p.Subject
	if actFor != nil && *actFor != "" && *actFor != p.Subject {
		if !p.Has(ScopeActAs) && !p.Has(ScopeAdmin) {
			return store.Who{}, rpc.Denied("acting for another subject needs %s", ScopeActAs)
		}
		subject = *actFor
	}
	ps, err := s.Store.Principals(ctx, subject)
	if err != nil {
		return store.Who{}, err
	}
	return store.Who{Subject: subject, Principals: ps, All: p.Has(ScopeAdmin), Via: p.Subject}, nil
}

// maxOrigin is the longest origin text of the audit log.
const maxOrigin = 2048

// withOrigin gives the audit text of a request to w. It must be a JSON
// object: the audit log is read by programs.
func withOrigin(w store.Who, origin *string) (store.Who, error) {
	if origin == nil || *origin == "" {
		return w, nil
	}
	var probe map[string]any
	if len(*origin) > maxOrigin || json.Unmarshal([]byte(*origin), &probe) != nil || probe == nil {
		return w, rpc.Invalid("origin must be a JSON object of at most %d bytes", maxOrigin)
	}
	w.Origin = origin
	return w, nil
}

// trusted reports whether the caller can set any owners.
func trusted(ctx context.Context) bool {
	p, err := rpc.Caller(ctx)
	return err == nil && (p.Has(ScopeActAs) || p.Has(ScopeAdmin))
}

// vouches reports whether the caller can write a record with trust "person"
// or "named": a person (kind user) for themself, or a service that acts for
// people (knowledge:act-as). Any caller can write "own" or "untrusted".
func vouches(ctx context.Context) bool {
	p, err := rpc.Caller(ctx)
	return err == nil && (p.Kind == authn.KindUser || p.Has(ScopeActAs) || p.Has(ScopeAdmin))
}

// canPromote reports whether the caller can move a record from untrusted:
// the person as themself (a user token, not acting for another subject), or
// a service that was given knowledge:promote by name. A persona, a model, or
// another member holds neither: they act through the service of Magi, which
// calls promote only after the person confirmed (docs/knowledge.md, "Trust").
func canPromote(ctx context.Context, w store.Who) bool {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return false
	}
	if p.Kind == authn.KindUser {
		return w.Subject == p.Subject
	}
	return slices.Contains(p.Scopes, ScopePromote)
}

// canUndo reports whether the caller can undo changes for w: the person as
// themself (a user token), an admin (knowledge:admin), or a service that was
// given knowledge:undo by name. A persona, a model, or another member holds
// none of them: they act through the service of Magi, which calls undo only
// in the API call of the person (docs/knowledge.md, "Audit and undo").
func canUndo(ctx context.Context, w store.Who) bool {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return false
	}
	if p.Kind == authn.KindUser {
		return w.Subject == p.Subject || p.Has(ScopeAdmin)
	}
	return slices.Contains(p.Scopes, ScopeUndo)
}

// trustOf validates the trust of a write.
func trustOf(ctx context.Context, requested *string) (string, error) {
	t, err := store.NormalizeTrust(requested)
	if err != nil {
		return "", rpc.Invalid("%s", strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
	}
	if (t == store.TrustPerson || t == store.TrustNamed) && !vouches(ctx) {
		return "", rpc.Denied("trust %s needs a person or a service that acts for people", t)
	}
	return t, nil
}

// owners checks requested owners: the default is the subject; others must
// be the subject or its groups unless the caller is trusted.
func owners(ctx context.Context, w store.Who, requested []string) ([]string, error) {
	o := store.Set(requested)
	if len(o) == 0 {
		return []string{w.Subject}, nil
	}
	if err := principalList("owners", o); err != nil {
		return nil, err
	}
	if !trusted(ctx) {
		for _, x := range o {
			if !slices.Contains(w.Principals, x) {
				return nil, rpc.Denied("owner %s is not you or one of your groups", x)
			}
		}
	}
	return o, nil
}

var principal = regexp.MustCompile(`^(user|service|runner|group):[^\s]{1,250}$`)

func principalList(name string, v []string) error {
	if len(v) > maxList {
		return rpc.Invalid("%s has too many items", name)
	}
	for _, x := range v {
		if !principal.MatchString(x) {
			return rpc.Invalid("%s: %q is not a subject or a group", name, x)
		}
	}
	return nil
}

func mapErr(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return rpc.NotFound("%s not found", what)
	case errors.Is(err, store.ErrConflict):
		return rpc.Conflict("%s conflicts with an existing record", what)
	case errors.Is(err, store.ErrInvalid):
		return rpc.Invalid("%s", strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
	}
	return err
}

func required(name, v string) error {
	if strings.TrimSpace(v) == "" {
		return rpc.Invalid("%s is required", name)
	}
	return short(name, &v)
}

func short(name string, v *string) error {
	if v != nil && len(*v) > maxText {
		return rpc.Invalid("%s is too long", name)
	}
	return nil
}

func long(name string, v *string) error {
	if v != nil && len(*v) > maxLong {
		return rpc.Invalid("%s is too long", name)
	}
	return nil
}

func texts(name string, v []string) error {
	if len(v) > maxList {
		return rpc.Invalid("%s has too many items", name)
	}
	for _, x := range v {
		if len(x) > maxText {
			return rpc.Invalid("%s has an item that is too long", name)
		}
	}
	return nil
}

func checkProps(p map[string]string) error {
	if len(p) > maxProps {
		return rpc.Invalid("too many properties")
	}
	for k, v := range p {
		if k == "" || len(k) > 256 || len(v) > maxLong {
			return rpc.Invalid("property %q is empty or too long", k)
		}
	}
	return nil
}

func checkConfidence(c *float64) error {
	if c != nil && !(*c >= 0 && *c <= 1) {
		return rpc.Invalid("confidence must be from 0 to 1")
	}
	return nil
}

var refKinds = []string{"utterance", "workflow-run", "artifact", "external", "url", "entity", "claim", "episode", "procedure"}

func checkRefs(refs []api.Ref) error {
	if len(refs) > maxList {
		return rpc.Invalid("too many refs")
	}
	for _, r := range refs {
		if !slices.Contains(refKinds, r.Kind) {
			return rpc.Invalid("ref kind must be one of %s", strings.Join(refKinds, ", "))
		}
		if err := errors.Join(required("ref", r.Ref), short("ref title", r.Title)); err != nil {
			return err
		}
	}
	return nil
}

func limit(v *uint64) int {
	if v == nil || *v == 0 {
		return defaultLimit
	}
	return int(min(*v, maxLimit))
}

func (s *Service) PutEntity(ctx context.Context, req api.PutEntityRequest) (api.Entity, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Entity{}, err
	}
	if w, err = withOrigin(w, req.Origin); err != nil {
		return api.Entity{}, err
	}
	o, err := owners(ctx, w, req.Owners)
	if err != nil {
		return api.Entity{}, err
	}
	if err := errors.Join(required("kind", req.Kind), required("label", req.Label), texts("aliases", req.Aliases), long("summary", req.Summary),
		checkProps(req.Properties), checkConfidence(req.Confidence), principalList("readers", req.Readers), short("utterance", req.Utterance)); err != nil {
		return api.Entity{}, err
	}
	if x := req.External; x != nil {
		if err := errors.Join(required("external.system", x.System), required("external.locator", x.Locator), short("external.context", x.Context)); err != nil {
			return api.Entity{}, err
		}
	}
	trust, err := trustOf(ctx, req.Trust)
	if err != nil {
		return api.Entity{}, err
	}
	e, err := s.Store.PutEntity(ctx, w, store.EntityInput{Trust: trust, Kind: strings.ToLower(strings.TrimSpace(req.Kind)), Label: strings.TrimSpace(req.Label),
		Aliases: req.Aliases, Summary: req.Summary, Properties: req.Properties, External: req.External, Owners: o, Readers: req.Readers,
		Confidence: req.Confidence, Utterance: req.Utterance})
	return e, mapErr(err, "entity")
}

func (s *Service) Self(ctx context.Context, req api.SelfRequest) (api.Entity, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Entity{}, err
	}
	if err := short("label", req.Label); err != nil {
		return api.Entity{}, err
	}
	label := ""
	if req.Label != nil {
		label = strings.TrimSpace(*req.Label)
	}
	e, err := s.Store.Self(ctx, w, w.Subject, label)
	return e, mapErr(err, "entity")
}

func (s *Service) Assert(ctx context.Context, req api.AssertRequest) (api.Claim, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Claim{}, err
	}
	if w, err = withOrigin(w, req.Origin); err != nil {
		return api.Claim{}, err
	}
	o, err := owners(ctx, w, req.Owners)
	if err != nil {
		return api.Claim{}, err
	}
	if err := errors.Join(required("subject", req.Subject), required("relation", req.Relation), long("value", req.Value), short("object", req.Object),
		checkConfidence(req.Confidence), short("authored_by", req.AuthoredBy), texts("derived_from", req.DerivedFrom), checkRefs(req.Evidence),
		principalList("readers", req.Readers), short("utterance", req.Utterance)); err != nil {
		return api.Claim{}, err
	}
	if (req.Object == nil) == (req.Value == nil) {
		return api.Claim{}, rpc.Invalid("give object or value")
	}
	// The subject and the object must be entities that the caller can read.
	if _, err := s.Store.Entity(ctx, s.Store.DB, w, req.Subject); err != nil {
		return api.Claim{}, mapErr(err, "subject")
	}
	if req.Object != nil {
		obj, err := s.Store.Entity(ctx, s.Store.DB, w, *req.Object)
		if err != nil {
			return api.Claim{}, mapErr(err, "object")
		}
		if obj.Id == req.Subject {
			return api.Claim{}, rpc.Invalid("a claim cannot have its subject as object")
		}
		req.Object = &obj.Id
	}
	if req.Value != nil && strings.TrimSpace(*req.Value) == "" {
		return api.Claim{}, rpc.Invalid("value is empty")
	}
	trust, err := trustOf(ctx, req.Trust)
	if err != nil {
		return api.Claim{}, err
	}
	c, err := s.Store.Assert(ctx, w, store.ClaimInput{Trust: trust, Subject: req.Subject, Relation: strings.ToLower(strings.TrimSpace(req.Relation)),
		Object: req.Object, Value: req.Value, ValidFrom: req.ValidFromMs, ValidTo: req.ValidToMs, Confidence: req.Confidence,
		AuthoredBy: req.AuthoredBy, DerivedFrom: req.DerivedFrom, Evidence: req.Evidence, Owners: o, Readers: req.Readers, Utterance: req.Utterance})
	return c, mapErr(err, "claim")
}

func (s *Service) Change(ctx context.Context, req api.ChangeRequest) (api.ChangeResponse, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.ChangeResponse{}, err
	}
	if w, err = withOrigin(w, req.Origin); err != nil {
		return api.ChangeResponse{}, err
	}
	if err := errors.Join(required("id", req.Id), long("reason", req.Reason), short("utterance", req.Utterance), checkProps(req.Set)); err != nil {
		return api.ChangeResponse{}, err
	}
	if req.Operation == "undo" {
		// id is the id of a change. Who may undo is checked first, so that
		// a caller who may not learns nothing of the change.
		if !canUndo(ctx, w) {
			return api.ChangeResponse{}, rpc.Denied("only the person or an admin can undo a change")
		}
		if len(req.Set) > 0 || req.AtMs != nil || (req.Erase != nil && *req.Erase) {
			return api.ChangeResponse{}, rpc.Invalid("undo takes an id, and a reason")
		}
		r, err := s.Store.Undo(ctx, w, req.Id, req.Reason, req.Utterance)
		return r, mapErr(err, "change")
	}
	switch req.Operation {
	case "promote":
		if !canPromote(ctx, w) {
			return api.ChangeResponse{}, rpc.Denied("only the person can promote an untrusted record")
		}
	}
	if t, ok := req.Set["trust"]; ok && t != store.TrustUntrusted {
		return api.ChangeResponse{}, rpc.Invalid("a change can only set trust to untrusted; the person promotes with promote")
	}
	// An object must be an entity that the caller can read.
	if o, ok := req.Set["object"]; ok {
		e, err := s.Store.Entity(ctx, s.Store.DB, w, o)
		if err != nil {
			return api.ChangeResponse{}, mapErr(err, "object")
		}
		req.Set["object"] = e.Id
	}
	r, err := s.Store.Change(ctx, w, store.ChangeInput{ID: req.Id, Operation: req.Operation, Set: req.Set, At: req.AtMs,
		Erase: req.Erase != nil && *req.Erase, Reason: req.Reason, Utterance: req.Utterance})
	return r, mapErr(err, "record")
}

func (s *Service) Merge(ctx context.Context, req api.MergeRequest) (api.Change, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Change{}, err
	}
	if w, err = withOrigin(w, req.Origin); err != nil {
		return api.Change{}, err
	}
	if err := errors.Join(required("keep", req.Keep), required("merge", req.Merge), long("reason", req.Reason), short("utterance", req.Utterance)); err != nil {
		return api.Change{}, err
	}
	c, err := s.Store.Merge(ctx, w, req.Keep, req.Merge, req.Reason, req.Utterance)
	return c, mapErr(err, "entity")
}

func (s *Service) Unmerge(ctx context.Context, req api.UnmergeRequest) (api.Change, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Change{}, err
	}
	if err := required("change", req.Change); err != nil {
		return api.Change{}, err
	}
	c, err := s.Store.Unmerge(ctx, w, req.Change)
	return c, mapErr(err, "merge")
}

func (s *Service) RecordEpisode(ctx context.Context, req api.RecordEpisodeRequest) (api.Episode, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Episode{}, err
	}
	o, err := owners(ctx, w, req.Owners)
	if err != nil {
		return api.Episode{}, err
	}
	id := ""
	if req.Id != nil {
		id = *req.Id
	}
	if id == "" {
		if err := errors.Join(required("kind", req.Kind), required("title", req.Title)); err != nil {
			return api.Episode{}, err
		}
	}
	if err := errors.Join(short("title", &req.Title), long("summary", req.Summary), texts("participants", req.Participants), texts("entities", req.Entities),
		checkRefs(req.Refs), short("procedure", req.Procedure), long("outcome", req.Outcome), principalList("readers", req.Readers)); err != nil {
		return api.Episode{}, err
	}
	e, err := s.Store.RecordEpisode(ctx, w, store.EpisodeInput{ID: id, Kind: req.Kind, Title: req.Title, Summary: req.Summary,
		Started: req.StartedMs, Ended: req.EndedMs, Participants: req.Participants, Entities: req.Entities, Refs: req.Refs,
		Procedure: req.Procedure, Outcome: req.Outcome, Owners: o, Readers: req.Readers})
	return e, mapErr(err, "episode")
}

var procedureStatus = []string{"draft", "approved"}

func (s *Service) PutProcedure(ctx context.Context, req api.PutProcedureRequest) (api.Procedure, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Procedure{}, err
	}
	o, err := owners(ctx, w, req.Owners)
	if err != nil {
		return api.Procedure{}, err
	}
	status := "draft"
	if req.Status != nil {
		status = *req.Status
	}
	if !slices.Contains(procedureStatus, status) {
		return api.Procedure{}, rpc.Invalid("status must be draft or approved")
	}
	if err := errors.Join(required("name", req.Name), required("goal", req.Goal), required("steps", req.Steps), long("steps", &req.Steps),
		checkProps(req.Inputs), long("definition", req.Definition), texts("entities", req.Entities), principalList("readers", req.Readers),
		long("reason", req.Reason)); err != nil {
		return api.Procedure{}, err
	}
	p, err := s.Store.PutProcedure(ctx, w, store.ProcedureInput{Name: strings.TrimSpace(req.Name), Goal: req.Goal, Steps: req.Steps, Inputs: req.Inputs,
		Definition: req.Definition, Status: status, Entities: req.Entities, Owners: o, Readers: req.Readers, Reason: req.Reason})
	return p, mapErr(err, "procedure")
}

var stances = []string{"supports", "contradicts", "mentions"}

func (s *Service) LinkEvidence(ctx context.Context, req api.LinkEvidenceRequest) (api.Evidence, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Evidence{}, err
	}
	stance := "supports"
	if req.Stance != nil {
		stance = *req.Stance
	}
	if !slices.Contains(stances, stance) {
		return api.Evidence{}, rpc.Invalid("stance must be supports, contradicts, or mentions")
	}
	if err := errors.Join(required("subject", req.Subject), checkRefs([]api.Ref{req.Ref}), long("note", req.Note)); err != nil {
		return api.Evidence{}, err
	}
	v, err := s.Store.LinkEvidence(ctx, w, req.Subject, req.Ref, stance, req.Note)
	return v, mapErr(err, "subject")
}

func (s *Service) Dispute(ctx context.Context, req api.DisputeRequest) (api.Dispute, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Dispute{}, err
	}
	if err := errors.Join(required("record", req.Record), required("reason", req.Reason), long("reason", &req.Reason), long("proposed", req.Proposed)); err != nil {
		return api.Dispute{}, err
	}
	d, err := s.Store.Dispute(ctx, w, req.Record, req.Reason, req.Proposed)
	return d, mapErr(err, "record")
}

func (s *Service) ResolveDispute(ctx context.Context, req api.ResolveDisputeRequest) (api.Dispute, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Dispute{}, err
	}
	if err := errors.Join(required("id", req.Id), long("resolution", req.Resolution)); err != nil {
		return api.Dispute{}, err
	}
	d, err := s.Store.ResolveDispute(ctx, w, req.Id, req.Accept, req.Resolution)
	return d, mapErr(err, "open dispute")
}

func (s *Service) ListDisputes(ctx context.Context, req api.ListDisputesRequest) (api.ListDisputesResponse, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.ListDisputesResponse{}, err
	}
	state := ""
	if req.State != nil {
		state = *req.State
	}
	ds, err := s.Store.Disputes(ctx, w, state)
	return api.ListDisputesResponse{Disputes: ds}, err
}

func (s *Service) PutGroup(ctx context.Context, req api.PutGroupRequest) (api.Group, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.Group{}, err
	}
	if !strings.HasPrefix(req.Id, "group:") || !principal.MatchString(req.Id) {
		return api.Group{}, rpc.Invalid(`id must start with "group:"`)
	}
	if err := errors.Join(required("label", req.Label), principalList("members", req.Members), principalList("owners", req.Owners)); err != nil {
		return api.Group{}, err
	}
	o := store.Set(req.Owners)
	if len(o) == 0 {
		o = []string{w.Subject}
	}
	if !trusted(ctx) && !slices.Contains(o, w.Subject) {
		return api.Group{}, rpc.Invalid("you must be an owner of the group")
	}
	g, err := s.Store.PutGroup(ctx, w, req.Id, req.Label, req.Members, o)
	return g, mapErr(err, "group")
}

var recordTypes = []string{"entity", "claim", "episode", "procedure"}

func (s *Service) Search(ctx context.Context, req api.SearchRequest) (api.SearchResponse, error) {
	for _, t := range req.Types {
		if !slices.Contains(recordTypes, t) {
			return api.SearchResponse{}, rpc.Invalid("types must be from %s", strings.Join(recordTypes, ", "))
		}
	}
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.SearchResponse{}, err
	}
	in := store.SearchInput{Types: req.Types, At: req.AtMs, IncludeEnded: req.IncludeEnded != nil && *req.IncludeEnded, Limit: limit(req.Limit)}
	if req.Text != nil {
		in.Text = strings.TrimSpace(*req.Text)
	}
	if req.Kind != nil {
		in.Kind = *req.Kind
	}
	if req.Relation != nil {
		in.Relation = *req.Relation
	}
	if err := errors.Join(short("text", &in.Text), short("kind", &in.Kind), short("relation", &in.Relation)); err != nil {
		return api.SearchResponse{}, err
	}
	r, err := s.Store.Search(ctx, w, in)
	return r, mapErr(err, "search")
}

func (s *Service) About(ctx context.Context, req api.AboutRequest) (api.AboutResponse, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.AboutResponse{}, err
	}
	r, err := s.Store.About(ctx, w, req.Id, req.AtMs, req.IncludeEnded != nil && *req.IncludeEnded)
	return r, mapErr(err, "entity")
}

func (s *Service) Neighbors(ctx context.Context, req api.NeighborsRequest) (api.NeighborsResponse, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.NeighborsResponse{}, err
	}
	depth := 1
	if req.Depth != nil && *req.Depth > 0 {
		depth = int(min(*req.Depth, maxDepth))
	}
	rel := ""
	if req.Relation != nil {
		rel = *req.Relation
	}
	r, err := s.Store.Neighbors(ctx, w, req.Id, rel, depth, limit(req.Limit))
	return r, mapErr(err, "entity")
}

func (s *Service) Timeline(ctx context.Context, req api.TimelineRequest) (api.TimelineResponse, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.TimelineResponse{}, err
	}
	if req.ToMs <= req.FromMs {
		return api.TimelineResponse{}, rpc.Invalid("to_ms must be after from_ms")
	}
	e := ""
	if req.Entity != nil {
		e = *req.Entity
	}
	r, err := s.Store.Timeline(ctx, w, e, req.FromMs, req.ToMs, limit(req.Limit))
	return r, mapErr(err, "timeline")
}

func (s *Service) History(ctx context.Context, req api.HistoryRequest) (api.HistoryResponse, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.HistoryResponse{}, err
	}
	if req.Id == "" {
		// The audit log of the caller: all changes of the records they own.
		n := defaultAudit
		if req.Limit != nil && *req.Limit > 0 {
			n = int(min(*req.Limit, maxAudit))
		}
		var since uint64
		if req.SinceMs != nil {
			since = *req.SinceMs
		}
		cs, err := s.Store.Audit(ctx, w, since, n)
		return api.HistoryResponse{Changes: cs}, err
	}
	cs, err := s.Store.History(ctx, w, req.Id)
	return api.HistoryResponse{Changes: cs}, mapErr(err, "record")
}

func (s *Service) Get(ctx context.Context, req api.GetRequest) (api.GetResponse, error) {
	w, err := s.who(ctx, req.ActFor)
	if err != nil {
		return api.GetResponse{}, err
	}
	r, err := s.Store.Get(ctx, w, req.Id)
	return r, mapErr(err, "record")
}
