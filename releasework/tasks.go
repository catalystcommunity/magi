// Package releasework connects a trusted release controller to Corndogs.
// It does not verify human approval or grant authority to change a deployment.
package releasework

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/workflow"
)

type Binding struct {
	CandidateID     string           `json:"candidateId"`
	CandidateDigest string           `json:"candidateDigest"`
	ApprovalID      string           `json:"approvalId"`
	ApprovalDigest  string           `json:"approvalDigest"`
	Controller      w.ApplicationRef `json:"controller"`
	Recorder        w.ApplicationRef `json:"recorder"`
	Target          string           `json:"target"`
}

type Outcome struct {
	Phase         string `json:"phase"`
	Reason        string `json:"reason"`
	AuditDigest   string `json:"auditDigest"`
	CompletedAtMS int64  `json:"completedAtMs"`
}

type Receipt struct {
	Installation string `json:"installation"`
	Queue        string `json:"queue"`
	TaskID       string `json:"taskId"`
	Revision     int64  `json:"revision"`
}

type Tasks struct {
	Journal    *workflow.Journal
	Controller w.ApplicationRef
	Recorder   w.ApplicationRef
	Target     string
}

var uuid = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (t *Tasks) validate(b Binding) error {
	if t.Journal == nil || t.Journal.Pool == nil || t.Journal.Transport == nil || t.Target == "" || b.Controller != t.Controller || b.Recorder != t.Recorder || b.Target != t.Target || !uuid.MatchString(b.CandidateID) || !uuid.MatchString(b.ApprovalID) || !digest.MatchString(b.CandidateDigest) || !digest.MatchString(b.ApprovalDigest) {
		return workflow.ErrInvalid
	}
	return nil
}

// The key includes the controller and target, but not mutable proposal bytes.
// Reusing a candidate ID with a changed digest must conflict, not create work.
func key(b Binding) string {
	raw, _ := json.Marshal(struct {
		Controller        w.ApplicationRef
		Target, Candidate string
	}{b.Controller, b.Target, b.CandidateID})
	h := sha256.Sum256(raw)
	return "release/" + hex.EncodeToString(h[:])
}

func provenance(b Binding) workflow.Provenance {
	return workflow.Provenance{Actor: b.Controller,
		Admission: w.RecordRef{Authority: b.Recorder.Identity, Kind: "release-approval", RecordId: b.ApprovalID},
		Cause:     w.RecordRef{Authority: b.Controller.Identity, Kind: "release-candidate", RecordId: b.CandidateID}}
}

func (t *Tasks) submission(ctx context.Context, b Binding) (api.SubmissionReceipt, error) {
	var empty api.SubmissionReceipt
	if e := t.validate(b); e != nil {
		return empty, e
	}
	raw, _ := json.Marshal(b)
	id := key(b)
	request := api.SubmitKeyedTaskRequest{Guarded: true, Queue: id, CurrentState: "queued", AutoTargetState: "queued", Timeout: -1, Payload: raw}
	if e := t.Journal.Prepare(ctx, id+"/submit", "SubmitKeyedTask", api.EncodeSubmitKeyedTaskRequest(request), provenance(b)); e != nil {
		return empty, e
	}
	i, e := t.Journal.Resolve(ctx, id+"/submit")
	if e != nil {
		return empty, e
	}
	return api.DecodeSubmissionReceipt(i.Receipt)
}

func receipt(j *workflow.Journal, r api.OperationReceipt) Receipt {
	return Receipt{Installation: j.Installation, Queue: r.Queue, TaskID: r.TaskUuid, Revision: r.ResultRevision}
}

func (t *Tasks) startIntent(ctx context.Context, b Binding, sub api.SubmissionReceipt) (api.OperationReceipt, error) {
	var empty api.OperationReceipt
	state := "queued"
	r := api.UpdateGuardedTaskRequest{Queue: sub.Queue, Uuid: sub.TaskUuid, ExpectedRevision: 1, ExpectedState: &state, NewState: "running", AutoTargetState: "running", Timeout: -1}
	if e := t.Journal.Prepare(ctx, key(b)+"/start", "UpdateGuardedTask", api.EncodeUpdateGuardedTaskRequest(r), provenance(b)); e != nil {
		return empty, e
	}
	i, e := t.Journal.Resolve(ctx, key(b)+"/start")
	if e != nil {
		return empty, e
	}
	return api.DecodeOperationReceipt(i.Receipt)
}

