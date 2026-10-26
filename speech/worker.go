package speech

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
	"unicode/utf8"
)

type Work struct {
	Actor   w.ApplicationRef
	Request w.SpeechSubmit
	Input   []byte
	Token   string
}
type Result struct {
	PCM        []byte
	Transcript string
	SampleRate uint64
}
type Backend interface {
	Process(context.Context, Work) (Result, error)
}
type Worker struct {
	Store   *Store
	Kind    w.SpeechKind
	Backend Backend
}

// Sweep expires abandoned work. It never replays an uncertain inference call.
func (s *Store) Sweep(ctx context.Context) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT actor,status,input,output FROM speech_requests WHERE (state IN ('uploading','queued','running') AND (deadline<=now() OR state='running' AND lease_until<=now())) OR (expires_at<=now() AND (octet_length(input)>0 OR octet_length(output)>0)) ORDER BY accepted_at LIMIT 32 FOR UPDATE SKIP LOCKED`)
	if e != nil {
		return e
	}
	type entry struct {
		a w.ApplicationRef
		r row
	}
	var all []entry
	for rows.Next() {
		var a, b []byte
		var r row
		if e = rows.Scan(&a, &b, &r.input, &r.output); e != nil {
			rows.Close()
			return e
		}
		actor, err := w.DecodeApplicationRef(a)
		if err != nil {
			rows.Close()
			return err
		}
		r.status, err = w.DecodeSpeechStatus(b)
		if err != nil {
			rows.Close()
			return err
		}
		all = append(all, entry{actor, r})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range all {
		if !terminal(v.r.status.State) {
			v.r.status.State = "failed"
			v.r.status.ErrorCode = "worker_lost"
			if !time.Now().Before(v.r.status.Deadline) {
				v.r.status.ErrorCode = "deadline_exceeded"
			}
		}
		expire(&v.r, time.Now())
		if e = save(ctx, tx, v.a, v.r); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) Claim(ctx context.Context, kind w.SpeechKind) (Work, bool, error) {
	var out Work
	if kind != "stt" && kind != "tts" {
		return out, false, ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, false, e
	}
	defer tx.Rollback(ctx)
	lock := policyLock + 2
	if kind == "stt" {
		lock++
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); e != nil {
		return out, false, e
	}
	// One active call per kind in this first host profile. Other kinds proceed.
	var active int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM speech_requests WHERE kind=$1 AND state='running' AND lease_until>now() AND deadline>now()`, string(kind)).Scan(&active); e != nil {
		return out, false, e
	}
	if active > 0 {
		return out, false, nil
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, policyLock); e != nil {
		return out, false, e
	}
	var a, b, req []byte
	e = tx.QueryRow(ctx, `SELECT actor,status,request,input FROM speech_requests WHERE kind=$1 AND state='queued' AND deadline>now() ORDER BY accepted_at LIMIT 1 FOR UPDATE SKIP LOCKED`, string(kind)).Scan(&a, &b, &req, &out.Input)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, false, nil
	}
	if e != nil {
		return out, false, e
	}
	if out.Actor, e = w.DecodeApplicationRef(a); e != nil {
		return out, false, e
	}
	if out.Request, e = w.DecodeSpeechSubmit(req); e != nil {
		return out, false, e
	}
	status, e := w.DecodeSpeechStatus(b)
	if e != nil {
		return out, false, e
	}
	if e = s.authorize(ctx, tx, out.Actor, status.Ref.Owner); e != nil {
		if !errors.Is(e, ErrDenied) {
			return out, false, e
		}
		status.State = "failed"
		status.ErrorCode = "authorization_revoked"
		if e = save(ctx, tx, out.Actor, row{status: status, input: out.Input, output: []byte{}}); e != nil {
			return out, false, e
		}
		return out, false, tx.Commit(ctx)
	}
	var token [24]byte
	if _, e = rand.Read(token[:]); e != nil {
		return out, false, e
	}
	out.Token = hex.EncodeToString(token[:])
	status.State = "running"
	_, e = tx.Exec(ctx, `UPDATE speech_requests SET state='running',status=$4,token=$5,lease_until=LEAST(deadline,now()+interval '15 seconds') WHERE actor=$1 AND owner=$2 AND id=$3`, a, w.EncodeScopeRef(status.Ref.Owner), status.Ref.RequestId, w.EncodeSpeechStatus(status), out.Token)
	if e != nil {
		return out, false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO speech_observations(actor,owner,id,status) VALUES($1,$2,$3,$4)`, a, w.EncodeScopeRef(status.Ref.Owner), status.Ref.RequestId, w.EncodeSpeechStatus(status)); e != nil {
		return out, false, e
	}
	return out, true, tx.Commit(ctx)
}
func (s *Store) Renew(ctx context.Context, work Work) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, work.Actor, work.Request.Owner); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE speech_requests SET lease_until=LEAST(deadline,now()+interval '15 seconds') WHERE actor=$1 AND owner=$2 AND id=$3 AND token=$4 AND state='running' AND deadline>now() AND lease_until>now()`, w.EncodeApplicationRef(work.Actor), w.EncodeScopeRef(work.Request.Owner), work.Request.RequestId, work.Token)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
func (s *Store) Finish(ctx context.Context, work Work, result Result, code string) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.authorize(ctx, tx, work.Actor, work.Request.Owner); e != nil {
		return e
	}
	r, e := load(ctx, tx, work.Actor, requestRef(work.Request))
	if e != nil {
		return e
	}
	var live bool
	if e = tx.QueryRow(ctx, `SELECT lease_until>now() AND deadline>now() FROM speech_requests WHERE actor=$1 AND owner=$2 AND id=$3`, w.EncodeApplicationRef(work.Actor), w.EncodeScopeRef(work.Request.Owner), work.Request.RequestId).Scan(&live); e != nil {
		return e
	}
	if r.token != work.Token || r.status.State != "running" || !live {
		return ErrConflict
	}
	if code != "" {
		r.status.State = "failed"
		r.status.ErrorCode = code
	} else {
		if work.Request.Kind == "tts" {
			if len(result.PCM) == 0 || len(result.PCM) > MaxOutputBytes || len(result.PCM)%2 != 0 || result.SampleRate != 24000 || result.Transcript != "" {
				return ErrInvalid
			}
			hash := sha256.Sum256(result.PCM)
			digest := hash[:]
			r.output = result.PCM
			r.status.OutputBytes = uint64(len(result.PCM))
			r.status.OutputSampleRate = result.SampleRate
			r.status.OutputDigest = &digest
		} else {
			if !utf8.ValidString(result.Transcript) || len(result.Transcript) > 8192 || strings.TrimSpace(result.Transcript) == "" || len(result.PCM) != 0 {
				return ErrInvalid
			}
			r.status.Transcript = &result.Transcript
		}
		r.status.State = "completed"
	}
	if e = save(ctx, tx, work.Actor, r); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (wk *Worker) Tick(ctx context.Context) (bool, error) {
	if wk.Backend == nil {
		return false, ErrInvalid
	}
	if e := wk.Store.Sweep(ctx); e != nil {
		return false, e
	}
	work, ok, e := wk.Store.Claim(ctx, wk.Kind)
	if e != nil || !ok {
		return false, e
	}
	call, cancel := context.WithDeadline(ctx, work.Request.Deadline)
	defer cancel()
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-call.Done():
				return
			case <-ticker.C:
				if wk.Store.Renew(call, work) != nil {
					cancel()
					return
				}
			}
		}
	}()
	result, err := wk.Backend.Process(call, work)
	close(done)
	<-renewed
	code := ""
	if err != nil {
		code = "backend_failed"
	}
	if call.Err() != nil {
		code = "cancelled_or_deadline"
	}
	finish, bound := context.WithTimeout(ctx, 5*time.Second)
	defer bound()
	e = wk.Store.Finish(finish, work, result, code)
	if errors.Is(e, ErrConflict) || errors.Is(e, ErrDenied) {
		return true, nil
	}
	if errors.Is(e, ErrInvalid) {
		e = wk.Store.Finish(finish, work, Result{}, "invalid_backend_result")
	}
	return true, e
}
