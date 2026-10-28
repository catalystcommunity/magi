package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/workflow/api"
	"github.com/catalystcommunity/magi/primitives/workflow/store"
)

// memQueue is an in-memory Queue for tests without Corndogs.
type memQueue struct {
	mu    sync.Mutex
	tasks []*memTask
}

type memTask struct {
	uuid, queue, key, state string
	rev                     int64
}

func (m *memQueue) find(f func(*memTask) bool) *memTask {
	for _, t := range m.tasks {
		if f(t) {
			return t
		}
	}
	return nil
}

func (m *memQueue) Submit(_ context.Context, queue, key string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(func(t *memTask) bool { return t.queue == queue && t.key == key })
	if t == nil {
		t = &memTask{uuid: newID("mem_"), queue: queue, key: key, state: statePending, rev: 1}
		m.tasks = append(m.tasks, t)
	}
	return Ticket{UUID: t.uuid, Revision: t.rev}, nil
}

func (m *memQueue) Claim(_ context.Context, queue string) (*Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(func(t *memTask) bool { return t.queue == queue && t.state == statePending })
	if t == nil {
		return nil, nil
	}
	t.state, t.rev = stateWorking, t.rev+1
	return &Delivery{Ticket: Ticket{UUID: t.uuid, Revision: t.rev}, Key: t.key}, nil
}

func (m *memQueue) Finish(_ context.Context, _ string, tk Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.find(func(t *memTask) bool { return t.uuid == tk.UUID }); t != nil {
		t.state = "completed"
	}
	return nil
}

func (m *memQueue) Release(_ context.Context, _ string, tk Ticket) (Ticket, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(func(t *memTask) bool { return t.uuid == tk.UUID })
	if t == nil || t.state != stateWorking {
		return tk, false, nil
	}
	t.state, t.rev = statePending, t.rev+1
	return Ticket{UUID: t.uuid, Revision: t.rev}, true, nil
}

var testPub, testPriv, _ = ed25519.GenerateKey(rand.Reader)

type env struct {
	svc     *Service
	st      *store.Store
	url     string
	issuer  *authn.Issuer
	clients map[string]*api.WorkflowClient
}

// testDB creates a database for one test and drops it afterwards.
func testDB(t *testing.T) string {
	base := os.Getenv("MAGI_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("MAGI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := "wf_test_" + strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(newID("")))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	return u.String()
}

// backends returns the queues to test: memory always, Corndogs when
// MAGI_TEST_CORNDOGS_URL is set.
func backends(t *testing.T, run func(t *testing.T, q func(t *testing.T) Queue)) {
	t.Run("memory", func(t *testing.T) { run(t, func(*testing.T) Queue { return &memQueue{} }) })
	t.Run("corndogs", func(t *testing.T) {
		addr := os.Getenv("MAGI_TEST_CORNDOGS_URL")
		if addr == "" {
			t.Skip("MAGI_TEST_CORNDOGS_URL is not set")
		}
		run(t, func(t *testing.T) Queue {
			q, err := NewCorndogs(context.Background(), addr, "test."+newID("")+".")
			if err != nil {
				t.Fatal(err)
			}
			return q
		})
	})
}

func newEnv(t *testing.T, dbURL string, q Queue) *env {
	ctx := context.Background()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: testPriv}
	svc := &Service{Store: st, Queue: q, ClaimPoll: 50 * time.Millisecond, WatchPoll: 50 * time.Millisecond}
	rs := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": testPub}}}
	rs.Register("WorkflowService", api.WorkflowServiceOps(svc))
	hs := httptest.NewServer(rpc.Mux(rs, nil))
	t.Cleanup(hs.Close)
	return &env{svc: svc, st: st, url: hs.URL, issuer: iss, clients: map[string]*api.WorkflowClient{}}
}

