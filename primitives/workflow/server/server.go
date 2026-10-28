// Package server implements WorkflowService.
package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/workflow/api"
	"github.com/catalystcommunity/magi/primitives/workflow/store"
)

// Scopes.
const (
	ScopeAdmin  = "workflow:admin"
	ScopeActAs  = "workflow:act-as"
	ScopeWorker = "workflow:worker"
	ScopeDecide = "workflow:decide"
)

const (
	maxWait          = 30 * time.Second
	defaultLeaseMs   = 60_000
	minLeaseMs       = 1_000
	maxLeaseMs       = 3_600_000
	defaultListLimit = 50
	maxListLimit     = 500
)

// Service implements api.WorkflowService.
type Service struct {
	Store *store.Store
	Queue Queue
	Log   *slog.Logger
	// ClaimPoll is the interval between queue polls in claim. Default 200ms.
	ClaimPoll time.Duration
	// WatchPoll is the interval between database reads in watch-run. Default 500ms.
	WatchPoll time.Duration

	mu   sync.Mutex
	defs map[string]*api.Definition
}

var _ api.WorkflowService = (*Service)(nil)

func newID(prefix string) string {
	b := make([]byte, 15)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func now() uint64 { return uint64(store.Now()) }

func storeErr(err error, what string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return rpc.NotFound("%s not found", what)
	case errors.Is(err, store.ErrSkip):
		return nil
	}
	return err
}

func actAs(p authn.Principal) bool { return p.Has(ScopeAdmin) || p.Has(ScopeActAs) }

func canRead(p authn.Principal, r *api.Run) bool {
	return r.Owner == p.Subject || actAs(p) || p.Has(ScopeDecide) || p.Has(ScopeWorker)
}

func worker(ctx context.Context) error {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return err
	}
	if !p.Has(ScopeWorker) && !p.Has(ScopeAdmin) {
		return rpc.Denied("scope %s required", ScopeWorker)
	}
	return nil
}

// definition returns a pinned definition revision. Revisions never change.
func (s *Service) definition(ctx context.Context, name string, rev uint64) (*api.Definition, error) {
	k := name + "@" + strconv.FormatUint(rev, 10)
	s.mu.Lock()
	d := s.defs[k]
	s.mu.Unlock()
	if d != nil {
		return d, nil
	}
	def, err := s.Store.GetDefinition(ctx, name, rev)
	if err != nil {
		return nil, storeErr(err, "definition "+k)
	}
	s.mu.Lock()
	if s.defs == nil {
		s.defs = map[string]*api.Definition{}
	}
	s.defs[k] = &def
	s.mu.Unlock()
	return &def, nil
}

func (s *Service) PutDefinition(ctx context.Context, req api.Definition) (api.Definition, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return req, err
	}
	if err := Validate(req); err != nil {
		return req, err
	}
	for i := range req.Steps {
		if req.Steps[i].Next == nil {
			req.Steps[i].Next = map[string]string{}
		}
	}
	return s.Store.PutDefinition(ctx, req, p.Subject, func(owner string) error {
		if owner != p.Subject && !actAs(p) {
			return rpc.Denied("definition %s belongs to %s", req.Name, owner)
		}
		return nil
	})
}

func (s *Service) GetDefinition(ctx context.Context, req api.GetDefinitionRequest) (api.Definition, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.Definition{}, err
	}
	d, err := s.Store.GetDefinition(ctx, req.Name, 0)
	return d, storeErr(err, "definition "+req.Name)
}

