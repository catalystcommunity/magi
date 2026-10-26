//go:build integration

package workflow

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	w "github.com/catalystcommunity/magi/generated/go"
	sw "github.com/catalystcommunity/magi/generated/workflows/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixtureAdapter struct {
	calls  atomic.Int64
	fail   bool
	caller w.ApplicationRef
}

func (a *fixtureAdapter) Call(ctx context.Context, r CallRequest) (w.WorkflowValues, w.RecordRef, error) {
	a.calls.Add(1)
	if r.Actor != a.caller || r.Definition.Owner != definitionOwner || r.Prompt == "" || r.Key == "" {
		return w.WorkflowValues{}, w.RecordRef{}, ErrDenied
	}
	if a.fail {
		return w.WorkflowValues{}, w.RecordRef{}, errors.New("synthetic lost reply")
	}
	return textValues("brief", "Draft for "+r.Inputs.Values[0].Value.(string)), w.RecordRef{Authority: a.caller.Identity, Kind: "fixture-result", RecordId: r.Key}, nil
}
func serviceFixture(t *testing.T, f *fixture) (*Store, *Engine, *fixtureAdapter) {
	t.Helper()
	j, _ := f.journal(t)
	s := &Store{Journal: j, Application: actor}
	must(t, s.Initialize(context.Background()))
	for _, action := range []string{"read", "write", "run", "execute", "complete"} {
		must(t, s.SetGrant(context.Background(), Grant{Actor: actor, Owner: definitionOwner, Action: action, Allowed: true}))
	}
	_, e := (&Sweeper{Store: s}).Tick(context.Background(), 1)
	must(t, e)
	adapter := &fixtureAdapter{caller: actor}
	return s, &Engine{Store: s, Adapters: map[string]CallAdapter{"fixture.draft": adapter}}, adapter
}
func startFixture(t *testing.T, s *Store, id string) w.StartWorkflow {
	t.Helper()
	ctx := context.Background()
	ref, e := s.Publish(ctx, actor, sampleDefinition(), []byte("local admitted publication"))
	must(t, e)
	r := w.StartWorkflow{RunId: id, Definition: ref, Inputs: textValues("subject", "Calendar planning"), Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}, Causes: []w.RecordRef{}}
	_, e = s.Start(ctx, actor, r, []byte("local admitted run"))
	must(t, e)
	return r
}
func observe(t *testing.T, s *Store, id string) w.WorkflowRun {
	t.Helper()
	v, e := s.ReadRun(context.Background(), actor, w.ReadWorkflowRun{Owner: definitionOwner, RunId: id})
	must(t, e)
	return v
}
func advanceTwice(t *testing.T, s *Store, id string) {
	t.Helper()
	must(t, s.Advance(context.Background(), definitionOwner, id))
	must(t, s.Advance(context.Background(), definitionOwner, id))
}
func TestIndependentWorkflowService(t *testing.T) {
	for _, mode := range []string{"file", "postgres"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode)
			ctx := context.Background()
			testWorkflowCoordination(t, f)
			testWorkflowSweeping(t, f)
			testCallWorkers(t, f)
			t.Run("publish-immutable-and-draft-cas", func(t *testing.T) {
				s, _, _ := serviceFixture(t, f)
				d := sampleDefinition()
				ref, e := s.Publish(ctx, actor, d, []byte("proof"))
				must(t, e)
				again, e := s.Publish(ctx, actor, d, []byte("new signature"))
				must(t, e)
				if !bytes.Equal(ref.Digest, again.Digest) {
					t.Fatal("digest changed")
				}
				d.Title = "Changed"
				if _, e = s.Publish(ctx, actor, d, nil); !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
				d.Revision = "v2"
				next, e := s.Publish(ctx, actor, d, nil)
				must(t, e)
				if bytes.Equal(ref.Digest, next.Digest) {
					t.Fatal("revision not pinned")
				}
				draft := w.SaveWorkflowDraft{Draft: w.WorkflowDraft{Definition: d, Positions: []w.WorkflowPosition{{StepId: "draft", X: 10, Y: 20}}}}
				first, e := s.SaveDraft(ctx, actor, draft)
				must(t, e)
				if first.Version != 1 {
					t.Fatal(first.Version)
				}
				replay, e := s.SaveDraft(ctx, actor, draft)
				must(t, e)
				if replay.Version != 1 {
					t.Fatal("retry creates version")
				}
				draft.Draft.Positions[0].X = 99
				if _, e = s.SaveDraft(ctx, actor, draft); !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
				draft.ExpectedVersion = 1
				draft.Draft.Version = 1
				_, e = s.SaveDraft(ctx, actor, draft)
				must(t, e)
				saved, e := s.ReadDefinition(ctx, actor, next)
				must(t, e)
				if !bytes.Equal(w.EncodeWorkflowDefinition(saved), w.EncodeWorkflowDefinition(d)) {
					t.Fatal("layout changes execution")
				}
			})
			t.Run("reusable-call-human-finish-without-longhouse", func(t *testing.T) {
				s, engine, adapter := serviceFixture(t, f)
				r := startFixture(t, s, "run-one")
				_, e := s.Start(ctx, actor, r, nil)
				must(t, e)
				changed := r
				changed.Inputs = textValues("subject", "different")
				if _, e = s.Start(ctx, actor, changed, nil); !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
				advanceTwice(t, s, r.RunId)
				must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
				advanceTwice(t, s, r.RunId)
				v := observe(t, s, r.RunId)
				human := v.Steps[1]
				if human.Phase != "waiting_human" {
					t.Fatal(v)
				}
				response := w.CompleteWorkflowHuman{Owner: definitionOwner, RunId: r.RunId, StepId: "review", TaskId: *human.TaskId, TaskRevision: *human.TaskRevision, Result: textValues("approved", "Approved brief"), Evidence: w.RecordRef{Authority: actor.Identity, Kind: "voice-response", RecordId: "response-one"}}
				_, e = s.CompleteHuman(ctx, actor, response)
				must(t, e)
				_, e = s.CompleteHuman(ctx, actor, response)
				must(t, e)
				bad := response
				bad.TaskRevision++
				if _, e = s.CompleteHuman(ctx, actor, bad); !errors.Is(e, ErrStale) {
					t.Fatal(e)
				}
				bad = response
				bad.Result = textValues("approved", "Changed")
				if _, e = s.CompleteHuman(ctx, actor, bad); !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
				for range 4 {
					must(t, s.Advance(ctx, definitionOwner, r.RunId))
				}
				v = observe(t, s, r.RunId)
				if v.Result == nil || v.Result.Values[0].Value != "Approved brief" || adapter.calls.Load() != 1 {
					t.Fatal(v, adapter.calls.Load())
				}
				_, e = s.CompleteHuman(ctx, actor, response)
				must(t, e)
				r.RunId = "run-two"
				r.Inputs = textValues("subject", "Another meeting")
				_, e = s.Start(ctx, actor, r, nil)
				must(t, e)
				advanceTwice(t, s, r.RunId)
				must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
				if adapter.calls.Load() != 2 {
					t.Fatal("runs share an attempt")
				}
			})
			t.Run("concurrent-coordinators-dispatch-once", func(t *testing.T) {
				s, engine, adapter := serviceFixture(t, f)
				r := startFixture(t, s, "concurrent")
				advanceTwice(t, s, r.RunId)
				var wg sync.WaitGroup
				errs := make(chan error, 4)
				for range 4 {
					wg.Go(func() { errs <- engine.Execute(ctx, definitionOwner, r.RunId, "draft") })
				}
				wg.Wait()
				close(errs)
				for e := range errs {
					if e != nil && !errors.Is(e, ErrReconcile) {
						t.Fatal(e)
					}
				}
				if adapter.calls.Load() != 1 {
					t.Fatal("duplicate dispatch", adapter.calls.Load())
				}
			})
			t.Run("human-same-state-revision-change-is-stale", func(t *testing.T) {
				s, engine, _ := serviceFixture(t, f)
				r := startFixture(t, s, "human-aba")
				advanceTwice(t, s, r.RunId)
				must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
				advanceTwice(t, s, r.RunId)
				human := observe(t, s, r.RunId).Steps[1]
				state := "waiting_human"
				_, e := api.NewCorndogsClient(s.Journal.Transport).UpdateGuardedTask(ctx, api.UpdateGuardedTaskRequest{Queue: stepKey(definitionOwner, r.RunId, "review"), Uuid: *human.TaskId, OperationId: api.NewOperationID(), ExpectedRevision: *human.TaskRevision, ExpectedState: &state, NewState: state, AutoTargetState: state, Timeout: -1})
				must(t, e)
				_, e = s.CompleteHuman(ctx, actor, w.CompleteWorkflowHuman{Owner: definitionOwner, RunId: r.RunId, StepId: "review", TaskId: *human.TaskId, TaskRevision: *human.TaskRevision, Result: textValues("approved", "Old answer"), Evidence: w.RecordRef{Authority: actor.Identity, Kind: "response", RecordId: "stale"}})
				if !errors.Is(e, ErrStale) {
					t.Fatal(e)
				}
			})
			t.Run("unknown-effect-never-blindly-retried", func(t *testing.T) {
				s, engine, adapter := serviceFixture(t, f)
				adapter.fail = true
				r := startFixture(t, s, "unknown")
				advanceTwice(t, s, r.RunId)
				if e := engine.Execute(ctx, definitionOwner, r.RunId, "draft"); !errors.Is(e, ErrReconcile) {
					t.Fatal(e)
				}
				restarted := &Engine{Store: &Store{Journal: &Journal{Pool: s.Journal.Pool, Installation: s.Journal.Installation, Transport: s.Journal.Transport}, Application: s.Application}, Adapters: engine.Adapters}
				if e := restarted.Execute(ctx, definitionOwner, r.RunId, "draft"); !errors.Is(e, ErrReconcile) {
					t.Fatal(e)
				}
				if adapter.calls.Load() != 1 || observe(t, s, r.RunId).Steps[0].Phase != "outcome_unknown" {
					t.Fatal("effect repeated")
				}
			})
			t.Run("process-crash-after-effect-before-result", func(t *testing.T) {
				j, dsn := f.journal(t)
				s := &Store{Journal: j, Application: actor}
				must(t, s.Initialize(ctx))
				for _, action := range []string{"read", "write", "run", "execute"} {
					must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: action, Allowed: true}))
				}
				_, e := j.Pool.Exec(ctx, `CREATE TABLE fixture_external_effects (operation text PRIMARY KEY)`)
				must(t, e)
				r := startFixture(t, s, "process-crash")
				advanceTwice(t, s, r.RunId)
				child := exec.Command(os.Args[0], "-test.run=^TestWorkflowRuntimeCrashChild$")
				child.Env = append(os.Environ(), "OPAL_WORKFLOW_RUNTIME_CRASH_DSN="+dsn, "OPAL_WORKFLOW_RUNTIME_CRASH_SEED="+f.seeds[0])
				output, e := child.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(e, &exit) || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatalf("child did not crash at the effect boundary: %v %s", e, output)
				}
				adapter := &fixtureAdapter{caller: actor}
				engine := &Engine{Store: s, Adapters: map[string]CallAdapter{"fixture.draft": adapter}}
				if e = engine.Execute(ctx, definitionOwner, r.RunId, "draft"); !errors.Is(e, ErrReconcile) {
					t.Fatal(e)
				}
				var count int
				must(t, j.Pool.QueryRow(ctx, `SELECT count(*) FROM fixture_external_effects`).Scan(&count))
				if count != 1 || adapter.calls.Load() != 0 {
					t.Fatal("unknown effect was repeated", count, adapter.calls.Load())
				}
			})
			t.Run("lost-completion-reply-recovery", func(t *testing.T) {
				s, engine, adapter := serviceFixture(t, f)
				r := startFixture(t, s, "lost")
				advanceTwice(t, s, r.RunId)
				must(t, engine.Execute(ctx, definitionOwner, r.RunId, "draft"))
				s.Journal.Transport = &faultTransport{base: s.Journal.Transport, method: "CompleteGuardedTask"}
				if e := s.Advance(ctx, definitionOwner, r.RunId); !errors.Is(e, api.ErrOutcomeUncertain) {
					t.Fatal(e)
				}
				advanceTwice(t, s, r.RunId)
				if observe(t, s, r.RunId).Steps[1].Phase != "waiting_human" || adapter.calls.Load() != 1 {
					t.Fatal("did not recover original effect")
				}
			})
			t.Run("revocation-and-uninterpreted-source-policy-deny", func(t *testing.T) {
				s, engine, adapter := serviceFixture(t, f)
				r := startFixture(t, s, "policy")
				advanceTwice(t, s, r.RunId)
				must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: "execute", Allowed: false}))
				if e := engine.Execute(ctx, definitionOwner, r.RunId, "draft"); !errors.Is(e, ErrDenied) {
					t.Fatal(e)
				}
				if adapter.calls.Load() != 0 {
					t.Fatal("dispatch after withdrawal")
				}
				must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: definitionOwner, Action: "read", Allowed: false}))
				if _, e := s.ReadRun(ctx, actor, w.ReadWorkflowRun{Owner: definitionOwner, RunId: r.RunId}); !errors.Is(e, ErrDenied) {
					t.Fatal(e)
				}
				r.RunId = "restricted"
				r.Restrictions.Requirements = []w.SourceRestriction{{Owner: definitionOwner}}
				if _, e := s.Start(ctx, actor, r, nil); !errors.Is(e, ErrInvalid) {
					t.Fatal("ignored source restrictions", e)
				}
			})
			t.Run("standalone-generated-client-authenticated-http", func(t *testing.T) {
				s, _, _ := serviceFixture(t, f)
				public, private, e := ed25519.GenerateKey(rand.Reader)
				must(t, e)
				foreign := actor
				foreign.Identity.Domain = "partner.local"
				foreign.ApplicationId = "external-workflows-client"
				for _, action := range []string{"write", "read", "run", "complete"} {
					must(t, s.SetGrant(ctx, Grant{Actor: foreign, Owner: definitionOwner, Action: action, Allowed: true}))
				}
				service := &Service{Store: s, Keys: workflowTestKeys{public, foreign}}
				httpServer := httptest.NewUnstartedServer(nil)
				origin := "http://" + httpServer.Listener.Addr().String()
				handler, e := p.NewRPCHandler(origin, service.Dispatch, s.Check)
				must(t, e)
				httpServer.Config.Handler = handler
				httpServer.Start()
				defer httpServer.Close()
				carrier, e := NewHTTPTransport(httpServer.URL + "/csil/v1/rpc")
				must(t, e)
				client := sw.NewWorkflowsClient(carrier)
				sign := func(op string, body []byte) sw.SignedRecord {
					packet, e := Sign(op, body, foreign, s.Application, "test-key", private, time.Now().UTC().Truncate(time.Millisecond))
					must(t, e)
					out, e := sw.DecodeSignedRecord(w.EncodeSignedRecord(packet))
					must(t, e)
					return out
				}
				validation, e := client.Validate(ctx, sign("validate", w.EncodeWorkflowDefinition(sampleDefinition())))
				must(t, e)
				if !validation.Valid {
					t.Fatal(validation)
				}
				ref, e := client.Publish(ctx, sign("publish", w.EncodeWorkflowDefinition(sampleDefinition())))
				must(t, e)
				saved, e := client.ReadDefinition(ctx, sign("read-definition", sw.EncodeReadWorkflowDefinition(sw.ReadWorkflowDefinition{Definition: ref})))
				must(t, e)
				if saved.Id != "meeting-brief" {
					t.Fatal(saved)
				}
				_, e = client.Publish(ctx, sign("validate", w.EncodeWorkflowDefinition(sampleDefinition())))
				if e == nil {
					t.Fatal("operation substitution accepted")
				}
				if _, e = NewHTTPTransport("http://untrusted.example/csil/v1/rpc"); e == nil {
					t.Fatal("plaintext remote accepted")
				}
				parsed, e := url.Parse(httpServer.URL)
				must(t, e)
				if parsed.Host == "" {
					t.Fatal("missing host")
				}
				must(t, s.SetGrant(ctx, Grant{Actor: foreign, Owner: definitionOwner, Action: "read", Allowed: false}))
				if _, e = client.ReadDefinition(ctx, sign("read-definition", sw.EncodeReadWorkflowDefinition(sw.ReadWorkflowDefinition{Definition: ref}))); e == nil {
					t.Fatal("read revocation ignored")
				}
			})
		})
	}
}

