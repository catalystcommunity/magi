package workflow

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"slices"

	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
)

//go:embed service.sql
var serviceMigration string
var ErrDenied = errors.New("workflow operation denied")
var ErrUnavailable = errors.New("workflow service unavailable")

const workflowPolicyLock = 427361020

type Store struct {
	Journal     *Journal
	Application w.ApplicationRef
}
type Grant struct {
	Actor   w.ApplicationRef `json:"actor"`
	Owner   w.ScopeRef       `json:"owner"`
	Action  string           `json:"action"`
	Allowed bool             `json:"allowed"`
}

func (s *Store) Initialize(ctx context.Context) error {
	if !validApplication(s.Application) || s.Journal == nil {
		return ErrInvalid
	}
	if e := s.Journal.Initialize(ctx); e != nil {
		return e
	}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, workflowPolicyLock); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, serviceMigration); e != nil {
		return e
	}
	raw := w.EncodeApplicationRef(s.Application)
	var saved []byte
	var version int
	e = tx.QueryRow(ctx, `SELECT application,version FROM workflow_service`).Scan(&saved, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		_, e = tx.Exec(ctx, `INSERT INTO workflow_service(application,version) VALUES($1,4)`, raw)
		saved, version = raw, 4
	}
	if e != nil {
		return e
	}
	if (version < 1 || version > 4) || !bytes.Equal(saved, raw) {
		return ErrConflict
	}
	if version < 4 {
		// Stop older service processes before this administrator migration.
		_, e = tx.Exec(ctx, `ALTER TABLE workflow_service DROP CONSTRAINT workflow_service_version_check;
			ALTER TABLE workflow_service ADD CONSTRAINT workflow_service_version_check CHECK(version=4) NOT VALID;
			UPDATE workflow_service SET version=4;
			ALTER TABLE workflow_service VALIDATE CONSTRAINT workflow_service_version_check`)
		if e != nil {
			return e
		}
	}
	// Backfill older admissions. Repeated migration does not reopen completed work.
	if _, e = tx.Exec(ctx, `INSERT INTO workflow_coordination(owner,run_id)
		SELECT owner,id FROM workflow_runs ON CONFLICT DO NOTHING`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO workflow_sweep_health(singleton) VALUES(true) ON CONFLICT DO NOTHING`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO workflow_timeout_work(owner,run_id,step_id)
		SELECT step.owner,step.run_id,step.step_id FROM workflow_run_steps step
		JOIN workflow_intents intent ON intent.installation=$1 AND intent.id=step.claim_id
		ON CONFLICT DO NOTHING`, s.Journal.Installation); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Check(ctx context.Context) error {
	var saved []byte
	var version int
	if e := s.Journal.Pool.QueryRow(ctx, `SELECT application,version FROM workflow_service`).Scan(&saved, &version); e != nil {
		return e
	}
	if version != 4 || !bytes.Equal(saved, w.EncodeApplicationRef(s.Application)) {
		return ErrConflict
	}
	_, e := s.Journal.Check(ctx)
	return e
}
func (s *Store) SetGrant(ctx context.Context, g Grant) error {
	if !validApplication(g.Actor) || !validScope(g.Owner) || !slices.Contains([]string{"read", "write", "run", "complete", "execute"}, g.Action) {
		return ErrInvalid
	}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, workflowPolicyLock); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO workflow_grants VALUES($1,$2,$3,$4) ON CONFLICT(actor,owner,action) DO UPDATE SET allowed=excluded.allowed`, w.EncodeApplicationRef(g.Actor), w.EncodeScopeRef(g.Owner), g.Action, g.Allowed)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) authorize(ctx context.Context, tx pgx.Tx, a w.ApplicationRef, owner w.ScopeRef, action string) error {
	if !validApplication(a) || !validScope(owner) {
		return ErrDenied
	}
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, workflowPolicyLock); e != nil {
		return e
	}
	var allowed bool
	e := tx.QueryRow(ctx, `SELECT allowed FROM workflow_grants WHERE actor=$1 AND owner=$2 AND action=$3`, w.EncodeApplicationRef(a), w.EncodeScopeRef(owner), action).Scan(&allowed)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !allowed {
		return ErrDenied
	}
	return e
}

// Drafts may be incomplete. Publishing, unlike saving, requires a valid graph.
func (s *Store) SaveDraft(ctx context.Context, a w.ApplicationRef, r w.SaveWorkflowDraft) (w.WorkflowDraft, error) {
	d := r.Draft
	raw := w.EncodeSaveWorkflowDraft(r)
	if !validScope(d.Definition.Owner) || !portName.MatchString(d.Definition.Id) || r.ExpectedVersion >= p.MaxInteger || d.Version != r.ExpectedVersion || len(raw) > MaxDefinitionBytes || len(d.Positions) > 64 {
		return d, ErrInvalid
	}
	if _, e := p.Decode(raw, w.DecodeSaveWorkflowDraft, w.EncodeSaveWorkflowDraft); e != nil {
		return d, ErrInvalid
	}
	seen := map[string]bool{}
	for _, v := range d.Positions {
		if !portName.MatchString(v.StepId) || seen[v.StepId] || v.X < -100000 || v.X > 100000 || v.Y < -100000 || v.Y > 100000 {
			return d, ErrInvalid
		}
		seen[v.StepId] = true
	}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return d, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, d.Definition.Owner, "write"); e != nil {
		return d, e
	}
	// Serialize creation as well as replacement; the key includes the owner.
	key := string(w.EncodeScopeRef(d.Definition.Owner)) + "\x00" + d.Definition.Id
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,20))`, fmtKey(key)); e != nil {
		return d, e
	}
	var body, request, actor []byte
	var version uint64
	e = tx.QueryRow(ctx, `SELECT version,body,request,actor FROM workflow_drafts WHERE owner=$1 AND id=$2 FOR UPDATE`, w.EncodeScopeRef(d.Definition.Owner), d.Definition.Id).Scan(&version, &body, &request, &actor)
	if e == nil && bytes.Equal(request, raw) && bytes.Equal(actor, w.EncodeApplicationRef(a)) {
		v, e := w.DecodeWorkflowDraft(body)
		return v, e
	}
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return d, e
	}
	if version != r.ExpectedVersion {
		return d, ErrConflict
	}
	d.Version = version + 1
	_, e = tx.Exec(ctx, `INSERT INTO workflow_drafts VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(owner,id) DO UPDATE SET version=excluded.version,body=excluded.body,request=excluded.request,actor=excluded.actor`, w.EncodeScopeRef(d.Definition.Owner), d.Definition.Id, d.Version, w.EncodeWorkflowDraft(d), raw, w.EncodeApplicationRef(a))
	if e != nil {
		return d, e
	}
	return d, tx.Commit(ctx)
}
func (s *Store) ReadDraft(ctx context.Context, a w.ApplicationRef, r w.ReadWorkflowDraft) (w.WorkflowDraft, error) {
	var v w.WorkflowDraft
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, r.Owner, "read"); e != nil {
		return v, e
	}
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT body FROM workflow_drafts WHERE owner=$1 AND id=$2`, w.EncodeScopeRef(r.Owner), r.Id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if e != nil {
		return v, e
	}
	return w.DecodeWorkflowDraft(raw)
}
func (s *Store) Publish(ctx context.Context, a w.ApplicationRef, d w.WorkflowDefinition, proof []byte) (w.WorkflowDefinitionRef, error) {
	if proof == nil {
		proof = []byte{}
	}
	ref := definitionRef(d)
	if !ValidateDefinition(d).Valid || len(proof) > p.MaxFrame {
		return ref, ErrInvalid
	}
	raw := w.EncodeWorkflowDefinition(d)
	if _, e := p.Decode(raw, w.DecodeWorkflowDefinition, w.EncodeWorkflowDefinition); e != nil {
		return ref, ErrInvalid
	}
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return ref, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, d.Owner, "write"); e != nil {
		return ref, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO workflow_definitions VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, w.EncodeScopeRef(d.Owner), d.Id, d.Revision, raw, w.EncodeApplicationRef(a), proof)
	if e != nil {
		return ref, e
	}
	var saved []byte
	e = tx.QueryRow(ctx, `SELECT body FROM workflow_definitions WHERE owner=$1 AND id=$2 AND revision=$3`, w.EncodeScopeRef(d.Owner), d.Id, d.Revision).Scan(&saved)
	if e != nil {
		return ref, e
	}
	if !bytes.Equal(raw, saved) {
		return ref, ErrConflict
	}
	return ref, tx.Commit(ctx)
}
func readDefinition(ctx context.Context, tx pgx.Tx, ref w.WorkflowDefinitionRef) (w.WorkflowDefinition, error) {
	var d w.WorkflowDefinition
	var raw []byte
	if !validScope(ref.Owner) || !portName.MatchString(ref.Id) || !portName.MatchString(ref.Revision) || len(ref.Digest) != 32 {
		return d, ErrInvalid
	}
	e := tx.QueryRow(ctx, `SELECT body FROM workflow_definitions WHERE owner=$1 AND id=$2 AND revision=$3`, w.EncodeScopeRef(ref.Owner), ref.Id, ref.Revision).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if e != nil {
		return d, e
	}
	d, e = w.DecodeWorkflowDefinition(raw)
	if e != nil {
		return d, e
	}
	if !bytes.Equal(ref.Digest, definitionRef(d).Digest) {
		return d, ErrConflict
	}
	return d, nil
}
func (s *Store) ReadDefinition(ctx context.Context, a w.ApplicationRef, ref w.WorkflowDefinitionRef) (w.WorkflowDefinition, error) {
	tx, e := s.Journal.Pool.Begin(ctx)
	if e != nil {
		return w.WorkflowDefinition{}, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, ref.Owner, "read"); e != nil {
		return w.WorkflowDefinition{}, e
	}
	return readDefinition(ctx, tx, ref)
}
