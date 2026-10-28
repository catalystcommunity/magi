// Package server implements ExecutionService. It keeps environment records,
// starts environments through a backend, and sends commands and file
// operations to the envd agent inside each environment.
package server

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/backend"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	"github.com/catalystcommunity/magi/primitives/execution/store"
)

const (
	ScopeActAs = "execution:act-as"
	ScopeAdmin = "execution:admin"

	// MaxRequest is the rpc body limit. It leaves room for a write body.
	MaxRequest = 72 << 20

	StatusCreating  = "creating"
	StatusReady     = "ready"
	StatusFailed    = "failed"
	StatusDestroyed = "destroyed"

	defaultLimit = 100
	maxLimit     = 1000
	maxWait      = 10 * time.Minute
)

// Service is ExecutionService.
type Service struct {
	DB       *store.DB
	Backends map[string]backend.Backend
	// Default is the backend name for a spec without one.
	Default string
	// DefaultImage is used for a spec without an image.
	DefaultImage string
	DefaultTTL   time.Duration
	MaxTTL       time.Duration
	Log          *slog.Logger
	Now          func() time.Time

	clients sync.Map // backend name -> *http.Client
}

var _ api.ExecutionService = (*Service)(nil)

// Register adds the service operations to s.
func Register(s *rpc.Server, svc *Service) {
	s.Register("ExecutionService", api.ExecutionServiceOps(svc))
}

func (s *Service) now() uint64 {
	if s.Now != nil {
		return uint64(s.Now().UnixMilli())
	}
	return uint64(time.Now().UnixMilli())
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) ttls() (def, max time.Duration) {
	def, max = s.DefaultTTL, s.MaxTTL
	if def == 0 {
		def = time.Hour
	}
	if max == 0 {
		max = 7 * 24 * time.Hour
	}
	return min(def, max), max
}

func privileged(p authn.Principal) bool { return p.Has(ScopeActAs) || p.Has(ScopeAdmin) }

func newID() string {
	b := make([]byte, 10)
	rand.Read(b)
	return "env_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (s *Service) checkSpec(spec *api.EnvironmentSpec) (string, error) {
	name := s.Default
	if spec.Backend != nil && *spec.Backend != "" {
		name = *spec.Backend
	}
	if _, ok := s.Backends[name]; !ok {
		return "", rpc.Invalid("backend %q is not available", name)
	}
	spec.Backend = &name
	if spec.Image == "" {
		spec.Image = s.DefaultImage
	}
	if spec.Image == "" || len(spec.Image) > 512 || strings.ContainsAny(spec.Image, " \x00\n") {
		return "", rpc.Invalid("image is required")
	}
	if spec.Network != nil {
		switch *spec.Network {
		case "", "none", "cluster", "egress":
		default:
			return "", rpc.Invalid(`network must be "none", "cluster", or "egress"`)
		}
	}
	if spec.Workdir != nil && *spec.Workdir != "" {
		w := *spec.Workdir
		if !path.IsAbs(w) || path.Clean(w) != w || w == "/" || strings.ContainsAny(w, "\x00") || len(w) > 256 {
			return "", rpc.Invalid("workdir must be a clean absolute path below /")
		}
	}
	check := func(v *uint64, max uint64, what string) error {
		if v != nil && *v > max {
			return rpc.Invalid("%s must be at most %d", what, max)
		}
		return nil
	}
	if err := errors.Join(check(spec.CpuMillis, 64000, "cpu_millis"), check(spec.MemoryMb, 256<<10, "memory_mb"),
		check(spec.DiskMb, 1<<20, "disk_mb")); err != nil {
		return "", rpc.Invalid("%v", err)
	}
	if len(spec.Env) > 128 {
		return "", rpc.Invalid("at most 128 env entries")
	}
	for k, v := range spec.Env {
		if !envName.MatchString(k) || len(v) > 64<<10 || strings.IndexByte(v, 0) >= 0 {
			return "", rpc.Invalid("env %q: names are [A-Za-z_][A-Za-z0-9_]*; values are at most 64 KiB without NUL", k)
		}
	}
	if len(spec.Labels) > 64 {
		return "", rpc.Invalid("at most 64 labels")
	}
	for k, v := range spec.Labels {
		if k == "" || len(k) > 256 || len(v) > 4096 {
			return "", rpc.Invalid("label keys must be 1-256 bytes and values at most 4096 bytes")
		}
	}
	return name, nil
}

