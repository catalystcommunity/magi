package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var migration string

var (
	ErrInvalid   = errors.New("invalid workflow intent")
	ErrConflict  = errors.New("workflow intent conflicts with retained evidence")
	ErrReconcile = errors.New("workflow operation requires reconciliation")
	ErrRejected  = errors.New("workflow operation was rejected")
	ErrStale     = errors.New("workflow claim is no longer current")
	ErrNotFound  = errors.New("workflow record not found")
)
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)

func validIdentity(v w.IdentityRef) bool {
	return identifier.MatchString(v.AccountId) && len(v.Domain) <= 253 && regexpDomain.MatchString(v.Domain)
}

var regexpDomain = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)+$`)

func validApplication(v w.ApplicationRef) bool {
	return validIdentity(v.Identity) && identifier.MatchString(v.ApplicationId) && identifier.MatchString(v.InstanceId)
}
func validRef(v w.RecordRef) bool {
	return validIdentity(v.Authority) && identifier.MatchString(v.Kind) && identifier.MatchString(v.RecordId)
}

type Journal struct {
	Pool *pgxpool.Pool
	// Installation identifies one Corndogs storage authority, not a network URL.
	Installation string
	Transport    api.Transport
}
type Provenance struct {
	Actor     w.ApplicationRef `json:"actor"`
	Admission w.RecordRef      `json:"admission"`
	Cause     w.RecordRef      `json:"cause"`
}
type Intent struct {
	ID, Method, Queue, Key, State     string
	Request, Provenance, Receipt      []byte
	FirstSend, SafeUntil, LastChecked *time.Time
	RetentionSeconds                  *int64
	Attempts                          int64
	RejectionCode                     *int64
}

func (j *Journal) Initialize(ctx context.Context) error {
	if !identifier.MatchString(j.Installation) {
		return ErrInvalid
	}
	tx, e := j.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(427361010)`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, migration); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO workflow_journal_binding(installation) VALUES($1) ON CONFLICT DO NOTHING`, j.Installation); e != nil {
		return e
	}
	var saved string
	if e = tx.QueryRow(ctx, `SELECT installation FROM workflow_journal_binding`).Scan(&saved); e != nil {
		return e
	}
	if saved != j.Installation {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (j *Journal) Check(ctx context.Context) (api.GetServerInfoResponse, error) {
	var empty api.GetServerInfoResponse
	var saved string
	if e := j.Pool.QueryRow(ctx, `SELECT installation FROM workflow_journal_binding`).Scan(&saved); e != nil {
		return empty, e
	}
	if saved != j.Installation || j.Transport == nil {
		return empty, ErrConflict
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	v, e := api.NewCorndogsClient(j.Transport).GetServerInfo(c, api.GetServerInfoRequest{})
	if e != nil {
		return empty, e
	}
	if !slices.Contains(v.Features, api.FeatureSubmissionKeys) || !slices.Contains(v.Features, api.FeatureTaskGuards) || v.SubmissionKeyPolicy != api.PolicyRequired || v.TaskGuardPolicy != api.PolicyRequired || v.ReceiptRetentionSeconds < 60 || v.ReceiptRetentionSeconds > 31536000 {
		return empty, api.ErrUnsupported
	}
	return v, nil
}

func requestKey(installation, id string) string {
	sum := sha256.Sum256([]byte("opal-corndogs-intent-v1\x00" + installation + "\x00" + id))
	return "opal-" + hex.EncodeToString(sum[:])
}

type command struct {
	request                 []byte
	queue, key, task, state string
	revision                int64
}

func normalize(installation, id, method string, raw []byte) (command, error) {
	c := command{key: requestKey(installation, id)}
	switch method {
	case "SubmitKeyedTask":
		v, e := decode(raw, api.DecodeSubmitKeyedTaskRequest, api.EncodeSubmitKeyedTaskRequest)
		if e != nil || !v.Guarded || v.SubmissionKey != "" && v.SubmissionKey != c.key {
			return c, ErrInvalid
		}
		if !identifier.MatchString(v.CurrentState) || !identifier.MatchString(v.AutoTargetState) || v.Timeout == 0 || v.Timeout < -1 || v.Timeout > 86400 || len(v.Payload) > 16384 {
			return c, ErrInvalid
		}
		v.SubmissionKey = c.key
		c.queue = v.Queue
		c.request = api.EncodeSubmitKeyedTaskRequest(v)
	case "ClaimGuardedTask":
		v, e := decode(raw, api.DecodeClaimGuardedTaskRequest, api.EncodeClaimGuardedTaskRequest)
		if e != nil || v.OperationId != "" && v.OperationId != c.key {
			return c, ErrInvalid
		}
		if !identifier.MatchString(v.CurrentState) || !identifier.MatchString(v.OverrideCurrentState) || !identifier.MatchString(v.OverrideAutoTargetState) || v.OverrideTimeout < 1 || v.OverrideTimeout > 300 {
			return c, ErrInvalid
		}
		v.OperationId = c.key
		c.queue = v.Queue
		c.state = v.OverrideCurrentState
		c.request = api.EncodeClaimGuardedTaskRequest(v)
	case "UpdateGuardedTask":
		v, e := decode(raw, api.DecodeUpdateGuardedTaskRequest, api.EncodeUpdateGuardedTaskRequest)
		if e != nil || v.OperationId != "" && v.OperationId != c.key || v.ExpectedState == nil {
			return c, ErrInvalid
		}
		if !identifier.MatchString(v.NewState) || !identifier.MatchString(v.AutoTargetState) || v.Timeout == 0 || v.Timeout < -1 || v.Timeout > 86400 || v.Payload != nil && len(*v.Payload) > 16384 {
			return c, ErrInvalid
		}
		v.OperationId = c.key
		c.queue = v.Queue
		c.task = v.Uuid
		c.state = *v.ExpectedState
		c.revision = v.ExpectedRevision
		c.request = api.EncodeUpdateGuardedTaskRequest(v)
	case "CompleteGuardedTask":
		v, e := decode(raw, api.DecodeCompleteGuardedTaskRequest, api.EncodeCompleteGuardedTaskRequest)
		if e != nil || v.OperationId != "" && v.OperationId != c.key || v.ExpectedState == nil {
			return c, ErrInvalid
		}
		v.OperationId = c.key
		c.queue = v.Queue
		c.task = v.Uuid
		c.state = *v.ExpectedState
		c.revision = v.ExpectedRevision
		c.request = api.EncodeCompleteGuardedTaskRequest(v)
	case "CancelGuardedTask":
		v, e := decode(raw, api.DecodeCancelGuardedTaskRequest, api.EncodeCancelGuardedTaskRequest)
		if e != nil || v.OperationId != "" && v.OperationId != c.key || v.ExpectedState == nil {
			return c, ErrInvalid
		}
		v.OperationId = c.key
		c.queue = v.Queue
		c.task = v.Uuid
		c.state = *v.ExpectedState
		c.revision = v.ExpectedRevision
		c.request = api.EncodeCancelGuardedTaskRequest(v)
	default:
		return c, ErrInvalid
	}
	if !identifier.MatchString(c.queue) || c.task != "" && (!identifier.MatchString(c.task) || c.revision < 1 || !identifier.MatchString(c.state)) {
		return c, ErrInvalid
	}
	if method != "SubmitKeyedTask" && method != "ClaimGuardedTask" && c.task == "" {
		return c, ErrInvalid
	}
	return c, nil
}

// PrepareTx joins the caller's admitted business transaction. It creates no
// remote work. Retrying the same logical ID cannot generate a different key.
func (j *Journal) PrepareTx(ctx context.Context, tx pgx.Tx, id, method string, raw []byte, why Provenance) error {
	if !identifier.MatchString(id) || !identifier.MatchString(j.Installation) || !validApplication(why.Actor) || !validRef(why.Admission) || !validRef(why.Cause) {
		return ErrInvalid
	}
	var installation string
	if e := tx.QueryRow(ctx, `SELECT installation FROM workflow_journal_binding`).Scan(&installation); e != nil {
		return e
	}
	if installation != j.Installation {
		return ErrConflict
	}
	c, e := normalize(j.Installation, id, method, raw)
	if e != nil {
		return e
	}
	provenance, e := json.Marshal(why)
	if e != nil || len(provenance) > 8192 {
		return ErrInvalid
	}
	if method == "CompleteGuardedTask" {
		var exists bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_results WHERE installation=$1 AND task_uuid=$2 AND revision=$3)`, j.Installation, c.task, c.revision).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return ErrConflict
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO workflow_intents(installation,id,method,queue,request_key,request,provenance) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(installation,id) DO NOTHING`, j.Installation, id, method, c.queue, c.key, c.request, provenance)
	if e != nil {
		return e
	}
	var saved, prior []byte
	var op string
	if e = tx.QueryRow(ctx, `SELECT method,request,provenance FROM workflow_intents WHERE installation=$1 AND id=$2`, j.Installation, id).Scan(&op, &saved, &prior); e != nil {
		return e
	}
	if op != method || !bytes.Equal(saved, c.request) || !bytes.Equal(prior, provenance) {
		return ErrConflict
	}
	return nil
}
func (j *Journal) Prepare(ctx context.Context, id, method string, raw []byte, why Provenance) error {
	tx, e := j.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = j.PrepareTx(ctx, tx, id, method, raw, why); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// SaveResultTx commits the business result before Corndogs completion. Include
// local effects and lifecycle outbox records in this transaction when possible.
func (j *Journal) SaveResultTx(ctx context.Context, tx pgx.Tx, task string, revision int64, result []byte, evidence string) error {
	if !identifier.MatchString(task) || revision < 1 || !identifier.MatchString(evidence) || len(result) > p.MaxBlob || len(result) == 0 {
		return ErrInvalid
	}
	_, e := tx.Exec(ctx, `INSERT INTO workflow_results VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, j.Installation, task, revision, result, evidence)
	if e != nil {
		return e
	}
	var saved []byte
	var ref string
	if e = tx.QueryRow(ctx, `SELECT result,evidence FROM workflow_results WHERE installation=$1 AND task_uuid=$2 AND revision=$3`, j.Installation, task, revision).Scan(&saved, &ref); e != nil {
		return e
	}
	if !bytes.Equal(saved, result) || ref != evidence {
		return ErrConflict
	}
	return nil
}