type crashAfterEffect struct{ pool *pgxpool.Pool }

func (a crashAfterEffect) Call(ctx context.Context, r CallRequest) (w.WorkflowValues, w.RecordRef, error) {
	if _, e := a.pool.Exec(ctx, `INSERT INTO fixture_external_effects VALUES($1)`, r.Key); e != nil {
		return w.WorkflowValues{}, w.RecordRef{}, e
	}
	syscall.Kill(os.Getpid(), syscall.SIGKILL)
	return w.WorkflowValues{}, w.RecordRef{}, ErrReconcile
}
func TestWorkflowRuntimeCrashChild(t *testing.T) {
	dsn := os.Getenv("OPAL_WORKFLOW_RUNTIME_CRASH_DSN")
	if dsn == "" {
		return
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, dsn)
	must(t, e)
	defer pool.Close()
	s := &Store{Application: actor, Journal: &Journal{Pool: pool, Installation: "fixture", Transport: &Transport{Seeds: []string{os.Getenv("OPAL_WORKFLOW_RUNTIME_CRASH_SEED")}}}}
	_, e = (&Sweeper{Store: s}).Tick(ctx, 1)
	must(t, e)
	engine := &Engine{Store: s, Adapters: map[string]CallAdapter{"fixture.draft": crashAfterEffect{pool}}}
	must(t, engine.Execute(ctx, definitionOwner, "process-crash", "draft"))
	t.Fatal("crash hook did not run")
}
