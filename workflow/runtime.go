package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
)

func fmtKey(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func stepKey(owner w.ScopeRef, run, step string) string {
	return "wf/" + fmtKey(string(w.EncodeScopeRef(owner))+"\x00"+run+"\x00"+step)
}
func (s *Store) provenance(run w.WorkflowRun, step string) Provenance {
	id := stepKey(run.Request.Definition.Owner, run.Request.RunId, step)
	return Provenance{Actor: run.Actor, Admission: w.RecordRef{Authority: s.Application.Identity, Kind: "workflow-admission", RecordId: id}, Cause: w.RecordRef{Authority: s.Application.Identity, Kind: "workflow-run", RecordId: run.Request.RunId}}
}
func readRun(ctx context.Context, tx pgx.Tx, owner w.ScopeRef, id string) (w.WorkflowRun, error) {
	var v w.WorkflowRun
	var raw, actor []byte
	e := tx.QueryRow(ctx, `SELECT body,actor,accepted_at FROM workflow_runs WHERE owner=$1 AND id=$2`, w.EncodeScopeRef(owner), id).Scan(&raw, &actor, &v.AcceptedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if e != nil {
		return v, e
	}
	v.Request, e = w.DecodeStartWorkflow(raw)
	if e != nil {
		return v, e
	}
	v.Actor, e = w.DecodeApplicationRef(actor)
	v.Steps = []w.WorkflowStepObservation{}
	return v, e
}
func (s *Store) Start(ctx context.Context, a w.ApplicationRef, r w.StartWorkflow, proof []byte) (w.WorkflowRun, error) {
	if proof == nil {
		proof = []byte{}
	}
	var v w.WorkflowRun
	// Source-policy interpretation is not implemented. Never silently discard it.
	if !portName.MatchString(r.RunId) || len(r.Causes) > 32 || len(r.Restrictions.Requirements) != 0 || len(proof) > p.MaxFrame {
		return v, ErrInvalid
	}
	for _, cause := range r.Causes {
		if !validRef(cause) {
			return v, ErrInvalid
		}
	}
	raw := w.EncodeStartWorkflow(r)
	if len(raw) > MaxDefinitionBytes {
		return v, ErrInvalid
	}
	if _, e := p.Decode(raw, w.DecodeStartWorkflow, w.EncodeStartWorkflow); e != nil {
		return v, ErrInvalid
	}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, r.Definition.Owner, "run"); e != nil {
		return v, e
	}
	if e = s.authorize(ctx, tx, a, r.Definition.Owner, "read"); e != nil {
		return v, e
	}
	d, e := readDefinition(ctx, tx, r.Definition)
	if e != nil {
		return v, e
	}
	if e = ValidateValues(d.Inputs, r.Inputs); e != nil {
		return v, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO workflow_runs(owner,id,body,actor,proof) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, w.EncodeScopeRef(r.Definition.Owner), r.RunId, raw, w.EncodeApplicationRef(a), proof)
	if e != nil {
		return v, e
	}
	v, e = readRun(ctx, tx, r.Definition.Owner, r.RunId)
	if e != nil {
		return v, e
	}
	if v.Actor != a || !bytes.Equal(raw, w.EncodeStartWorkflow(v.Request)) {
		return v, ErrConflict
	}
	// Admission and scheduling are one commit. An exact start replay cannot
	// reset a lease, backoff, or completed coordination record.
	if _, e = tx.Exec(ctx, `INSERT INTO workflow_coordination(owner,run_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, w.EncodeScopeRef(r.Definition.Owner), r.RunId); e != nil {
		return v, e
	}
	// A start receipt contains admitted input only. ReadRun supplies observations.
	return v, tx.Commit(ctx)
}

type stepRecord struct {
	input                           []byte
	prompt, submit, claim, complete string
	task                            *api.GuardedTask
	dispatched                      bool
	result, evidence, actor         []byte
}

func loadStep(ctx context.Context, tx pgx.Tx, owner w.ScopeRef, run, step string) (stepRecord, error) {
	var v stepRecord
	var task []byte
	e := tx.QueryRow(ctx, `SELECT input,prompt,submit_id,claim_id,complete_id,task,dispatched,result,evidence,result_actor FROM workflow_run_steps WHERE owner=$1 AND run_id=$2 AND step_id=$3 FOR UPDATE`, w.EncodeScopeRef(owner), run, step).Scan(&v.input, &v.prompt, &v.submit, &v.claim, &v.complete, &task, &v.dispatched, &v.result, &v.evidence, &v.actor)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if e != nil {
		return v, e
	}
	if len(task) > 0 {
		t, e := api.DecodeGuardedTask(task)
		if e != nil {
			return v, e
		}
		v.task = &t
	}
	return v, nil
}
func (s *Store) snapshot(ctx context.Context, tx pgx.Tx, v w.WorkflowRun, d w.WorkflowDefinition) (w.WorkflowRun, error) {
	sources := map[string]w.WorkflowValues{"$input": v.Request.Inputs}
	all := true
	// These are retained transition receipts, not a second writable task status.
	for _, step := range d.Steps {
		o := w.WorkflowStepObservation{StepId: step.Id, Phase: "dependencies_pending", ObservedAt: time.Now().UTC().Truncate(time.Millisecond)}
		r, e := loadStep(ctx, tx, d.Owner, v.Request.RunId, step.Id)
		if errors.Is(e, ErrNotFound) {
			v.Steps = append(v.Steps, o)
			all = false
			continue
		}
		if e != nil {
			return v, e
		}
		o.Phase = "submission_pending"
		if r.task != nil {
			o.TaskId = &r.task.Task.Uuid
			o.TaskRevision = &r.task.Revision
			o.Phase = "ready"
			if step.Kind == "human" {
				o.Phase = "waiting_human"
			}
		}
		if r.dispatched {
			o.Phase = "outcome_unknown"
		}
		if r.result == nil && step.Kind == "call" {
			var observation []byte
			err := tx.QueryRow(ctx, `SELECT observation FROM workflow_timeout_attempts WHERE owner=$1 AND run_id=$2 AND step_id=$3
				AND observation IS NOT NULL ORDER BY generation DESC LIMIT 1`, w.EncodeScopeRef(d.Owner), v.Request.RunId, step.Id).Scan(&observation)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return v, err
			}
			if err == nil {
				observed, err := api.DecodeGuardedTask(observation)
				if err != nil {
					return v, err
				}
				if observed.Task.CurrentState == "reconcile_required" {
					o.TaskRevision = &observed.Revision
					if !r.dispatched {
						o.Phase = "reconcile_required"
					}
				}
			}
		}
		if r.result != nil {
			values, e := w.DecodeWorkflowValues(r.result)
			if e != nil {
				return v, e
			}
			o.Result = &values
			o.Phase = "completion_pending"
			var state string
			e = tx.QueryRow(ctx, `SELECT state FROM workflow_intents WHERE installation=$1 AND id=$2`, s.Journal.Installation, r.complete).Scan(&state)
			if e != nil {
				return v, e
			}
			if state == "confirmed" {
				o.Phase = "completed"
				sources[step.Id] = values
			} else if state == "reconcile_required" || state == "rejected" {
				o.Phase = state
			}
		}
		if o.Phase != "completed" {
			all = false
		}
		v.Steps = append(v.Steps, o)
	}
	if all {
		result, e := bindValues(d.Results, sources)
		if e != nil {
			return v, e
		}
		v.Result = &result
	}
	return v, nil
}
func (s *Store) ReadRun(ctx context.Context, a w.ApplicationRef, r w.ReadWorkflowRun) (w.WorkflowRun, error) {
	var v w.WorkflowRun
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, r.Owner, "read"); e != nil {
		return v, e
	}
	v, e = readRun(ctx, tx, r.Owner, r.RunId)
	if e != nil {
		return v, e
	}
	d, e := readDefinition(ctx, tx, v.Request.Definition)
	if e != nil {
		return v, e
	}
	return s.snapshot(ctx, tx, v, d)
}

// Advance admits ready steps and recovers their Corndogs transitions. It never
// invokes a capability. Call execution and human completion are separate actions.
func (s *Store) Advance(ctx context.Context, owner w.ScopeRef, id string) error {
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	v, e := readRun(ctx, tx, owner, id)
	if e != nil {
		return e
	}
	if e = s.authorize(ctx, tx, v.Actor, owner, "run"); e != nil {
		return e
	}
	d, e := readDefinition(ctx, tx, v.Request.Definition)
	if e != nil {
		return e
	}
	v, e = s.snapshot(ctx, tx, v, d)
	if e != nil {
		return e
	}
	sources := map[string]w.WorkflowValues{"$input": v.Request.Inputs}
	done := map[string]bool{}
	for _, o := range v.Steps {
		if o.Phase == "completed" {
			done[o.StepId] = true
			sources[o.StepId] = *o.Result
		}
	}
	for _, step := range d.Steps {
		if done[step.Id] {
			continue
		}
		ready := true
		for _, dep := range step.DependsOn {
			if !done[dep] {
				ready = false
			}
		}
		if !ready {
			continue
		}
		values, e := bindValues(step.Bindings, sources)
		if e != nil {
			return e
		}
		prompt, e := RenderPrompt(step, values)
		if e != nil {
			return e
		}
		key := stepKey(owner, id, step.Id)
		state := "ready"
		if step.Kind == "human" {
			state = "waiting_human"
		}
		raw := api.EncodeSubmitKeyedTaskRequest(api.SubmitKeyedTaskRequest{Queue: key, Guarded: true, CurrentState: state, AutoTargetState: state, Timeout: -1, Payload: []byte(key)})
		if e = s.Journal.PrepareTx(ctx, tx, key+"/submit", "SubmitKeyedTask", raw, s.provenance(v, step.Id)); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO workflow_run_steps(owner,run_id,step_id,input,prompt,submit_id,claim_id,complete_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, w.EncodeScopeRef(owner), id, step.Id, w.EncodeWorkflowValues(values), prompt, key+"/submit", key+"/claim", key+"/complete")
		if e != nil {
			return e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	// Keep network calls out of the local transaction and its policy locks.
	for _, step := range d.Steps {
		if e = s.recoverStep(ctx, v, step); e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
	}
	return nil
}
func (s *Store) recoverStep(ctx context.Context, v w.WorkflowRun, step w.WorkflowStep) error {
	owner, id := v.Request.Definition.Owner, v.Request.RunId
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	r, e := loadStep(ctx, tx, owner, id, step.Id)
	tx.Rollback(ctx)
	if e != nil {
		return e
	}
	receipt, e := s.Journal.Resolve(ctx, r.submit)
	if e != nil {
		return e
	}
	if r.task == nil {
		sub, e := api.DecodeSubmissionReceipt(receipt.Receipt)
		if e != nil {
			return e
		}
		got, e := api.NewCorndogsClient(s.Journal.Transport).GetGuardedTask(ctx, api.GetGuardedTaskRequest{Queue: sub.Queue, Uuid: sub.TaskUuid})
		if e != nil {
			return e
		}
		state := "ready"
		if step.Kind == "human" {
			state = "waiting_human"
		}
		if got.Task == nil || !got.Task.Guarded || got.Task.Terminal || got.Task.Revision != 1 || got.Task.Task.CurrentState != state {
			return ErrReconcile
		}
		_, e = s.Journal.Pool.Exec(ctx, `UPDATE workflow_run_steps SET task=$4 WHERE owner=$1 AND run_id=$2 AND step_id=$3 AND task IS NULL`, w.EncodeScopeRef(owner), id, step.Id, api.EncodeGuardedTask(*got.Task))
		if e != nil {
			return e
		}
		r.task = got.Task
	}
	if step.Kind == "finish" && r.result == nil {
		values, e := w.DecodeWorkflowValues(r.input)
		if e != nil {
			return e
		}
		if e = s.storeResult(ctx, v, step, *r.task, values, s.Application, w.RecordRef{Authority: s.Application.Identity, Kind: "workflow-finish", RecordId: stepKey(owner, id, step.Id)}); e != nil {
			return e
		}
		r.result = r.input
	}
	if r.result != nil {
		_, e = s.Journal.Resolve(ctx, r.complete)
		return e
	}
	return nil
}
func (s *Store) storeResult(ctx context.Context, v w.WorkflowRun, step w.WorkflowStep, task api.GuardedTask, values w.WorkflowValues, actor w.ApplicationRef, evidence w.RecordRef, proof ...[]byte) error {
	if e := ValidateValues(step.Outputs, values); e != nil {
		return e
	}
	if !validRef(evidence) {
		return ErrInvalid
	}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	owner, id := v.Request.Definition.Owner, v.Request.RunId
	if e = s.authorize(ctx, tx, v.Actor, owner, "run"); e != nil {
		return e
	}
	if step.Kind == "human" {
		if e = s.authorize(ctx, tx, actor, owner, "complete"); e != nil {
			return e
		}
	}
	r, e := loadStep(ctx, tx, owner, id, step.Id)
	if e != nil {
		return e
	}
	if r.task == nil || r.task.Task.Uuid != task.Task.Uuid || r.task.Revision != task.Revision {
		return ErrStale
	}
	raw, ev, by := w.EncodeWorkflowValues(values), w.EncodeRecordRef(evidence), w.EncodeApplicationRef(actor)
	if r.result != nil {
		if !bytes.Equal(r.result, raw) || !bytes.Equal(r.evidence, ev) || !bytes.Equal(r.actor, by) {
			return ErrConflict
		}
		return nil
	}
	if e = s.Journal.SaveResultTx(ctx, tx, task.Task.Uuid, task.Revision, raw, stepKey(owner, id, step.Id)); e != nil {
		return e
	}
	request := api.CompleteGuardedTaskRequest{Queue: task.Task.Queue, Uuid: task.Task.Uuid, ExpectedRevision: task.Revision, ExpectedState: &task.Task.CurrentState}
	if e = s.Journal.PrepareTx(ctx, tx, r.complete, "CompleteGuardedTask", api.EncodeCompleteGuardedTaskRequest(request), s.provenance(v, step.Id)); e != nil {
		return e
	}
	var retained []byte
	if len(proof) > 0 {
		retained = proof[0]
	}
	if len(retained) > p.MaxFrame {
		return ErrInvalid
	}
	_, e = tx.Exec(ctx, `UPDATE workflow_run_steps SET result=$4,evidence=$5,result_actor=$6,completion_proof=$7 WHERE owner=$1 AND run_id=$2 AND step_id=$3`, w.EncodeScopeRef(owner), id, step.Id, raw, ev, by, retained)
	if e != nil {
		return e
	}
	// A result must not wait for an earlier unknown-outcome backoff. Preserve
	// this wake even if an active coordinator finishes its older observation.
	_, e = tx.Exec(ctx, `UPDATE workflow_coordination SET wake_requested=true,due_at=LEAST(due_at,clock_timestamp()) WHERE owner=$1 AND run_id=$2 AND due_at IS NOT NULL`, w.EncodeScopeRef(owner), id)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) CompleteHuman(ctx context.Context, a w.ApplicationRef, r w.CompleteWorkflowHuman, proof ...[]byte) (w.WorkflowHumanReceipt, error) {
	receipt := w.WorkflowHumanReceipt{RunId: r.RunId, StepId: r.StepId, TaskId: r.TaskId, TaskRevision: r.TaskRevision}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return receipt, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, r.Owner, "complete"); e != nil {
		return receipt, e
	}
	v, e := readRun(ctx, tx, r.Owner, r.RunId)
	if e != nil {
		return receipt, e
	}
	d, e := readDefinition(ctx, tx, v.Request.Definition)
	if e != nil {
		return receipt, e
	}
	saved, e := loadStep(ctx, tx, r.Owner, r.RunId, r.StepId)
	if e != nil {
		return receipt, e
	}
	if saved.task == nil || saved.task.Task.Uuid != r.TaskId || saved.task.Revision != r.TaskRevision {
		return receipt, ErrStale
	}
	var step w.WorkflowStep
	for _, item := range d.Steps {
		if item.Id == r.StepId {
			step = item
		}
	}
	if step.Kind != "human" {
		return receipt, ErrInvalid
	}
	if e = tx.Commit(ctx); e != nil {
		return receipt, e
	}
	if saved.result == nil {
		current, e := api.NewCorndogsClient(s.Journal.Transport).GetGuardedTask(ctx, api.GetGuardedTaskRequest{Queue: saved.task.Task.Queue, Uuid: r.TaskId})
		if e != nil {
			return receipt, e
		}
		if current.Task == nil || current.Task.Terminal || !current.Task.Guarded || current.Task.Revision != r.TaskRevision || current.Task.Task.CurrentState != "waiting_human" {
			return receipt, ErrStale
		}
	}
	e = s.storeResult(ctx, v, step, *saved.task, r.Result, a, r.Evidence, proof...)
	return receipt, e
}

// CallAdapter must enforce destination authorization. An operation key identifies
// one logical effect. A failed or lost reply is never retried automatically.
type CallRequest struct {
	Key           string
	Actor         w.ApplicationRef
	Definition    w.WorkflowDefinitionRef
	RunID, StepID string
	Inputs        w.WorkflowValues
	Prompt        string
}
type CallAdapter interface {
	Call(context.Context, CallRequest) (w.WorkflowValues, w.RecordRef, error)
}
type Engine struct {
	Store    *Store
	Adapters map[string]CallAdapter
	// CallTimeout is operator configuration, not a value supplied by a workflow.
	// Zero preserves the original eight-second limit.
	CallTimeout time.Duration
}

func (engine *Engine) callBudget() (time.Duration, error) {
	budget := engine.CallTimeout
	if budget == 0 {
		budget = 8 * time.Second
	}
	if budget < time.Second || budget > 120*time.Second || budget%time.Second != 0 {
		return 0, ErrInvalid
	}
	return budget, nil
}

func (engine *Engine) Execute(ctx context.Context, owner w.ScopeRef, id, stepID string) error {
	budget, e := engine.callBudget()
	if e != nil {
		return e
	}
	claimSeconds := int64(budget/time.Second) + 22
	s := engine.Store
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	v, e := readRun(ctx, tx, owner, id)
	if e != nil {
		return e
	}
	if e = s.authorize(ctx, tx, v.Actor, owner, "execute"); e != nil {
		return e
	}
	d, e := readDefinition(ctx, tx, v.Request.Definition)
	if e != nil {
		return e
	}
	var step w.WorkflowStep
	for _, item := range d.Steps {
		if item.Id == stepID {
			step = item
		}
	}
	if step.Kind != "call" || step.Capability == nil {
		return ErrInvalid
	}
	adapter := engine.Adapters[*step.Capability]
	if adapter == nil {
		return ErrUnavailable
	}
	r, e := loadStep(ctx, tx, owner, id, stepID)
	if e != nil {
		return e
	}
	if r.result != nil {
		return nil
	}
	if r.dispatched {
		return ErrReconcile
	}
	if r.task == nil {
		return ErrConflict
	}
	if e = s.sweepReady(ctx, tx); e != nil {
		return e
	}
	request := api.ClaimGuardedTaskRequest{Queue: r.task.Task.Queue, CurrentState: "ready", OverrideCurrentState: "running", OverrideAutoTargetState: "reconcile_required", OverrideTimeout: claimSeconds}
	if e = s.Journal.PrepareTx(ctx, tx, r.claim, "ClaimGuardedTask", api.EncodeClaimGuardedTaskRequest(request), s.provenance(v, stepID)); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO workflow_timeout_work(owner,run_id,step_id,due_at)
		VALUES($1,$2,$3,clock_timestamp()+$4::bigint*interval '1 second') ON CONFLICT DO NOTHING`, w.EncodeScopeRef(owner), id, stepID, claimSeconds+5); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if _, e = s.Journal.Resolve(ctx, r.claim); e != nil {
		return e
	}
	task, e := s.Journal.CurrentClaim(ctx, r.claim)
	if e != nil {
		return e
	}
	if task == nil || task.Task.Uuid != r.task.Task.Uuid {
		return ErrReconcile
	}
	// A historical claim is not permission to start a call near or after expiry,
	// even if the timeout evaluator has not yet applied the release.
	if e = claimWindow(*task, time.Now(), budget); e != nil {
		return e
	}
	tx, e = s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, v.Actor, owner, "execute"); e != nil {
		return e
	}
	if e = s.authorize(ctx, tx, v.Actor, owner, "run"); e != nil {
		return e
	}
	if e = s.sweepReady(ctx, tx); e != nil {
		return e
	}
	r, e = loadStep(ctx, tx, owner, id, stepID)
	if e != nil {
		return e
	}
	if r.dispatched {
		return ErrReconcile
	}
	_, e = tx.Exec(ctx, `UPDATE workflow_run_steps SET task=$4,dispatched=true WHERE owner=$1 AND run_id=$2 AND step_id=$3`, w.EncodeScopeRef(owner), id, stepID, api.EncodeGuardedTask(*task))
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	values, e := w.DecodeWorkflowValues(r.input)
	if e != nil {
		return e
	}
	if e = claimWindow(*task, time.Now(), budget); e != nil {
		return e
	}
	callCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	output, evidence, e := adapter.Call(callCtx, CallRequest{Key: stepKey(owner, id, stepID), Actor: v.Actor, Definition: v.Request.Definition, RunID: id, StepID: stepID, Inputs: values, Prompt: r.prompt})
	if e != nil {
		return ErrReconcile
	}
	return s.storeResult(ctx, v, step, *task, output, s.Application, evidence)
}

func callClaimWindow(task api.GuardedTask, now time.Time) error {
	return claimWindow(task, now, 8*time.Second)
}
func claimWindow(task api.GuardedTask, now time.Time, budget time.Duration) error {
	claimDuration := budget + 22*time.Second
	if !task.Guarded || task.Terminal || task.Task.CurrentState != "running" || task.Task.AutoTargetState != "reconcile_required" || task.Task.Timeout != int64(claimDuration/time.Second) {
		return ErrStale
	}
	updated := time.Unix(0, task.Task.UpdateTime)
	if updated.After(now.Add(5*time.Second)) || !updated.Add(claimDuration).After(now.Add(budget+5*time.Second)) {
		return ErrStale
	}
	return nil
}
