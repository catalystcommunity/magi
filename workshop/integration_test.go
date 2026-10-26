//go:build integration

package workshop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/catalystcommunity/magi/execution"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/harness"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func must(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func docker(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("docker fixture: %v: %s", e, out)
	}
	return strings.TrimSpace(string(out))
}
func TestProposalReceiptsAndCandidateRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var random [8]byte
	_, e := rand.Read(random[:])
	must(t, e)
	id := "opal-workshop-test-" + hex.EncodeToString(random[:])
	container := docker(t, "run", "--pull=never", "-d", "--name", id, "--label", "opal.fixture="+id, "--memory", "512m", "--cpus", "2", "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=synthetic-only", "postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd")
	if len(container) != 64 {
		t.Fatal("container ID")
	}
	defer func() {
		if docker(t, "inspect", "-f", `{{index .Config.Labels "opal.fixture"}}`, container) != id {
			t.Error("ownership mismatch")
			return
		}
		docker(t, "rm", "-f", "-v", container)
	}()
	pool, e := pgxpool.New(ctx, "postgres://postgres:synthetic-only@"+docker(t, "port", container, "5432/tcp")+"/postgres?sslmode=disable")
	must(t, e)
	defer pool.Close()
	until := time.Now().Add(20 * time.Second)
	for pool.Ping(ctx) != nil {
		if time.Now().After(until) {
			t.Fatal("database readiness")
		}
		time.Sleep(50 * time.Millisecond)
	}
	owner := w.ScopeRef{Authority: w.IdentityRef{Domain: "local.opal.test", AccountId: "tester"}, ScopeId: "public"}
	store := &Store{Pool: pool}
	must(t, store.Initialize(ctx))
	task := `{"task":"fixture"}`
	must(t, store.Admit(ctx, owner, id, id, []byte(task)))
	if !errors.Is(store.Admit(ctx, owner, id, id, []byte(task)), workflow.ErrReconcile) {
		t.Fatal("admission replay permitted")
	}
	if !errors.Is(store.Admit(ctx, owner, id, id, []byte(`{"changed":true}`)), workflow.ErrConflict) {
		t.Fatal("changed admission permitted")
	}
	defer func() {
		record, e := store.Read(context.Background(), owner, id)
		if e == nil && record.Container.ID != "" {
			bound, end := context.WithTimeout(context.Background(), 10*time.Second)
			defer end()
			if e := record.Container.Destroy(bound, id); e != nil {
				t.Error(e)
			}
		}
	}()
	source := "def format_elapsed(ms):\n if isinstance(ms,bool) or not isinstance(ms,int): raise TypeError()\n if ms<0: raise ValueError()\n return f'{ms} ms' if ms<1000 else f'{ms/1000:.2f} s'\n"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		turn := int(calls.Add(1)) - 1
		var count int
		if e := pool.QueryRow(r.Context(), `SELECT count(*) FROM workshop_events WHERE run=$1 AND operation=$2 AND outcome='dispatched' AND response IS NULL`, id, "model-"+string(rune('0'+turn))).Scan(&count); e != nil || count != 1 {
			t.Error("model invoked before durable receipt", e, count)
		}
		var action any
		switch turn {
		case 0:
			action = map[string]any{"tool": "read_file", "arguments": map[string]string{"path": "solution.py"}}
		case 1:
			action = map[string]any{"tool": "write_file", "arguments": map[string]string{"path": "solution.py", "text": source}}
		case 2:
			action = map[string]any{"tool": "check", "arguments": map[string]string{}}
		default:
			action = map[string]string{"answer": "Candidate is ready for review."}
		}
		content, _ := json.Marshal(action)
		wr.Header().Set("Content-Type", "application/json")
		json.NewEncoder(wr).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": string(content)}}}})
	}))
	defer server.Close()
	python, e := exec.LookPath("python3")
	must(t, e)
	python, e = filepath.Abs(python)
	must(t, e)
	adapter := &Adapter{Store: store, Config: question.WorkerConfig{Owner: owner, Model: question.ModelConfig{Endpoint: server.URL, Model: "fixture", Python: python, MaxTokens: 512}}}
	result, e := adapter.execute(ctx, workflow.CallRequest{RunID: id}, id, "Extend the formatter.")
	must(t, e)
	if len(result.Values) != 2 || calls.Load() != 4 {
		t.Fatal("missing task result", result, calls.Load())
	}
	record, e := store.Read(ctx, owner, id)
	must(t, e)
	hash := sha256.Sum256([]byte(source))
	if record.State != "candidate_ready" || record.Candidate != source || record.Digest != hex.EncodeToString(hash[:]) || len(record.Events) != 12 {
		t.Fatalf("bad retained result: state=%s events=%d", record.State, len(record.Events))
	}
	for _, event := range record.Events {
		if event.Outcome != "returned" || event.FinishedAt == nil || len(event.Response) == 0 {
			t.Fatal("incomplete event", event.Operation)
		}
	}
	if e = store.Begin(ctx, owner, id, "model-0", "model", []byte(`{}`)); !errors.Is(e, workflow.ErrReconcile) {
		t.Fatal("effect receipt replay permitted")
	}
	must(t, store.Hold(ctx, owner, id, true))
	if e = store.Cleanup(ctx, owner, id); !errors.Is(e, workflow.ErrConflict) {
		t.Fatal("live hold ignored", e)
	}
	must(t, store.Hold(ctx, owner, id, false))
	must(t, store.Cleanup(ctx, owner, id))
	must(t, store.Cleanup(ctx, owner, id))
	after, e := store.Read(ctx, owner, id)
	must(t, e)
	if after.State != "closed" || after.Candidate != source || len(after.Events) != len(record.Events)+1 {
		t.Fatal("cleanup lost evidence")
	}
	beforeBundle, e := PrepareSource(owner, owner.Authority, record)
	must(t, e)
	afterBundle, e := PrepareSource(owner, owner.Authority, after)
	must(t, e)
	beforeBytes, _ := json.Marshal(beforeBundle)
	afterBytes, _ := json.Marshal(afterBundle)
	if !bytes.Equal(beforeBytes, afterBytes) || ValidateSource(afterBundle, []byte(harness.InitialSource)) != nil {
		t.Fatal("retained source cannot be exported after environment removal")
	}
	// A lost create/bind reply must not leave an undiscoverable environment.
	orphanID := id + "-orphan"
	must(t, store.Admit(ctx, owner, orphanID, orphanID, []byte(`{"task":"lost bind"}`)))
	orphan, e := execution.Create(ctx, orphanID)
	must(t, e)
	defer orphan.Destroy(context.Background(), orphanID)
	_, e = pool.Exec(ctx, `UPDATE workshop_runs SET expires_at=clock_timestamp()-interval '1 second',held=true WHERE run=$1`, orphanID)
	must(t, e)
	must(t, store.Cleanup(ctx, owner, orphanID))
	cleaned, e := store.Read(ctx, owner, orphanID)
	must(t, e)
	if cleaned.State != "closed" || cleaned.Container.ID != orphan.ID || len(cleaned.Events) != 1 || cleaned.Events[0].Outcome != "returned" {
		t.Fatal("orphan recovery did not retain target and receipt")
	}
	// A lost removal reply is also safe to reconcile, without redispatch.
	_, e = pool.Exec(ctx, `UPDATE workshop_runs SET state='closing' WHERE run=$1`, orphanID)
	must(t, e)
	must(t, store.Cleanup(ctx, owner, orphanID))
}
