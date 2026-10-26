//go:build integration

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/catalystcommunity/magi/eventing"
	ew "github.com/catalystcommunity/magi/generated/events/go"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var corndogsImage = configuredCorndogsImage()

const postgresImage = "postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd"

func must(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func docker(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("fixture docker %s: %v: %s", args[0], e, out)
	}
	return strings.TrimSpace(string(out))
}
func freePort(t testing.TB) string {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	must(t, e)
	defer l.Close()
	return l.Addr().String()
}

type fixture struct {
	pool     *pgxpool.Pool
	address  string
	seeds    []string
	server   string
	servers  []string
	database string
}

func newFixture(t *testing.T, mode string) *fixture {
	t.Helper()
	id := "opal-workflow-" + api.NewOperationID()
	network := id
	containers := []string{}
	docker(t, "network", "create", "--label", "opal.workflow="+id, network)
	t.Cleanup(func() {
		for i := len(containers) - 1; i >= 0; i-- {
			name := containers[i]
			if docker(t, "inspect", "-f", `{{index .Config.Labels "opal.workflow"}}`, name) != id {
				t.Error("unsafe cleanup target")
				continue
			}
			docker(t, "rm", "-f", "-v", name)
		}
		if docker(t, "network", "inspect", "-f", `{{index .Labels "opal.workflow"}}`, network) == id {
			docker(t, "network", "rm", network)
		}
		t.Log("Removed only this test's containers, volumes, and network")
	})
	run := func(name string, args []string, image string) {
		docker(t, append(append([]string{"run", "--pull=never", "-d", "--name", name, "--network", network, "--label", "opal.workflow=" + id, "--memory", "512m", "--cpus", "2", "--pids-limit", "256"}, args...), image)...)
		containers = append(containers, name)
	}
	address := freePort(t)
	pgname := id + "-pg"
	run(pgname, []string{"-p", address + ":5432", "-e", "POSTGRES_PASSWORD=synthetic-only"}, postgresImage)
	admin, e := pgxpool.New(context.Background(), "postgres://postgres:synthetic-only@"+address+"/postgres?sslmode=disable")
	must(t, e)
	t.Cleanup(admin.Close)
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		e = admin.Ping(ctx)
		cancel()
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("database not ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, e = admin.Exec(context.Background(), `CREATE ROLE fixture LOGIN PASSWORD 'synthetic-runtime' NOSUPERUSER NOBYPASSRLS`)
	must(t, e)
	f := &fixture{pool: admin, address: address, database: pgname}
	if mode == "postgres" {
		_, e = admin.Exec(context.Background(), `CREATE DATABASE corndogs`)
		must(t, e)
	}
	nodes := 1
	if mode == "postgres" {
		nodes = 2
	}
	for i := 0; i < nodes; i++ {
		name := fmt.Sprintf("%s-cd-%d", id, i)
		addr := freePort(t)
		args := []string{"-p", addr + ":5080", "-e", "STORAGE_BACKEND=" + mode, "-e", "CORNDOGS_FILESTORE_DIR=/tmp/opal-workflow-data", "-e", "CORNDOGS_FILESTORE_SYNC=always", "-e", "CORNDOGS_SUBMISSION_KEY_POLICY=required", "-e", "CORNDOGS_TASK_GUARD_POLICY=required", "-e", "LOGLEVEL=error"}
		if mode == "postgres" {
			args = append(args, "-e", "DATABASE_HOST="+pgname, "-e", "DATABASE_NAME=corndogs", "-e", "DATABASE_USER=postgres", "-e", "DATABASE_PASSWORD=synthetic-only", "-e", "DATABASE_STARTUP_TIMEOUT_SECONDS=20")
		}
		run(name, args, corndogsImage)
		f.seeds = append(f.seeds, addr)
		f.server = name
		f.servers = append(f.servers, name)
		client := api.NewCorndogsClient(&Transport{Seeds: []string{addr}})
		deadline = time.Now().Add(30 * time.Second)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, e = client.GetServerInfo(ctx, api.GetServerInfoRequest{})
			cancel()
			if e == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("Corndogs not ready: %v", e)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return f
}
func (f *fixture) journal(t testing.TB) (*Journal, string) {
	t.Helper()
	name := "wf_" + api.NewOperationID()
	_, e := f.pool.Exec(context.Background(), "CREATE DATABASE "+name+" OWNER fixture")
	must(t, e)
	dsn := "postgres://fixture:synthetic-runtime@" + f.address + "/" + name + "?sslmode=disable"
	cfg, e := pgxpool.ParseConfig(dsn)
	must(t, e)
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
	must(t, e)
	t.Cleanup(pool.Close)
	j := &Journal{Pool: pool, Installation: "fixture", Transport: &Transport{Seeds: f.seeds}}
	must(t, j.Initialize(context.Background()))
	return j, dsn
}

var actor = w.ApplicationRef{Identity: w.IdentityRef{Domain: "catalystsquad.local", AccountId: "engineering-manager"}, ApplicationId: "workflow", InstanceId: "one"}
var why = Provenance{Actor: actor, Admission: w.RecordRef{Authority: actor.Identity, Kind: "admission", RecordId: "synthetic-policy"}, Cause: w.RecordRef{Authority: actor.Identity, Kind: "command", RecordId: "synthetic-request"}}

func submission(queue string) []byte {
	return api.EncodeSubmitKeyedTaskRequest(api.SubmitKeyedTaskRequest{Guarded: true, Queue: queue, CurrentState: "ready", AutoTargetState: "ready", Timeout: -1, Payload: []byte("synthetic input")})
}
func claim(queue string) []byte {
	return api.EncodeClaimGuardedTaskRequest(api.ClaimGuardedTaskRequest{Queue: queue, CurrentState: "ready", OverrideCurrentState: "working", OverrideAutoTargetState: "ready", OverrideTimeout: 2})
}
func submit(t testing.TB, j *Journal, id, queue string) api.SubmissionReceipt {
	t.Helper()
	ctx := context.Background()
	must(t, j.Prepare(ctx, id, "SubmitKeyedTask", submission(queue), why))
	v, e := j.Resolve(ctx, id)
	must(t, e)
	r, e := api.DecodeSubmissionReceipt(v.Receipt)
	must(t, e)
	return r
}

type faultTransport struct {
	base      api.Transport
	method    string
	lost      atomic.Bool
	crash     bool
	retention int64
}

func (f *faultTransport) Call(ctx context.Context, service, op string, request []byte) ([]byte, error) {
	r, e := f.base.Call(ctx, service, op, request)
	if e == nil && op == "GetServerInfo" && f.retention != 0 {
		info, _ := api.DecodeGetServerInfoResponse(r)
		info.ReceiptRetentionSeconds = f.retention
		r = api.EncodeGetServerInfoResponse(info)
	}
	if e == nil && op == f.method && f.lost.CompareAndSwap(false, true) {
		if f.crash {
			syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
		return nil, api.ErrOutcomeUncertain
	}
	return r, e
}

func TestCorndogsJournalRecovery(t *testing.T) {
	for _, mode := range []string{"file", "postgres"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode)
			ctx := context.Background()
			t.Run("concurrent-logical-step-has-one-intent-and-task", func(t *testing.T) {
				j, _ := f.journal(t)
				queue := api.NewOperationID()
				var wg sync.WaitGroup
				errs := make(chan error, 8)
				ids := make(chan string, 8)
				for range 8 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						e := j.Prepare(ctx, "logical-step", "SubmitKeyedTask", submission(queue), why)
						if e != nil {
							errs <- e
							return
						}
						v, e := j.Resolve(ctx, "logical-step")
						if e != nil {
							errs <- e
							return
						}
						r, e := api.DecodeSubmissionReceipt(v.Receipt)
						errs <- e
						ids <- r.TaskUuid
					}()
				}
				wg.Wait()
				close(errs)
				close(ids)
				for e := range errs {
					must(t, e)
				}
				id := ""
				for next := range ids {
					if id != "" && id != next {
						t.Fatal("duplicate task")
					}
					id = next
				}
				changed, _ := api.DecodeSubmitKeyedTaskRequest(submission(queue))
				changed.Payload = []byte("different")
				if e := j.Prepare(ctx, "logical-step", "SubmitKeyedTask", api.EncodeSubmitKeyedTaskRequest(changed), why); !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
			})
			t.Run("T09-lost-submit-reply-recovered-by-new-coordinator", func(t *testing.T) {
				j, _ := f.journal(t)
				queue := api.NewOperationID()
				fault := &faultTransport{base: j.Transport, method: "SubmitKeyedTask"}
				j.Transport = fault
				must(t, j.Prepare(ctx, "step", "SubmitKeyedTask", submission(queue), why))
				_, e := j.Resolve(ctx, "step")
				if !errors.Is(e, api.ErrOutcomeUncertain) {
					t.Fatal(e)
				}
				before, e := j.Read(ctx, "step")
				must(t, e)
				j = &Journal{Pool: j.Pool, Installation: j.Installation, Transport: fault.base}
				after, e := j.Resolve(ctx, "step")
				must(t, e)
				if !before.FirstSend.Equal(*after.FirstSend) || !before.SafeUntil.Equal(*after.SafeUntil) || after.Attempts != 1 {
					t.Fatal("recovery resent or extended deadline")
				}
				if after.State != "confirmed" {
					t.Fatal(after.State)
				}
			})
			t.Run("expired-absent-receipt-never-resubmits", func(t *testing.T) {
				j, _ := f.journal(t)
				must(t, j.Prepare(ctx, "expired", "SubmitKeyedTask", submission(api.NewOperationID()), why))
				_, e := j.arm(ctx, "expired", 3600)
				must(t, e)
				_, e = j.Pool.Exec(ctx, `UPDATE workflow_intents SET first_send=clock_timestamp()-interval '2 hours',safe_until=clock_timestamp()-interval '1 hour'`)
				must(t, e)
				v, e := j.Resolve(ctx, "expired")
				if !errors.Is(e, ErrReconcile) || v.Attempts != 0 || v.State != "reconcile_required" {
					t.Fatalf("unsafe retry: %+v %v", v, e)
				}
			})
			t.Run("expired-local-window-can-read-existing-receipt", func(t *testing.T) {
				j, _ := f.journal(t)
				base := j.Transport
				j.Transport = &faultTransport{base: base, method: "SubmitKeyedTask"}
				must(t, j.Prepare(ctx, "step", "SubmitKeyedTask", submission(api.NewOperationID()), why))
				_, e := j.Resolve(ctx, "step")
				if !errors.Is(e, api.ErrOutcomeUncertain) {
					t.Fatal(e)
				}
				_, e = j.Pool.Exec(ctx, `UPDATE workflow_intents SET safe_until=clock_timestamp()-interval '1 second'`)
				must(t, e)
				j.Transport = base
				v, e := j.Resolve(ctx, "step")
				must(t, e)
				if v.Attempts != 1 || v.State != "confirmed" {
					t.Fatal("read recovery sent another mutation")
				}
			})
			t.Run("journal-and-Corndogs-restart-retain-uncertain-intent", func(t *testing.T) {
				j, _ := f.journal(t)
				base := j.Transport
				j.Transport = &faultTransport{base: base, method: "SubmitKeyedTask"}
				must(t, j.Prepare(ctx, "step", "SubmitKeyedTask", submission(api.NewOperationID()), why))
				_, e := j.Resolve(ctx, "step")
				if !errors.Is(e, api.ErrOutcomeUncertain) {
					t.Fatal(e)
				}
				before, e := j.Read(ctx, "step")
				must(t, e)
				for _, name := range f.servers {
					docker(t, "kill", "--signal=KILL", name)
				}
				docker(t, "kill", "--signal=KILL", f.database)
				docker(t, "start", f.database)
				deadline := time.Now().Add(30 * time.Second)
				for {
					c, cancel := context.WithTimeout(ctx, time.Second)
					e = f.pool.Ping(c)
					cancel()
					if e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("database restart failed")
					}
					time.Sleep(50 * time.Millisecond)
				}
				for _, name := range f.servers {
					docker(t, "start", name)
				}
				j.Transport = base
				deadline = time.Now().Add(30 * time.Second)
				for {
					c, cancel := context.WithTimeout(ctx, time.Second)
					_, e = j.Check(c)
					cancel()
					if e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("Corndogs restart failed")
					}
					time.Sleep(50 * time.Millisecond)
				}
				after, e := j.Resolve(ctx, "step")
				must(t, e)
				if after.Attempts != 1 || !before.FirstSend.Equal(*after.FirstSend) {
					t.Fatal("lost journal after restart")
				}
			})
			t.Run("changed-retention-and-clock-reversal-stop-replay", func(t *testing.T) {
				for _, kind := range []string{"retention", "clock"} {
					t.Run(kind, func(t *testing.T) {
						j, _ := f.journal(t)
						must(t, j.Prepare(ctx, "step", "SubmitKeyedTask", submission(api.NewOperationID()), why))
						_, e := j.arm(ctx, "step", 3600)
						must(t, e)
						if kind == "retention" {
							j.Transport = &faultTransport{base: j.Transport, retention: 60}
						} else {
							_, e = j.Pool.Exec(ctx, `UPDATE workflow_intents SET last_checked=clock_timestamp()+interval '10 minutes'`)
							must(t, e)
						}
						v, e := j.Resolve(ctx, "step")
						if !errors.Is(e, ErrReconcile) || v.Attempts != 0 {
							t.Fatalf("unsafe retry: %v", e)
						}
					})
				}
			})
			t.Run("empty-poll-stays-empty-and-next-poll-is-distinct", func(t *testing.T) {
				j, _ := f.journal(t)
				queue := api.NewOperationID()
				must(t, j.Prepare(ctx, "empty", "ClaimGuardedTask", claim(queue), why))
				v, e := j.Resolve(ctx, "empty")
				must(t, e)
				if len(v.Receipt) != 0 {
					t.Fatal("unexpected claim")
				}
				submit(t, j, "submit", queue)
				v, e = j.Resolve(ctx, "empty")
				must(t, e)
				if len(v.Receipt) != 0 {
					t.Fatal("empty intent claimed new work")
				}
				must(t, j.Prepare(ctx, "next", "ClaimGuardedTask", claim(queue), why))
				_, e = j.Resolve(ctx, "next")
				must(t, e)
				current, e := j.CurrentClaim(ctx, "next")
				must(t, e)
				if current == nil {
					t.Fatal("new poll missed task")
				}
			})
			t.Run("T08-lost-claim-and-same-state-reassignment-fence-old-worker", func(t *testing.T) {
				j, _ := f.journal(t)
				queue := api.NewOperationID()
				submit(t, j, "submit", queue)
				base := j.Transport
				j.Transport = &faultTransport{base: base, method: "ClaimGuardedTask"}
				must(t, j.Prepare(ctx, "old", "ClaimGuardedTask", claim(queue), why))
				_, e := j.Resolve(ctx, "old")
				if !errors.Is(e, api.ErrOutcomeUncertain) {
					t.Fatal(e)
				}
				j.Transport = base
				_, e = j.Resolve(ctx, "old")
				must(t, e)
				old, e := j.CurrentClaim(ctx, "old")
				must(t, e)
				_, e = api.NewCorndogsClient(j.Transport).CleanUpTimedOut(ctx, api.CleanUpTimedOutRequest{AtTime: time.Now().Add(5 * time.Second).UnixNano()})
				must(t, e)
				must(t, j.Prepare(ctx, "new", "ClaimGuardedTask", claim(queue), why))
				_, e = j.Resolve(ctx, "new")
				must(t, e)
				current, e := j.CurrentClaim(ctx, "new")
				must(t, e)
				if current.Revision <= old.Revision || current.Task.CurrentState != old.Task.CurrentState {
					t.Fatal("fixture did not create ABA reassignment")
				}
				if _, e = j.CurrentClaim(ctx, "old"); !errors.Is(e, ErrStale) {
					t.Fatal(e)
				}
				tx, e := j.Pool.Begin(ctx)
				must(t, e)
				defer tx.Rollback(ctx)
				must(t, j.SaveResultTx(ctx, tx, old.Task.Uuid, old.Revision, []byte("old result"), "evidence:old"))
				must(t, tx.Commit(ctx))
				must(t, j.Prepare(ctx, "old-complete", "CompleteGuardedTask", api.EncodeCompleteGuardedTaskRequest(api.CompleteGuardedTaskRequest{Uuid: old.Task.Uuid, Queue: queue, ExpectedRevision: old.Revision, ExpectedState: &old.Task.CurrentState}), why))
				_, e = j.Resolve(ctx, "old-complete")
				code, ok := api.ServiceErrorCode(e)
				if !ok || code != api.CodeRevisionConflict {
					t.Fatal(e)
				}
				v, e := j.Resolve(ctx, "old-complete")
				if !errors.Is(e, ErrRejected) || v.State != "rejected" {
					t.Fatal(e)
				}
			})
			t.Run("completion-requires-result-for-exact-revision", func(t *testing.T) {
				j, _ := f.journal(t)
				r := submit(t, j, "submit", api.NewOperationID())
				state := "ready"
				e := j.Prepare(ctx, "complete", "CompleteGuardedTask", api.EncodeCompleteGuardedTaskRequest(api.CompleteGuardedTaskRequest{Uuid: r.TaskUuid, Queue: r.Queue, ExpectedRevision: 1, ExpectedState: &state}), why)
				if !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
			})
			t.Run("human-binding-duplicates-and-rollback", func(t *testing.T) {
				j, _ := f.journal(t)
				queue := api.NewOperationID()
				request := api.SubmitKeyedTaskRequest{Guarded: true, Queue: queue, CurrentState: "waiting_human", AutoTargetState: "waiting_human", Timeout: -1}
				must(t, j.Prepare(ctx, "human", "SubmitKeyedTask", api.EncodeSubmitKeyedTaskRequest(request), why))
				intent, e := j.Resolve(ctx, "human")
				must(t, e)
				receipt, e := api.DecodeSubmissionReceipt(intent.Receipt)
				must(t, e)
				client := api.NewCorndogsClient(j.Transport)
				view, e := client.GetGuardedTask(ctx, api.GetGuardedTaskRequest{Queue: queue, Uuid: receipt.TaskUuid})
				must(t, e)
				binding := HumanBinding{ID: "assignment", Service: w.ApplicationRef{Identity: w.IdentityRef{Domain: "partner.local", AccountId: "longhouse"}, ApplicationId: "tasks", InstanceId: "one"}, Assignment: w.RecordRef{Authority: w.IdentityRef{Domain: "partner.local", AccountId: "household"}, Kind: "task", RecordId: "external-task"}, AssignmentGeneration: "one", Task: *view.Task, CompletionIntent: "human-complete"}
				tx, e := j.Pool.Begin(ctx)
				must(t, e)
				defer tx.Rollback(ctx)
				must(t, j.BindHumanTx(ctx, tx, binding))
				must(t, tx.Commit(ctx))
				_, e = client.CleanUpTimedOut(ctx, api.CleanUpTimedOutRequest{AtTime: time.Now().Add(365 * 24 * time.Hour).UnixNano()})
				must(t, e)
				parked, e := client.GetGuardedTask(ctx, api.GetGuardedTaskRequest{Queue: queue, Uuid: receipt.TaskUuid})
				must(t, e)
				if parked.Task.Revision != binding.Task.Revision {
					t.Fatal("parked human task timed out")
				}
				completion := HumanCompletion{Assignment: binding.Assignment, AssignmentGeneration: "one", SourceRevision: "task-revision-2", Completer: w.IdentityRef{Domain: "partner.local", AccountId: "human"}, Evidence: w.RecordRef{Authority: binding.Service.Identity, Kind: "completion", RecordId: "completion-1"}, Result: []byte("human result")}
				for _, kind := range []string{"wrong-service", "wrong-generation", "rollback", "accept", "duplicate", "conflict"} {
					t.Run(kind, func(t *testing.T) {
						source := binding.Service
						c := completion
						if kind == "wrong-service" {
							source.InstanceId = "other"
						}
						if kind == "wrong-generation" {
							c.AssignmentGeneration = "old"
						}
						if kind == "conflict" {
							c.Result = []byte("changed")
						}
						tx, e := j.Pool.Begin(ctx)
						must(t, e)
						defer tx.Rollback(ctx)
						changed, e := j.CompleteHumanTx(ctx, tx, binding.ID, source, c, why)
						if kind == "wrong-service" || kind == "wrong-generation" {
							if !errors.Is(e, ErrInvalid) {
								t.Fatal(e)
							}
							return
						}
						if kind == "conflict" {
							if !errors.Is(e, ErrConflict) {
								t.Fatal(e)
							}
							return
						}
						must(t, e)
						if kind == "duplicate" && changed || kind != "duplicate" && !changed {
							t.Fatal("incorrect completion deduplication")
						}
						if kind == "rollback" {
							must(t, tx.Rollback(ctx))
							if _, e = j.Read(ctx, binding.CompletionIntent); !errors.Is(e, ErrNotFound) {
								t.Fatal("partial completion commit")
							}
						} else {
							must(t, tx.Commit(ctx))
						}
					})
				}
				_, e = j.Resolve(ctx, binding.CompletionIntent)
				must(t, e)
				_, e = j.Resolve(ctx, binding.CompletionIntent)
				must(t, e)
				terminal, e := client.GetGuardedTask(ctx, api.GetGuardedTaskRequest{Queue: queue, Uuid: receipt.TaskUuid})
				must(t, e)
				if !terminal.Task.Terminal || terminal.Task.Revision != binding.Task.Revision+1 {
					t.Fatal("human completion advanced more than once")
				}
			})
			t.Run("process-killed-after-intent-and-after-remote-commit", func(t *testing.T) {
				for _, stage := range []string{"prepared", "remote"} {
					t.Run(stage, func(t *testing.T) {
						j, dsn := f.journal(t)
						queue := api.NewOperationID()
						crash(t, stage, dsn, f.seeds[0], queue, nil)
						v, e := j.Resolve(ctx, "crash-submit")
						must(t, e)
						if v.State != "confirmed" {
							t.Fatal(v.State)
						}
					})
				}
			})
			t.Run("result-completion-and-event-outbox-recover-separate-commits", func(t *testing.T) {
				j, dsn := f.journal(t)
				queue := api.NewOperationID()
				submit(t, j, "submit", queue)
				must(t, j.Prepare(ctx, "claim", "ClaimGuardedTask", claim(queue), why))
				_, e := j.Resolve(ctx, "claim")
				must(t, e)
				task, e := j.CurrentClaim(ctx, "claim")
				must(t, e)
				local := &eventing.Local{Pool: j.Pool, Application: eventActor}
				must(t, local.Initialize(ctx))
				_, e = j.Pool.Exec(ctx, `CREATE TABLE business_effect(id text PRIMARY KEY)`)
				must(t, e)
				crash(t, "result", dsn, f.seeds[0], queue, task)
				j.Transport = &faultTransport{base: j.Transport, method: "CompleteGuardedTask"}
				_, e = j.Resolve(ctx, "complete")
				if !errors.Is(e, api.ErrOutcomeUncertain) {
					t.Fatal(e)
				}
				_, e = j.Resolve(ctx, "complete")
				must(t, e)
				var count int
				must(t, j.Pool.QueryRow(ctx, `SELECT count(*) FROM business_effect`).Scan(&count))
				if count != 1 {
					t.Fatal("business result lost")
				}
				brokerDB, _ := f.journal(t)
				store := &eventing.Store{Pool: brokerDB.Pool, Application: eventBroker}
				must(t, store.Initialize(ctx))
				must(t, store.SetGrant(ctx, eventing.Grant{Application: eventActor, Producer: eventActor.Identity, EventType: "workflow.result", Publish: true}))
				must(t, store.SetGrant(ctx, eventing.Grant{Application: eventConsumer, Producer: eventActor.Identity, EventType: "workflow.result", Receive: true}))
				_, e = store.Subscribe(ctx, eventConsumer, ew.EventSubscription{SubscriptionId: "results", Producer: eventActor.Identity, EventType: "workflow.result"})
				must(t, e)
				lost := false
				publish := func(c context.Context, _ ew.ApplicationRef, b []byte) (ew.PublicationReceipt, error) {
					raw, e := store.Publish(c, eventActor, b)
					if e != nil {
						return ew.PublicationReceipt{}, e
					}
					if !lost {
						lost = true
						return ew.PublicationReceipt{}, eventing.ErrUnavailable
					}
					return ew.DecodePublicationReceipt(raw)
				}
				_, e = local.PublishOne(ctx, publish)
				if !errors.Is(e, eventing.ErrUnavailable) {
					t.Fatal(e)
				}
				_, e = j.Pool.Exec(ctx, `UPDATE event_local_outbox SET due=clock_timestamp()`)
				must(t, e)
				_, e = local.PublishOne(ctx, publish)
				must(t, e)
				raw, e := store.Pull(ctx, eventConsumer, ew.PullEvents{SubscriptionId: "results", Limit: 8})
				must(t, e)
				batch, e := ew.DecodeDeliveryBatch(raw)
				must(t, e)
				if len(batch.Deliveries) != 1 {
					t.Fatal("duplicate event obligation")
				}
				consumerDB, _ := f.journal(t)
				consumer := &eventing.Local{Pool: consumerDB.Pool, Application: eventConsumer}
				must(t, consumer.Initialize(ctx))
				_, e = consumerDB.Pool.Exec(ctx, `CREATE TABLE received(id text PRIMARY KEY)`)
				must(t, e)
				for range 2 {
					_, e = consumer.Accept(ctx, eventBroker, batch.Deliveries[0])
					must(t, e)
					tx, e := consumerDB.Pool.Begin(ctx)
					must(t, e)
					defer tx.Rollback(ctx)
					_, e = consumer.ApplyTx(ctx, tx, eventBroker, batch.Deliveries[0].DeliveryId, func(tx pgx.Tx, ev ew.Event) error {
						_, e := tx.Exec(ctx, `INSERT INTO received VALUES($1)`, ev.Ref.RecordId)
						return e
					})
					must(t, e)
					must(t, tx.Commit(ctx))
				}
			})
			t.Run("journal-is-bound-to-one-storage-authority", func(t *testing.T) {
				j, _ := f.journal(t)
				j.Installation = "foreign"
				if !errors.Is(j.Initialize(ctx), ErrConflict) {
					t.Fatal("wrong installation accepted")
				}
			})
		})
	}
}