// as returns a client for subject with scopes.
func (e *env) as(t *testing.T, subject string, scopes ...string) *api.WorkflowClient {
	kind := authn.KindUser
	if strings.HasPrefix(subject, "service:") {
		kind = authn.KindService
	}
	tok, err := e.issuer.Issue(authn.Principal{Subject: subject, Kind: kind, Scopes: scopes}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return api.NewWorkflowClient(rpc.NewClient(e.url, rpc.StaticToken(tok)))
}

func setup(t *testing.T, q Queue) (*env, *api.WorkflowClient, *api.WorkflowClient) {
	e := newEnv(t, testDB(t), q)
	return e, e.as(t, "user:alice@example.local"), e.as(t, "service:agent", ScopeWorker)
}

func code(t *testing.T, err error, want int) {
	t.Helper()
	if got := rpc.CodeOf(err); got != want {
		t.Fatalf("error code %d (%v), want %d", got, err, want)
	}
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func claim(t *testing.T, w *api.WorkflowClient, queue string, leaseMs uint64) *api.Assignment {
	t.Helper()
	r, err := w.Claim(context.Background(), api.ClaimRequest{Queue: queue, Worker: "w1", WaitMs: ptr(uint64(3000)), LeaseMs: ptr(leaseMs)})
	if err != nil || r.Assignment == nil {
		t.Fatalf("claim %s: %+v %v", queue, r, err)
	}
	return r.Assignment
}

func TestTaskHumanTask(t *testing.T) {
	backends(t, func(t *testing.T, q func(*testing.T) Queue) {
		ctx := context.Background()
		e, alice, w := setup(t, q(t))
		bob := e.as(t, "user:bob@example.local")
		admin := e.as(t, "user:root@example.local", ScopeAdmin)

		def := must[api.Definition](t)(alice.PutDefinition(ctx, testDef()))
		if def.Revision != 1 {
			t.Fatalf("revision %d", def.Revision)
		}
		_, err := bob.PutDefinition(ctx, testDef())
		code(t, err, 403) // alice owns the name
		bad := testDef()
		bad.Start = "nope"
		_, err = alice.PutDefinition(ctx, bad)
		code(t, err, 400)
		if got := must[api.Definition](t)(bob.GetDefinition(ctx, api.GetDefinitionRequest{Name: "change"})); got.Revision != 1 || len(got.Steps) != 3 {
			t.Fatalf("get-definition: %+v", got)
		}

		run := must[api.Run](t)(alice.Start(ctx, api.StartRequest{Definition: "change", Key: ptr("k1"), Vars: map[string]string{"repo": "magi"}}))
		again := must[api.Run](t)(alice.Start(ctx, api.StartRequest{Definition: "change", Key: ptr("k1")}))
		if again.Id != run.Id || run.Status != RunRunning || run.History[0].Status != StepPending {
			t.Fatalf("idempotent start: %s %s %+v", run.Id, again.Id, run)
		}
		_, err = bob.Start(ctx, api.StartRequest{Definition: "change", Owner: ptr("user:alice@example.local")})
		code(t, err, 403)
		other := must[api.Run](t)(e.as(t, "service:magi", ScopeActAs).Start(ctx, api.StartRequest{Definition: "change", Owner: ptr("user:carol@example.local")}))
		if other.Owner != "user:carol@example.local" {
			t.Fatalf("act-as owner: %s", other.Owner)
		}

		// A new revision does not change a running run.
		d2 := testDef()
		d2.Description = ptr("second")
		if must[api.Definition](t)(alice.PutDefinition(ctx, d2)).Revision != 2 {
			t.Fatal("revision 2 expected")
		}

		_, err = alice.Claim(ctx, api.ClaimRequest{Queue: "builds", Worker: "x"})
		code(t, err, 403) // needs workflow:worker
		a := claim(t, w, "builds", 60000)
		if a.Step != "build" || a.Attempt != 1 || a.Vars["repo"] != "magi" || a.Owner != "user:alice@example.local" {
			t.Fatalf("assignment: %+v", a)
		}
		must[api.Ack](t)(w.Heartbeat(ctx, api.HeartbeatRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, LeaseMs: ptr(uint64(60000))}))
		must[api.Ack](t)(w.Progress(ctx, api.ProgressRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Note: "compiling", Ref: ptr("log:1")}))
		_, err = w.Progress(ctx, api.ProgressRequest{Run: a.Run, Step: a.Step, Claim: "clm_wrong", Note: "x"})
		code(t, err, 409)

		// watch-run returns when the run changes.
		before := must[api.Run](t)(alice.GetRun(ctx, api.GetRunRequest{Id: a.Run}))
		if before.History[0].Status != StepClaimed || len(before.History[0].Progress) != 1 {
			t.Fatalf("claimed run: %+v", before.History[0])
		}
		start := time.Now()
		quiet := must[api.Run](t)(alice.WatchRun(ctx, api.WatchRunRequest{Id: a.Run, AfterMs: before.UpdatedMs, WaitMs: ptr(uint64(300))}))
		if quiet.UpdatedMs != before.UpdatedMs || time.Since(start) < 250*time.Millisecond {
			t.Fatalf("watch-run without a change returned early: %v", time.Since(start))
		}
		watched := make(chan api.Run, 1)
		go func() {
			r, _ := alice.WatchRun(ctx, api.WatchRunRequest{Id: a.Run, AfterMs: before.UpdatedMs, WaitMs: ptr(uint64(10000))})
			watched <- r
		}()
		time.Sleep(200 * time.Millisecond)
		r := must[api.Run](t)(w.Complete(ctx, api.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "ok", Outputs: map[string]string{"artifact": "art_1"}}))
		if r.Status != RunWaiting || r.CurrentStep != "review" || r.History[1].Status != StepWaiting {
			t.Fatalf("after build: %+v", r)
		}
		select {
		case got := <-watched:
			if got.UpdatedMs <= before.UpdatedMs || got.Status != RunWaiting {
				t.Fatalf("watch-run: %+v", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("watch-run did not return after a change")
		}
		_, err = w.Complete(ctx, api.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "ok"})
		code(t, err, 409) // the claim is finished

		// Decisions.
		_, err = bob.Decide(ctx, api.DecideRequest{Run: a.Run, Step: "review", Outcome: "approve"})
		code(t, err, 403)
		_, err = alice.Decide(ctx, api.DecideRequest{Run: a.Run, Step: "review", Outcome: "maybe"})
		code(t, err, 400)
		_, err = alice.Decide(ctx, api.DecideRequest{Run: a.Run, Step: "build", Outcome: "approve"})
		code(t, err, 409)
		decider := e.as(t, "user:dana@example.local", ScopeDecide)
		r = must[api.Run](t)(decider.Decide(ctx, api.DecideRequest{Run: a.Run, Step: "review", Outcome: "approve", Comment: ptr("looks good")}))
		if r.Status != RunRunning || r.CurrentStep != "deploy" || *r.History[1].DecidedBy != "user:dana@example.local" || r.Vars["review.comment"] != "looks good" {
			t.Fatalf("after decide: %+v", r)
		}

		d := claim(t, w, "deploys", 60000)
		r = must[api.Run](t)(w.Complete(ctx, api.CompleteRequest{Run: d.Run, Step: d.Step, Claim: d.Claim, Outcome: "ok"}))
		if r.Status != RunSucceeded || len(r.History) != 3 || r.Vars["artifact"] != "art_1" || r.Revision != 1 {
			t.Fatalf("final: %+v", r)
		}
		for i, want := range []string{"build", "review", "deploy"} {
			if r.History[i].Step != want || r.History[i].EndedMs == nil {
				t.Fatalf("history %d: %+v", i, r.History[i])
			}
		}

		// Read access and listing.
		_, err = bob.GetRun(ctx, api.GetRunRequest{Id: r.Id})
		code(t, err, 404)
		if n := len(must[api.ListRunsResponse](t)(alice.ListRuns(ctx, api.ListRunsRequest{})).Runs); n != 1 {
			t.Fatalf("alice runs: %d", n)
		}
		if n := len(must[api.ListRunsResponse](t)(bob.ListRuns(ctx, api.ListRunsRequest{})).Runs); n != 0 {
			t.Fatalf("bob runs: %d", n)
		}
		if n := len(must[api.ListRunsResponse](t)(admin.ListRuns(ctx, api.ListRunsRequest{})).Runs); n != 2 {
			t.Fatalf("admin runs: %d", n)
		}
		if n := len(must[api.ListRunsResponse](t)(admin.ListRuns(ctx, api.ListRunsRequest{Status: ptr(RunSucceeded)})).Runs); n != 1 {
			t.Fatalf("admin succeeded runs: %d", n)
		}

		// Cancel the other run; its queued task is closed.
		_, err = bob.Cancel(ctx, api.CancelRequest{Id: other.Id})
		code(t, err, 404)
		c := must[api.Run](t)(admin.Cancel(ctx, api.CancelRequest{Id: other.Id, Reason: ptr("test")}))
		if c.Status != RunCancelled || c.History[0].Status != StepCancelled {
			t.Fatalf("cancel: %+v", c)
		}
		if got := must[api.ClaimResponse](t)(w.Claim(ctx, api.ClaimRequest{Queue: "builds", Worker: "w1", WaitMs: ptr(uint64(300))})); got.Assignment != nil {
			t.Fatalf("claimed a cancelled task: %+v", got.Assignment)
		}
		if n := openTasks(t, e); n != 0 {
			t.Fatalf("%d open tasks", n)
		}
	})
}

