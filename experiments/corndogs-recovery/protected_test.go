package recovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

func keyedRequest(q string) api.SubmitKeyedTaskRequest {
	return api.SubmitKeyedTaskRequest{SubmissionKey: api.NewSubmissionKey(), Guarded: true,
		Queue: q, CurrentState: "ready", AutoTargetState: "working", Timeout: -1,
		Payload: []byte("payload:" + q), Priority: 7}
}

func keyed(t *testing.T, c *api.CorndogsClient, req api.SubmitKeyedTaskRequest) api.SubmitKeyedTaskResponse {
	t.Helper()
	r, err := c.SubmitKeyedTask(callContext(t), req)
	if err != nil || r.Task == nil || !r.Task.Guarded || r.Receipt.TaskUuid != r.Task.Task.Uuid {
		t.Fatalf("keyed submit: %+v %v", r, err)
	}
	return r
}

func guardedState(t *testing.T, c *api.CorndogsClient, task api.GuardedTask) api.GuardedTask {
	t.Helper()
	r, err := c.GetGuardedTask(callContext(t), api.GetGuardedTaskRequest{Uuid: task.Task.Uuid, Queue: task.Task.Queue})
	if err != nil || r.Task == nil {
		t.Fatalf("guarded lookup: %+v %v", r, err)
	}
	return *r.Task
}

func guardedClaimRequest(q, working string) api.ClaimGuardedTaskRequest {
	return api.ClaimGuardedTaskRequest{OperationId: api.NewOperationID(), Queue: q, CurrentState: "ready",
		OverrideCurrentState: working, OverrideAutoTargetState: "ready", OverrideTimeout: 2}
}

func guardedClaim(t *testing.T, c *api.CorndogsClient, req api.ClaimGuardedTaskRequest) api.GuardedTask {
	t.Helper()
	r, err := c.ClaimGuardedTask(callContext(t), req)
	if err != nil || r.Delivery == nil || !bytes.Equal(r.Delivery.Payload, []byte("payload:"+req.Queue)) {
		t.Fatalf("guarded claim: %+v %v", r, err)
	}
	return r.Delivery.Task
}

func code(t *testing.T, err error, want uint64) {
	t.Helper()
	got, ok := api.ServiceErrorCode(err)
	if !ok || got != want {
		t.Fatalf("service error: got %v, want code %d", err, want)
	}
}

func (f *fixture) address(t *testing.T) string {
	t.Helper()
	if f.cluster {
		return f.leader(t).rpc
	}
	for _, n := range f.nodes {
		if n.running {
			return n.rpc
		}
	}
	t.Fatal("no running node")
	return ""
}

func (f *fixture) clients(t *testing.T, count int) []*api.CorndogsClient {
	t.Helper()
	result := make([]*api.CorndogsClient, count)
	for i := range result {
		if !f.cluster {
			result[i] = client(t, f.nodes[i%len(f.nodes)].rpc)
			continue
		}
		seeds := []string{}
		for _, n := range f.nodes {
			seeds = append(seeds, n.rpc)
		}
		tr := &api.ClusterTransport{Seeds: seeds}
		t.Cleanup(func() { _ = tr.Close() })
		result[i] = api.NewCorndogsClient(tr)
	}
	return result
}