var eventActor = ew.ApplicationRef{Identity: ew.IdentityRef{Domain: "catalystsquad.local", AccountId: "engineering-manager"}, ApplicationId: "workflow", InstanceId: "one"}
var eventBroker = ew.ApplicationRef{Identity: ew.IdentityRef{Domain: "catalystsquad.local", AccountId: "events"}, ApplicationId: "events", InstanceId: "one"}
var eventConsumer = ew.ApplicationRef{Identity: ew.IdentityRef{Domain: "partner.local", AccountId: "reporter"}, ApplicationId: "report", InstanceId: "one"}

func resultEvent() []byte {
	return ew.EncodeEvent(ew.Event{Ref: ew.RecordRef{Authority: eventActor.Identity, Kind: "event", RecordId: "workflow-result"}, Producer: eventActor.Identity, Recipients: []ew.IdentityRef{eventConsumer.Identity}, EventType: "workflow.result", OccurredAt: time.Unix(1800000000, 0).UTC(), RecordedAt: time.Unix(1800000000, 0).UTC(), Payload: ew.TypedPayload{Schema: ew.SchemaRef{Name: "result", Version: "1", Digest: make([]byte, 32)}, Data: []byte("result")}, Causes: []ew.RecordRef{}, Restrictions: ew.Restrictions{Requirements: []ew.SourceRestriction{}}})
}
func crash(t testing.TB, stage, dsn, address, queue string, task *api.GuardedTask) {
	t.Helper()
	raw, _ := json.Marshal(task)
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalCrashChild$")
	cmd.Env = append(os.Environ(), "OPAL_WF_CRASH="+stage, "OPAL_WF_DSN="+dsn, "OPAL_WF_ADDRESS="+address, "OPAL_WF_QUEUE="+queue, "OPAL_WF_TASK="+string(raw))
	out, e := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("child did not reach crash boundary: %v %s", e, out)
	}
}
func TestJournalCrashChild(t *testing.T) {
	stage := os.Getenv("OPAL_WF_CRASH")
	if stage == "" {
		return
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, os.Getenv("OPAL_WF_DSN"))
	must(t, e)
	defer pool.Close()
	j := &Journal{Pool: pool, Installation: "fixture", Transport: &Transport{Seeds: []string{os.Getenv("OPAL_WF_ADDRESS")}}}
	queue := os.Getenv("OPAL_WF_QUEUE")
	if stage == "prepared" || stage == "remote" {
		must(t, j.Prepare(ctx, "crash-submit", "SubmitKeyedTask", submission(queue), why))
		if stage == "remote" {
			j.Transport = &faultTransport{base: j.Transport, method: "SubmitKeyedTask", crash: true}
			_, e = j.Resolve(ctx, "crash-submit")
			must(t, e)
		}
	} else if stage == "result" {
		var task api.GuardedTask
		must(t, json.Unmarshal([]byte(os.Getenv("OPAL_WF_TASK")), &task))
		tx, e := pool.Begin(ctx)
		must(t, e)
		defer tx.Rollback(ctx)
		_, e = tx.Exec(ctx, `INSERT INTO business_effect VALUES('one')`)
		must(t, e)
		must(t, j.SaveResultTx(ctx, tx, task.Task.Uuid, task.Revision, []byte("durable result"), "evidence:result"))
		must(t, j.PrepareTx(ctx, tx, "complete", "CompleteGuardedTask", api.EncodeCompleteGuardedTaskRequest(api.CompleteGuardedTaskRequest{Uuid: task.Task.Uuid, Queue: queue, ExpectedRevision: task.Revision, ExpectedState: &task.Task.CurrentState}), why))
		local := &eventing.Local{Pool: pool, Application: eventActor}
		must(t, local.EnqueueTx(ctx, tx, eventBroker, resultEvent()))
		must(t, tx.Commit(ctx))
	} else {
		t.Fatal("unknown crash boundary")
	}
	must(t, syscall.Kill(os.Getpid(), syscall.SIGKILL))
	t.Fatal("SIGKILL returned")
}

func configuredCorndogsImage() string {
	if image := os.Getenv("MAGI_CORNDOGS_IMAGE"); image != "" {
		return image
	}
	return "registry.catalystsquad.local/corndogs@sha256:e614bf3f88dd37d40ec7c5488bc66c06be3efbd73576eef5adfe1b9da1c64a70"
}
