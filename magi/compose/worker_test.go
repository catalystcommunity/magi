package compose

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// lostClaimWorkflow says that the claim is not current, and counts the
// reports of the step.
type lostClaimWorkflow struct {
	workflowapi.WorkflowService
	completes atomic.Int32
}

func (w *lostClaimWorkflow) Heartbeat(context.Context, workflowapi.HeartbeatRequest) (workflowapi.Ack, error) {
	return workflowapi.Ack{}, rpc.Conflict("claim is not current")
}

func (w *lostClaimWorkflow) Complete(context.Context, workflowapi.CompleteRequest) (workflowapi.Run, error) {
	w.completes.Add(1)
	return workflowapi.Run{}, nil
}

// A worker that loses its claim stops the work and reports nothing: a
// different worker has the step (docs/decisions/0004).
func TestWorkerStopsWhenTheClaimIsLost(t *testing.T) {
	old := heartbeatEvery
	heartbeatEvery = 10 * time.Millisecond
	defer func() { heartbeatEvery = old }()
	wf := &lostClaimWorkflow{}
	sys, _, _ := adminEnv(t, Config{}, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"WorkflowService": safeOps(workflowapi.WorkflowServiceOps(wf)),
	})
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		sys.run(sys.AsService(context.Background()), workflowapi.Assignment{Run: "run_1", Step: "reply", Claim: "c1"},
			func(ctx context.Context, _ workflowapi.Assignment, _ func(string)) (Result, error) {
				select {
				case <-ctx.Done():
					close(stopped)
					return Result{}, ctx.Err()
				case <-time.After(10 * time.Second):
					return Result{Outcome: "done"}, nil
				}
			})
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not stop after the claim was lost")
	}
	<-done
	if n := wf.completes.Load(); n != 0 {
		t.Fatalf("the worker reported the step %d times after it lost the claim", n)
	}
}
