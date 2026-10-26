package recovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

// Only test-owned containers and networks are changed. Tests require explicit opt-in.
var serverImage = configuredCorndogsImage()

const postgresImage = "postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd"

type node struct {
	name, rpc, http string
	running         bool
}

type fixture struct {
	t                     *testing.T
	id, network, database string
	nodes                 []*node
	containers            []string
	cluster               bool
}

func docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("docker %v: %w: %s", args, err, b)
	}
	return strings.TrimSpace(string(b)), nil
}

func mustDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := docker(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func port(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

func callContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func client(t *testing.T, address string) *api.CorndogsClient {
	t.Helper()
	tr := &api.StreamTransport{Addr: address, DialTimeout: time.Second}
	t.Cleanup(func() { _ = tr.Close() })
	return api.NewCorndogsClient(tr)
}

func (f *fixture) writer(t *testing.T) *api.CorndogsClient {
	t.Helper()
	if !f.cluster {
		for _, n := range f.nodes {
			if n.running {
				return client(t, n.rpc)
			}
		}
		t.Fatal("no running server")
	}
	return client(t, f.leader(t).rpc)
}

func waitFor(t *testing.T, description string, check func() bool) {
	t.Helper()
	end := time.Now().Add(35 * time.Second)
	for time.Now().Before(end) {
		if check() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", description)
}

var httpClient = &http.Client{Timeout: time.Second}

func getHTTP(address string) string {
	r, err := httpClient.Get(address)
	if err != nil {
		return ""
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	return string(b)
}

func ready(t *testing.T, n *node) {
	t.Helper()
	waitFor(t, "ready "+n.name, func() bool { return getHTTP("http://"+n.http+"/healthz") != "" })
}

func (f *fixture) leader(t *testing.T) *node {
	t.Helper()
	var found *node
	var previous *node
	var stableSince time.Time
	waitFor(t, "exactly one running file-cluster leader", func() bool {
		found = nil
		count := 0
		for _, n := range f.nodes {
			if !n.running {
				continue
			}
			for _, line := range strings.Split(getHTTP("http://"+n.http+"/metrics"), "\n") {
				if strings.HasPrefix(line, "corndogs_cluster_is_leader{") && strings.HasSuffix(line, " 1") {
					found = n
					count++
				}
			}
		}
		if count != 1 || found != previous {
			previous = found
			stableSince = time.Now()
			return false
		}
		// Metrics are sampled every two seconds. Require more than one sample.
		return time.Since(stableSince) >= 2200*time.Millisecond
	})
	return found
}

func newFixture(t *testing.T, mode string, count int, syncMode string, required ...bool) *fixture {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, id: fmt.Sprintf("opal-recovery-%x", b), cluster: mode == "file" && count > 1}
	f.network = f.id
	mustDocker(t, "network", "create", "--label", "opal.recovery="+f.id, f.network)
	t.Cleanup(func() {
		for i := len(f.containers) - 1; i >= 0; i-- {
			name := f.containers[i]
			label, err := docker("inspect", "--format", `{{index .Config.Labels "opal.recovery"}}`, name)
			if err != nil || label != f.id {
				t.Errorf("refuse cleanup of unverified container %s: %v", name, err)
				continue
			}
			if t.Failed() {
				logs, _ := docker("logs", "--tail", "25", name)
				t.Logf("container %s last logs:\n%s", name, logs)
			}
			if _, err := docker("rm", "-f", "-v", name); err != nil {
				t.Error(err)
			}
		}
		label, err := docker("network", "inspect", "--format", `{{index .Labels "opal.recovery"}}`, f.network)
		if err != nil || label != f.id {
			t.Errorf("refuse cleanup of unverified network %s", f.network)
			return
		}
		if _, err := docker("network", "rm", f.network); err != nil {
			t.Error(err)
		}
		t.Logf("cleanup: removed test containers, their anonymous volumes, and network %s", f.id)
	})
	if mode == "postgres" {
		f.database = f.id + "-db"
		f.run(t, f.database, []string{"-e", "POSTGRES_PASSWORD=opal-test-only", "-e", "POSTGRES_DB=recovery"}, postgresImage)
		waitFor(t, "PostgreSQL ready", func() bool {
			_, err := docker("exec", f.database, "pg_isready", "-U", "postgres", "-d", "recovery")
			return err == nil
		})
	}
	for i := 0; i < count; i++ {
		f.nodes = append(f.nodes, &node{name: fmt.Sprintf("%s-n%d", f.id, i), rpc: "127.0.0.1:" + port(t), http: "127.0.0.1:" + port(t)})
	}
	peers := []string{}
	for i, n := range f.nodes {
		peers = append(peers, fmt.Sprintf("n%d=%s:5090", i, n.name))
	}
	for i, n := range f.nodes {
		args := []string{"-p", n.rpc + ":5080", "-p", n.http + ":8080", "-e", "STORAGE_BACKEND=" + mode,
			"-e", "CORNDOGS_FILESTORE_DIR=/tmp/opal-recovery-data", "-e", "CORNDOGS_FILESTORE_SYNC=" + syncMode,
			"-e", "PROMETHEUS_ENABLED=true", "-e", "LOGLEVEL=error"}
		if len(required) > 0 && required[0] {
			args = append(args, "-e", "CORNDOGS_SUBMISSION_KEY_POLICY=required", "-e", "CORNDOGS_TASK_GUARD_POLICY=required")
		}
		if mode == "postgres" {
			args = append(args, "-e", "DATABASE_HOST="+f.database, "-e", "DATABASE_NAME=recovery", "-e", "DATABASE_USER=postgres", "-e", "DATABASE_PASSWORD=opal-test-only", "-e", "DATABASE_STARTUP_TIMEOUT_SECONDS=20")
		}
		if f.cluster {
			args = append(args, "-e", "CORNDOGS_CLUSTER_ENABLED=true", "-e", fmt.Sprintf("CORNDOGS_CLUSTER_NODE_ID=n%d", i), "-e", "CORNDOGS_CLUSTER_PEERS="+strings.Join(peers, ","), "-e", "CORNDOGS_CLUSTER_RPC_ADVERTISE="+n.rpc)
		}
		f.run(t, n.name, args, serverImage)
		n.running = true
		ready(t, n)
	}
	if f.cluster {
		t.Logf("initial leader: %s", f.leader(t).name)
	}
	t.Logf("fixture mode=%s nodes=%d sync=%s server=%s postgres=%s", mode, count, syncMode, serverImage, postgresImage)
	return f
}

func (f *fixture) run(t *testing.T, name string, args []string, image string) {
	t.Helper()
	base := []string{"run", "-d", "--pull=never", "--name", name, "--network", f.network,
		"--label", "opal.recovery=" + f.id, "--memory", "512m", "--cpus", "1", "--pids-limit", "256"}
	// Register only resources created successfully by this invocation.
	mustDocker(t, append(append(base, args...), image)...)
	f.containers = append(f.containers, name)
}

func (f *fixture) kill(t *testing.T, n *node) {
	t.Helper()
	mustDocker(t, "kill", "--signal=KILL", n.name)
	n.running = false
}

func (f *fixture) start(t *testing.T, n *node) {
	t.Helper()
	mustDocker(t, "start", n.name)
	n.running = true
	ready(t, n)
}

func submit(t *testing.T, c *api.CorndogsClient, q string) api.Task {
	t.Helper()
	r, err := c.SubmitTask(callContext(t), api.SubmitTaskRequest{Queue: q, CurrentState: "ready", AutoTargetState: "working", Payload: []byte("payload:" + q), Priority: 7})
	if err != nil || r.Task == nil {
		t.Fatalf("submit: task=%v error=%v", r.Task, err)
	}
	return *r.Task
}

func state(t *testing.T, c *api.CorndogsClient, task api.Task) api.Task {
	t.Helper()
	r, err := c.GetTaskStateByID(callContext(t), api.GetTaskStateByIDRequest{Uuid: task.Uuid, Queue: task.Queue})
	if err != nil || r.Task == nil {
		t.Fatalf("lookup %s: task=%v error=%v", task.Uuid, r.Task, err)
	}
	return *r.Task
}

func claim(t *testing.T, c *api.CorndogsClient, q, working string) api.Task {
	t.Helper()
	r, err := c.GetNextTask(callContext(t), api.GetNextTaskRequest{Queue: q, CurrentState: "ready", OverrideCurrentState: working, OverrideAutoTargetState: "ready", OverrideTimeout: 2})
	if err != nil || r.Delivery == nil {
		t.Fatalf("claim: delivery=%v error=%v", r.Delivery, err)
	}
	if !bytes.Equal(r.Delivery.Payload, []byte("payload:"+q)) {
		t.Fatal("payload changed")
	}
	return r.Delivery.Task
}

func TestLegacyCompatibility(t *testing.T) {
	if os.Getenv("OPAL_RECOVERY_DOCKER") != "1" {
		t.Skip("set OPAL_RECOVERY_DOCKER=1 to create isolated Docker fixtures")
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
			f := newFixture(t, cfg.mode, cfg.count, cfg.sync)
			t.Run("acknowledged_state_survives_SIGKILL", f.restart)
			t.Run("concurrent_claims_are_unique", f.concurrentClaims)
			t.Run("timeout_requires_sweep", f.timeout)
			t.Run("explicit_legacy_retry_still_duplicates", f.lostSubmit)
			t.Run("new_cluster_client_does_not_replay_legacy", f.clusterLostSubmit)
			t.Run("lost_completion_reply_is_reconcilable", f.lostComplete)
			for _, same := range []bool{false, true} {
				name := "legacy_completion_is_not_guarded"
				if same {
					name = "legacy_completion_same_state_is_not_guarded"
				}
				t.Run(name, func(t *testing.T) { f.staleCompletion(t, same) })
			}
			t.Run("legacy_update_is_not_guarded", f.staleUpdate)
			t.Run("legacy_cannot_mutate_guarded_tasks", f.compatibilityGuards)
			if cfg.count > 1 {
				t.Run("one_node_failure_and_rejoin", f.failover)
			}
			if f.cluster {
				t.Run("no_majority_no_write_ack", f.noQuorum)
			}
		})
	}
}

func (f *fixture) restart(t *testing.T) {
	c := f.writer(t)
	tasks := make([]api.Task, 24)
	for i := range tasks {
		tasks[i] = submit(t, c, fmt.Sprintf("restart-%d", i))
	}
	done, err := c.CompleteTask(callContext(t), api.CompleteTaskRequest{Uuid: tasks[0].Uuid, Queue: tasks[0].Queue, CurrentState: "ready"})
	if err != nil || done.Task == nil {
		t.Fatalf("complete: %v", err)
	}
	cancelled, err := c.CancelTask(callContext(t), api.CancelTaskRequest{Uuid: tasks[1].Uuid, Queue: tasks[1].Queue, CurrentState: "ready"})
	if err != nil || cancelled.Task == nil {
		t.Fatalf("cancel: %v", err)
	}
	claim(t, c, tasks[2].Queue, "working")
	u, err := c.UpdateTask(callContext(t), api.UpdateTaskRequest{Uuid: tasks[3].Uuid, Queue: tasks[3].Queue, CurrentState: "ready", NewState: "waiting_human", AutoTargetState: "ready"})
	if err != nil || u.Task == nil {
		t.Fatalf("update: %v", err)
	}
	before := make([]api.Task, len(tasks))
	for i, task := range tasks {
		before[i] = state(t, c, task)
	}
	// A process kill does not simulate a disk loss or host power loss.
	for _, n := range f.nodes {
		f.kill(t, n)
	}
	if f.database != "" {
		mustDocker(t, "kill", "--signal=KILL", f.database)
		mustDocker(t, "start", f.database)
		waitFor(t, "PostgreSQL restart", func() bool {
			_, err := docker("exec", f.database, "pg_isready", "-U", "postgres", "-d", "recovery")
			return err == nil
		})
	}
	for _, n := range f.nodes {
		f.start(t, n)
	}
	c = f.writer(t)
	for i, task := range tasks {
		want := "ready"
		switch i {
		case 0:
			want = "completed"
		case 1:
			want = "canceled"
		case 2:
			want = "working"
		case 3:
			want = "waiting_human"
		}
		got := state(t, c, task)
		if got != before[i] || got.CurrentState != want || (i >= 2 && got.Priority != 7) {
			t.Errorf("task %s: got=%+v before=%+v want_state=%s", task.Uuid, got, before[i], want)
		}
		if i >= 4 {
			claim(t, c, task.Queue, "working")
		}
	}
	t.Log("checked 24 tasks against pre-kill metadata, including complete/cancel archive, claimed state, updated state, live priority, and 20 payloads")
}

func (f *fixture) concurrentClaims(t *testing.T) {
	c := f.writer(t)
	q := "concurrent"
	expected := map[string]bool{}
	for i := 0; i < 64; i++ {
		expected[submit(t, c, q).Uuid] = true
	}
	clients := make([]*api.CorndogsClient, 8)
	for i := range clients {
		if f.cluster {
			seeds := []string{}
			for _, n := range f.nodes {
				seeds = append(seeds, n.rpc)
			}
			tr := &api.ClusterTransport{Seeds: seeds}
			t.Cleanup(func() { _ = tr.Close() })
			clients[i] = api.NewCorndogsClient(tr)
		} else {
			clients[i] = client(t, f.nodes[i%len(f.nodes)].rpc)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]int{}
	failures := []string{}
	for _, cl := range clients {
		wg.Add(1)
		go func(c *api.CorndogsClient) {
			defer wg.Done()
			for j := 0; j < 80; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				r, err := c.GetNextTask(ctx, api.GetNextTaskRequest{Queue: q, CurrentState: "ready", OverrideTimeout: -1})
				cancel()
				if err != nil {
					mu.Lock()
					failures = append(failures, err.Error())
					mu.Unlock()
					return
				}
				if r.Delivery == nil {
					return
				}
				mu.Lock()
				seen[r.Delivery.Task.Uuid]++
				if !bytes.Equal(r.Delivery.Payload, []byte("payload:"+q)) {
					failures = append(failures, "payload changed")
				}
				mu.Unlock()
			}
		}(cl)
	}
	wg.Wait()
	if len(failures) > 0 {
		t.Errorf("claim errors: %v", failures)
	}
	if len(seen) != len(expected) {
		t.Errorf("claimed=%d expected=%d", len(seen), len(expected))
	}
	for id, n := range seen {
		if n != 1 || !expected[id] {
			t.Errorf("unexpected/duplicate claim %s count=%d", id, n)
		}
	}
	t.Logf("64 tasks, 8 concurrent clients, %d unique claims", len(seen))
}

func (f *fixture) timeout(t *testing.T) {
	c := f.writer(t)
	q := "timeout"
	task := submit(t, c, q)
	first := claim(t, c, q, "working")
	// Wait past the actual lease deadline, but do not run cleanup yet.
	time.Sleep(2200 * time.Millisecond)
	if got := state(t, c, task); got.CurrentState != "working" {
		t.Fatalf("changed without sweep: %+v", got)
	}
	r, err := c.CleanUpTimedOut(callContext(t), api.CleanUpTimedOutRequest{Queue: q, AtTime: first.UpdateTime + int64(3*time.Second)})
	if err != nil || r.TimedOut != 1 {
		t.Fatalf("sweep: %+v %v", r, err)
	}
	second := claim(t, c, q, "working")
	if second.Uuid != first.Uuid {
		t.Fatal("timeout created a different task")
	}
	t.Log("expired task stayed working until explicit cleanup; same task was then reclaimed")
}

// dropProxy reads a full response from the real server, then closes the client
// connection without sending that response. It drops only the first response.
// Later connections pass through, so ClusterTransport retries are visible.
func dropProxy(t *testing.T, upstream string) (string, <-chan error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	var mu sync.Mutex
	var conns []net.Conn
	track := func(c net.Conn) { mu.Lock(); conns = append(conns, c); mu.Unlock() }
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		first := true
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			track(in)
			drop := first
			first = false
			go func() {
				defer in.Close()
				out, err := net.DialTimeout("tcp", upstream, time.Second)
				if err != nil {
					if drop {
						finished <- err
					}
					return
				}
				track(out)
				defer out.Close()
				if !drop {
					done := make(chan struct{})
					go func() { _, _ = io.Copy(out, in); _ = out.Close(); close(done) }()
					_, _ = io.Copy(in, out)
					_ = in.Close()
					<-done
					return
				}
				_ = in.SetDeadline(time.Now().Add(10 * time.Second))
				_ = out.SetDeadline(time.Now().Add(10 * time.Second))
				request, err := readFrame(in)
				if err == nil {
					_, err = out.Write(request)
				}
				if err == nil {
					_, err = readFrame(out)
				}
				finished <- err
			}()
		}
	}()
	return l.Addr().String(), finished
}

