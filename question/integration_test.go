//go:build integration

package question

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

const qaPostgres = "postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd"

var qaCorndogs = configuredCorndogsImage()

func must(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func docker(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("Q&A fixture Docker %s failed: %v: %s", args[0], e, out)
	}
	return strings.TrimSpace(string(out))
}
func loopback(t testing.TB) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	must(t, e)
	address := l.Addr().String()
	l.Close()
	return address
}

type qaFixture struct {
	config  DevConfig
	profile Profile
	client  *Client
	pool    *pgxpool.Pool
}

func newQAFixture(t *testing.T, endpoint string) *qaFixture {
	t.Helper()
	var random [10]byte
	_, e := rand.Read(random[:])
	must(t, e)
	id := "opal-qa-" + hex.EncodeToString(random[:])
	start := func(suffix, image, port string, env []string) string {
		name := id + suffix
		args := []string{"run", "--pull=never", "-d", "--name", name, "--label", "opal.qa=" + id, "--memory", "512m", "--cpus", "2", "--pids-limit", "256", "-p", "127.0.0.1::" + port}
		for _, value := range env {
			args = append(args, "-e", value)
		}
		args = append(args, image)
		docker(t, args...)
		t.Cleanup(func() {
			if docker(t, "inspect", "-f", `{{index .Config.Labels "opal.qa"}}`, name) != id {
				t.Error("cleanup ownership mismatch")
				return
			}
			docker(t, "rm", "-f", "-v", name)
		})
		return docker(t, "port", name, port+"/tcp")
	}
	pg := start("-pg", qaPostgres, "5432", []string{"POSTGRES_PASSWORD=local-qa-only"})
	cd := start("-cd", qaCorndogs, "5080", []string{"STORAGE_BACKEND=file", "CORNDOGS_FILESTORE_DIR=/tmp/qa-data", "CORNDOGS_FILESTORE_SYNC=always", "CORNDOGS_SUBMISSION_KEY_POLICY=required", "CORNDOGS_TASK_GUARD_POLICY=required", "LOGLEVEL=error"})
	dsn := "postgres://postgres:local-qa-only@" + pg + "/postgres?sslmode=disable"
	pool, e := pgxpool.New(context.Background(), dsn)
	must(t, e)
	t.Cleanup(pool.Close)
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		e = pool.Ping(ctx)
		if e == nil {
			_, e = api.NewCorndogsClient(&workflow.Transport{Seeds: []string{cd}}).GetServerInfo(ctx, api.GetServerInfoRequest{})
		}
		cancel()
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("storage startup timeout", e)
		}
		time.Sleep(50 * time.Millisecond)
	}
	python, e := exec.LookPath("python3")
	must(t, e)
	python, e = filepath.Abs(python)
	must(t, e)
	dir := filepath.Join(t.TempDir(), "qa")
	must(t, InitDev(dir, loopback(t), dsn, cd, ModelConfig{Endpoint: endpoint, Model: "opal-test-small", Python: python, MaxTokens: 128}))
	c, e := LoadDevConfig(filepath.Join(dir, "server.json"))
	must(t, e)
	p, e := LoadProfile(filepath.Join(dir, "client.json"))
	must(t, e)
	client, e := NewClient(p)
	must(t, e)
	return &qaFixture{config: c, profile: p, client: client, pool: pool}
}
func (f *qaFixture) serve(t *testing.T) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{}, 1)
	go func() {
		done <- ServeDev(ctx, f.config, func(message string) {
			if strings.Contains(message, "API ready") {
				ready <- struct{}{}
			}
		})
	}()
	select {
	case <-ready:
	case e := <-done:
		cancel()
		t.Fatal("Q&A startup", e)
	case <-time.After(40 * time.Second):
		cancel()
		t.Fatal("Q&A startup timeout")
	}
	var stopped atomic.Bool
	stop := func() {
		if stopped.Swap(true) {
			return
		}
		cancel()
		select {
		case e := <-done:
			must(t, e)
		case <-time.After(12 * time.Second):
			t.Fatal("Q&A shutdown timeout")
		}
	}
	t.Cleanup(stop)
	return stop
}
func denied(err error) bool {
	var remote *protocol.RemoteError
	return errors.As(err, &remote) && remote.Detail.Code == 1403
}
func TestQuestionEndToEnd(t *testing.T) {
	parent := t
	var calls atomic.Int64
	var fail atomic.Bool
	var block atomic.Bool
	started := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []map[string]string `json:"messages"`
		}
		if json.Unmarshal(raw, &req) != nil || len(req.Messages) != 2 || req.Messages[1]["content"] == "" {
			t.Error("model input")
		}
		wr.Header().Set("Content-Type", "application/json")
		if block.Load() {
			started <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-time.After(15 * time.Second):
			}
			return
		}
		if fail.Load() {
			wr.WriteHeader(503)
			wr.Write([]byte(`{"error":"synthetic outage"}`))
			return
		}
		wr.Write([]byte(`{"model":"test-provider","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"4"}}],"usage":{"completion_tokens":1}}`))
	}))
	defer provider.Close()
	f := newQAFixture(t, provider.URL+"/v1/chat/completions")
	stop := f.serve(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Run("signed-question-python-model-answer-and-evidence", func(t *testing.T) {
		_, e := f.client.Submit(ctx, "first", "What is two plus two?")
		must(t, e)
		answer, e := f.client.Wait(ctx, "first")
		must(t, e)
		if answer != "4" || calls.Load() != 1 {
			t.Fatal(answer, calls.Load())
		}
		ev, e := f.client.Evidence(ctx, "first")
		must(t, e)
		if ev.Outcome != "returned" || ev.Response == nil || !bytes.Contains(ev.Request, []byte("What is two plus two?")) || !bytes.Contains(*ev.Response, []byte("completion_tokens")) || ev.FinishedAt == nil {
			t.Fatal("incomplete evidence")
		}
		if _, e = DisplayEvidence(ev); e != nil {
			t.Fatal(e)
		}
		var proof []byte
		must(t, f.pool.QueryRow(ctx, `SELECT proof FROM workflow_runs WHERE id='first'`).Scan(&proof))
		if len(proof) == 0 {
			t.Fatal("missing signed admission")
		}
		var timeout int64
		var claim []byte
		must(t, f.pool.QueryRow(ctx, `SELECT request FROM workflow_intents WHERE method='ClaimGuardedTask' LIMIT 1`).Scan(&claim))
		request, e := api.DecodeClaimGuardedTaskRequest(claim)
		must(t, e)
		timeout = request.OverrideTimeout
		if timeout != 142 {
			t.Fatal("model deadline did not extend claim", timeout)
		}
	})
	t.Run("same-run-retry-does-not-call-model-again", func(t *testing.T) {
		_, e := f.client.Submit(ctx, "first", "What is two plus two?")
		must(t, e)
		_, e = f.client.Wait(ctx, "first")
		must(t, e)
		if calls.Load() != 1 {
			t.Fatal("duplicate model call")
		}
		if _, e = f.client.Submit(ctx, "first", "Changed question"); e == nil {
			t.Fatal("changed run accepted")
		}
	})
	t.Run("evidence-requires-signature-and-scope", func(t *testing.T) {
		profile := f.profile
		profile.Owner.ScopeId = "other"
		client, e := NewClient(profile)
		must(t, e)
		_, e = client.Evidence(ctx, "first")
		if !denied(e) {
			t.Fatal(e)
		}
		profile = f.profile
		profile.PrivateKey = bytes.Repeat([]byte{7}, 64)
		client, e = NewClient(profile)
		must(t, e)
		_, e = client.Evidence(ctx, "first")
		if !denied(e) {
			t.Fatal(e)
		}
	})
	t.Run("new-question-has-no-prior-context", func(t *testing.T) {
		_, e := f.client.Submit(ctx, "second", "Reply with four.")
		must(t, e)
		_, e = f.client.Wait(ctx, "second")
		must(t, e)
		ev, e := f.client.Evidence(ctx, "second")
		must(t, e)
		if bytes.Contains(ev.Request, []byte("two plus two")) {
			t.Fatal("context crossed questions")
		}
	})
	t.Run("restart-retains-result-and-evidence", func(t *testing.T) {
		stop()
		stop = f.serve(parent)
		answer, e := f.client.Wait(ctx, "first")
		must(t, e)
		if answer != "4" || calls.Load() != 2 {
			t.Fatal("restart replay")
		}
		_, e = f.client.Evidence(ctx, "first")
		must(t, e)
	})
	t.Run("provider-failure-retained-without-retry", func(t *testing.T) {
		fail.Store(true)
		_, e := f.client.Submit(ctx, "failed", "Test unavailable provider.")
		must(t, e)
		if _, e = f.client.Wait(ctx, "failed"); e == nil {
			t.Fatal("provider failure hidden")
		}
		ev, e := f.client.Evidence(ctx, "failed")
		must(t, e)
		if ev.ErrorCode == nil || *ev.ErrorCode != "provider_http_503" || ev.Outcome != "failed" || ev.Response == nil || !bytes.Contains(*ev.Response, []byte("outage")) {
			t.Fatal("missing failure evidence")
		}
		stop()
		stop = f.serve(parent)
		_, e = f.client.Submit(ctx, "failed", "Test unavailable provider.")
		must(t, e)
		if _, e = f.client.Wait(ctx, "failed"); e == nil {
			t.Fatal("failure replay hidden")
		}
		if calls.Load() != 3 {
			t.Fatal("failed call replayed", calls.Load())
		}
	})
	t.Run("worker-stop-during-call-does-not-replay", func(t *testing.T) {
		fail.Store(false)
		block.Store(true)
		_, e := f.client.Submit(ctx, "interrupted", "Test interrupted model call.")
		must(t, e)
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("model was not reached")
		}
		stop()
		block.Store(false)
		stop = f.serve(parent)
		if _, e = f.client.Wait(ctx, "interrupted"); e == nil {
			t.Fatal("interrupted call appeared complete")
		}
		ev, e := f.client.Evidence(ctx, "interrupted")
		must(t, e)
		if ev.Outcome != "failed" || ev.ErrorCode == nil || *ev.ErrorCode != "worker_process_failed" {
			t.Fatal("interrupted evidence missing")
		}
		if calls.Load() != 4 {
			t.Fatal("interrupted call replayed", calls.Load())
		}
	})
}

func configuredCorndogsImage() string {
	if image := os.Getenv("MAGI_CORNDOGS_IMAGE"); image != "" {
		return image
	}
	return "registry.catalystsquad.local/corndogs@sha256:e614bf3f88dd37d40ec7c5488bc66c06be3efbd73576eef5adfe1b9da1c64a70"
}