func (s *Service) Start(ctx context.Context, req api.StartRequest) (api.Run, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Run{}, err
	}
	owner := p.Subject
	if req.Owner != nil && *req.Owner != "" && *req.Owner != owner {
		if !actAs(p) {
			return api.Run{}, rpc.Denied("scope %s required to set owner", ScopeActAs)
		}
		owner = *req.Owner
	}
	def, err := s.Store.GetDefinition(ctx, req.Definition, 0)
	if err != nil {
		return api.Run{}, storeErr(err, "definition "+req.Definition)
	}
	t := now()
	vars := map[string]string{}
	maps.Copy(vars, req.Vars)
	run := api.Run{Id: newID("run_"), Definition: def.Name, Revision: def.Revision, Owner: owner,
		Vars: vars, History: []api.StepState{}, CreatedMs: t, UpdatedMs: t}
	var tasks []store.Task
	if task := enter(&run, &def, def.Start, 1, t); task != nil {
		tasks = append(tasks, *task)
	}
	key := ""
	if req.Key != nil {
		key = *req.Key
	}
	r, existed, err := s.Store.CreateRun(ctx, run, key, tasks)
	if err != nil {
		return r, err
	}
	if existed {
		if r.Definition != req.Definition {
			return r, rpc.Conflict("key %q was used for a run of %s", key, r.Definition)
		}
		return r, nil
	}
	s.flush(ctx, tasks)
	return r, nil
}

func (s *Service) GetRun(ctx context.Context, req api.GetRunRequest) (api.Run, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Run{}, err
	}
	r, err := s.Store.GetRun(ctx, req.Id)
	if err == nil && !canRead(p, &r) {
		err = store.ErrNotFound
	}
	return r, storeErr(err, "run "+req.Id)
}

func (s *Service) ListRuns(ctx context.Context, req api.ListRunsRequest) (api.ListRunsResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListRunsResponse{}, err
	}
	owner := p.Subject
	if actAs(p) {
		owner = ""
	}
	limit := defaultListLimit
	if req.Limit != nil && *req.Limit > 0 {
		limit = int(min(*req.Limit, maxListLimit))
	}
	deref := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	runs, err := s.Store.ListRuns(ctx, owner, deref(req.Status), deref(req.Definition), limit)
	return api.ListRunsResponse{Runs: runs}, err
}

func (s *Service) WatchRun(ctx context.Context, req api.WatchRunRequest) (api.Run, error) {
	wait := maxWait
	if req.WaitMs != nil {
		wait = min(time.Duration(*req.WaitMs)*time.Millisecond, maxWait)
	}
	deadline := time.Now().Add(wait)
	poll := cmp(s.WatchPoll, 500*time.Millisecond)
	for {
		r, err := s.GetRun(ctx, api.GetRunRequest{Id: req.Id})
		if err != nil || r.UpdatedMs > req.AfterMs || !time.Now().Before(deadline) {
			return r, err
		}
		if !sleep(ctx, min(poll, time.Until(deadline))) {
			return r, nil
		}
	}
}

func cmp(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Service) Claim(ctx context.Context, req api.ClaimRequest) (api.ClaimResponse, error) {
	if err := worker(ctx); err != nil {
		return api.ClaimResponse{}, err
	}
	if req.Queue == "" || req.Worker == "" {
		return api.ClaimResponse{}, rpc.Invalid("queue and worker are required")
	}
	var wait time.Duration
	if req.WaitMs != nil {
		wait = min(time.Duration(*req.WaitMs)*time.Millisecond, maxWait)
	}
	lease := uint64(defaultLeaseMs)
	if req.LeaseMs != nil && *req.LeaseMs > 0 {
		lease = max(min(*req.LeaseMs, maxLeaseMs), minLeaseMs)
	}
	deadline := time.Now().Add(wait)
	poll := cmp(s.ClaimPoll, 200*time.Millisecond)
	for {
		d, err := s.Queue.Claim(ctx, req.Queue)
		if err != nil {
			return api.ClaimResponse{}, rpc.Unavailable("queue: %v", err)
		}
		if d != nil {
			a, err := s.accept(ctx, req, d, lease)
			if err != nil || a != nil {
				return api.ClaimResponse{Assignment: a}, err
			}
			continue
		}
		if !time.Now().Before(deadline) || !sleep(ctx, min(poll, time.Until(deadline))) {
			return api.ClaimResponse{}, nil
		}
	}
}

