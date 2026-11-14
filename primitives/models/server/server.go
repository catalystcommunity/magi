// Package server implements ModelService.
package server

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/models/api"
	"github.com/catalystcommunity/magi/primitives/models/store"
)

// ScopeAdmin permits the changes of the catalog.
const ScopeAdmin = "models:admin"

// Catalog is the storage of the service (store.DB, or a memory store in
// tests).
type Catalog interface {
	PutModel(ctx context.Context, m api.Model) error
	GetModel(ctx context.Context, name string) (api.Model, error)
	DeleteModel(ctx context.Context, name string) (bool, error)
	Models(ctx context.Context) ([]api.Model, error)
	PutTask(ctx context.Context, t api.TaskDefault) error
	GetTask(ctx context.Context, task string) (api.TaskDefault, error)
	Tasks(ctx context.Context) ([]api.TaskDefault, error)
}

type Service struct {
	DB  Catalog
	Now func() time.Time
}

var _ api.ModelService = (*Service)(nil)

// Register adds the service operations to s.
func Register(s *rpc.Server, svc *Service) {
	s.Register("ModelService", api.ModelServiceOps(svc))
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

func (s *Service) now() uint64 {
	if s.Now != nil {
		return uint64(s.Now().UnixMilli())
	}
	return uint64(time.Now().UnixMilli())
}

func admin(ctx context.Context) error {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return err
	}
	if !p.Has(ScopeAdmin) {
		return rpc.Denied("changes need %s", ScopeAdmin)
	}
	return nil
}

func reader(ctx context.Context) error {
	_, err := rpc.Caller(ctx)
	return err
}

func notFound(err error, what, name string) error {
	if errors.Is(err, store.ErrNotFound) {
		return rpc.NotFound("%s %s", what, name)
	}
	return err
}

