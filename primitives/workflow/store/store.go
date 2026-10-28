// Package store keeps workflow definitions, runs, and task attempts in
// PostgreSQL. A run is one row with its full document. A run transition locks
// the run row first and then its task rows, so replicas serialize per run.
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	api "github.com/catalystcommunity/magi/primitives/workflow/api"
)

//go:embed schema.sql
var schema string

// ErrNotFound reports a missing record.
var ErrNotFound = errors.New("not found")

// ErrSkip, returned from an UpdateRun function, rolls back without an error.
var ErrSkip = errors.New("skip")

// Task states.
const (
	TaskSubmit  = "submit"
	TaskQueued  = "queued"
	TaskClaimed = "claimed"
	TaskFinish  = "finish"
	TaskDone    = "done"
)

// Task is one attempt of a task step and its queue bookkeeping.
type Task struct {
	Key        string
	Run        string
	Index      int
	Step       string
	Queue      string
	State      string
	UUID       string
	Revision   int64
	Claim      string
	Worker     string
	LeaseUntil int64
}

type Store struct{ DB *pgxpool.Pool }

// Open connects and applies the schema.
func Open(ctx context.Context, url string) (*Store, error) {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() { s.DB.Close() }

func Now() int64 { return time.Now().UnixMilli() }

// PutDefinition stores def as the next revision of its name. The first
// writer of a name owns it; check reports whether a later writer may write.
func (s *Store) PutDefinition(ctx context.Context, def api.Definition, owner string, check func(owner string) error) (api.Definition, error) {
	for range 5 {
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			var rev uint64
			var first *string
			err := tx.QueryRow(ctx, `SELECT coalesce(max(revision),0),
				(SELECT owner FROM workflow_definitions WHERE name=$1 ORDER BY revision LIMIT 1)
				FROM workflow_definitions WHERE name=$1`, def.Name).Scan(&rev, &first)
			if err != nil {
				return err
			}
			o := owner
			if first != nil {
				if err := check(*first); err != nil {
					return err
				}
				o = *first
			}
			def.Revision = rev + 1
			doc, _ := json.Marshal(def)
			_, err = tx.Exec(ctx, `INSERT INTO workflow_definitions (name, revision, owner, doc, created_ms) VALUES ($1,$2,$3,$4,$5)`,
				def.Name, def.Revision, o, doc, Now())
			return err
		})
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			continue // concurrent put took the revision
		}
		return def, err
	}
	return def, errors.New("put definition: too much contention")
}

// GetDefinition returns one revision, or the latest when revision is 0.
func (s *Store) GetDefinition(ctx context.Context, name string, revision uint64) (api.Definition, error) {
	var doc []byte
	err := s.DB.QueryRow(ctx, `SELECT doc FROM workflow_definitions WHERE name=$1 AND ($2=0 OR revision=$2)
		ORDER BY revision DESC LIMIT 1`, name, int64(revision)).Scan(&doc)
	var d api.Definition
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(doc, &d)
}

// CreateRun inserts run and its first tasks. When key is set and the owner
// already has a run with that key, it returns that run and existed=true.
func (s *Store) CreateRun(ctx context.Context, run api.Run, key string, tasks []Task) (api.Run, bool, error) {
	var existed bool
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		doc, _ := json.Marshal(run)
		var k *string
		if key != "" {
			k = &key
		}
		tag, err := tx.Exec(ctx, `INSERT INTO workflow_runs (id, owner, definition, status, start_key, doc, created_ms, updated_ms)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (owner, start_key) WHERE start_key IS NOT NULL DO NOTHING`,
			run.Id, run.Owner, run.Definition, run.Status, k, doc, int64(run.CreatedMs), int64(run.UpdatedMs))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			existed = true
			var doc []byte
			if err := tx.QueryRow(ctx, `SELECT doc FROM workflow_runs WHERE owner=$1 AND start_key=$2`, run.Owner, key).Scan(&doc); err != nil {
				return err
			}
			run = api.Run{}
			return json.Unmarshal(doc, &run)
		}
		return putTasks(ctx, tx, tasks)
	})
	return run, existed, err
}

func (s *Store) GetRun(ctx context.Context, id string) (api.Run, error) {
	return scanRun(s.DB.QueryRow(ctx, `SELECT doc FROM workflow_runs WHERE id=$1`, id))
}