func TestProtectedRecovery(t *testing.T) {
	if os.Getenv("OPAL_RECOVERY_DOCKER") != "1" {
		t.Skip("set OPAL_RECOVERY_DOCKER=1")
	}
	for _, cfg := range []struct {
		name, mode, sync string
		count            int
	}{
		{"file_group", "file", "group", 1}, {"file_always", "file", "always", 1},
		{"postgres_single", "postgres", "group", 1}, {"postgres_two", "postgres", "group", 2},
		{"file_cluster_three", "file", "group", 3},
	} {
		t.Run(cfg.name, func(t *testing.T) {
			f := newFixture(t, cfg.mode, cfg.count, cfg.sync, true)
			t.Run("required_policies", f.requiredPolicies)
			t.Run("key_conflict_and_concurrent_replay", f.keyReplay)
			t.Run("lost_submit_stream", func(t *testing.T) { f.keyLostReply(t, false, false) })
			t.Run("lost_submit_cluster_transport", func(t *testing.T) { f.keyLostReply(t, true, false) })
			t.Run("lost_submit_fresh_client", func(t *testing.T) { f.keyLostReply(t, false, true) })
			t.Run("lost_claim_reply", f.claimLostReply)
			t.Run("lost_group_claim_reply", f.groupClaimLostReply)
			t.Run("stale_worker_different_state", func(t *testing.T) { f.guardedStale(t, false) })
			t.Run("stale_worker_same_state_ABA", func(t *testing.T) { f.guardedStale(t, true) })
			t.Run("mutation_receipts_and_human_wait", f.mutationReceipts)
			t.Run("concurrent_claims", f.guardedConcurrent)
			t.Run("full_restart_tasks_and_receipts", f.guardedRestart)
			if cfg.count > 1 {
				t.Run("failover_tasks_and_receipts", f.guardedFailover)
			}
			if f.cluster {
				t.Run("minority_write_and_reconciliation", f.guardedQuorum)
			}
		})
	}
}

func (f *fixture) requiredPolicies(t *testing.T) {
	c := f.writer(t)
	info, err := c.RequireFeatures(callContext(t), api.FeatureSubmissionKeys, api.FeatureTaskGuards)
	if err != nil || info.SubmissionKeyPolicy != api.PolicyRequired || info.TaskGuardPolicy != api.PolicyRequired || info.ReceiptRetentionSeconds != 3600 {
		t.Fatalf("server contract: %+v %v", info, err)
	}
	t.Logf("server contract: %+v", info)
	_, err = c.SubmitTask(callContext(t), api.SubmitTaskRequest{Queue: "policy", CurrentState: "ready"})
	code(t, err, api.CodeSubmissionKeyRequired)
	r := keyedRequest("policy")
	r.Guarded = false
	_, err = c.SubmitKeyedTask(callContext(t), r)
	code(t, err, api.CodeTaskGuardRequired)
	_, err = c.GetNextTask(callContext(t), api.GetNextTaskRequest{Queue: "policy", CurrentState: "ready"})
	code(t, err, api.CodeTaskGuardRequired)
	counts, err := c.GetTaskStateCounts(callContext(t), api.GetTaskStateCountsRequest{Queue: "policy"})
	if err != nil || counts.Count != 0 {
		t.Fatalf("rejected submissions changed storage: %+v %v", counts, err)
	}
}

func (f *fixture) keyReplay(t *testing.T) {
	req := keyedRequest("key-replay")
	clients := f.clients(t, 8)
	results := make(chan api.SubmitKeyedTaskResponse, len(clients))
	errs := make(chan error, len(clients))
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *api.CorndogsClient) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			r, err := c.SubmitKeyedTask(ctx, req)
			results <- r
			errs <- err
		}(c)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	first := 0
	for r := range results {
		if r.Task == nil {
			t.Fatal("missing task")
		}
		if id == "" {
			id = r.Receipt.TaskUuid
		}
		if r.Receipt.TaskUuid != id {
			t.Fatal("duplicate task for one key")
		}
		if !r.Replayed {
			first++
		}
	}
	if first != 1 {
		t.Fatalf("first acceptances=%d", first)
	}
	c := f.writer(t)
	changed := req
	changed.Payload = []byte("different")
	_, err := c.SubmitKeyedTask(callContext(t), changed)
	code(t, err, api.CodeSubmissionKeyConflict)
	counts, err := c.GetTaskStateCounts(callContext(t), api.GetTaskStateCountsRequest{Queue: req.Queue})
	if err != nil || counts.Count != 1 {
		t.Fatalf("count: %+v %v", counts, err)
	}
	r, err := c.LookupSubmission(callContext(t), api.LookupSubmissionRequest{Queue: req.Queue, SubmissionKey: req.SubmissionKey})
	if err != nil || r.Receipt == nil || r.Receipt.TaskUuid != id || r.Task == nil {
		t.Fatalf("lookup: %+v %v", r, err)
	}
	if r.Receipt.ExpiresAt-r.Receipt.AcceptedAt != int64(time.Hour) {
		t.Fatal("unexpected receipt window")
	}
	changed = req
	changed.Queue = "key-other-queue"
	if keyed(t, c, changed).Receipt.TaskUuid == id {
		t.Fatal("key namespace is not queue scoped")
	}
}

