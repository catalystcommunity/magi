// Package server implements ArtifactService.
package server

import (
	"context"
	"errors"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/artifacts/api"
	"github.com/catalystcommunity/magi/primitives/artifacts/store"
)

const (
	ScopeActAs = "artifacts:act-as"
	ScopeAdmin = "artifacts:admin"

	// MaxBody is the largest artifact body.
	MaxBody = 16 << 20
	// MaxRequest is the rpc server body limit. It leaves room for encoding.
	MaxRequest = 20 << 20

	defaultLimit = 100
	maxLimit     = 1000
	maxLabels    = 64
)

type Service struct {
	DB    *store.DB
	Blobs store.Blobs
	Now   func() time.Time
}

var _ api.ArtifactService = (*Service)(nil)

// Register adds the service operations to s.
func Register(s *rpc.Server, svc *Service) {
	s.Register("ArtifactService", api.ArtifactServiceOps(svc))
}

func (s *Service) now() uint64 {
	if s.Now != nil {
		return uint64(s.Now().UnixMilli())
	}
	return uint64(time.Now().UnixMilli())
}

func privileged(p authn.Principal) bool { return p.Has(ScopeActAs) || p.Has(ScopeAdmin) }

func checkLabels(l map[string]string) error {
	if len(l) > maxLabels {
		return rpc.Invalid("at most %d labels", maxLabels)
	}
	for k, v := range l {
		if k == "" || len(k) > 256 || len(v) > 4096 {
			return rpc.Invalid("label keys must be 1-256 bytes and values at most 4096 bytes")
		}
	}
	return nil
}

func (s *Service) Put(ctx context.Context, req api.PutRequest) (api.Artifact, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Artifact{}, err
	}
	owner := p.Subject
	if req.Owner != nil && *req.Owner != "" && *req.Owner != p.Subject {
		if !privileged(p) {
			return api.Artifact{}, rpc.Denied("owner override needs %s", ScopeActAs)
		}
		owner = *req.Owner
	}
	if req.MediaType == "" || len(req.MediaType) > 256 {
		return api.Artifact{}, rpc.Invalid("media_type is required")
	}
	if len(req.Body) > MaxBody {
		return api.Artifact{}, rpc.Invalid("body is larger than %d bytes", MaxBody)
	}
	if err := checkLabels(req.Labels); err != nil {
		return api.Artifact{}, err
	}
	digest, err := s.Blobs.Write(req.Body)
	if err != nil {
		return api.Artifact{}, err
	}
	return s.DB.Put(ctx, api.Artifact{
		Digest: digest, MediaType: req.MediaType, Size: uint64(len(req.Body)),
		Owner: owner, Labels: req.Labels, CreatedMs: s.now(),
	})
}

// record returns the record the caller can read for digest: its own, or for
// a privileged caller the oldest one. Callers without access get NotFound,
// so that a digest's existence does not leak.
func (s *Service) record(ctx context.Context, digest string) (api.Artifact, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Artifact{}, err
	}
	if !store.ValidDigest(digest) {
		return api.Artifact{}, rpc.Invalid("digest must be sha256:<64 lowercase hex>")
	}
	a, err := s.DB.Get(ctx, digest, p.Subject)
	if errors.Is(err, store.ErrNotFound) && privileged(p) {
		a, err = s.DB.First(ctx, digest)
	}
	if errors.Is(err, store.ErrNotFound) {
		return a, rpc.NotFound("artifact %s", digest)
	}
	return a, err
}

func (s *Service) Get(ctx context.Context, req api.GetRequest) (api.GetResponse, error) {
	a, err := s.record(ctx, req.Digest)
	if err != nil {
		return api.GetResponse{}, err
	}
	body, err := s.Blobs.Read(a.Digest)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetResponse{}, rpc.Unavailable("body of %s is missing", a.Digest)
	}
	if err != nil {
		return api.GetResponse{}, err
	}
	return api.GetResponse{Artifact: a, Body: body}, nil
}

func (s *Service) Stat(ctx context.Context, req api.StatRequest) (api.Artifact, error) {
	return s.record(ctx, req.Digest)
}

func (s *Service) List(ctx context.Context, req api.ListRequest) (api.ListResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListResponse{}, err
	}
	q := store.Query{Owner: p.Subject, LabelValue: req.LabelValue, Limit: defaultLimit}
	if req.LabelKey != nil {
		q.LabelKey = *req.LabelKey
	}
	if q.LabelKey == "" && req.LabelValue != nil {
		return api.ListResponse{}, rpc.Invalid("label_value needs label_key")
	}
	if req.BeforeMs != nil {
		q.BeforeMs = *req.BeforeMs
	}
	if req.Limit != nil && *req.Limit > 0 {
		q.Limit = int(min(*req.Limit, maxLimit))
	}
	list, err := s.DB.List(ctx, q)
	return api.ListResponse{Artifacts: list}, err
}

func (s *Service) Label(ctx context.Context, req api.LabelRequest) (api.Artifact, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Artifact{}, err
	}
	if !store.ValidDigest(req.Digest) {
		return api.Artifact{}, rpc.Invalid("digest must be sha256:<64 lowercase hex>")
	}
	if err := checkLabels(req.Labels); err != nil {
		return api.Artifact{}, err
	}
	a, err := s.DB.Label(ctx, req.Digest, p.Subject, req.Labels)
	if errors.Is(err, store.ErrNotFound) {
		return a, rpc.NotFound("artifact %s", req.Digest)
	}
	return a, err
}
