// Probes of case pr5. Target: magi/runner
package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// probeMagi keeps the notes of the work step.
type probeMagi struct {
	*fakeMagi
	pmu    sync.Mutex
	pnotes []string
}

func (f *probeMagi) Progress(_ context.Context, req magiapi.RunnerProgressRequest) (magiapi.RunnerAck, error) {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	f.pnotes = append(f.pnotes, req.Note)
	return magiapi.RunnerAck{Ok: true}, nil
}

// probeWork runs the work step with a stand-in for `python -m magi_apprentice
// run` that prints the given text. With transcript, the stand-in also writes
// the transcript file.
func probeWork(t *testing.T, stdout string, transcript bool) (map[string]string, []string, error) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is not installed")
	}
	d := t.TempDir()
	pkg := filepath.Join(d, "magi_apprentice")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "import sys\n"
	if transcript {
		script += "open(sys.argv[sys.argv.index('--transcript') + 1], 'w').write('{}\\n')\n"
	}
	script += "sys.stdout.write(open(__file__ + '.out').read())\n"
	if err := os.WriteFile(filepath.Join(pkg, "__main__.py"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "__main__.py.out"), []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	fake := &probeMagi{fakeMagi: &fakeMagi{artifacts: map[string][]byte{}}}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, MaxBody: 64 << 20}
	srv.Register("RunnerService", magiapi.RunnerServiceOps(fake))
	ts := httptest.NewServer(rpc.Mux(srv, nil))
	defer ts.Close()
	tok, _ := iss.Issue(authn.Principal{Subject: "runner:test", Kind: authn.KindService, Scopes: []string{"magi:runner"}}, time.Hour)
	r := &Runner{Cfg: Config{StateDir: t.TempDir()}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Magi: magiapi.NewRunnerClient(rpc.NewClient(ts.URL, rpc.StaticToken(tok)))}
	r.Cfg.Apprentice.Python, r.Cfg.Apprentice.Path = python, d
	res, err := r.work(context.Background(),
		magiapi.RunnerAssignment{Run: "run_w", Step: "work", Vars: map[string]string{
			"environment": "e", "workdir": "/work/repo", "instructions": "do it"}},
		magiapi.RunnerStep{Run: "run_w", Step: "work", Claim: "c"})
	fake.pmu.Lock()
	defer fake.pmu.Unlock()
	return res.Outputs, append([]string(nil), fake.pnotes...), err
}

const probeCost = `{"status": "finished", "summary": "done", "steps": 3, "tool_calls": 7,
	"usage": {"prompt_tokens": 1200, "completion_tokens": 300, "cached_tokens": 400, "cost": 0.0314}}`
const probeNoCost = `{"status": "finished", "summary": "done", "steps": 2, "tool_calls": 4,
	"usage": {"prompt_tokens": 900, "completion_tokens": 100}}`

func probeNote(notes []string, part string) string {
	for _, n := range notes {
		if strings.Contains(n, part) {
			return n
		}
	}
	return ""
}

// Add the outputs apprentice.cost_usd (format %.3f) and apprentice.prompt_tokens.
func TestProbeCostOutputs(t *testing.T) {
	out, _, err := probeWork(t, probeCost, false)
	if err != nil {
		t.Fatal(err)
	}
	if out["apprentice.cost_usd"] != "0.031" || out["apprentice.prompt_tokens"] != "1200" {
		t.Errorf("cost_usd %q, prompt_tokens %q", out["apprentice.cost_usd"], out["apprentice.prompt_tokens"])
	}
}

// Add " (cost $X.XXX)" to the note "apprentice ... after N steps".
func TestProbeNoteWithCost(t *testing.T) {
	_, notes, err := probeWork(t, probeCost, false)
	if err != nil {
		t.Fatal(err)
	}
	n := probeNote(notes, "apprentice finished after 3 steps")
	if !strings.Contains(n, " (cost $0.031)") {
		t.Errorf("no cost in the note: %q", notes)
	}
}

// When there is no cost, leave the outputs out.
func TestProbeNoCostNoOutputs(t *testing.T) {
	out, _, err := probeWork(t, probeNoCost, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"apprentice.cost_usd", "apprentice.prompt_tokens"} {
		if v, ok := out[k]; ok {
			t.Errorf("%s is set without a cost: %q", k, v)
		}
	}
	if out["apprentice.status"] != "finished" || out["apprentice.steps"] != "2" {
		t.Errorf("the earlier outputs changed: %v", out)
	}
}

// When there is no cost, leave the text out.
func TestProbeNoCostNoText(t *testing.T) {
	_, notes, err := probeWork(t, probeNoCost, false)
	if err != nil {
		t.Fatal(err)
	}
	n := probeNote(notes, "apprentice finished after 2 steps")
	if n == "" || strings.Contains(n, "cost") {
		t.Errorf("the note without a cost: %q", notes)
	}
}

// The behavior that the task does not name stays: output that is not JSON
// is an error of the step.
func TestProbeNotJSONIsAnError(t *testing.T) {
	_, _, err := probeWork(t, "Traceback (most recent call last)\n", true)
	if err == nil || !strings.Contains(err.Error(), "apprentice failed") {
		t.Errorf("err = %v", err)
	}
}

// The behavior that the task does not name stays: the status error is an
// error of the step, and its note is sent.
func TestProbeErrorStatus(t *testing.T) {
	_, notes, err := probeWork(t, `{"status": "error", "summary": "s", "error": "boom", "steps": 1, "tool_calls": 0}`, true)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
	if probeNote(notes, "apprentice error after 1 steps") == "" {
		t.Errorf("no note for the failed run: %q", notes)
	}
}

// The behavior that the task does not name stays: the transcript is an
// output of the step.
func TestProbeTranscriptOutput(t *testing.T) {
	out, _, err := probeWork(t, probeCost, true)
	if err != nil {
		t.Fatal(err)
	}
	if out["apprentice.transcript"] == "" {
		t.Errorf("no transcript output: %v", out)
	}
}