func (f *fixture) keyLostReply(t *testing.T, cluster, fresh bool) {
	req := keyedRequest(fmt.Sprintf("key-lost-%t-%t", cluster, fresh))
	proxy, finished := dropProxy(t, f.address(t))
	var c *api.CorndogsClient
	if cluster {
		tr := &api.ClusterTransport{Seeds: []string{proxy}}
		t.Cleanup(func() { _ = tr.Close() })
		c = api.NewCorndogsClient(tr)
	} else {
		tr := &api.StreamTransport{Addr: proxy, DialTimeout: time.Second}
		if fresh {
			tr.MaxAttempts = 1
		}
		t.Cleanup(func() { _ = tr.Close() })
		c = api.NewCorndogsClient(tr)
	}
	r, err := c.SubmitKeyedTask(callContext(t), req)
	lostResponse(t, finished)
	if fresh {
		if !errors.Is(err, api.ErrOutcomeUncertain) {
			t.Fatalf("expected uncertain outcome: %v", err)
		}
		// A new connection uses the same saved intent. This is not a disk journal test.
		c = f.writer(t)
		r = keyed(t, c, req)
	} else if err != nil {
		t.Fatal(err)
	}
	if !r.Replayed || r.Task == nil {
		t.Fatalf("expected replay: %+v", r)
	}
	c = f.writer(t)
	lookup, err := c.LookupSubmission(callContext(t), api.LookupSubmissionRequest{Queue: req.Queue, SubmissionKey: req.SubmissionKey})
	if err != nil || lookup.Receipt == nil || *lookup.Receipt != r.Receipt {
		t.Fatalf("lookup: %+v %v", lookup, err)
	}
	counts, err := c.GetTaskStateCounts(callContext(t), api.GetTaskStateCountsRequest{Queue: req.Queue})
	if err != nil || counts.Count != 1 {
		t.Fatalf("count: %+v %v", counts, err)
	}
}

func (f *fixture) claimLostReply(t *testing.T) {
	c := f.writer(t)
	req := keyedRequest("claim-lost")
	task := keyed(t, c, req)
	keyed(t, c, keyedRequest(req.Queue))
	proxy, finished := dropProxy(t, f.address(t))
	claimReq := guardedClaimRequest(req.Queue, "working")
	r, err := client(t, proxy).ClaimGuardedTask(callContext(t), claimReq)
	lostResponse(t, finished)
	if err != nil || r.Delivery == nil || !r.Replayed {
		t.Fatalf("claim replay: %+v %v", r, err)
	}
	repeat, err := c.ClaimGuardedTask(callContext(t), claimReq)
	if err != nil || repeat.Delivery == nil || repeat.Delivery.Task != r.Delivery.Task {
		t.Fatalf("claim repeat: %+v %v", repeat, err)
	}
	counts, err := c.GetTaskStateCounts(callContext(t), api.GetTaskStateCountsRequest{Queue: task.Task.Task.Queue})
	if err != nil || counts.Count != 2 || counts.StateCounts["ready"] != 1 || counts.StateCounts["working"] != 1 {
		t.Fatalf("lost claim consumed another task: %+v %v", counts, err)
	}
}

func (f *fixture) groupClaimLostReply(t *testing.T) {
	c := f.writer(t)
	task := keyed(t, c, keyedRequest("group-claim-lost"))
	proxy, finished := dropProxy(t, f.address(t))
	req := api.ClaimGuardedTaskGroupRequest{OperationId: api.NewOperationID(), Queues: []string{"empty-group", task.Task.Task.Queue}, CurrentState: "ready", OverrideCurrentState: "working", OverrideTimeout: -1}
	r, err := client(t, proxy).ClaimGuardedTaskGroup(callContext(t), req)
	lostResponse(t, finished)
	if err != nil || r.Delivery == nil || !r.Replayed || r.Delivery.Task.Task.Uuid != task.Receipt.TaskUuid {
		t.Fatalf("group replay: %+v %v", r, err)
	}
}