// redacted returns spec without env values. Values are given to the
// backend only; they are not stored or returned.
func redacted(spec api.EnvironmentSpec) api.EnvironmentSpec {
	if spec.Env != nil {
		m := make(map[string]string, len(spec.Env))
		for k := range spec.Env {
			m[k] = ""
		}
		spec.Env = m
	}
	return spec
}

func (s *Service) Create(ctx context.Context, req api.CreateRequest) (api.Environment, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Environment{}, err
	}
	owner := p.Subject
	if req.Owner != nil && *req.Owner != "" && *req.Owner != p.Subject {
		if !privileged(p) {
			return api.Environment{}, rpc.Denied("owner override needs %s", ScopeActAs)
		}
		owner = *req.Owner
	}
	spec := req.Spec
	name, err := s.checkSpec(&spec)
	if err != nil {
		return api.Environment{}, err
	}
	def, max := s.ttls()
	ttl := def
	if spec.TtlSeconds != nil && *spec.TtlSeconds != 0 {
		ttl = time.Duration(*spec.TtlSeconds) * time.Second
		if ttl > max {
			return api.Environment{}, rpc.Invalid("ttl_seconds must be at most %d", int64(max.Seconds()))
		}
	}
	now := s.now()
	rec := store.Record{
		Env: api.Environment{
			Id: newID(), Spec: redacted(spec), Owner: owner, Status: StatusCreating,
			CreatedMs: now, ExpiresMs: now + uint64(ttl.Milliseconds()),
		},
		Backend: name,
		Token:   newToken(),
	}
	if err := s.DB.Insert(ctx, rec); err != nil {
		return api.Environment{}, err
	}
	s.log().Info("environment create", "id", rec.Env.Id, "owner", owner, "backend", name, "image", spec.Image)
	if err := s.Backends[name].Create(ctx, rec.Env.Id, spec, rec.Token); err != nil {
		s.log().Error("environment create failed", "id", rec.Env.Id, "err", err)
		s.DB.SetState(context.WithoutCancel(ctx), rec.Env.Id, StatusFailed, "create: "+err.Error(), "", s.now())
		return api.Environment{}, rpc.Unavailable("create environment %s: %v", rec.Env.Id, err)
	}
	if req.WaitReadyMs != nil && *req.WaitReadyMs > 0 {
		wait := min(time.Duration(*req.WaitReadyMs)*time.Millisecond, maxWait)
		wctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		for {
			if rec, err = s.refresh(wctx, rec); err != nil && wctx.Err() == nil {
				return api.Environment{}, err
			}
			if rec.Env.Status != StatusCreating {
				break
			}
			select {
			case <-wctx.Done():
				return rec.Env, nil
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return rec.Env, nil
}

// refresh asks the backend about a creating environment and records the
// answer. A ready environment must also answer an authenticated ping.
func (s *Service) refresh(ctx context.Context, rec store.Record) (store.Record, error) {
	if rec.Env.Status != StatusCreating {
		return rec, nil
	}
	be, ok := s.Backends[rec.Backend]
	if !ok {
		return rec, nil
	}
	st, err := be.Status(ctx, rec.Env.Id)
	if err != nil {
		return rec, rpc.Unavailable("environment status: %v", err)
	}
	status, msg := StatusCreating, st.Message
	switch st.State {
	case backend.Ready:
		c := &envd.Client{Base: st.Endpoint, Token: rec.Token, HTTP: s.client(rec.Backend)}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := c.Ping(pctx)
		cancel()
		if err == nil {
			status, msg = StatusReady, ""
		} else {
			msg = "envd is not reachable: " + err.Error()
		}
	case backend.Failed:
		status = StatusFailed
	case backend.Gone:
		status = StatusFailed
		msg = "environment is gone: " + msg
	}
	if status == rec.Env.Status && msg == deref(rec.Env.Message) {
		return rec, nil
	}
	if status == StatusFailed {
		s.log().Warn("environment failed", "id", rec.Env.Id, "message", msg)
	}
	return s.DB.SetState(context.WithoutCancel(ctx), rec.Env.Id, status, msg, st.Endpoint, s.now())
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Service) client(name string) *http.Client {
	if c, ok := s.clients.Load(name); ok {
		return c.(*http.Client)
	}
	c, _ := s.clients.LoadOrStore(name, s.Backends[name].HTTP())
	return c.(*http.Client)
}

// load returns a record the caller may use.
func (s *Service) load(ctx context.Context, id string) (store.Record, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return store.Record{}, err
	}
	if id == "" {
		return store.Record{}, rpc.Invalid("id is required")
	}
	rec, err := s.DB.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return rec, rpc.NotFound("environment %s not found", id)
	}
	if err != nil {
		return rec, err
	}
	if rec.Env.Owner != p.Subject && !privileged(p) {
		return rec, rpc.Denied("environment %s belongs to another owner", id)
	}
	return rec, nil
}

