package speech

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

//go:embed schema.sql
var migration string

const policyLock int64 = 427361030

type Store struct {
	Pool        *pgxpool.Pool
	Application w.ApplicationRef
}
type Grant struct {
	Actor   w.ApplicationRef `json:"actor"`
	Owner   w.ScopeRef       `json:"owner"`
	Allowed bool             `json:"allowed"`
}
type row struct {
	request       w.SpeechSubmit
	status        w.SpeechStatus
	input, output []byte
	token         string
}

func (s *Store) Initialize(ctx context.Context) error {
	if !validActor(s.Application) {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policyLock); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, migration); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO speech_service(application,version) VALUES($1,1) ON CONFLICT DO NOTHING`, w.EncodeApplicationRef(s.Application)); e != nil {
		return e
	}
	var saved []byte
	var version int
	if e = tx.QueryRow(ctx, `SELECT application,version FROM speech_service`).Scan(&saved, &version); e != nil {
		return e
	}
	if version != 1 || !bytes.Equal(saved, w.EncodeApplicationRef(s.Application)) {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
func (s *Store) Check(ctx context.Context) error {
	var a []byte
	var version int
	if e := s.Pool.QueryRow(ctx, `SELECT application,version FROM speech_service`).Scan(&a, &version); e != nil {
		return e
	}
	if version != 1 || !bytes.Equal(a, w.EncodeApplicationRef(s.Application)) {
		return ErrConflict
	}
	return nil
}
func (s *Store) SetGrant(ctx context.Context, g Grant) error {
	if !validActor(g.Actor) || !validScope(g.Owner) {
		return ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policyLock); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO speech_grants VALUES($1,$2,$3) ON CONFLICT(actor,owner) DO UPDATE SET allowed=excluded.allowed`, w.EncodeApplicationRef(g.Actor), w.EncodeScopeRef(g.Owner), g.Allowed); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) authorize(ctx context.Context, tx pgx.Tx, a w.ApplicationRef, owner w.ScopeRef) error {
	if !validActor(a) || !validScope(owner) {
		return ErrDenied
	}
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, policyLock); e != nil {
		return e
	}
	var allowed bool
	e := tx.QueryRow(ctx, `SELECT allowed FROM speech_grants WHERE actor=$1 AND owner=$2`, w.EncodeApplicationRef(a), w.EncodeScopeRef(owner)).Scan(&allowed)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !allowed {
		return ErrDenied
	}
	return e
}
func (s *Store) Allowed(ctx context.Context, a w.ApplicationRef, owner w.ScopeRef) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	return s.authorize(ctx, tx, a, owner)
}
func load(ctx context.Context, tx pgx.Tx, a w.ApplicationRef, ref w.SpeechRef) (row, error) {
	var r row
	var req, status []byte
	e := tx.QueryRow(ctx, `SELECT request,status,input,output,token FROM speech_requests WHERE actor=$1 AND owner=$2 AND id=$3 FOR UPDATE`, w.EncodeApplicationRef(a), w.EncodeScopeRef(ref.Owner), ref.RequestId).Scan(&req, &status, &r.input, &r.output, &r.token)
	if e != nil {
		return r, e
	}
	if r.request, e = p.Decode(req, w.DecodeSpeechSubmit, w.EncodeSpeechSubmit); e != nil {
		return r, e
	}
	if r.status, e = p.Decode(status, w.DecodeSpeechStatus, w.EncodeSpeechStatus); e != nil {
		return r, e
	}
	if r.status.Ref.Generation != ref.Generation {
		return r, ErrConflict
	}
	return r, nil
}
func save(ctx context.Context, tx pgx.Tx, a w.ApplicationRef, r row) error {
	if _, e := tx.Exec(ctx, `INSERT INTO speech_observations(actor,owner,id,status) SELECT actor,owner,id,$4 FROM speech_requests WHERE actor=$1 AND owner=$2 AND id=$3 AND status<>$4`, w.EncodeApplicationRef(a), w.EncodeScopeRef(r.status.Ref.Owner), r.status.Ref.RequestId, w.EncodeSpeechStatus(r.status)); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `UPDATE speech_requests SET status=$4,state=$5,input=$6,output=$7 WHERE actor=$1 AND owner=$2 AND id=$3`, w.EncodeApplicationRef(a), w.EncodeScopeRef(r.status.Ref.Owner), r.status.Ref.RequestId, w.EncodeSpeechStatus(r.status), string(r.status.State), r.input, r.output)
	return e
}
func terminal(state w.SpeechState) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}
func expire(r *row, now time.Time) bool {
	changed := false
	if !terminal(r.status.State) && !now.Before(r.status.Deadline) {
		r.status.State = "failed"
		r.status.ErrorCode = "deadline_exceeded"
		changed = true
	}
	if !now.Before(r.status.ExpiresAt) && (len(r.input) > 0 || len(r.output) > 0) {
		r.input = []byte{}
		r.output = []byte{}
		changed = true
	}
	return changed
}
func (s *Store) Submit(ctx context.Context, a w.ApplicationRef, v w.SpeechSubmit, proof []byte) (w.SpeechStatus, error) {
	var empty w.SpeechStatus
	if !validRef(requestRef(v)) {
		return empty, ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return empty, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, v.Owner); e != nil {
		return empty, e
	}
	// Serialize admission counts, including across API replicas.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policyLock+1); e != nil {
		return empty, e
	}
	prior, e := load(ctx, tx, a, requestRef(v))
	if e == nil {
		if !bytes.Equal(w.EncodeSpeechSubmit(v), w.EncodeSpeechSubmit(prior.request)) {
			return empty, ErrConflict
		}
		if expire(&prior, time.Now()) {
			if e = save(ctx, tx, a, prior); e != nil {
				return empty, e
			}
		}
		return prior.status, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return empty, e
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if e = validate(v, now); e != nil {
		return empty, e
	}
	var total, active int
	if e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE actor=$1 AND state IN ('uploading','queued','running') AND deadline>now()) FROM speech_requests`, w.EncodeApplicationRef(a)).Scan(&total, &active); e != nil {
		return empty, e
	}
	if total >= 10000 || active >= 16 {
		return empty, ErrCapacity
	}
	state := w.SpeechState("queued")
	revision := KokoroRevision
	if v.Kind == "stt" {
		state = "uploading"
		revision = ParakeetRevision
	}
	status := w.SpeechStatus{Ref: requestRef(v), Kind: v.Kind, State: state, AcceptedAt: now, Deadline: v.Deadline, ExpiresAt: now.Add(time.Duration(v.RetentionSeconds) * time.Second), ModelRevision: revision, ErrorCode: ""}
	if _, e = tx.Exec(ctx, `INSERT INTO speech_requests(actor,owner,id,request,proof,status,kind,state,accepted_at,deadline,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, w.EncodeApplicationRef(a), w.EncodeScopeRef(v.Owner), v.RequestId, w.EncodeSpeechSubmit(v), proof, w.EncodeSpeechStatus(status), string(v.Kind), string(state), now, v.Deadline, status.ExpiresAt); e != nil {
		return empty, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO speech_observations(actor,owner,id,status) VALUES($1,$2,$3,$4)`, w.EncodeApplicationRef(a), w.EncodeScopeRef(v.Owner), v.RequestId, w.EncodeSpeechStatus(status)); e != nil {
		return empty, e
	}
	return status, tx.Commit(ctx)
}

func (s *Store) Evidence(ctx context.Context, a w.ApplicationRef, ref w.SpeechRef) (w.SpeechEvidence, error) {
	var out w.SpeechEvidence
	if !validRef(ref) {
		return out, ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, ref.Owner); e != nil {
		return out, e
	}
	r, e := load(ctx, tx, a, ref)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if e != nil {
		return out, e
	}
	out.Request = r.request
	var proof []byte
	if e = tx.QueryRow(ctx, `SELECT proof FROM speech_requests WHERE actor=$1 AND owner=$2 AND id=$3`, w.EncodeApplicationRef(a), w.EncodeScopeRef(ref.Owner), ref.RequestId).Scan(&proof); e != nil {
		return out, e
	}
	if out.Admission, e = w.DecodeSignedRecord(proof); e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT status,recorded_at FROM speech_observations WHERE actor=$1 AND owner=$2 AND id=$3 ORDER BY sequence LIMIT 256`, w.EncodeApplicationRef(a), w.EncodeScopeRef(ref.Owner), ref.RequestId)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	out.Observations = []w.SpeechObservation{}
	for rows.Next() {
		var b []byte
		var at time.Time
		if e = rows.Scan(&b, &at); e != nil {
			return out, e
		}
		status, err := w.DecodeSpeechStatus(b)
		if err != nil {
			return out, err
		}
		out.Observations = append(out.Observations, w.SpeechObservation{RecordedAt: at.UTC().Truncate(time.Millisecond), Status: status})
	}
	return out, rows.Err()
}
func (s *Store) operation(ctx context.Context, a w.ApplicationRef, ref w.SpeechRef, fn func(*row) error) (w.SpeechStatus, error) {
	var empty w.SpeechStatus
	if !validRef(ref) {
		return empty, ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return empty, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, a, ref.Owner); e != nil {
		return empty, e
	}
	r, e := load(ctx, tx, a, ref)
	if errors.Is(e, pgx.ErrNoRows) {
		return empty, ErrDenied
	}
	if e != nil {
		return empty, e
	}
	expire(&r, time.Now())
	if fn != nil {
		if e = fn(&r); e != nil {
			return empty, e
		}
	}
	if e = save(ctx, tx, a, r); e != nil {
		return empty, e
	}
	return r.status, tx.Commit(ctx)
}
func (s *Store) Read(ctx context.Context, a w.ApplicationRef, ref w.SpeechRef) (w.SpeechStatus, error) {
	return s.operation(ctx, a, ref, nil)
}
func (s *Store) Cancel(ctx context.Context, a w.ApplicationRef, ref w.SpeechRef) (w.SpeechStatus, error) {
	return s.operation(ctx, a, ref, func(r *row) error {
		// Cancellation also withdraws a completed but not yet played result.
		r.status.State = "cancelled"
		r.status.Transcript = nil
		r.status.ErrorCode = "cancelled"
		r.input = []byte{}
		r.output = []byte{}
		return nil
	})
}
func (s *Store) Upload(ctx context.Context, a w.ApplicationRef, v w.SpeechUpload) (w.SpeechStatus, error) {
	if len(v.Pcm) == 0 || len(v.Pcm) > MaxChunk || len(v.Pcm)%2 != 0 || v.Offset%MaxChunk != 0 || v.Offset > MaxInputSamples*2 {
		return w.SpeechStatus{}, ErrInvalid
	}
	return s.operation(ctx, a, v.Ref, func(r *row) error {
		if r.request.Kind != "stt" || r.status.State == "cancelled" || r.status.State == "failed" || !time.Now().Before(r.status.ExpiresAt) {
			return ErrConflict
		}
		end := v.Offset + uint64(len(v.Pcm))
		if len(v.Pcm) != MaxChunk && end != *r.request.InputSamples*2 {
			return ErrInvalid
		}
		if end <= uint64(len(r.input)) {
			if bytes.Equal(r.input[v.Offset:end], v.Pcm) {
				return nil
			}
			return ErrConflict
		}
		if r.status.State != "uploading" || v.Offset != uint64(len(r.input)) || end > *r.request.InputSamples*2 {
			return ErrConflict
		}
		r.input = append(r.input, v.Pcm...)
		r.status.InputBytes = end
		if end == *r.request.InputSamples*2 {
			hash := sha256.Sum256(r.input)
			if !bytes.Equal(hash[:], *r.request.InputDigest) {
				r.status.State = "failed"
				r.status.ErrorCode = "input_digest_mismatch"
				r.input = []byte{}
			} else {
				r.status.State = "queued"
			}
		}
		return nil
	})
}
func (s *Store) Audio(ctx context.Context, a w.ApplicationRef, v w.SpeechReadAudio) (w.SpeechAudioChunk, error) {
	out := w.SpeechAudioChunk{Ref: v.Ref, Offset: v.Offset}
	_, e := s.operation(ctx, a, v.Ref, func(r *row) error {
		if r.status.State != "completed" || r.request.Kind != "tts" || !time.Now().Before(r.status.ExpiresAt) || v.Offset%2 != 0 || v.Offset >= uint64(len(r.output)) {
			return ErrConflict
		}
		end := min(v.Offset+MaxChunk, uint64(len(r.output)))
		out.Pcm = bytes.Clone(r.output[v.Offset:end])
		out.TotalBytes = uint64(len(r.output))
		out.Digest = bytes.Clone(*r.status.OutputDigest)
		return nil
	})
	return out, e
}
