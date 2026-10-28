package compose

import (
	"context"
	"fmt"
	"os"
	"time"

	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// Result is the outcome of one task step.
type Result struct {
	Outcome string
	Outputs map[string]string
}

// Handler does the work of one claimed task step.
type Handler func(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error)

const lease = 2 * time.Minute

// Work claims steps from queue until ctx ends.
func (s *System) Work(ctx context.Context, queue string, h Handler) {
	host, _ := os.Hostname()
	worker := fmt.Sprintf("magi/%s/%s", host, queue)
	for ctx.Err() == nil {
		sctx := s.AsService(ctx)
		resp, err := s.Workflow.Claim(sctx, workflowapi.ClaimRequest{
			Queue: queue, Worker: worker, WaitMs: ptr(uint64(20000)), LeaseMs: ptr(uint64(lease.Milliseconds())),
		})
		if err != nil {
			s.Log.Warn("claim failed", "queue", queue, "err", err)
			sleep(ctx, 3*time.Second)
			continue
		}
		if resp.Assignment == nil {
			continue
		}
		s.run(sctx, *resp.Assignment, h)
	}
}

func (s *System) run(ctx context.Context, a workflowapi.Assignment, h Handler) {
	log := s.Log.With("run", a.Run, "step", a.Step, "attempt", a.Attempt)
	hctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(lease / 3)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				if _, err := s.Workflow.Heartbeat(ctx, workflowapi.HeartbeatRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, LeaseMs: ptr(uint64(lease.Milliseconds()))}); err != nil {
					log.Warn("heartbeat failed", "err", err)
				}
			}
		}
	}()
	note := func(msg string) {
		if _, err := s.Workflow.Progress(ctx, workflowapi.ProgressRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Note: msg}); err != nil {
			log.Warn("progress failed", "err", err)
		}
	}
	log.Info("step started")
	res, err := h(hctx, a, note)
	if ctx.Err() != nil {
		// Shutting down: do not report a failure. The lease expires and
		// another worker, possibly the next version of Magi, resumes the step.
		log.Warn("stopped before the step finished; the lease will expire")
		return
	}
	req := workflowapi.CompleteRequest{Run: a.Run, Step: a.Step, Claim: a.Claim, Outcome: res.Outcome, Outputs: res.Outputs}
	if err != nil {
		log.Error("step failed", "err", err)
		req.Outcome, req.Error = "fail", ptr(err.Error())
	}
	if _, err := s.Workflow.Complete(ctx, req); err != nil {
		log.Error("complete failed", "err", err)
		return
	}
	log.Info("step finished", "outcome", req.Outcome)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