func TestRetryAndFail(t *testing.T) {
	backends(t, func(t *testing.T, q func(*testing.T) Queue) {
		ctx := context.Background()
		_, alice, w := setup(t, q(t))
		must[api.Definition](t)(alice.PutDefinition(ctx, testDef()))
		run := must[api.Run](t)(alice.Start(ctx, api.StartRequest{Definition: "change"}))

		a := claim(t, w, "builds", 60000)
		r := must[api.Run](t)(w.Complete(ctx, api.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "retry"}))
		if r.Status != RunRunning || len(r.History) != 2 || r.History[0].Status != StepFailed || r.History[1].Attempt != 2 {
			t.Fatalf("after retry: %+v", r)
		}
		a = claim(t, w, "builds", 60000)
		if a.Attempt != 2 {
			t.Fatalf("attempt %d", a.Attempt)
		}
		r = must[api.Run](t)(w.Complete(ctx, api.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "retry"}))
		if r.Status != RunFailed || r.Error == nil {
			t.Fatalf("retry past max_attempts: %+v", r)
		}

		run = must[api.Run](t)(alice.Start(ctx, api.StartRequest{Definition: "change"}))
		a = claim(t, w, "builds", 60000)
		if a.Run != run.Id {
			t.Fatalf("claimed %s, want %s", a.Run, run.Id)
		}
		r = must[api.Run](t)(w.Complete(ctx, api.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "fail", Error: ptr("compile error")}))
		if r.Status != RunFailed || *r.Error != "step build failed: compile error" {
			t.Fatalf("fail: %+v", r)
		}
	})
}