func (f *fixture) guardedStale(t *testing.T, same bool) {
	c := f.writer(t)
	req := keyedRequest(fmt.Sprintf("guarded-stale-%t", same))
	keyed(t, c, req)
	claimReq := guardedClaimRequest(req.Queue, "working-A")
	a := guardedClaim(t, c, claimReq)
	r, err := c.CleanUpTimedOut(callContext(t), api.CleanUpTimedOutRequest{Queue: req.Queue, AtTime: a.Task.UpdateTime + int64(3*time.Second)})
	if err != nil || r.TimedOut != 1 {
		t.Fatalf("sweep: %+v %v", r, err)
	}
	next := "working-B"
	if same {
		next = "working-A"
	}
	b := guardedClaim(t, c, guardedClaimRequest(req.Queue, next))
	if b.Revision <= a.Revision || b.Task.Uuid != a.Task.Uuid {
		t.Fatal("invalid reassignment revision")
	}
	_, err = c.ClaimGuardedTask(callContext(t), claimReq)
	code(t, err, api.CodeClaimSuperseded)
	_, err = c.CompleteGuardedTask(callContext(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: a.Task.Uuid, Queue: req.Queue, ExpectedRevision: a.Revision, ExpectedState: &a.Task.CurrentState})
	code(t, err, api.CodeRevisionConflict)
	_, err = c.CancelGuardedTask(callContext(t), api.CancelGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: a.Task.Uuid, Queue: req.Queue, ExpectedRevision: a.Revision})
	code(t, err, api.CodeRevisionConflict)
	_, err = c.UpdateGuardedTask(callContext(t), api.UpdateGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: a.Task.Uuid, Queue: req.Queue, ExpectedRevision: a.Revision, NewState: "stale"})
	code(t, err, api.CodeRevisionConflict)
	wrong := "wrong-state"
	_, err = c.CompleteGuardedTask(callContext(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: b.Task.Uuid, Queue: req.Queue, ExpectedRevision: b.Revision, ExpectedState: &wrong})
	code(t, err, api.CodeRevisionConflict)
	if got := guardedState(t, c, b); got != b {
		t.Fatalf("stale calls changed task: %+v -> %+v", b, got)
	}
	_, err = c.CompleteGuardedTask(callContext(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: b.Task.Uuid, Queue: req.Queue, ExpectedRevision: b.Revision})
	if err != nil {
		t.Fatal(err)
	}
	terminal := guardedState(t, c, b)
	_, err = c.UpdateGuardedTask(callContext(t), api.UpdateGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: b.Task.Uuid, Queue: req.Queue, ExpectedRevision: terminal.Revision, NewState: "ready"})
	code(t, err, api.CodeRevisionConflict)
}