// ListRuns returns runs, newest first. An empty owner lists all owners.
func (s *Store) ListRuns(ctx context.Context, owner, status, definition string, limit int) ([]api.Run, error) {
	rows, err := s.DB.Query(ctx, `SELECT doc FROM workflow_runs
		WHERE ($1='' OR owner=$1) AND ($2='' OR status=$2) AND ($3='' OR definition=$3)
		ORDER BY created_ms DESC, id LIMIT $4`, owner, status, definition, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []api.Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func scanRun(row pgx.Row) (api.Run, error) {
	var doc []byte
	var r api.Run
	if err := row.Scan(&doc); errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	} else if err != nil {
		return r, err
	}
	return r, json.Unmarshal(doc, &r)
}

// RunTx is an open transition of one locked run.
type RunTx struct {
	ctx context.Context
	tx  pgx.Tx
	run string
	put []Task
}

// Task locks and returns the task row of this run that matches where:
// a key, or a claim token.
func (t *RunTx) Task(key, claim string) (*Task, error) {
	row := t.tx.QueryRow(t.ctx, `SELECT `+taskCols+` FROM workflow_tasks
		WHERE run_id=$1 AND (($2<>'' AND key=$2) OR ($3<>'' AND claim=$3)) FOR UPDATE`, t.run, key, claim)
	task, err := scanTask(row)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return task, err
}

// OpenTasks locks the tasks of this run that are not finished.
func (t *RunTx) OpenTasks() ([]Task, error) {
	rows, err := t.tx.Query(t.ctx, `SELECT `+taskCols+` FROM workflow_tasks
		WHERE run_id=$1 AND state IN ('submit','queued','claimed') FOR UPDATE`, t.run)
	if err != nil {
		return nil, err
	}
	return collectTasks(rows)
}

// Put inserts or updates a task row when the transition commits.
func (t *RunTx) Put(task Task) { t.put = append(t.put, task) }

// UpdateRun locks run id, calls fn, and stores the changed run and tasks.
// It returns the stored run and the keys of the tasks fn put.
func (s *Store) UpdateRun(ctx context.Context, id string, fn func(*RunTx, *api.Run) error) (api.Run, []string, error) {
	var run api.Run
	var keys []string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, `SELECT doc FROM workflow_runs WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		rt := &RunTx{ctx: ctx, tx: tx, run: id}
		if err := fn(rt, &run); err != nil {
			return err
		}
		run.UpdatedMs = max(uint64(Now()), run.UpdatedMs+1)
		doc, _ := json.Marshal(run)
		if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET status=$2, doc=$3, updated_ms=$4 WHERE id=$1`,
			id, run.Status, doc, int64(run.UpdatedMs)); err != nil {
			return err
		}
		for _, t := range rt.put {
			keys = append(keys, t.Key)
		}
		return putTasks(ctx, tx, rt.put)
	})
	return run, keys, err
}

func putTasks(ctx context.Context, tx pgx.Tx, tasks []Task) error {
	for _, t := range tasks {
		if _, err := tx.Exec(ctx, `INSERT INTO workflow_tasks (key, run_id, idx, step, queue, state, task_uuid, revision, claim, worker, lease_until, updated_ms)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (key) DO UPDATE SET state=$6, task_uuid=$7, revision=$8, claim=$9, worker=$10, lease_until=$11, updated_ms=$12`,
			t.Key, t.Run, t.Index, t.Step, t.Queue, t.State, t.UUID, t.Revision, t.Claim, t.Worker, t.LeaseUntil, Now()); err != nil {
			return err
		}
	}
	return nil
}

// Heartbeat extends the lease of an unexpired claim.
func (s *Store) Heartbeat(ctx context.Context, run, step, claim string, until int64) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE workflow_tasks SET lease_until=$4, updated_ms=$5
		WHERE run_id=$1 AND step=$2 AND claim=$3 AND state='claimed' AND lease_until >= $5`, run, step, claim, until, Now())
	return tag.RowsAffected() == 1, err
}

// WithTask locks one task row without waiting, calls fn, and stores the
// task that fn returns. It does nothing when another process holds the row.
func (s *Store) WithTask(ctx context.Context, key string, fn func(Task) (Task, error)) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		t, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskCols+` FROM workflow_tasks WHERE key=$1 FOR UPDATE SKIP LOCKED`, key))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		nt, err := fn(*t)
		if err != nil {
			return err
		}
		return putTasks(ctx, tx, []Task{nt}) // also marks the row as checked now
	})
}

// Due returns tasks that need queue work: unsent submissions and unclosed
// queue tasks older than settle, claims whose lease ended, and queued tasks
// not changed since stale (to check for a lost claim).
func (s *Store) Due(ctx context.Context, settle, stale int64, limit int) ([]Task, error) {
	now := Now()
	rows, err := s.DB.Query(ctx, `SELECT `+taskCols+` FROM workflow_tasks WHERE
		(state IN ('submit','finish') AND updated_ms <= $1) OR
		(state='claimed' AND lease_until < $2) OR
		(state='queued' AND updated_ms <= $3)
		ORDER BY updated_ms LIMIT $4`, now-settle, now, now-stale, limit)
	if err != nil {
		return nil, err
	}
	return collectTasks(rows)
}

const taskCols = `key, run_id, idx, step, queue, state, task_uuid, revision, claim, worker, lease_until`

func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	err := row.Scan(&t.Key, &t.Run, &t.Index, &t.Step, &t.Queue, &t.State, &t.UUID, &t.Revision, &t.Claim, &t.Worker, &t.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

func collectTasks(rows pgx.Rows) ([]Task, error) {
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}