// Start reserves one durable task for the single supervised controller. This
// task deliberately has no lease or automatic reassignment. Once an external
// intent is recorded, the controller must recover it even if Corndogs is down.
// This is not a distributed worker fence or a queue of arbitrary task agents.
func (t *Tasks) Start(ctx context.Context, b Binding) (Receipt, error) {
	var empty Receipt
	sub, e := t.submission(ctx, b)
	if e != nil {
		return empty, e
	}
	r, e := t.startIntent(ctx, b, sub)
	if e != nil {
		return empty, e
	}
	current, e := api.NewCorndogsClient(t.Journal.Transport).GetGuardedTask(ctx, api.GetGuardedTaskRequest{Queue: r.Queue, Uuid: r.TaskUuid})
	if e != nil {
		return empty, e
	}
	v := current.Task
	// Submit normalizes -1 to zero, but UpdateGuardedTask retains -1 in 0.8.0.
	// Both disable timeout evaluation. Check the exact installed update here.
	if v == nil || v.Terminal || !v.Guarded || v.Revision != r.ResultRevision || v.Task.Queue != sub.Queue || v.Task.Uuid != sub.TaskUuid || v.Task.CurrentState != "running" || v.Task.AutoTargetState != "running" || v.Task.Timeout != -1 {
		return empty, workflow.ErrStale
	}
	return receipt(t.Journal, r), nil
}

// Finish retains the exact result and completion intent in one transaction.
// It uses the original reservation revision, never a fresh worker's revision.
// A completed Corndogs task means that an outcome was recorded, not that the
// release succeeded. The result explicitly distinguishes rollback and failure.
func (t *Tasks) Finish(ctx context.Context, b Binding, outcome Outcome) (Receipt, error) {
	var empty Receipt
	if !digest.MatchString(outcome.AuditDigest) || outcome.CompletedAtMS <= 0 || outcome.CompletedAtMS > 9007199254740991 || len(outcome.Reason) > 128 || (outcome.Phase != "healthy" && outcome.Phase != "restored" && outcome.Phase != "recovery_required") || (outcome.Phase == "healthy" && outcome.Reason != "") || (outcome.Phase != "healthy" && outcome.Reason == "") {
		return empty, workflow.ErrInvalid
	}
	sub, e := t.submission(ctx, b)
	if e != nil {
		return empty, e
	}
	state, revision := "queued", int64(1)
	// An interrupted start can have an uncertain remote result. Resolve its
	// original intent. Never complete using a guessed revision or new start ID.
	_, e = t.Journal.Read(ctx, key(b)+"/start")
	if e == nil {
		r, err := t.startIntent(ctx, b, sub)
		if err != nil {
			return empty, err
		}
		state, revision = "running", r.ResultRevision
	} else if !errors.Is(e, workflow.ErrNotFound) {
		return empty, e
	}
	result, _ := json.Marshal(struct {
		Binding Binding `json:"binding"`
		Outcome Outcome `json:"outcome"`
	}{b, outcome})
	tx, e := t.Journal.Pool.Begin(ctx)
	if e != nil {
		return empty, e
	}
	defer tx.Rollback(ctx)
	if e = t.Journal.SaveResultTx(ctx, tx, sub.TaskUuid, revision, result, "release/"+b.CandidateID); e != nil {
		return empty, e
	}
	r := api.CompleteGuardedTaskRequest{Queue: sub.Queue, Uuid: sub.TaskUuid, ExpectedRevision: revision, ExpectedState: &state}
	if e = t.Journal.PrepareTx(ctx, tx, key(b)+"/finish", "CompleteGuardedTask", api.EncodeCompleteGuardedTaskRequest(r), provenance(b)); e != nil {
		return empty, e
	}
	if e = tx.Commit(ctx); e != nil {
		return empty, e
	}
	i, e := t.Journal.Resolve(ctx, key(b)+"/finish")
	if e != nil {
		return empty, e
	}
	op, e := api.DecodeOperationReceipt(i.Receipt)
	return receipt(t.Journal, op), e
}