const intentColumns = `id,method,queue,request_key,state,request,provenance,receipt,first_send,safe_until,last_checked,retention_seconds,attempts,rejection_code`

func scanIntent(row pgx.Row) (Intent, error) {
	var v Intent
	e := row.Scan(&v.ID, &v.Method, &v.Queue, &v.Key, &v.State, &v.Request, &v.Provenance, &v.Receipt, &v.FirstSend, &v.SafeUntil, &v.LastChecked, &v.RetentionSeconds, &v.Attempts, &v.RejectionCode)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return v, e
}
func (j *Journal) Read(ctx context.Context, id string) (Intent, error) {
	return scanIntent(j.Pool.QueryRow(ctx, `SELECT `+intentColumns+` FROM workflow_intents WHERE installation=$1 AND id=$2`, j.Installation, id))
}

// arm commits the first possible send time before any mutation leaves Magi.
// A later retry never extends the original window. The 30-second margin covers
// call time, modest clock error, and scheduling. Clock reversal stops replay.
func (j *Journal) arm(ctx context.Context, id string, retention int64) (Intent, error) {
	tx, e := j.Pool.Begin(ctx)
	if e != nil {
		return Intent{}, e
	}
	defer tx.Rollback(ctx)
	v, e := scanIntent(tx.QueryRow(ctx, `SELECT `+intentColumns+` FROM workflow_intents WHERE installation=$1 AND id=$2 FOR UPDATE`, j.Installation, id))
	if e != nil {
		return v, e
	}
	if v.State != "pending" {
		return v, tx.Commit(ctx)
	}
	var clock time.Time
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&clock); e != nil {
		return v, e
	}
	badClock := time.Since(clock) > 5*time.Second || time.Until(clock) > 5*time.Second || v.LastChecked != nil && clock.Before(*v.LastChecked)
	if badClock || v.RetentionSeconds != nil && *v.RetentionSeconds != retention {
		_, e = tx.Exec(ctx, `UPDATE workflow_intents SET state='reconcile_required' WHERE installation=$1 AND id=$2`, j.Installation, id)
		if e != nil {
			return v, e
		}
		v.State = "reconcile_required"
		return v, tx.Commit(ctx)
	}
	if v.FirstSend == nil {
		first := clock
		end := clock.Add(time.Duration(retention)*time.Second - 30*time.Second)
		v.FirstSend = &first
		v.SafeUntil = &end
		v.RetentionSeconds = &retention
	}
	_, e = tx.Exec(ctx, `UPDATE workflow_intents SET first_send=$3,safe_until=$4,retention_seconds=$5,last_checked=$6 WHERE installation=$1 AND id=$2`, j.Installation, id, v.FirstSend, v.SafeUntil, retention, clock)
	if e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}

