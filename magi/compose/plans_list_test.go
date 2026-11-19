package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// flippingWorkflow has one to-do run that changes its status after the
// first ListRuns call, as a run does when its step starts or ends while
// the list is read one status at a time.
type flippingWorkflow struct {
	workflowapi.WorkflowService
	mu       sync.Mutex
	from, to string
	calls    []string
}

func (f *flippingWorkflow) ListRuns(_ context.Context, r workflowapi.ListRunsRequest) (workflowapi.ListRunsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := f.from
	if len(f.calls) > 0 {
		status = f.to
	}
	f.calls = append(f.calls, deref(r.Status))
	out := workflowapi.ListRunsResponse{Runs: []workflowapi.Run{}}
	if deref(r.Status) == status {
		out.Runs = append(out.Runs, workflowapi.Run{Id: "run_1", Definition: Todo, Owner: "user:a", Status: status,
			CreatedMs: 1, UpdatedMs: 2})
	}
	return out, nil
}

func flippingSystem(t *testing.T, f *flippingWorkflow) *System {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	tok, err := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("WorkflowService", workflowapi.WorkflowServiceOps(f))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	return &System{Workflow: workflowapi.NewWorkflowClient(rpc.NewClient(h.URL, rpc.StaticToken(tok)))}
}

// A to-do that starts or waits while the list is read is listed one time.
func TestTodosListARunThatChangesStatusOnce(t *testing.T) {
	for _, c := range []struct{ from, to string }{{"running", "waiting"}, {"waiting", "running"}} {
		for _, ended := range []bool{false, true} {
			f := &flippingWorkflow{from: c.from, to: c.to}
			runs, err := flippingSystem(t, f).todos(context.Background(), "user:a", ended)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 || runs[0].Id != "run_1" {
				t.Errorf("%s -> %s (ended %v): got %d runs, queries %v", c.from, c.to, ended, len(runs), f.calls)
			}
		}
	}
}