// accept records a queue claim on its run. It returns nil when the queue
// task is stale; then the queue task is closed if the step is over.
func (s *Service) accept(ctx context.Context, req api.ClaimRequest, d *Delivery, lease uint64) (*api.Assignment, error) {
	var a *api.Assignment
	closeTask := true
	if i := strings.LastIndexByte(d.Key, '/'); i > 0 {
		_, _, err := s.Store.UpdateRun(ctx, d.Key[:i], func(tx *store.RunTx, r *api.Run) error {
			t, err := tx.Task(d.Key, "")
			if err != nil || t == nil {
				return errors.Join(err, store.ErrSkip)
			}
			st := &r.History[t.Index]
			if t.State == store.TaskClaimed {
				closeTask = false // a duplicate of a live claim; the owner closes it
				return store.ErrSkip
			}
			if (t.State != store.TaskSubmit && t.State != store.TaskQueued) || st.Status != StepPending || finished(r.Status) {
				return store.ErrSkip
			}
			t.State, t.UUID, t.Revision = store.TaskClaimed, d.UUID, d.Revision
			t.Claim, t.Worker, t.LeaseUntil = newID("clm_"), req.Worker, store.Now()+int64(lease)
			tx.Put(*t)
			st.Status, st.Worker = StepClaimed, &req.Worker
			vars := map[string]string{}
			maps.Copy(vars, r.Vars)
			a = &api.Assignment{Run: r.Id, Step: t.Step, Attempt: st.Attempt, Claim: t.Claim, Owner: r.Owner, Vars: vars}
			return nil
		})
		if err != nil && !errors.Is(err, store.ErrSkip) && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	if a == nil && closeTask {
		if err := s.Queue.Finish(ctx, req.Queue, d.Ticket); err != nil && s.Log != nil {
			s.Log.Warn("close stale queue task", "key", d.Key, "err", err)
		}
	}
	return a, nil
}

// claimed locks the task of a claim and checks that the claim is current.
func claimed(tx *store.RunTx, r *api.Run, step, claim string) (*store.Task, error) {
	if claim == "" {
		return nil, rpc.Invalid("claim is required")
	}
	t, err := tx.Task("", claim)
	if err != nil {
		return nil, err
	}
	if t == nil || t.State != store.TaskClaimed || t.Step != step || t.LeaseUntil < store.Now() || finished(r.Status) {
		return nil, rpc.Conflict("claim is not current")
	}
	return t, nil
}

func (s *Service) Heartbeat(ctx context.Context, req api.HeartbeatRequest) (api.Ack, error) {
	if err := worker(ctx); err != nil {
		return api.Ack{}, err
	}
	lease := uint64(defaultLeaseMs)
	if req.LeaseMs != nil && *req.LeaseMs > 0 {
		lease = max(min(*req.LeaseMs, maxLeaseMs), minLeaseMs)
	}
	ok, err := s.Store.Heartbeat(ctx, req.Run, req.Step, req.Claim, store.Now()+int64(lease))
	if err == nil && !ok {
		err = rpc.Conflict("claim is not current")
	}
	return api.Ack{Ok: ok}, err
}

func (s *Service) Progress(ctx context.Context, req api.ProgressRequest) (api.Ack, error) {
	if err := worker(ctx); err != nil {
		return api.Ack{}, err
	}
	_, _, err := s.Store.UpdateRun(ctx, req.Run, func(tx *store.RunTx, r *api.Run) error {
		t, err := claimed(tx, r, req.Step, req.Claim)
		if err != nil {
			return err
		}
		st := &r.History[t.Index]
		st.Progress = append(st.Progress, api.Progress{AtMs: now(), Note: req.Note, Ref: req.Ref})
		return nil
	})
	return api.Ack{Ok: err == nil}, storeErr(err, "run "+req.Run)
}

func (s *Service) Complete(ctx context.Context, req api.CompleteRequest) (api.Run, error) {
	if err := worker(ctx); err != nil {
		return api.Run{}, err
	}
	if req.Outcome == "" {
		return api.Run{}, rpc.Invalid("outcome is required")
	}
	var tasks []store.Task
	r, _, err := s.Store.UpdateRun(ctx, req.Run, func(tx *store.RunTx, r *api.Run) error {
		t, err := claimed(tx, r, req.Step, req.Claim)
		if err != nil {
			return err
		}
		def, err := s.definition(ctx, r.Definition, r.Revision)
		if err != nil {
			return err
		}
		step := findStep(def, t.Step)
		if req.Outcome != "fail" && req.Outcome != "retry" && !outcomeAllowed(step, req.Outcome) {
			return rpc.Invalid("outcome %q is not allowed for step %s", req.Outcome, step.Id)
		}
		status := StepDone
		if req.Outcome == "fail" || req.Outcome == "retry" {
			status = StepFailed
		}
		ts := now()
		endStep(&r.History[t.Index], status, req.Outcome, ts)
		maps.Copy(r.Vars, req.Outputs)
		t.State = store.TaskFinish
		tasks = append(tasks, *t)
		errText := ""
		if req.Error != nil {
			errText = *req.Error
		}
		if nt := advance(r, def, req.Outcome, errText, ts); nt != nil {
			tasks = append(tasks, *nt)
		}
		for _, t := range tasks {
			tx.Put(t)
		}
		return nil
	})
	if err != nil {
		return r, storeErr(err, "run "+req.Run)
	}
	s.flush(ctx, tasks)
	return r, nil
}

func (s *Service) Decide(ctx context.Context, req api.DecideRequest) (api.Run, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Run{}, err
	}
	if req.Outcome == "" {
		return api.Run{}, rpc.Invalid("outcome is required")
	}
	var tasks []store.Task
	r, _, err := s.Store.UpdateRun(ctx, req.Run, func(tx *store.RunTx, r *api.Run) error {
		if r.Owner != p.Subject && !actAs(p) && !p.Has(ScopeDecide) {
			return rpc.Denied("only the run owner or scope %s may decide", ScopeDecide)
		}
		cur := current(r)
		if r.Status != RunWaiting || cur == nil || cur.Step != req.Step || cur.Status != StepWaiting {
			return rpc.Conflict("step %s is not waiting for a decision", req.Step)
		}
		def, err := s.definition(ctx, r.Definition, r.Revision)
		if err != nil {
			return err
		}
		if !outcomeAllowed(findStep(def, req.Step), req.Outcome) {
			return rpc.Invalid("outcome %q is not allowed for step %s", req.Outcome, req.Step)
		}
		ts := now()
		endStep(cur, StepDone, req.Outcome, ts)
		cur.DecidedBy, cur.Comment = &p.Subject, req.Comment
		if req.Comment != nil {
			r.Vars[req.Step+".comment"] = *req.Comment
		}
		if nt := advance(r, def, req.Outcome, "", ts); nt != nil {
			tasks = append(tasks, *nt)
			tx.Put(*nt)
		}
		return nil
	})
	if err != nil {
		return r, storeErr(err, "run "+req.Run)
	}
	s.flush(ctx, tasks)
	return r, nil
}