// Resolve recovers a historical receipt, or sends the retained request within
// its original window. It never performs a workflow's business effect.
func (j *Journal) Resolve(ctx context.Context, id string) (Intent, error) {
	v, e := j.Read(ctx, id)
	if e != nil {
		return v, e
	}
	if v.State != "pending" {
		return outcome(v)
	}
	info, e := j.Check(ctx)
	if e != nil {
		return v, e
	}
	v, e = j.arm(ctx, id, info.ReceiptRetentionSeconds)
	if e != nil {
		return v, e
	}
	if v.State != "pending" {
		return outcome(v)
	}
	c := api.NewCorndogsClient(j.Transport)
	lookupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	receipt, e := j.lookup(lookupCtx, c, v)
	cancel()
	if e != nil {
		return v, e
	}
	if receipt != nil {
		return j.confirm(ctx, v, receipt)
	}
	// Check the window again after a slow lookup. All carrier sends and reads
	// share this deadline. No background goroutine can send after it expires.
	if v.SafeUntil == nil || time.Until(*v.SafeUntil) <= 0 {
		_, e = j.Pool.Exec(ctx, `UPDATE workflow_intents SET state='reconcile_required' WHERE installation=$1 AND id=$2 AND state='pending'`, j.Installation, id)
		if e != nil {
			return v, e
		}
		v, e = j.Read(ctx, id)
		if e != nil {
			return v, e
		}
		return outcome(v)
	}
	window, e := j.sendWindow(ctx, id)
	if e != nil {
		// Another coordinator can confirm the same intent after our lookup.
		// Return its durable result instead of reporting a recovery fault.
		if errors.Is(e, ErrReconcile) {
			latest, readErr := j.Read(ctx, id)
			if readErr == nil && latest.State != "pending" {
				return outcome(latest)
			}
		}
		return v, e
	}
	callCtx, stop := context.WithTimeout(ctx, window)
	defer stop()
	raw, e := j.Transport.Call(callCtx, "CorndogsService", v.Method, v.Request)
	if e != nil {
		if code, final := api.ServiceErrorCode(e); final {
			_, save := j.Pool.Exec(ctx, `UPDATE workflow_intents SET state='rejected',rejection_code=$3 WHERE installation=$1 AND id=$2 AND state='pending'`, j.Installation, id, int64(code))
			if save != nil {
				return v, save
			}
		}
		return v, e
	}
	if v.Method == "SubmitKeyedTask" {
		r, e := decode(raw, api.DecodeSubmitKeyedTaskResponse, api.EncodeSubmitKeyedTaskResponse)
		if e != nil {
			return v, e
		}
		if e = validSubmission(v, r.Receipt); e != nil {
			return v, e
		}
		return j.confirm(ctx, v, api.EncodeSubmissionReceipt(r.Receipt))
	}
	if v.Method == "ClaimGuardedTask" {
		r, e := decode(raw, api.DecodeClaimGuardedTaskResponse, api.EncodeClaimGuardedTaskResponse)
		if e != nil {
			return v, e
		}
		// Corndogs 0.8.0 does not retain an empty-poll receipt. Keep that observed
		// empty result locally, so retrying this intent is not a fresh poll.
		if r.Delivery == nil {
			return j.confirm(ctx, v, []byte{})
		}
	}
	lookupCtx, cancel = context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	receipt, e = j.lookup(lookupCtx, c, v)
	if e != nil {
		return v, e
	}
	if receipt == nil {
		return v, ErrReconcile
	}
	return j.confirm(ctx, v, receipt)
}