func TestLeaseExpiry(t *testing.T) {
	backends(t, func(t *testing.T, q func(*testing.T) Queue) {
		ctx := context.Background()
		e, alice, w := setup(t, q(t))
		must[api.Definition](t)(alice.PutDefinition(ctx, testDef()))
		must[api.Run](t)(alice.Start(ctx, api.StartRequest{Definition: "change"}))

		a := claim(t, w, "builds", 1000)
		time.Sleep(1100 * time.Millisecond)
		_, err := w.Heartbeat(ctx, api.HeartbeatRequest{Run: a.Run, Step: a.Step, Claim: a.Claim})
		code(t, err, 409)
		e.svc.SweepOnce(ctx)
		r := must[api.Run](t)(alice.GetRun(ctx, api.GetRunRequest{Id: a.Run}))
		if len(r.History) != 2 || *r.History[0].Outcome != "expired" || r.History[1].Attempt != 2 || r.History[1].Status != StepPending {
			t.Fatalf("after expiry: %+v", r.History)
		}

		b := claim(t, w, "builds", 1000)
		if b.Attempt != 2 || b.Claim == a.Claim {
			t.Fatalf("second claim: %+v", b)
		}
		_, err = w.Complete(ctx, api.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "ok"})
		code(t, err, 409) // stale claim of attempt 1

		time.Sleep(1100 * time.Millisecond)
		e.svc.SweepOnce(ctx)
		r = must[api.Run](t)(alice.GetRun(ctx, api.GetRunRequest{Id: a.Run}))
		if r.Status != RunFailed || len(r.History) != 2 {
			t.Fatalf("after max_attempts expiries: %+v", r)
		}
		if n := openTasks(t, e); n != 0 {
			t.Fatalf("%d open tasks", n)
		}
		_, err = w.Complete(ctx, api.CompleteRequest{Run: b.Run, Step: b.Step, Claim: b.Claim, Outcome: "ok"})
		code(t, err, 409)
	})
}

