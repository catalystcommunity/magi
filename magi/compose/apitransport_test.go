package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// These tests send the calls through the HTTP transport with the generated
// clients. A direct handler call with a hand-built principal cannot show
// that the token kind and the scopes survive the token and the transport.

// historyKnowledge records the history request and returns one change.
type historyKnowledge struct {
	promoKnowledge
}

func (f *historyKnowledge) History(_ context.Context, r knowledgeapi.HistoryRequest) (knowledgeapi.HistoryResponse, error) {
	f.log.record("knowledge.History", r)
	return knowledgeapi.HistoryResponse{Changes: []knowledgeapi.Change{{Id: "chg_7", Record: "clm_9", Operation: "assert", Actor: injectionUser, AtMs: 5}}}, nil
}

// claimWorkflow records the queue of a claim and offers no work.
type claimWorkflow struct {
	workflowapi.WorkflowService
	log *sinkLog
}

func (f *claimWorkflow) Claim(_ context.Context, r workflowapi.ClaimRequest) (workflowapi.ClaimResponse, error) {
	f.log.record("workflow.Claim", r)
	return workflowapi.ClaimResponse{}, nil
}

// transportTokens serves the services of s behind one rpc.Mux and returns
// the URL and a function that issues real tokens for it.
func transportTokens(t *testing.T, s *System, magi bool, runners bool) (string, func(sub, kind string, scopes ...string) context.Context) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	srv := &rpc.Server{Verifier: ver, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if magi {
		srv.Register("MagiService", magiapi.MagiServiceOps(&Service{S: s}))
	}
	if runners {
		srv.Register("RunnerService", magiapi.RunnerServiceOps(&Runners{S: s}))
	}
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	return h.URL, func(sub, kind string, scopes ...string) context.Context {
		tok, err := iss.Issue(authn.Principal{Subject: sub, Kind: kind, Scopes: scopes}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return rpc.WithToken(context.Background(), tok)
	}
}

// Undo and promote work for the person's own token and are refused for a
// service token, even one with every scope.
func TestUndoAndPromoteOverHTTP(t *testing.T) {
	log := &sinkLog{}
	s := promotionSystem(t, log)
	url, tok := transportTokens(t, s, true, false)
	mc := magiapi.NewMagiClient(rpc.NewClient(url, nil))
	person := tok(injectionUser, authn.KindUser)
	service := tok("service:other", authn.KindService, "*")

	undo := magiapi.ChangeKnowledgeRequest{Id: "chg_1", Operation: "undo", Reason: ptr("wrong")}
	if _, err := mc.ChangeKnowledge(service, undo); rpc.CodeOf(err) != 403 || len(undone(log)) != 0 {
		t.Fatalf("a service undid: %v", err)
	}
	if _, err := mc.ChangeKnowledge(person, undo); err != nil {
		t.Fatal(err)
	}
	hits := undone(log)
	if len(hits) != 1 || !strings.Contains(hits[0].body, `"chg_1"`) || originOf(t, hits[0])["by"] != "api" {
		t.Fatalf("the person's undo: %+v", hits)
	}

	promote := magiapi.ChangeKnowledgeRequest{Id: "clm_9", Operation: "promote"}
	if _, err := mc.ChangeKnowledge(service, promote); rpc.CodeOf(err) != 403 || len(promoted(log)) != 0 {
		t.Fatalf("a service promoted: %v", err)
	}
	if _, err := mc.ChangeKnowledge(person, promote); err != nil {
		t.Fatal(err)
	}
	if hits := promoted(log); len(hits) != 1 || !strings.Contains(hits[0].body, `"clm_9"`) {
		t.Fatalf("the person's promote: %+v", hits)
	}
}

// A history call with no id asks for the audit log of the caller.
func TestKnowledgeHistoryEmptyIDOverHTTP(t *testing.T) {
	log := &sinkLog{}
	kc, ktok := serveFake(t, "KnowledgeService", knowledgeapi.KnowledgeServiceOps(&historyKnowledge{promoKnowledge{injKnowledge{log: log}}}))
	s := &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Cfg: Config{ServiceToken: ktok, Repo: "magi"}, Knowledge: knowledgeapi.NewKnowledgeClient(kc)}
	url, tok := transportTokens(t, s, true, false)
	mc := magiapi.NewMagiClient(rpc.NewClient(url, nil))

	res, err := mc.KnowledgeHistory(tok(injectionUser, authn.KindUser), magiapi.KnowledgeHistoryRequest{Limit: ptr(uint64(10))})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 1 || res.Changes[0].Id != "chg_7" || res.Changes[0].Operation != "assert" {
		t.Fatalf("changes: %+v", res.Changes)
	}
	hits := log.reached("knowledge.History", "")
	if len(hits) != 1 || !strings.Contains(hits[0].body, `"id":""`) || !strings.Contains(hits[0].body, `"act_for":"`+injectionUser+`"`) || !strings.Contains(hits[0].body, `"limit":10`) {
		t.Fatalf("the history request: %+v", hits)
	}
}

// A runner that is not approved gets 403 for publish steps and release
// artifacts through the real transport. An approved runner reaches the workflow.
func TestPublishGateOverHTTP(t *testing.T) {
	log := &sinkLog{}
	wc, wtok := serveFake(t, "WorkflowService", workflowapi.WorkflowServiceOps(&claimWorkflow{log: log}))
	s := &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Cfg: Config{ServiceToken: wtok, PublishRunners: []string{"eval"}},
		Workflow: workflowapi.NewWorkflowClient(wc)}
	url, tok := transportTokens(t, s, false, true)
	rc := magiapi.NewRunnerClient(rpc.NewClient(url, nil))
	bad := tok("runner:hostnet", authn.KindService, ScopeRunner)

	for _, step := range publishSteps {
		if _, err := rc.Claim(bad, magiapi.RunnerClaimRequest{Queue: RunnerQueue("hostnet", step)}); rpc.CodeOf(err) != 403 {
			t.Errorf("claim %s: %v", step, err)
		}
		if _, err := rc.Complete(bad, magiapi.RunnerCompleteRequest{Step: magiapi.RunnerStep{Run: "run_1", Step: step, Claim: "c"}, Outcome: "done"}); rpc.CodeOf(err) != 403 {
			t.Errorf("complete %s: %v", step, err)
		}
	}
	for _, kind := range releaseArtifactKinds {
		if _, err := rc.PutArtifact(bad, magiapi.RunnerPutArtifactRequest{Labels: map[string]string{"kind": kind}}); rpc.CodeOf(err) != 403 {
			t.Errorf("put %s: %v", kind, err)
		}
	}
	if n := len(log.reached("workflow.Claim", "")); n != 0 {
		t.Fatalf("a refused claim reached the workflow: %d", n)
	}

	// The approved runner passes the gate. Its Complete and PutArtifact need
	// a held claim and an artifact store, so only Claim is covered here.
	good := tok("runner:eval", authn.KindService, ScopeRunner)
	q := RunnerQueue("eval", publishSteps[0])
	if _, err := rc.Claim(good, magiapi.RunnerClaimRequest{Queue: q, WaitMs: ptr(uint64(1))}); err != nil {
		t.Fatal(err)
	}
	if hits := log.reached("workflow.Claim", q); len(hits) != 1 {
		t.Fatalf("the approved claim: %+v", log.hits)
	}
}