func (f *fixture) mutationReceipts(t *testing.T) {
	c := f.writer(t)
	initial := keyedRequest("human-wait")
	initial.CurrentState = "waiting_human"
	initial.AutoTargetState = "waiting_human"
	task := *keyed(t, c, initial).Task
	for _, op := range []string{"update", "complete", "cancel"} {
		t.Run(op, func(t *testing.T) {
			if op == "cancel" {
				task = *keyed(t, c, keyedRequest("cancel-receipt")).Task
			}
			id := api.NewOperationID()
			proxy, finished := dropProxy(t, f.address(t))
			faulty := client(t, proxy)
			var result api.GuardedTask
			var replay bool
			var err error
			switch op {
			case "update":
				req := api.UpdateGuardedTaskRequest{OperationId: id, Uuid: task.Task.Uuid, Queue: task.Task.Queue, ExpectedRevision: task.Revision, NewState: "waiting_human", AutoTargetState: "waiting_human", Timeout: -1}
				r, e := faulty.UpdateGuardedTask(callContext(t), req)
				result, replay, err = r.Task, r.Replayed, e
				changed := req
				changed.NewState = "changed"
				_, conflict := c.UpdateGuardedTask(callContext(t), changed)
				code(t, conflict, api.CodeOperationConflict)
			case "complete":
				r, e := faulty.CompleteGuardedTask(callContext(t), api.CompleteGuardedTaskRequest{OperationId: id, Uuid: task.Task.Uuid, Queue: task.Task.Queue, ExpectedRevision: task.Revision})
				result, replay, err = r.Task, r.Replayed, e
			case "cancel":
				r, e := faulty.CancelGuardedTask(callContext(t), api.CancelGuardedTaskRequest{OperationId: id, Uuid: task.Task.Uuid, Queue: task.Task.Queue, ExpectedRevision: task.Revision})
				result, replay, err = r.Task, r.Replayed, e
			}
			lostResponse(t, finished)
			if err != nil || !replay || result.Revision != task.Revision+1 {
				t.Fatalf("%s replay: %+v %v", op, result, err)
			}
			if result.Terminal != (op != "update") {
				t.Fatalf("terminal=%t for %s", result.Terminal, op)
			}
			receipt, err := c.LookupOperation(callContext(t), api.LookupOperationRequest{OperationId: id})
			if err != nil || receipt.Receipt == nil || receipt.Receipt.ResultRevision != result.Revision || receipt.Receipt.TaskUuid != task.Task.Uuid {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
			if got := guardedState(t, c, result); got != result {
				t.Fatalf("stored result differs: %+v", got)
			}
			task = result
		})
	}
	replay := keyed(t, c, initial)
	if !replay.Replayed || !replay.Task.Terminal || replay.Task.Task.CurrentState != "completed" {
		t.Fatalf("submission replay reopened terminal task: %+v", replay)
	}
}

func (f *fixture) guardedConcurrent(t *testing.T) {
	c := f.writer(t)
	q := "guarded-concurrent"
	expected := map[string]bool{}
	for i := 0; i < 64; i++ {
		expected[keyed(t, c, keyedRequest(q)).Receipt.TaskUuid] = true
	}
	clients := f.clients(t, 8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]int{}
	failures := []string{}
	for _, c := range clients {
		wg.Add(1)
		go func(c *api.CorndogsClient) {
			defer wg.Done()
			for i := 0; i < 80; i++ {
				req := guardedClaimRequest(q, "working")
				req.OverrideTimeout = -1
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				r, err := c.ClaimGuardedTask(ctx, req)
				cancel()
				mu.Lock()
				if err != nil {
					failures = append(failures, err.Error())
					mu.Unlock()
					return
				}
				if r.Delivery == nil {
					mu.Unlock()
					return
				}
				seen[r.Delivery.Task.Task.Uuid]++
				if !bytes.Equal(r.Delivery.Payload, []byte("payload:"+q)) {
					failures = append(failures, "payload changed")
				}
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	if len(failures) != 0 || len(seen) != len(expected) {
		t.Fatalf("claims=%d errors=%v", len(seen), failures)
	}
	for id, count := range seen {
		if count != 1 || !expected[id] {
			t.Fatalf("duplicate or unknown claim %s: %d", id, count)
		}
	}
}

func (f *fixture) guardedRestart(t *testing.T) {
	c := f.writer(t)
	requests := make([]api.SubmitKeyedTaskRequest, 24)
	before := make([]api.GuardedTask, len(requests))
	receipts := make([]api.SubmissionReceipt, len(requests))
	for i := range requests {
		requests[i] = keyedRequest(fmt.Sprintf("guarded-restart-%d", i))
		r := keyed(t, c, requests[i])
		before[i], receipts[i] = *r.Task, r.Receipt
	}
	completeReq := api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: before[0].Task.Uuid, Queue: before[0].Task.Queue, ExpectedRevision: before[0].Revision}
	done, err := c.CompleteGuardedTask(callContext(t), completeReq)
	if err != nil {
		t.Fatal(err)
	}
	before[0] = done.Task
	cancelReq := api.CancelGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: before[1].Task.Uuid, Queue: before[1].Task.Queue, ExpectedRevision: before[1].Revision}
	cancelled, err := c.CancelGuardedTask(callContext(t), cancelReq)
	if err != nil {
		t.Fatal(err)
	}
	before[1] = cancelled.Task
	claimReq := guardedClaimRequest(before[2].Task.Queue, "working")
	claimReq.OverrideTimeout = -1
	before[2] = guardedClaim(t, c, claimReq)
	updateReq := api.UpdateGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: before[3].Task.Uuid, Queue: before[3].Task.Queue, ExpectedRevision: before[3].Revision, NewState: "waiting_human", AutoTargetState: "waiting_human", Timeout: -1}
	updated, err := c.UpdateGuardedTask(callContext(t), updateReq)
	if err != nil {
		t.Fatal(err)
	}
	before[3] = updated.Task
	for _, n := range f.nodes {
		f.kill(t, n)
	}
	if f.database != "" {
		mustDocker(t, "kill", "--signal=KILL", f.database)
		mustDocker(t, "start", f.database)
		waitFor(t, "PostgreSQL restarted", func() bool {
			_, err := docker("exec", f.database, "pg_isready", "-U", "postgres", "-d", "recovery")
			return err == nil
		})
	}
	for _, n := range f.nodes {
		f.start(t, n)
	}
	c = f.writer(t)
	for i, req := range requests {
		r := keyed(t, c, req)
		if !r.Replayed || r.Receipt != receipts[i] || *r.Task != before[i] {
			t.Fatalf("restart changed task or receipt %d: %+v", i, r)
		}
		if got := guardedState(t, c, before[i]); got != before[i] {
			t.Fatalf("state changed for task %d", i)
		}
		if i >= 4 {
			guardedClaim(t, c, guardedClaimRequest(req.Queue, "working"))
		}
	}
	for _, id := range []string{completeReq.OperationId, cancelReq.OperationId, claimReq.OperationId, updateReq.OperationId} {
		r, err := c.LookupOperation(callContext(t), api.LookupOperationRequest{OperationId: id})
		if err != nil || r.Receipt == nil {
			t.Fatalf("missing operation after restart: %+v %v", r, err)
		}
	}
	completeReplay, err := c.CompleteGuardedTask(callContext(t), completeReq)
	if err != nil || !completeReplay.Replayed || completeReplay.Task != before[0] {
		t.Fatalf("completion replay: %+v %v", completeReplay, err)
	}
	if got := guardedClaim(t, c, claimReq); got != before[2] {
		t.Fatal("claim replay changed after restart")
	}
	cancelReplay, err := c.CancelGuardedTask(callContext(t), cancelReq)
	if err != nil || !cancelReplay.Replayed || cancelReplay.Task != before[1] {
		t.Fatalf("cancel replay: %+v %v", cancelReplay, err)
	}
	updateReplay, err := c.UpdateGuardedTask(callContext(t), updateReq)
	if err != nil || !updateReplay.Replayed || updateReplay.Task != before[3] {
		t.Fatalf("update replay: %+v %v", updateReplay, err)
	}
	t.Log("24 task and submission records, four operation receipts, terminal records, human wait, active claim, and 20 payloads survived SIGKILL")
}

func (f *fixture) guardedFailover(t *testing.T) {
	c := f.writer(t)
	req := keyedRequest("guarded-failover")
	original := keyed(t, c, req)
	claimReq := guardedClaimRequest(req.Queue, "working")
	claimReq.OverrideTimeout = -1
	claimed := guardedClaim(t, c, claimReq)
	old := f.nodes[0]
	if f.cluster {
		old = f.leader(t)
	}
	f.kill(t, old)
	t.Cleanup(func() {
		if !old.running {
			f.start(t, old)
		}
	})
	c = f.writer(t)
	replay := keyed(t, c, req)
	if !replay.Replayed || replay.Receipt != original.Receipt || *replay.Task != claimed {
		t.Fatalf("failover lost submission: %+v", replay)
	}
	if got := guardedClaim(t, c, claimReq); got != claimed {
		t.Fatal("failover lost claim")
	}
	r, err := c.LookupOperation(callContext(t), api.LookupOperationRequest{OperationId: claimReq.OperationId})
	if err != nil || r.Receipt == nil || r.Receipt.ResultRevision != claimed.Revision {
		t.Fatalf("failover receipt: %+v %v", r, err)
	}
	keyed(t, c, keyedRequest("guarded-after-failover"))
	f.start(t, old)
	// ClusterTransport follows a follower's redirect for recovery reads.
	tr := &api.ClusterTransport{Seeds: []string{old.rpc}}
	t.Cleanup(func() { _ = tr.Close() })
	rejoined := api.NewCorndogsClient(tr)
	// HTTP health does not mean the rejoined node already knows its leader.
	// Retry only this read, with a bounded wait. A wrong successful result fails.
	start := time.Now()
	var lastErr error
	waitFor(t, "rejoined node can route a recovery read", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r, err := rejoined.GetGuardedTask(ctx, api.GetGuardedTaskRequest{Uuid: claimed.Task.Uuid, Queue: claimed.Task.Queue})
		lastErr = err
		if err != nil {
			return false
		}
		if r.Task == nil || *r.Task != claimed {
			t.Fatalf("recovery read after rejoin differs: %+v", r)
		}
		return true
	})
	t.Logf("rejoined recovery read available after %s; last error=%v", time.Since(start), lastErr)
}