func (j *Journal) sendWindow(ctx context.Context, id string) (time.Duration, error) {
	tx, e := j.Pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	v, e := scanIntent(tx.QueryRow(ctx, `SELECT `+intentColumns+` FROM workflow_intents WHERE installation=$1 AND id=$2 FOR UPDATE`, j.Installation, id))
	if e != nil {
		return 0, e
	}
	if v.State != "pending" {
		return 0, ErrReconcile
	}
	var clock time.Time
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&clock); e != nil {
		return 0, e
	}
	if v.SafeUntil == nil || v.LastChecked == nil || clock.Before(*v.LastChecked) || !clock.Before(*v.SafeUntil) || time.Since(clock) > 5*time.Second || time.Until(clock) > 5*time.Second {
		if _, e = tx.Exec(ctx, `UPDATE workflow_intents SET state='reconcile_required' WHERE installation=$1 AND id=$2`, j.Installation, id); e != nil {
			return 0, e
		}
		if e = tx.Commit(ctx); e != nil {
			return 0, e
		}
		return 0, ErrReconcile
	}
	window := min(v.SafeUntil.Sub(clock), 8*time.Second)
	if _, e = tx.Exec(ctx, `UPDATE workflow_intents SET attempts=attempts+1,last_checked=$3 WHERE installation=$1 AND id=$2`, j.Installation, id, clock); e != nil {
		return 0, e
	}
	// Use a monotonic local deadline from this point. Commit time consumes part
	// of the remaining window, rather than extending the first send deadline.
	start := time.Now()
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	window -= time.Since(start)
	if window <= 0 {
		return 0, ErrReconcile
	}
	return window, nil
}

