package workflow

import (
	"bytes"
	"context"
	"errors"
	"time"

	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
)

// BeginCallEvidence is a trusted adapter helper, not a public write API. A
// prepared record is an intent, not proof that the destination received it.
// Adapters must remove secrets before calling this method.
func (s *Store) BeginCallEvidence(ctx context.Context, v w.WorkflowCallEvidence) error {
	if !validScope(v.Call.Owner) || !portName.MatchString(v.Call.RunId) || !portName.MatchString(v.Call.StepId) ||
		len(v.Capability) == 0 || len(v.Capability) > 256 || len(v.AdapterRevision) == 0 || len(v.AdapterRevision) > 128 ||
		len(v.Destination) == 0 || len(v.Destination) > 2048 || len(v.MediaType) == 0 || len(v.MediaType) > 128 ||
		len(v.Request) == 0 || len(v.Request) > 16384 || v.Outcome != "prepared" || v.Response != nil || v.FinishedAt != nil || v.ErrorCode != nil {
		return ErrInvalid
	}
	v.StartedAt = time.Now().UTC().Truncate(time.Millisecond)
	raw := w.EncodeWorkflowCallEvidence(v)
	if _, e := p.Decode(raw, w.DecodeWorkflowCallEvidence, w.EncodeWorkflowCallEvidence); e != nil {
		return ErrInvalid
	}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	r, e := loadStep(ctx, tx, v.Call.Owner, v.Call.RunId, v.Call.StepId)
	if e != nil {
		return e
	}
	if !r.dispatched || r.result != nil {
		return ErrConflict
	}
	// Duplicate preparation must never become permission for another call.
	_, e = tx.Exec(ctx, `INSERT INTO workflow_call_evidence(owner,run_id,step_id,body) VALUES($1,$2,$3,$4)`,
		w.EncodeScopeRef(v.Call.Owner), v.Call.RunId, v.Call.StepId, raw)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) FinishCallEvidence(ctx context.Context, call w.ReadWorkflowCall, response []byte, code string) error {
	if len(response) > 65536 || len(code) > 64 || (code != "" && !portName.MatchString(code)) {
		return ErrInvalid
	}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT body FROM workflow_call_evidence WHERE owner=$1 AND run_id=$2 AND step_id=$3 FOR UPDATE`,
		w.EncodeScopeRef(call.Owner), call.RunId, call.StepId).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	v, e := w.DecodeWorkflowCallEvidence(raw)
	if e != nil {
		return e
	}
	if v.Outcome != "prepared" {
		oldCode := ""
		if v.ErrorCode != nil {
			oldCode = *v.ErrorCode
		}
		if v.Response != nil && bytes.Equal(*v.Response, response) && oldCode == code {
			return nil
		}
		return ErrConflict
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	v.FinishedAt, v.Response, v.Outcome = &at, &response, "returned"
	if code != "" {
		v.ErrorCode, v.Outcome = &code, "failed"
	}
	raw = w.EncodeWorkflowCallEvidence(v)
	if _, e = p.Decode(raw, w.DecodeWorkflowCallEvidence, w.EncodeWorkflowCallEvidence); e != nil {
		return ErrInvalid
	}
	_, e = tx.Exec(ctx, `UPDATE workflow_call_evidence SET body=$4 WHERE owner=$1 AND run_id=$2 AND step_id=$3`, w.EncodeScopeRef(call.Owner), call.RunId, call.StepId, raw)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) ReadCall(ctx context.Context, actor w.ApplicationRef, call w.ReadWorkflowCall) (w.WorkflowCallEvidence, error) {
	var v w.WorkflowCallEvidence
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, actor, call.Owner, "read"); e != nil {
		return v, e
	}
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT body FROM workflow_call_evidence WHERE owner=$1 AND run_id=$2 AND step_id=$3`, w.EncodeScopeRef(call.Owner), call.RunId, call.StepId).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if e != nil {
		return v, e
	}
	return w.DecodeWorkflowCallEvidence(raw)
}