func (f *fixture) guardedQuorum(t *testing.T) {
	leader := f.leader(t)
	stopped := []*node{}
	t.Cleanup(func() {
		for _, n := range stopped {
			if !n.running {
				f.start(t, n)
			}
		}
	})
	for _, n := range f.nodes {
		if n != leader {
			f.kill(t, n)
			stopped = append(stopped, n)
		}
	}
	req := keyedRequest("guarded-minority")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	r, err := client(t, leader.rpc).SubmitKeyedTask(ctx, req)
	cancel()
	if err == nil {
		t.Fatalf("minority acknowledged: %+v", r)
	}
	t.Logf("minority error: %v; not-applied=%t uncertain=%t", err, errors.Is(err, api.ErrNotApplied), errors.Is(err, api.ErrOutcomeUncertain))
	for _, n := range stopped {
		f.start(t, n)
	}
	c := f.writer(t)
	first := keyed(t, c, req)
	replay := keyed(t, c, req)
	if !replay.Replayed || first.Receipt != replay.Receipt {
		t.Fatal("minority recovery did not converge on one receipt")
	}
	counts, err := c.GetTaskStateCounts(callContext(t), api.GetTaskStateCountsRequest{Queue: req.Queue})
	if err != nil || counts.Count != 1 {
		t.Fatalf("minority retry duplicated: %+v %v", counts, err)
	}
}