func readFrame(r io.Reader) ([]byte, error) {
	h := make([]byte, 4)
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h)
	if n > 32<<20 {
		return nil, fmt.Errorf("unexpected frame size %d", n)
	}
	b := make([]byte, int(n)+4)
	copy(b, h)
	_, err := io.ReadFull(r, b[4:])
	return b, err
}

func lostResponse(t *testing.T, completed <-chan error) {
	t.Helper()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("proxy did not receive full server response: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("proxy did not finish")
	}
}

func (f *fixture) lostSubmit(t *testing.T) {
	c := f.writer(t)
	address := f.nodes[0].rpc
	if f.cluster {
		address = f.leader(t).rpc
	}
	proxy, finished := dropProxy(t, address)
	faulty := client(t, proxy)
	q := "lost-submit"
	req := api.SubmitTaskRequest{Queue: q, CurrentState: "ready", AutoTargetState: "working", Payload: []byte("payload:" + q)}
	_, err := faulty.SubmitTask(callContext(t), req)
	if !errors.Is(err, api.ErrOutcomeUncertain) {
		t.Fatalf("expected uncertain legacy submission, got %v", err)
	}
	lostResponse(t, finished)
	before, err := c.GetTaskStateCounts(callContext(t), api.GetTaskStateCountsRequest{Queue: q})
	if err != nil || before.Count != 1 {
		t.Fatalf("committed before retry: %+v %v", before, err)
	}
	retry, err := c.SubmitTask(callContext(t), req)
	if err != nil || retry.Task == nil {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	first := claim(t, c, q, "working")
	second := claim(t, c, q, "working")
	t.Logf("lost reply then explicit retry: first=%s second=%s; identical payload", first.Uuid, second.Uuid)
	if first.Uuid == second.Uuid {
		t.Error("legacy explicit retry unexpectedly reused the first task")
	}
}

func (f *fixture) clusterLostSubmit(t *testing.T) {
	address := f.nodes[0].rpc
	if f.cluster {
		address = f.leader(t).rpc
	}
	proxy, finished := dropProxy(t, address)
	tr := &api.ClusterTransport{Seeds: []string{proxy}}
	t.Cleanup(func() { _ = tr.Close() })
	c := api.NewCorndogsClient(tr)
	q := "cluster-auto-retry"
	r, err := c.SubmitTask(callContext(t), api.SubmitTaskRequest{Queue: q, CurrentState: "ready", AutoTargetState: "working", Payload: []byte("payload:" + q)})
	lostResponse(t, finished)
	if !errors.Is(err, api.ErrOutcomeUncertain) || r.Task != nil {
		t.Fatalf("expected uncertain legacy outcome: %+v %v", r, err)
	}
	counts, err := f.writer(t).GetTaskStateCounts(callContext(t), api.GetTaskStateCountsRequest{Queue: q})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("one application call returned uncertain; task count=%d", counts.Count)
	if counts.Count != 1 {
		t.Error("ClusterTransport retried an uncertain legacy submission")
	}
}

func (f *fixture) lostComplete(t *testing.T) {
	c := f.writer(t)
	task := submit(t, c, "lost-complete")
	address := f.nodes[0].rpc
	if f.cluster {
		address = f.leader(t).rpc
	}
	proxy, finished := dropProxy(t, address)
	req := api.CompleteTaskRequest{Uuid: task.Uuid, Queue: task.Queue, CurrentState: "ready"}
	_, err := client(t, proxy).CompleteTask(callContext(t), req)
	if err == nil {
		t.Fatal("fault was not visible")
	}
	lostResponse(t, finished)
	if got := state(t, c, task); got.CurrentState != "completed" {
		t.Fatalf("cannot reconcile completion: %+v", got)
	}
	r, err := c.CompleteTask(callContext(t), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("archive lookup reconciled completion; repeated completion returns task=%v", r.Task)
}

func (f *fixture) staleCompletion(t *testing.T, same bool) {
	c := f.writer(t)
	q := fmt.Sprintf("stale-complete-%t", same)
	task := submit(t, c, q)
	a := claim(t, c, q, "working-A")
	r, err := c.CleanUpTimedOut(callContext(t), api.CleanUpTimedOutRequest{Queue: q, AtTime: a.UpdateTime + int64(3*time.Second)})
	if err != nil || r.TimedOut != 1 {
		t.Fatalf("sweep: %+v %v", r, err)
	}
	next := "working-B"
	if same {
		next = "working-A"
	}
	b := claim(t, c, q, next)
	result, err := c.CompleteTask(callContext(t), api.CompleteTaskRequest{Uuid: a.Uuid, Queue: q, CurrentState: a.CurrentState})
	got := state(t, c, task)
	t.Logf("old=%s new=%s result=%+v error=%v stored=%s", a.CurrentState, b.CurrentState, result.Task, err, got.CurrentState)
	if err != nil || got.CurrentState != "completed" {
		t.Error("legacy completion behavior changed; this API is not a guard")
	}
}

func (f *fixture) staleUpdate(t *testing.T) {
	c := f.writer(t)
	task := submit(t, c, "stale-update")
	claim(t, c, task.Queue, "working-B")
	r, err := c.UpdateTask(callContext(t), api.UpdateTaskRequest{Uuid: task.Uuid, Queue: task.Queue, CurrentState: "working-A", NewState: "incorrect-stale-result", AutoTargetState: "ready"})
	got := state(t, c, task)
	t.Logf("stale update result=%+v error=%v stored=%s", r.Task, err, got.CurrentState)
	if err != nil || got.CurrentState != "incorrect-stale-result" {
		t.Error("legacy update behavior changed; this API is not a guard")
	}
}

func (f *fixture) failover(t *testing.T) {
	c := f.writer(t)
	tasks := []api.Task{}
	for i := 0; i < 24; i++ {
		tasks = append(tasks, submit(t, c, fmt.Sprintf("failover-%d", i)))
	}
	old := f.nodes[0]
	if f.cluster {
		old = f.leader(t)
	}
	start := time.Now()
	f.kill(t, old)
	t.Cleanup(func() {
		if !old.running {
			f.start(t, old)
		}
	})
	c = f.writer(t)
	for _, task := range tasks {
		if got := state(t, c, task); got.CurrentState != "ready" {
			t.Errorf("lost acknowledged task: %+v", got)
		}
	}
	t.Logf("24 acknowledged task states available after leader/server loss in %s", time.Since(start))
	f.logCluster(t)
	newTask := submit(t, c, "after-failover")
	t.Logf("all 24 acknowledged tasks recovered and new write accepted after %s", time.Since(start))
	f.start(t, old)
	local := client(t, old.rpc)
	waitFor(t, "rejoined node catches up", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r, err := local.GetTaskStateByID(ctx, api.GetTaskStateByIDRequest{Uuid: newTask.Uuid, Queue: newTask.Queue})
		return err == nil && r.Task != nil
	})
	for _, task := range tasks {
		if got := state(t, local, task); got.CurrentState != "ready" {
			t.Errorf("rejoined node lost task: %+v", got)
		}
	}
}

func (f *fixture) noQuorum(t *testing.T) {
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
		if n != leader && n.running {
			f.kill(t, n)
			stopped = append(stopped, n)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := client(t, leader.rpc).SubmitTask(ctx, api.SubmitTaskRequest{Queue: "no-quorum", CurrentState: "ready", AutoTargetState: "working", Payload: []byte("uncertain")})
	t.Logf("minority write: task=%+v error=%v", r.Task, err)
	if err == nil && r.Task != nil {
		t.Error("minority acknowledged a write without a majority")
	}
	for _, n := range stopped {
		f.start(t, n)
	}
	submit(t, f.writer(t), "after-quorum-restored")
}

func (f *fixture) logCluster(t *testing.T) {
	if !f.cluster {
		return
	}
	for _, n := range f.nodes {
		if !n.running {
			continue
		}
		for _, line := range strings.Split(getHTTP("http://"+n.http+"/metrics"), "\n") {
			if strings.HasPrefix(line, "corndogs_cluster_") {
				t.Logf("%s %s", n.name, line)
			}
		}
	}
}

// These cases use separate clusters. A failed recovery cannot affect the next case.
func TestIsolatedCluster(t *testing.T) {
	if os.Getenv("OPAL_RECOVERY_DOCKER") != "1" {
		t.Skip("set OPAL_RECOVERY_DOCKER=1")
	}
	t.Run("failover", func(t *testing.T) { f := newFixture(t, "file", 3, "group"); f.failover(t) })
	t.Run("quorum", func(t *testing.T) { f := newFixture(t, "file", 3, "group"); f.noQuorum(t) })
}

func configuredCorndogsImage() string {
	if image := os.Getenv("MAGI_CORNDOGS_IMAGE"); image != "" {
		return image
	}
	return "registry.catalystsquad.local/corndogs@sha256:e614bf3f88dd37d40ec7c5488bc66c06be3efbd73576eef5adfe1b9da1c64a70"
}