// TestReplicas runs two services on one database and queue. Each task is
// claimed exactly once.
func TestReplicas(t *testing.T) {
	backends(t, func(t *testing.T, q func(*testing.T) Queue) {
		ctx := context.Background()
		dbURL := testDB(t)
		queue := q(t)
		replicas := []*env{newEnv(t, dbURL, queue), newEnv(t, dbURL, queue)}
		alice := replicas[0].as(t, "user:alice@example.local")
		def := api.Definition{Name: "one", Start: "s", Steps: []api.Step{{Id: "s", Kind: "task", Queue: ptr("q"), Next: map[string]string{}}}}
		must[api.Definition](t)(alice.PutDefinition(ctx, def))
		const runs = 12
		for i := range runs {
			must[api.Run](t)(alice.Start(ctx, api.StartRequest{Definition: "one", Key: ptr(fmt.Sprint(i))}))
		}
		var mu sync.Mutex
		seen := map[string]int{}
		var wg sync.WaitGroup
		for i := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w := replicas[i%2].as(t, "service:agent", ScopeWorker)
				for {
					r, err := w.Claim(ctx, api.ClaimRequest{Queue: "q", Worker: fmt.Sprint("w", i), WaitMs: ptr(uint64(500))})
					if err != nil {
						t.Error(err)
						return
					}
					a := r.Assignment
					if a == nil {
						return
					}
					mu.Lock()
					seen[a.Run]++
					mu.Unlock()
					if _, err := w.Complete(ctx, api.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "ok"}); err != nil {
						t.Error(err)
					}
				}
			}()
		}
		wg.Wait()
		if len(seen) != runs {
			t.Fatalf("claimed %d runs, want %d", len(seen), runs)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("run %s claimed %d times", id, n)
			}
		}
	})
}

func openTasks(t *testing.T, e *env) int {
	var n int
	if err := e.st.DB.QueryRow(context.Background(), `SELECT count(*) FROM workflow_tasks WHERE state <> 'done'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// flakyQueue fails the first submit, as a lost reply would.
type flakyQueue struct {
	Queue
	failed bool
}

func (f *flakyQueue) Submit(ctx context.Context, queue, key string) (Ticket, error) {
	if !f.failed {
		f.failed = true
		f.Queue.Submit(ctx, queue, key) // the task exists, but the reply is lost
		return Ticket{}, fmt.Errorf("lost reply")
	}
	return f.Queue.Submit(ctx, queue, key)
}

// TestRecovery covers a lost submit reply and a claim that reached the
// queue but not the database.
func TestRecovery(t *testing.T) {
	backends(t, func(t *testing.T, q func(*testing.T) Queue) {
		ctx := context.Background()
		oldSettle, oldStale := settleMs, staleMs
		settleMs, staleMs = 0, 0
		t.Cleanup(func() { settleMs, staleMs = oldSettle, oldStale })
		inner := q(t)
		e, alice, w := setup(t, &flakyQueue{Queue: inner})
		must[api.Definition](t)(alice.PutDefinition(ctx, testDef()))
		run := must[api.Run](t)(alice.Start(ctx, api.StartRequest{Definition: "change"}))
		e.svc.SweepOnce(ctx) // submit again with the same key: no second task

		// Lose a claim: take the task from the queue directly.
		if d, err := inner.Claim(ctx, "builds"); err != nil || d == nil || d.Key != run.Id+"/0" {
			t.Fatalf("direct claim: %+v %v", d, err)
		}
		e.svc.SweepOnce(ctx) // returns the held task to pending
		a := claim(t, w, "builds", 60000)
		if a.Run != run.Id || a.Attempt != 1 {
			t.Fatalf("recovered claim: %+v", a)
		}
		must[api.Run](t)(w.Complete(ctx, api.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: "ok"}))
		if got := must[api.ClaimResponse](t)(w.Claim(ctx, api.ClaimRequest{Queue: "builds", Worker: "w1", WaitMs: ptr(uint64(200))})); got.Assignment != nil {
			t.Fatalf("duplicate task: %+v", got.Assignment)
		}
		if n := openTasks(t, e); n != 0 {
			t.Fatalf("%d open tasks", n)
		}
	})
}