func (s *Service) PutModel(ctx context.Context, req api.PutModelRequest) (api.Model, error) {
	if err := admin(ctx); err != nil {
		return api.Model{}, err
	}
	m := req.Model
	if !nameRE.MatchString(m.Name) {
		return api.Model{}, rpc.Invalid("name must be 1-100 of a-z 0-9 . _ - and start with a letter or digit")
	}
	if m.Endpoint.BaseUrl == "" || m.Endpoint.Model == "" {
		return api.Model{}, rpc.Invalid("endpoint base_url and model are required")
	}
	if len(m.Tags) > 64 || len(m.Attributes) > 64 || len(m.Scores) > 256 || len(m.Calibration) > 256 {
		return api.Model{}, rpc.Invalid("too many tags, attributes, scores, or calibrations")
	}
	for _, t := range m.Tags {
		if !nameRE.MatchString(t) {
			return api.Model{}, rpc.Invalid("tag %q: use a-z 0-9 . _ -", t)
		}
	}
	for k, v := range m.Calibration {
		if v <= 0 {
			return api.Model{}, rpc.Invalid("calibration %s must be more than 0", k)
		}
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.Attributes == nil {
		m.Attributes = map[string]string{}
	}
	if m.Scores == nil {
		m.Scores = map[string]float64{}
	}
	if m.Calibration == nil {
		m.Calibration = map[string]float64{}
	}
	m.UpdatedMs = s.now()
	return m, s.DB.PutModel(ctx, m)
}

func (s *Service) GetModel(ctx context.Context, req api.GetModelRequest) (api.Model, error) {
	if err := reader(ctx); err != nil {
		return api.Model{}, err
	}
	m, err := s.DB.GetModel(ctx, req.Name)
	return m, notFound(err, "model", req.Name)
}

func (s *Service) DeleteModel(ctx context.Context, req api.DeleteModelRequest) (api.DeleteResponse, error) {
	if err := admin(ctx); err != nil {
		return api.DeleteResponse{}, err
	}
	ok, err := s.DB.DeleteModel(ctx, req.Name)
	return api.DeleteResponse{Deleted: ok}, err
}

func (s *Service) ListModels(ctx context.Context, req api.ListModelsRequest) (api.ListModelsResponse, error) {
	if err := reader(ctx); err != nil {
		return api.ListModelsResponse{}, err
	}
	all, err := s.DB.Models(ctx)
	if err != nil {
		return api.ListModelsResponse{}, err
	}
	out := []api.Model{}
	for _, m := range all {
		if !m.Enabled && (req.IncludeDisabled == nil || !*req.IncludeDisabled) {
			continue
		}
		if req.Tag != nil && *req.Tag != "" && mismatch(api.Model{Enabled: true, Tags: m.Tags}, api.Selector{All: []string{*req.Tag}}) != "" {
			continue
		}
		out = append(out, m)
	}
	return api.ListModelsResponse{Models: out}, nil
}

func (s *Service) Score(ctx context.Context, req api.ScoreRequest) (api.Model, error) {
	if err := admin(ctx); err != nil {
		return api.Model{}, err
	}
	m, err := s.DB.GetModel(ctx, req.Name)
	if err != nil {
		return m, notFound(err, "model", req.Name)
	}
	if m.Scores == nil {
		m.Scores = map[string]float64{}
	}
	if m.Calibration == nil {
		m.Calibration = map[string]float64{}
	}
	for k, v := range req.Scores {
		m.Scores[k] = v
	}
	for k, v := range req.Calibration {
		if v <= 0 {
			return api.Model{}, rpc.Invalid("calibration %s must be more than 0", k)
		}
		m.Calibration[k] = v
	}
	m.UpdatedMs = s.now()
	return m, s.DB.PutModel(ctx, m)
}

func (s *Service) PutTask(ctx context.Context, req api.PutTaskRequest) (api.TaskDefault, error) {
	if err := admin(ctx); err != nil {
		return api.TaskDefault{}, err
	}
	t := req.Task
	if !nameRE.MatchString(t.Task) {
		return api.TaskDefault{}, rpc.Invalid("task must be 1-100 of a-z 0-9 . _ -")
	}
	t.UpdatedMs = s.now()
	return t, s.DB.PutTask(ctx, t)
}

func (s *Service) GetTask(ctx context.Context, req api.GetTaskRequest) (api.TaskDefault, error) {
	if err := reader(ctx); err != nil {
		return api.TaskDefault{}, err
	}
	t, err := s.DB.GetTask(ctx, req.Task)
	return t, notFound(err, "task", req.Task)
}

func (s *Service) ListTasks(ctx context.Context, _ api.ListTasksRequest) (api.ListTasksResponse, error) {
	if err := reader(ctx); err != nil {
		return api.ListTasksResponse{}, err
	}
	ts, err := s.DB.Tasks(ctx)
	if ts == nil {
		ts = []api.TaskDefault{}
	}
	return api.ListTasksResponse{Tasks: ts}, err
}

// Select merges the task default (if any) with the selector of the request
// and orders the models. A task with no default uses only the selector.
func (s *Service) Select(ctx context.Context, req api.SelectRequest) (api.SelectResponse, error) {
	if err := reader(ctx); err != nil {
		return api.SelectResponse{}, err
	}
	var sel api.Selector
	if req.Task != nil && *req.Task != "" {
		t, err := s.DB.GetTask(ctx, *req.Task)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return api.SelectResponse{}, err
		}
		sel = t.Selector
	}
	if req.Selector != nil {
		sel = Merge(sel, *req.Selector)
	}
	all, err := s.DB.Models(ctx)
	if err != nil {
		return api.SelectResponse{}, err
	}
	c := Select(all, sel, req.Strict != nil && *req.Strict)
	if req.Limit != nil && *req.Limit > 0 && uint64(len(c)) > *req.Limit {
		c = c[:*req.Limit]
	}
	if c == nil {
		c = []api.Candidate{}
	}
	return api.SelectResponse{Candidates: c, Selector: sel}, nil
}
