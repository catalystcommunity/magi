// Package workshop is a bounded development workflow, not a release authority.
package workshop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/catalystcommunity/magi/execution"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) Unknown(ctx context.Context, owner w.ScopeRef, run string) error {
	_, e := s.Pool.Exec(ctx, `UPDATE workshop_runs SET state='unknown' WHERE owner=$1 AND run=$2 AND state IN ('admitted','running')`, w.EncodeScopeRef(owner), run)
	return e
}

type Event struct {
	Operation  string          `json:"operation"`
	Kind       string          `json:"kind"`
	Request    json.RawMessage `json:"request"`
	Response   json.RawMessage `json:"response,omitempty"`
	Outcome    string          `json:"outcome"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}
type Record struct {
	Run           string              `json:"run"`
	EnvironmentID string              `json:"environment_id"`
	Container     execution.Container `json:"container"`
	State         string              `json:"state"`
	ExpiresAt     time.Time           `json:"expires_at"`
	Held          bool                `json:"held"`
	Candidate     string              `json:"candidate"`
	Digest        string              `json:"digest"`
	Events        []Event             `json:"events"`
}

func (s *Store) Initialize(ctx context.Context) error {
	_, e := s.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS workshop_runs(owner bytea NOT NULL,run text NOT NULL,environment_id text NOT NULL UNIQUE,container bytea,request bytea NOT NULL,state text NOT NULL,expires_at timestamptz NOT NULL,held boolean NOT NULL DEFAULT false,candidate text NOT NULL DEFAULT '',digest text NOT NULL DEFAULT '',PRIMARY KEY(owner,run));
CREATE TABLE IF NOT EXISTS workshop_events(owner bytea NOT NULL,run text NOT NULL,operation text NOT NULL,kind text NOT NULL,request bytea NOT NULL,response bytea,outcome text NOT NULL,started_at timestamptz NOT NULL DEFAULT clock_timestamp(),finished_at timestamptz,PRIMARY KEY(owner,run,operation),FOREIGN KEY(owner,run) REFERENCES workshop_runs(owner,run));`)
	return e
}
func (s *Store) Admit(ctx context.Context, owner w.ScopeRef, run, env string, request []byte) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(427361050)`); e != nil {
		return e
	}
	var old []byte
	e = tx.QueryRow(ctx, `SELECT request FROM workshop_runs WHERE owner=$1 AND run=$2`, w.EncodeScopeRef(owner), run).Scan(&old)
	if e == nil {
		if !bytes.Equal(old, request) {
			return workflow.ErrConflict
		}
		return workflow.ErrReconcile
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM workshop_runs`).Scan(&count); e != nil {
		return e
	}
	if count >= 1000 {
		return errors.New("workshop capacity reached")
	}
	_, e = tx.Exec(ctx, `INSERT INTO workshop_runs(owner,run,environment_id,request,state,expires_at) VALUES($1,$2,$3,$4,'admitted',clock_timestamp()+interval '15 minutes')`, w.EncodeScopeRef(owner), run, env, request)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Bind(ctx context.Context, owner w.ScopeRef, run string, c execution.Container) error {
	raw, _ := json.Marshal(c)
	result, e := s.Pool.Exec(ctx, `UPDATE workshop_runs SET container=$3,state='running' WHERE owner=$1 AND run=$2 AND container IS NULL AND state='admitted'`, w.EncodeScopeRef(owner), run, raw)
	if e == nil && result.RowsAffected() != 1 {
		return workflow.ErrConflict
	}
	return e
}
func (s *Store) Begin(ctx context.Context, owner w.ScopeRef, run, operation, kind string, request []byte) error {
	if !json.Valid(request) || len(request) > 65536 {
		return workflow.ErrInvalid
	}
	result, e := s.Pool.Exec(ctx, `INSERT INTO workshop_events(owner,run,operation,kind,request,outcome) VALUES($1,$2,$3,$4,$5,'dispatched') ON CONFLICT DO NOTHING`, w.EncodeScopeRef(owner), run, operation, kind, request)
	if e == nil && result.RowsAffected() != 1 {
		return workflow.ErrReconcile
	}
	return e
}
func (s *Store) Finish(ctx context.Context, owner w.ScopeRef, run, operation string, response []byte, outcome string) error {
	if !json.Valid(response) || len(response) > 65536 {
		return workflow.ErrInvalid
	}
	result, e := s.Pool.Exec(ctx, `UPDATE workshop_events SET response=$4,outcome=$5,finished_at=clock_timestamp() WHERE owner=$1 AND run=$2 AND operation=$3 AND response IS NULL`, w.EncodeScopeRef(owner), run, operation, response, outcome)
	if e == nil && result.RowsAffected() != 1 {
		return workflow.ErrConflict
	}
	return e
}
func (s *Store) Complete(ctx context.Context, owner w.ScopeRef, run, candidate, digest, state string) error {
	result, e := s.Pool.Exec(ctx, `UPDATE workshop_runs SET candidate=$3,digest=$4,state=$5 WHERE owner=$1 AND run=$2 AND state='running' AND expires_at>clock_timestamp()`, w.EncodeScopeRef(owner), run, candidate, digest, state)
	if e == nil && result.RowsAffected() != 1 {
		return workflow.ErrReconcile
	}
	return e
}
func (s *Store) Read(ctx context.Context, owner w.ScopeRef, run string) (Record, error) {
	v := Record{Run: run, Events: []Event{}}
	var raw []byte
	e := s.Pool.QueryRow(ctx, `SELECT environment_id,container,state,expires_at,held,candidate,digest FROM workshop_runs WHERE owner=$1 AND run=$2`, w.EncodeScopeRef(owner), run).Scan(&v.EnvironmentID, &raw, &v.State, &v.ExpiresAt, &v.Held, &v.Candidate, &v.Digest)
	if e != nil {
		return v, e
	}
	if len(raw) > 0 && json.Unmarshal(raw, &v.Container) != nil {
		return v, workflow.ErrInvalid
	}
	rows, e := s.Pool.Query(ctx, `SELECT operation,kind,request,response,outcome,started_at,finished_at FROM workshop_events WHERE owner=$1 AND run=$2 ORDER BY started_at,operation LIMIT 32`, w.EncodeScopeRef(owner), run)
	if e != nil {
		return v, e
	}
	defer rows.Close()
	for rows.Next() {
		var event Event
		if e = rows.Scan(&event.Operation, &event.Kind, &event.Request, &event.Response, &event.Outcome, &event.StartedAt, &event.FinishedAt); e != nil {
			return v, e
		}
		v.Events = append(v.Events, event)
	}
	return v, rows.Err()
}
func (s *Store) Hold(ctx context.Context, owner w.ScopeRef, run string, held bool) error {
	result, e := s.Pool.Exec(ctx, `UPDATE workshop_runs SET held=$3 WHERE owner=$1 AND run=$2 AND state NOT IN ('closed','closing') AND expires_at>clock_timestamp()`, w.EncodeScopeRef(owner), run, held)
	if e == nil && result.RowsAffected() != 1 {
		return workflow.ErrConflict
	}
	return e
}

