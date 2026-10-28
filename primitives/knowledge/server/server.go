// Package server implements KnowledgeService.
//
// Access: a record has an owner. The caller reads and changes its own
// records. Scope knowledge:act-as lets a service set owner and act on that
// owner's records. Scope knowledge:admin permits all operations. scope on a
// node is a stored policy tag; it does not change visibility yet.
package server

import (
	"context"
	"errors"
	"strings"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
	"github.com/catalystcommunity/magi/primitives/knowledge/store"
)

const (
	ScopeActAs = "knowledge:act-as"
	ScopeAdmin = "knowledge:admin"

	defaultLimit = 50
	maxLimit     = 500
	maxDepth     = 3
	maxText      = 4096
	maxSummary   = 64 << 10
	maxProps     = 256
)

// Service implements api.KnowledgeService.
type Service struct{ Store *store.Store }

var _ api.KnowledgeService = (*Service)(nil)

// Register adds the service to s.
func Register(s *rpc.Server, impl *Service) {
	s.Register("KnowledgeService", api.KnowledgeServiceOps(impl))
}

// owner returns the owner a call acts for.
func owner(p authn.Principal, requested *string) (string, error) {
	if requested == nil || *requested == "" || *requested == p.Subject {
		return p.Subject, nil
	}
	if p.Has(ScopeActAs) || p.Has(ScopeAdmin) {
		return *requested, nil
	}
	return "", rpc.Denied("acting for another owner needs %s", ScopeActAs)
}

// canAccess reports whether p can read and change a record of recordOwner.
func canAccess(p authn.Principal, recordOwner string) bool {
	return recordOwner == p.Subject || p.Has(ScopeActAs) || p.Has(ScopeAdmin)
}

func caller(ctx context.Context, requested *string) (authn.Principal, string, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return p, "", err
	}
	o, err := owner(p, requested)
	return p, o, err
}

func mapErr(err error, what string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return rpc.NotFound("%s not found", what)
	case errors.Is(err, store.ErrConflict):
		return rpc.Conflict("%s conflicts with an existing record", what)
	}
	return err
}

func required(name, v string) error {
	if strings.TrimSpace(v) == "" {
		return rpc.Invalid("%s is required", name)
	}
	if len(v) > maxText {
		return rpc.Invalid("%s is too long", name)
	}
	return nil
}

