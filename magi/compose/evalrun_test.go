package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// evalWorkflow serves one run that waits for a decision, and records the
// decisions that Magi applies to it.
type evalWorkflow struct {
	workflowapi.WorkflowService
	run workflowapi.Run
	dec []workflowapi.DecideRequest
}

func (f *evalWorkflow) GetRun(_ context.Context, r workflowapi.GetRunRequest) (workflowapi.Run, error) {
	if r.Id != f.run.Id {
		return workflowapi.Run{}, rpc.NotFound("no run %s", r.Id)
	}
	return f.run, nil
}

func (f *evalWorkflow) Decide(_ context.Context, r workflowapi.DecideRequest) (workflowapi.Run, error) {
	f.dec = append(f.dec, r)
	return f.run, nil
}

// evalRunSystem starts a System with a MagiService on the real HTTP
// transport. The workflow service serves the given run. The returned issuer
// signs the tokens of the callers.
func evalRunSystem(t *testing.T, run workflowapi.Run) (*magiapi.MagiClient, *authn.Issuer, *evalWorkflow) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	tok, err := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService,
		Scopes: []string{"workflow:admin"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wf := &evalWorkflow{run: run}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("WorkflowService", workflowapi.WorkflowServiceOps(wf))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	s := &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cfg:      Config{ServiceToken: tok, Repo: "magi"},
		Workflow: workflowapi.NewWorkflowClient(rpc.NewClient(h.URL, rpc.StaticToken(tok)))}
	magi := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}},
		Log: s.Log}
	magi.Register("MagiService", magiapi.MagiServiceOps(&Service{S: s}))
	mh := httptest.NewServer(rpc.Mux(magi, nil))
	t.Cleanup(mh.Close)
	return magiapi.NewMagiClient(rpc.NewClient(mh.URL, nil)), iss, wf
}

// evalRunCtx is the context of the operator that decides.
func evalRunCtx(t *testing.T, iss *authn.Issuer, sub string) context.Context {
	t.Helper()
	tok, err := iss.Issue(authn.Principal{Subject: sub, Kind: authn.KindUser, Scopes: []string{ScopeOperate}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return rpc.WithToken(context.Background(), tok)
}

// An eval run ends at review: the decision approve is refused, revise and
// reject still work.
func TestEvalRunDecision(t *testing.T) {
	run := workflowapi.Run{Id: "run_1", Definition: CodeChange, Owner: "user:a@example.local",
		Status: "waiting", CurrentStep: "review", WaitingFor: []string{"decision"},
		Vars: map[string]string{"repo": "magi", "base": "0123456789abcdef"}}
	mc, iss, wf := evalRunSystem(t, run)
	ctx := evalRunCtx(t, iss, "user:op@example.local")
	_, err := mc.Decide(ctx, magiapi.DecideRequest{Task: "run_1", Outcome: "approve"})
	var e *rpc.Error
	if !errors.As(err, &e) || e.Code != 400 ||
		e.Message != "an eval run (base 0123456789abcdef) ends at review; reject it after the grade" {
		t.Fatalf("approve: %v, want 400 with the eval message", err)
	}
	for _, d := range []string{"revise", "reject"} {
		if _, err := mc.Decide(ctx, magiapi.DecideRequest{Task: "run_1", Outcome: d}); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	if len(wf.dec) != 2 || wf.dec[0].Outcome != "revise" || wf.dec[1].Outcome != "reject" {
		t.Fatalf("the workflow did not get revise and reject: %+v", wf.dec)
	}
	// A run without a base merges as before.
	run.Vars = map[string]string{"repo": "magi"}
	mc, iss2, wf2 := evalRunSystem(t, run)
	ctx2 := evalRunCtx(t, iss2, "user:op@example.local")
	if _, err := mc.Decide(ctx2, magiapi.DecideRequest{Task: "run_1", Outcome: "approve"}); err != nil {
		t.Fatalf("approve without a base: %v", err)
	}
	if len(wf2.dec) != 1 || wf2.dec[0].Outcome != "approve" {
		t.Fatalf("the workflow did not get the approve: %+v", wf2.dec)
	}
}
