package workflow

import (
	"bytes"
	"context"
	"encoding/json"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/jackc/pgx/v5"
)

// HumanBinding links an externally created assignment to one parked Corndogs
// revision. Creating the assignment is a separate, idempotent service operation.
// This is an internal record, not a new public or LinkKeys authorization API.
type HumanBinding struct {
	ID                   string           `json:"id"`
	Service              w.ApplicationRef `json:"service"`
	Assignment           w.RecordRef      `json:"assignment"`
	AssignmentGeneration string           `json:"assignment_generation"`
	Task                 api.GuardedTask  `json:"task"`
	CompletionIntent     string           `json:"completion_intent"`
}
type HumanCompletion struct {
	Assignment           w.RecordRef   `json:"assignment"`
	AssignmentGeneration string        `json:"assignment_generation"`
	SourceRevision       string        `json:"source_revision"`
	Completer            w.IdentityRef `json:"completer"`
	Evidence             w.RecordRef   `json:"evidence"`
	Result               []byte        `json:"result"`
}

// BindHumanTx joins the transaction that retains the assignment's creation
// receipt. It must not be called merely because a create request was sent.
func (j *Journal) BindHumanTx(ctx context.Context, tx pgx.Tx, b HumanBinding) error {
	if len(b.ID) > 128 {
		return ErrInvalid
	}
	if !validApplication(b.Service) || !validRef(b.Assignment) {
		return ErrInvalid
	}
	// Corndogs normalizes a submitted timeout of -1 to a stored timeout of 0.
	if !identifier.MatchString(b.ID) || !identifier.MatchString(b.CompletionIntent) || !identifier.MatchString(b.AssignmentGeneration) || b.Service.Identity.Domain == "" || b.Service.Identity.AccountId == "" || b.Service.ApplicationId == "" || b.Service.InstanceId == "" || b.Assignment.Authority.Domain == "" || b.Assignment.Authority.AccountId == "" || !identifier.MatchString(b.Assignment.Kind) || !identifier.MatchString(b.Assignment.RecordId) || !b.Task.Guarded || b.Task.Terminal || b.Task.Revision < 1 || b.Task.Task.CurrentState != "waiting_human" || b.Task.Task.AutoTargetState != "waiting_human" || b.Task.Task.Timeout != 0 || !identifier.MatchString(b.Task.Task.Uuid) || !identifier.MatchString(b.Task.Task.Queue) {
		return ErrInvalid
	}
	raw, e := json.Marshal(b)
	if e != nil || len(raw) > 16384 {
		return ErrInvalid
	}
	_, e = tx.Exec(ctx, `INSERT INTO workflow_human_bindings(installation,id,task_uuid,revision,binding) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, j.Installation, b.ID, b.Task.Task.Uuid, b.Task.Revision, raw)
	if e != nil {
		return e
	}
	var saved []byte
	if e = tx.QueryRow(ctx, `SELECT binding FROM workflow_human_bindings WHERE installation=$1 AND id=$2`, j.Installation, b.ID).Scan(&saved); e != nil {
		if e == pgx.ErrNoRows {
			return ErrConflict
		}
		return e
	}
	if !bytes.Equal(saved, raw) {
		return ErrConflict
	}
	return nil
}

// CompleteHumanTx runs after authenticated source and local completion-policy
// checks, often inside a consumer inbox transaction. It does not authenticate
// a message, authorize a human, or call an external service by itself.
func (j *Journal) CompleteHumanTx(ctx context.Context, tx pgx.Tx, id string, source w.ApplicationRef, c HumanCompletion, why Provenance) (bool, error) {
	var raw, prior []byte
	e := tx.QueryRow(ctx, `SELECT binding,completion FROM workflow_human_bindings WHERE installation=$1 AND id=$2 FOR UPDATE`, j.Installation, id).Scan(&raw, &prior)
	if e == pgx.ErrNoRows {
		return false, ErrNotFound
	}
	if e != nil {
		return false, e
	}
	var b HumanBinding
	if json.Unmarshal(raw, &b) != nil {
		return false, ErrInvalid
	}
	if !validIdentity(c.Completer) || !validRef(c.Evidence) {
		return false, ErrInvalid
	}
	if source != b.Service || c.Assignment != b.Assignment || c.AssignmentGeneration != b.AssignmentGeneration || !identifier.MatchString(c.SourceRevision) || c.Completer.Domain == "" || c.Completer.AccountId == "" || c.Evidence.Authority.Domain == "" || c.Evidence.Authority.AccountId == "" || !identifier.MatchString(c.Evidence.Kind) || !identifier.MatchString(c.Evidence.RecordId) || len(c.Result) == 0 || len(c.Result) > 16384 {
		return false, ErrInvalid
	}
	completion, e := json.Marshal(c)
	if e != nil {
		return false, e
	}
	if prior != nil {
		if !bytes.Equal(prior, completion) {
			return false, ErrConflict
		}
		return false, nil
	}
	if e = j.SaveResultTx(ctx, tx, b.Task.Task.Uuid, b.Task.Revision, c.Result, "human/"+b.ID); e != nil {
		return false, e
	}
	request := api.CompleteGuardedTaskRequest{Uuid: b.Task.Task.Uuid, Queue: b.Task.Task.Queue, ExpectedRevision: b.Task.Revision, ExpectedState: &b.Task.Task.CurrentState}
	if e = j.PrepareTx(ctx, tx, b.CompletionIntent, "CompleteGuardedTask", api.EncodeCompleteGuardedTaskRequest(request), why); e != nil {
		return false, e
	}
	_, e = tx.Exec(ctx, `UPDATE workflow_human_bindings SET completion=$3 WHERE installation=$1 AND id=$2`, j.Installation, id, completion)
	return true, e
}