func checkProps(p map[string]string) error {
	if len(p) > maxProps {
		return rpc.Invalid("too many properties")
	}
	for k, v := range p {
		if k == "" || len(k) > 256 || len(v) > maxSummary {
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

func (s *Service) PutNode(ctx context.Context, req api.PutNodeRequest) (api.Node, error) {
	p, o, err := caller(ctx, req.Owner)
	if err != nil {
		return api.Node{}, err
	}
	if err := errors.Join(required("kind", req.Kind), required("label", req.Label), checkProps(req.Properties), checkConfidence(req.Confidence)); err != nil {
		return api.Node{}, err
	}
	if req.Summary != nil && len(*req.Summary) > maxSummary {
		return api.Node{}, rpc.Invalid("summary is too long")
	}
	if x := req.External; x != nil {
		if err := errors.Join(required("external.system", x.System), required("external.locator", x.Locator)); err != nil {
			return api.Node{}, err
		}
	}
	in := store.NodeInput{Owner: o, Kind: req.Kind, Label: req.Label, Summary: req.Summary, Properties: req.Properties,
		External: req.External, Scope: req.Scope, AuthoredBy: req.AuthoredBy, Confidence: req.Confidence}
	if req.Id != nil && *req.Id != "" {
		cur, err := s.Store.Node(ctx, *req.Id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return api.Node{}, err
		}
		if err != nil || !canAccess(p, cur.Owner) {
			return api.Node{}, rpc.NotFound("node not found")
		}
		if req.Owner != nil && *req.Owner != "" && *req.Owner != cur.Owner {
			return api.Node{}, rpc.Invalid("owner of an existing node cannot change")
		}
		in.ID, in.Owner = cur.Id, cur.Owner
	}
	n, err := s.Store.PutNode(ctx, in)
	return n, mapErr(err, "node")
}

func (s *Service) GetNode(ctx context.Context, req api.GetNodeRequest) (api.NodeView, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.NodeView{}, err
	}
	v, err := s.Store.View(ctx, req.Id)
	if err != nil || !canAccess(p, v.Node.Owner) {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			return api.NodeView{}, rpc.NotFound("node not found")
		}
		return api.NodeView{}, err
	}
	return v, nil
}

func (s *Service) PutEdge(ctx context.Context, req api.PutEdgeRequest) (api.Edge, error) {
	_, o, err := caller(ctx, req.Owner)
	if err != nil {
		return api.Edge{}, err
	}
	if err := errors.Join(required("from_node", req.FromNode), required("to_node", req.ToNode), required("relation", req.Relation), checkProps(req.Properties), checkConfidence(req.Confidence)); err != nil {
		return api.Edge{}, err
	}
	e, err := s.Store.PutEdge(ctx, o, req)
	if errors.Is(err, store.ErrNotFound) {
		return e, rpc.NotFound("from_node and to_node must be nodes of owner %s", o)
	}
	return e, mapErr(err, "edge")
}

var refKinds = map[string]bool{"artifact": true, "utterance": true, "external": true, "url": true}
var stances = map[string]bool{"supports": true, "contradicts": true, "mentions": true}

func (s *Service) LinkEvidence(ctx context.Context, req api.LinkEvidenceRequest) (api.Evidence, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Evidence{}, err
	}
	if _, err := owner(p, req.Owner); err != nil {
		return api.Evidence{}, err
	}
	if err := errors.Join(required("subject", req.Subject), required("ref", req.Ref)); err != nil {
		return api.Evidence{}, err
	}
	if !refKinds[req.RefKind] {
		return api.Evidence{}, rpc.Invalid("ref_kind must be artifact, utterance, external, or url")
	}
	stance := "supports"
	if req.Stance != nil {
		stance = *req.Stance
	}
	if !stances[stance] {
		return api.Evidence{}, rpc.Invalid("stance must be supports, contradicts, or mentions")
	}
	if req.Note != nil && len(*req.Note) > maxSummary {
		return api.Evidence{}, rpc.Invalid("note is too long")
	}
	// The evidence belongs to the owner of its subject.
	subjectOwner, err := s.Store.OwnerOf(ctx, req.Subject, "nodes", "edges")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return api.Evidence{}, err
	}
	if err != nil || !canAccess(p, subjectOwner) {
		return api.Evidence{}, rpc.NotFound("subject not found")
	}
	if req.Owner != nil && *req.Owner != "" && *req.Owner != subjectOwner {
		return api.Evidence{}, rpc.Invalid("owner must be the owner of the subject")
	}
	v, err := s.Store.LinkEvidence(ctx, subjectOwner, req, stance)
	return v, mapErr(err, "evidence")
}

func (s *Service) Query(ctx context.Context, req api.QueryRequest) (api.QueryResponse, error) {
	_, o, err := caller(ctx, req.Owner)
	if err != nil {
		return api.QueryResponse{}, err
	}
	q := store.Query{Owner: o, Depth: 1, Limit: defaultLimit}
	if req.Text != nil {
		q.Text = strings.TrimSpace(*req.Text)
	}
	if req.Kind != nil {
		q.Kind = *req.Kind
	}
	if req.Near != nil {
		q.Near = *req.Near
	}
	if req.Depth != nil {
		q.Depth = int(min(*req.Depth, maxDepth))
	}
	if req.Limit != nil && *req.Limit > 0 {
		q.Limit = int(min(*req.Limit, maxLimit))
	}
	if len(q.Text) > maxText {
		return api.QueryResponse{}, rpc.Invalid("text is too long")
	}
	if q.Near != "" {
		if no, err := s.Store.OwnerOf(ctx, q.Near, "nodes"); err != nil || no != o {
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return api.QueryResponse{}, err
			}
			return api.QueryResponse{}, rpc.NotFound("near node not found")
		}
	}
	r, err := s.Store.Find(ctx, q)
	return r, mapErr(err, "query")
}

func (s *Service) Delete(ctx context.Context, req api.DeleteRequest) (api.DeleteResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.DeleteResponse{}, err
	}
	// A record that the caller cannot access is reported as not deleted, so
	// that the response does not show that the record exists.
	recordOwner, err := s.Store.OwnerOf(ctx, req.Id, "nodes", "edges", "evidence")
	if errors.Is(err, store.ErrNotFound) || (err == nil && !canAccess(p, recordOwner)) {
		return api.DeleteResponse{Deleted: false}, nil
	}
	if err != nil {
		return api.DeleteResponse{}, err
	}
	ok, err := s.Store.Delete(ctx, req.Id)
	return api.DeleteResponse{Deleted: ok}, err
}
