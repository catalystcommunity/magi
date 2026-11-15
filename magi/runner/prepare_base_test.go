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
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
	sourceserver "github.com/catalystcommunity/magi/primitives/source/server"
)

// evalUpstream makes a bare upstream whose default branch has two commits.
// It returns the URL of the upstream, and the hashes of the older and the
// newer commit.
func evalUpstream(t *testing.T) (up, base, head string) {
	t.Helper()
	up = t.TempDir()
	git(t, up, "init", "-q", "--bare", "--initial-branch=main", up)
	work := t.TempDir()
	git(t, work, "clone", "-q", up, work)
	os.WriteFile(filepath.Join(work, "greeting.txt"), []byte("hi\n"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "start")
	base = git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "-q", "origin", "HEAD:main")
	os.WriteFile(filepath.Join(work, "greeting.txt"), []byte("hello\n"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "newer")
	git(t, work, "push", "-q", "origin", "HEAD:main")
	head = git(t, work, "rev-parse", "HEAD")
	return up, base, head
}

// prepareVars runs the prepare step of a run against the upstream, with the
// given vars. It returns the runner, the outputs of the step, and the fake
// Magi that collects the notes.
func prepareVars(t *testing.T, up string, vars map[string]string) (*Runner, map[string]string, *fakeMagi, error) {
	t.Helper()
	box := sandbox.Config{}
	if p, err := exec.LookPath("bwrap"); err == nil {
		box.Bwrap = p
	} else if p, err := exec.LookPath("sandbox-exec"); err == nil {
		box.SandboxExec = p
	} else {
		t.Skip("neither bubblewrap nor sandbox-exec is installed")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	fake := &fakeMagi{artifacts: map[string][]byte{}}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, MaxBody: 64 << 20}
	srv.Register("RunnerService", magiapi.RunnerServiceOps(fake))
	ts := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(ts.Close)
	tok, _ := iss.Issue(authn.Principal{Subject: "runner:test", Kind: authn.KindService, Scopes: []string{"magi:runner"}}, time.Hour)

	self, _ := os.Executable()
	state := t.TempDir()
	box.Root, box.Envd, box.EnvdArgs = filepath.Join(state, "envs"), self, []string{"envd"}
	cfg := Config{Server: ts.URL, Name: "test", StateDir: state, Repos: map[string]Repo{"r": {Upstream: "file://" + up, DefaultBranch: "main", Publish: true}}}
	cfg.Author.Name, cfg.Author.Email = "Magi's runner", "magi@test.example"
	r := &Runner{Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Magi:   magiapi.NewRunnerClient(rpc.NewClient(ts.URL, rpc.StaticToken(tok))),
		Boxes:  &sandbox.Sandboxes{Cfg: box},
		Source: &sourceserver.Service{Dir: filepath.Join(state, "repos"), AllowFileUpstream: true}}
	ctx := context.Background()
	if err := r.ensureRepos(ctx); err != nil {
		t.Fatal(err)
	}
	stopExec, err := r.startExec()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopExec)
	t.Cleanup(func() { r.removeBox("run_1") })

	a := magiapi.RunnerAssignment{Run: "run_1", Step: "prepare", Claim: "c", Owner: "user:x@example.local", Vars: vars}
	res, err := r.prepare(ctx, a, magiapi.RunnerStep{Run: "run_1", Step: "prepare", Claim: "c"})
	return r, res.Outputs, fake, err
}

// The prepare step of an eval run starts the branch at the base, and reports
// the full hash of that base.
func TestPrepareWithBase(t *testing.T) {
	up, base, head := evalUpstream(t)
	r, out, fake, err := prepareVars(t, up, map[string]string{"repo": "r", "title": "Say hello", "base": base[:7]})
	if err != nil {
		t.Fatal(err)
	}
	if out["base_commit"] != base {
		t.Fatalf("base_commit = %q, want the full hash %q", out["base_commit"], base)
	}
	if out["base_commit"] == head {
		t.Fatal("the branch must start at the base, not at the head of the default branch")
	}
	// A base that is not hex of 7 to 40 characters fails before the script.
	if _, _, _, err := prepareVars(t, up, map[string]string{"repo": "r", "title": "Say hello", "base": "nothex"}); err == nil ||
		!strings.Contains(err.Error(), "is not a commit") {
		t.Fatalf("a base that is not hex: %v", err)
	}
	// A base that the history does not have fails the step.
	if _, _, _, err := prepareVars(t, up, map[string]string{"repo": "r", "title": "Say hello", "base": "0123456789abcdef"}); err == nil ||
		!strings.Contains(err.Error(), "clone failed") {
		t.Fatalf("a base that is not in the history: %v", err)
	}

	// The review note of an eval run tells the reviewer that it never merges.
	if err := r.writeFile(context.Background(), "run_1", out["workdir"]+"/greeting.txt", []byte("hello there\n")); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"repo": "r", "title": "Say hello", "base": base[:7], "check_command": "true"}
	for k, v := range out {
		vars[k] = v
	}
	a := magiapi.RunnerAssignment{Run: "run_1", Step: "check", Claim: "c", Owner: "user:x@example.local", Vars: vars}
	if _, err := r.check(context.Background(), a, magiapi.RunnerStep{Run: "run_1", Step: "check", Claim: "c"}); err != nil {
		t.Fatal(err)
	}
	if !hasNote(fake.notes, "Eval run (base "+base[:7]+"): it ends at review; approve is refused.") {
		t.Fatalf("no note of the eval run: %q", fake.notes)
	}
}