func (s *Service) Cancel(ctx context.Context, req api.CancelRequest) (api.Run, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Run{}, err
	}
	var tasks []store.Task
	r, _, err := s.Store.UpdateRun(ctx, req.Id, func(tx *store.RunTx, r *api.Run) error {
		if r.Owner != p.Subject && !actAs(p) {
			return store.ErrNotFound
		}
		if r.Status == RunCancelled {
			return store.ErrSkip
		}
		if finished(r.Status) {
			return rpc.Conflict("run is %s", r.Status)
		}
		if cur := current(r); cur != nil && cur.EndedMs == nil {
			endStep(cur, StepCancelled, "", now())
		}
		r.Status, r.CurrentStep = RunCancelled, ""
		msg := "cancelled by " + p.Subject
		if req.Reason != nil && *req.Reason != "" {
			msg += ": " + *req.Reason
		}
		r.Error = &msg
		open, err := tx.OpenTasks()
		for _, t := range open {
			t.State = store.TaskFinish
			tasks = append(tasks, t)
			tx.Put(t)
		}
		return err
	})
	if errors.Is(err, store.ErrSkip) {
		return s.Store.GetRun(ctx, req.Id)
	}
	if err != nil {
		return r, storeErr(err, "run "+req.Id)
	}
	s.flush(ctx, tasks)
	return r, nil
}