func (s *Service) Get(ctx context.Context, req api.GetRequest) (api.Environment, error) {
	rec, err := s.load(ctx, req.Id)
	if err != nil {
		return api.Environment{}, err
	}
	rec, err = s.refresh(ctx, rec)
	return rec.Env, err
}

func (s *Service) List(ctx context.Context, req api.ListRequest) (api.ListResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListResponse{}, err
	}
	owner := p.Subject
	if p.Has(ScopeAdmin) {
		owner = ""
	}
	if req.Owner != nil && *req.Owner != "" {
		if *req.Owner != p.Subject && !privileged(p) {
			return api.ListResponse{}, rpc.Denied("owner filter needs %s", ScopeActAs)
		}
		owner = *req.Owner
	}
	n := defaultLimit
	if req.Limit != nil && *req.Limit > 0 {
		n = int(min(*req.Limit, maxLimit))
	}
	recs, err := s.DB.List(ctx, owner, deref(req.Status), n)
	if err != nil {
		return api.ListResponse{}, err
	}
	out := api.ListResponse{Environments: make([]api.Environment, 0, len(recs))}
	for _, r := range recs {
		out.Environments = append(out.Environments, r.Env)
	}
	return out, nil
}

func (s *Service) destroy(ctx context.Context, rec store.Record) (store.Record, error) {
	if rec.Env.Status == StatusDestroyed {
		return rec, nil
	}
	if be, ok := s.Backends[rec.Backend]; ok {
		if err := be.Destroy(ctx, rec.Env.Id); err != nil {
			return rec, rpc.Unavailable("destroy environment: %v", err)
		}
	}
	s.log().Info("environment destroyed", "id", rec.Env.Id)
	out, err := s.DB.SetState(ctx, rec.Env.Id, StatusDestroyed, deref(rec.Env.Message), "", s.now())
	if errors.Is(err, store.ErrNotFound) { // Destroyed at the same time.
		return s.DB.Get(ctx, rec.Env.Id)
	}
	return out, err
}

func (s *Service) Destroy(ctx context.Context, req api.DestroyRequest) (api.Environment, error) {
	rec, err := s.load(ctx, req.Id)
	if err != nil {
		return api.Environment{}, err
	}
	rec, err = s.destroy(ctx, rec)
	return rec.Env, err
}

func (s *Service) Hold(ctx context.Context, req api.HoldRequest) (api.Environment, error) {
	rec, err := s.load(ctx, req.Id)
	if err != nil {
		return api.Environment{}, err
	}
	if rec.Env.Status == StatusDestroyed {
		return api.Environment{}, rpc.Conflict("environment %s is destroyed", req.Id)
	}
	rec, err = s.DB.SetHeld(ctx, req.Id, req.Held)
	return rec.Env, err
}

func (s *Service) Extend(ctx context.Context, req api.ExtendRequest) (api.Environment, error) {
	rec, err := s.load(ctx, req.Id)
	if err != nil {
		return api.Environment{}, err
	}
	if rec.Env.Status == StatusDestroyed {
		return api.Environment{}, rpc.Conflict("environment %s is destroyed", req.Id)
	}
	_, max := s.ttls()
	ttl := time.Duration(req.TtlSeconds) * time.Second
	if req.TtlSeconds == 0 || ttl > max {
		return api.Environment{}, rpc.Invalid("ttl_seconds must be 1-%d", int64(max.Seconds()))
	}
	rec, err = s.DB.SetExpiry(ctx, req.Id, s.now()+uint64(ttl.Milliseconds()))
	return rec.Env, err
}

// agent returns the envd client of a ready environment.
func (s *Service) agent(ctx context.Context, id string) (*envd.Client, store.Record, error) {
	rec, err := s.load(ctx, id)
	if err != nil {
		return nil, rec, err
	}
	if rec, err = s.refresh(ctx, rec); err != nil {
		return nil, rec, err
	}
	if rec.Env.Status != StatusReady {
		return nil, rec, rpc.Conflict("environment %s is %s", id, rec.Env.Status)
	}
	return &envd.Client{Base: rec.Endpoint, Token: rec.Token, HTTP: s.client(rec.Backend)}, rec, nil
}