func (f *fixture) compatibilityGuards(t *testing.T) {
	c := f.writer(t)
	info, err := c.GetServerInfo(callContext(t), api.GetServerInfoRequest{})
	if err != nil || info.SubmissionKeyPolicy != api.PolicyCompatibility || info.TaskGuardPolicy != api.PolicyCompatibility {
		t.Fatalf("default policies: %+v %v", info, err)
	}
	task := *keyed(t, c, keyedRequest("compat-guarded")).Task
	r, err := c.GetNextTask(callContext(t), api.GetNextTaskRequest{Queue: task.Task.Queue, CurrentState: "ready"})
	if err != nil || r.Delivery != nil {
		t.Fatalf("legacy claim selected guarded task: %+v %v", r, err)
	}
	_, err = c.CompleteTask(callContext(t), api.CompleteTaskRequest{Uuid: task.Task.Uuid, Queue: task.Task.Queue, CurrentState: "ready"})
	code(t, err, api.CodeTaskGuardRequired)
	_, err = c.CancelTask(callContext(t), api.CancelTaskRequest{Uuid: task.Task.Uuid, Queue: task.Task.Queue, CurrentState: "ready"})
	code(t, err, api.CodeTaskGuardRequired)
	_, err = c.UpdateTask(callContext(t), api.UpdateTaskRequest{Uuid: task.Task.Uuid, Queue: task.Task.Queue, CurrentState: "ready", NewState: "wrong"})
	code(t, err, api.CodeTaskGuardRequired)
	if got := guardedState(t, c, task); got != task {
		t.Fatal("legacy requests changed guarded task")
	}
}