// Cleanup records its exact target before removal. An interrupted removal can
// reconcile confirmed absence. It never repeats an agent or tool operation.
func (s *Store) Cleanup(ctx context.Context, owner w.ScopeRef, run string) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var id, state string
	var held bool
	var expires time.Time
	if e = tx.QueryRow(ctx, `SELECT environment_id,container,state,held,expires_at FROM workshop_runs WHERE owner=$1 AND run=$2 FOR UPDATE`, w.EncodeScopeRef(owner), run).Scan(&id, &raw, &state, &held, &expires); e != nil {
		return e
	}
	if state == "closed" {
		return nil
	}
	if time.Now().Before(expires) && (held || state == "running" || state == "admitted") {
		return workflow.ErrConflict
	}
	var c execution.Container
	if len(raw) == 0 {
		// Recover an orphan after create succeeded but its reply or bind was lost.
		c, e = execution.Find(ctx, id)
		if e != nil {
			return e
		}
	} else if json.Unmarshal(raw, &c) != nil {
		return workflow.ErrInvalid
	}
	request, _ := json.Marshal(map[string]any{"environment": id, "container": c, "hard_deadline": expires, "deadline_reached": !time.Now().Before(expires)})
	if _, e = tx.Exec(ctx, `INSERT INTO workshop_events(owner,run,operation,kind,request,outcome) VALUES($1,$2,'environment-cleanup','environment',$3,'dispatched') ON CONFLICT DO NOTHING`, w.EncodeScopeRef(owner), run, request); e != nil {
		return e
	}
	bound, _ := json.Marshal(c)
	if _, e = tx.Exec(ctx, `UPDATE workshop_runs SET state='closing',container=$3 WHERE owner=$1 AND run=$2`, w.EncodeScopeRef(owner), run, bound); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if c.ID != "" {
		if e = c.Destroy(ctx, id); e != nil {
			return e
		}
	}
	// Keep completion and its state transition atomic. A crash before this
	// commit leaves a closing record, not a reason to rerun the original task.
	tx, e = s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `UPDATE workshop_events SET response=$3,outcome='returned',finished_at=clock_timestamp() WHERE owner=$1 AND run=$2 AND operation='environment-cleanup' AND response IS NULL`, w.EncodeScopeRef(owner), run, []byte(`{"removed_or_absent":true}`)); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE workshop_runs SET state='closed' WHERE owner=$1 AND run=$2`, w.EncodeScopeRef(owner), run)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