func validSubmission(v Intent, r api.SubmissionReceipt) error {
	if r.Queue != v.Queue || r.SubmissionKey != v.Key || !r.Guarded || !identifier.MatchString(r.TaskUuid) || r.AcceptedAt <= 0 || r.ExpiresAt <= r.AcceptedAt {
		return ErrConflict
	}
	return nil
}
func (j *Journal) lookup(ctx context.Context, c *api.CorndogsClient, v Intent) ([]byte, error) {
	if v.Method == "SubmitKeyedTask" {
		r, e := c.LookupSubmission(ctx, api.LookupSubmissionRequest{Queue: v.Queue, SubmissionKey: v.Key})
		if e != nil {
			return nil, e
		}
		if r.Receipt == nil {
			return nil, nil
		}
		if e = validSubmission(v, *r.Receipt); e != nil {
			return nil, e
		}
		return api.EncodeSubmissionReceipt(*r.Receipt), nil
	}
	r, e := c.LookupOperation(ctx, api.LookupOperationRequest{OperationId: v.Key})
	if e != nil {
		return nil, e
	}
	if r.Receipt == nil {
		return nil, nil
	}
	receipt := r.Receipt
	cmd, e := normalize(j.Installation, v.ID, v.Method, v.Request)
	if e != nil {
		return nil, e
	}
	op := map[string]string{"ClaimGuardedTask": "claim", "UpdateGuardedTask": "update", "CompleteGuardedTask": "complete", "CancelGuardedTask": "cancel"}[v.Method]
	if receipt.OperationId != v.Key || receipt.Op != op || receipt.Queue != v.Queue || receipt.At <= 0 || receipt.ExpiresAt <= receipt.At || receipt.ResultRevision < 1 || !identifier.MatchString(receipt.TaskUuid) || cmd.task != "" && (receipt.TaskUuid != cmd.task || receipt.ResultRevision != cmd.revision+1) {
		return nil, ErrConflict
	}
	return api.EncodeOperationReceipt(*receipt), nil
}
func (j *Journal) confirm(ctx context.Context, v Intent, receipt []byte) (Intent, error) {
	tx, e := j.Pool.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	current, e := scanIntent(tx.QueryRow(ctx, `SELECT `+intentColumns+` FROM workflow_intents WHERE installation=$1 AND id=$2 FOR UPDATE`, j.Installation, v.ID))
	if e != nil {
		return v, e
	}
	if current.State == "confirmed" {
		if !bytes.Equal(current.Receipt, receipt) {
			return current, ErrConflict
		}
		return current, tx.Commit(ctx)
	}
	if current.State != "pending" {
		return current, ErrReconcile
	}
	if _, e = tx.Exec(ctx, `UPDATE workflow_intents SET state='confirmed',receipt=$3 WHERE installation=$1 AND id=$2`, j.Installation, v.ID, receipt); e != nil {
		return current, e
	}
	current.State = "confirmed"
	current.Receipt = bytes.Clone(receipt)
	return current, tx.Commit(ctx)
}
func outcome(v Intent) (Intent, error) {
	switch v.State {
	case "confirmed":
		return v, nil
	case "rejected":
		return v, ErrRejected
	case "reconcile_required":
		return v, ErrReconcile
	}
	return v, ErrInvalid
}

// CurrentClaim checks the authoritative current revision. A historical claim
// receipt alone must never start a worker. External effects still need their
// own idempotency or fencing protocol; this check is not an effect lock.
func (j *Journal) CurrentClaim(ctx context.Context, id string) (*api.GuardedTask, error) {
	v, e := j.Read(ctx, id)
	if e != nil {
		return nil, e
	}
	if v.Method != "ClaimGuardedTask" || v.State != "confirmed" {
		return nil, ErrInvalid
	}
	if len(v.Receipt) == 0 {
		return nil, nil
	}
	receipt, e := decode(v.Receipt, api.DecodeOperationReceipt, api.EncodeOperationReceipt)
	if e != nil {
		return nil, e
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	r, e := api.NewCorndogsClient(j.Transport).GetGuardedTask(c, api.GetGuardedTaskRequest{Queue: receipt.Queue, Uuid: receipt.TaskUuid})
	if e != nil {
		return nil, e
	}
	if r.Task == nil || r.Task.Terminal || !r.Task.Guarded || r.Task.Revision != receipt.ResultRevision || r.Task.Task.Queue != receipt.Queue || r.Task.Task.Uuid != receipt.TaskUuid || r.Task.Task.CurrentState != receipt.ResultState {
		return nil, ErrStale
	}
	return r.Task, nil
}