// agentError maps an envd failure to a client-visible error. When envd
// cannot be reached, it checks whether the environment still exists.
func (s *Service) agentError(ctx context.Context, rec store.Record, err error) error {
	var e *envd.Error
	if errors.As(err, &e) {
		switch e.Code {
		case http.StatusBadRequest:
			return rpc.Invalid("%s", e.Message)
		case http.StatusNotFound:
			return rpc.NotFound("%s", e.Message)
		case http.StatusForbidden:
			return rpc.Denied("%s", e.Message)
		}
		return rpc.Unavailable("envd: %s", e.Message)
	}
	if ctx.Err() == nil {
		if st, serr := s.Backends[rec.Backend].Status(ctx, rec.Env.Id); serr == nil && (st.State == backend.Failed || st.State == backend.Gone) {
			s.DB.SetState(context.WithoutCancel(ctx), rec.Env.Id, StatusFailed, "environment lost: "+st.Message, "", s.now())
		}
	}
	return rpc.Unavailable("environment %s is not reachable: %v", rec.Env.Id, err)
}

func checkPath(p string) error {
	if p == "" || strings.IndexByte(p, 0) >= 0 || len(p) > 4096 {
		return rpc.Invalid("path must be 1-4096 bytes without NUL")
	}
	return nil
}

func (s *Service) Exec(ctx context.Context, req api.ExecRequest) (api.ExecResult, error) {
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return api.ExecResult{}, rpc.Invalid("argv is required")
	}
	if req.Cwd != nil && *req.Cwd != "" {
		if err := checkPath(*req.Cwd); err != nil {
			return api.ExecResult{}, err
		}
	}
	c, rec, err := s.agent(ctx, req.Environment)
	if err != nil {
		return api.ExecResult{}, err
	}
	res, err := c.Exec(ctx, req)
	if err != nil {
		return res, s.agentError(ctx, rec, err)
	}
	return res, nil
}

func (s *Service) ReadFile(ctx context.Context, req api.ReadFileRequest) (api.FileContent, error) {
	if err := checkPath(req.Path); err != nil {
		return api.FileContent{}, err
	}
	c, rec, err := s.agent(ctx, req.Environment)
	if err != nil {
		return api.FileContent{}, err
	}
	res, err := c.ReadFile(ctx, req)
	if err != nil {
		return res, s.agentError(ctx, rec, err)
	}
	return res, nil
}

func (s *Service) WriteFile(ctx context.Context, req api.WriteFileRequest) (api.WriteFileResponse, error) {
	if err := checkPath(req.Path); err != nil {
		return api.WriteFileResponse{}, err
	}
	c, rec, err := s.agent(ctx, req.Environment)
	if err != nil {
		return api.WriteFileResponse{}, err
	}
	res, err := c.WriteFile(ctx, req)
	if err != nil {
		return res, s.agentError(ctx, rec, err)
	}
	return res, nil
}

func (s *Service) ListFiles(ctx context.Context, req api.ListFilesRequest) (api.ListFilesResponse, error) {
	if err := checkPath(req.Path); err != nil {
		return api.ListFilesResponse{}, err
	}
	c, rec, err := s.agent(ctx, req.Environment)
	if err != nil {
		return api.ListFilesResponse{}, err
	}
	res, err := c.ListFiles(ctx, req)
	if err != nil {
		return res, s.agentError(ctx, rec, err)
	}
	return res, nil
}

// Reap destroys expired, unheld environments and refreshes creating ones.
func (s *Service) Reap(ctx context.Context) error {
	expired, err := s.DB.Expired(ctx, s.now())
	if err != nil {
		return err
	}
	var errs []error
	for _, rec := range expired {
		s.log().Info("environment expired", "id", rec.Env.Id, "owner", rec.Env.Owner)
		if _, err := s.destroy(ctx, rec); err != nil {
			errs = append(errs, err)
		}
	}
	creating, err := s.DB.Creating(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, rec := range creating {
		if _, err := s.refresh(ctx, rec); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RunReaper calls Reap every interval until ctx ends.
func (s *Service) RunReaper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Reap(ctx); err != nil && ctx.Err() == nil {
				s.log().Warn("reaper", "err", err)
			}
		}
	}
}
