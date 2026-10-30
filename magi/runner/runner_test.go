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
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
	sourceserver "github.com/catalystcommunity/magi/primitives/source/server"
)

// The test binary is also the envd in the sandbox.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "envd" {
		envd.Main(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

// fakeMagi records what a runner sends.
type fakeMagi struct {
	mu        sync.Mutex
	posts     []string
	artifacts map[string][]byte
}

func (f *fakeMagi) Claim(context.Context, magiapi.RunnerClaimRequest) (magiapi.RunnerClaimResponse, error) {
	return magiapi.RunnerClaimResponse{}, nil
}
func (f *fakeMagi) Heartbeat(context.Context, magiapi.RunnerStep) (magiapi.RunnerAck, error) {
	return magiapi.RunnerAck{Ok: true}, nil
}
func (f *fakeMagi) Progress(context.Context, magiapi.RunnerProgressRequest) (magiapi.RunnerAck, error) {
	return magiapi.RunnerAck{Ok: true}, nil
}
func (f *fakeMagi) Complete(context.Context, magiapi.RunnerCompleteRequest) (magiapi.RunnerAck, error) {
	return magiapi.RunnerAck{Ok: true}, nil
}
func (f *fakeMagi) PutArtifact(_ context.Context, req magiapi.RunnerPutArtifactRequest) (magiapi.RunnerPutArtifactResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := "sha256:" + req.Labels["kind"]
	f.artifacts[d] = req.Body
	return magiapi.RunnerPutArtifactResponse{Digest: d}, nil
}
func (f *fakeMagi) Post(_ context.Context, req magiapi.RunnerPostRequest) (magiapi.RunnerAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, req.Text)
	return magiapi.RunnerAck{Ok: true}, nil
}
func (f *fakeMagi) RunStatus(context.Context, magiapi.RunnerRunStatusRequest) (magiapi.RunnerRunStatusResponse, error) {
	return magiapi.RunnerRunStatusResponse{Status: "running"}, nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.local", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCodeChangeOnRunner(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap is not installed")
	}
	// The upstream stands in for GitHub.
	up := t.TempDir()
	git(t, up, "init", "-q", "--bare", "--initial-branch=main", up)
	work := t.TempDir()
	git(t, work, "clone", "-q", up, work)
	os.MkdirAll(filepath.Join(work, "tools"), 0o755)
	os.WriteFile(filepath.Join(work, "tools", "check.sh"), []byte("#!/bin/sh\ngrep -q hello greeting.txt\n"), 0o755)
	os.WriteFile(filepath.Join(work, "greeting.txt"), []byte("hi\n"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "start")
	git(t, work, "push", "-q", "origin", "HEAD:main")

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	fake := &fakeMagi{artifacts: map[string][]byte{}}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, MaxBody: 64 << 20}
	srv.Register("RunnerService", magiapi.RunnerServiceOps(fake))
	ts := httptest.NewServer(rpc.Mux(srv, nil))
	defer ts.Close()
	tok, _ := iss.Issue(authn.Principal{Subject: "runner:test", Kind: authn.KindService, Scopes: []string{"magi:runner"}}, time.Hour)

	self, _ := os.Executable()
	state := t.TempDir()
	cfg := Config{Server: ts.URL, Name: "test", StateDir: state, Repos: map[string]Repo{"r": {Upstream: "file://" + up, DefaultBranch: "main"}}}
	r := &Runner{Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Magi:   magiapi.NewRunnerClient(rpc.NewClient(ts.URL, rpc.StaticToken(tok))),
		Boxes:  &sandbox.Sandboxes{Cfg: sandbox.Config{Root: filepath.Join(state, "envs"), Envd: self, EnvdArgs: []string{"envd"}, Bwrap: bwrap}},
		Source: &sourceserver.Service{Dir: filepath.Join(state, "repos"), AllowFileUpstream: true}}
	ctx := context.Background()
	if err := r.ensureRepos(ctx); err != nil {
		t.Fatal(err)
	}
	stopExec, err := r.startExec()
	if err != nil {
		t.Fatal(err)
	}
	defer stopExec()

	vars := map[string]string{"repo": "r", "title": "Say hello", "check_command": "./tools/check.sh"}
	step := func(name string, h handler) Result {
		t.Helper()
		a := magiapi.RunnerAssignment{Run: "run_1", Step: name, Claim: "c", Owner: "user:x@example.local", Vars: vars}
		res, err := h(ctx, a, magiapi.RunnerStep{Run: "run_1", Step: name, Claim: "c"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for k, v := range res.Outputs {
			vars[k] = v
		}
		return res
	}
	step("prepare", r.prepare)

	// The agent's tools reach the sandbox through the loopback API.
	if !strings.HasPrefix(r.execURL, "http://127.0.0.1:") {
		t.Fatalf("the execution API must be on loopback: %s", r.execURL)
	}
	b, _ := os.ReadFile(r.execTokenFile)
	ex := executionapi.NewExecutionClient(rpc.NewClient(r.execURL, rpc.StaticToken(string(b))))
	if _, err := ex.WriteFile(ctx, executionapi.WriteFileRequest{Environment: "run_1", Path: "/work/repo/greeting.txt", Body: []byte("hello\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := executionapi.NewExecutionClient(rpc.NewClient(r.execURL, nil)).Exec(ctx, executionapi.ExecRequest{Environment: "run_1", Argv: []string{"true"}}); rpc.CodeOf(err) != 401 {
		t.Fatalf("execution API without a token: %v", err)
	}

	if res := step("check", r.check); res.Outcome != "pass" {
		t.Fatalf("check = %+v", res)
	}
	if p := string(fake.artifacts[vars["patch"]]); !strings.Contains(p, "+hello") {
		t.Fatalf("patch = %q", p)
	}
	// Someone pushes to the upstream during review: merge goes on top.
	os.WriteFile(filepath.Join(work, "other.txt"), []byte("other\n"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "other")
	git(t, work, "push", "-q", "origin", "HEAD:main")
	if res := step("merge", r.merge); res.Outcome != "done" {
		t.Fatalf("merge = %+v", res)
	}
	if got := git(t, up, "rev-parse", "main"); got == vars["merge.commit"] {
		t.Fatal("merge must not publish")
	}
	if res := step("publish", r.publish); res.Outcome != "done" {
		t.Fatalf("publish = %+v (posts %q)", res, fake.posts)
	}
	if got := git(t, up, "rev-parse", "main"); got != vars["merge.commit"] {
		t.Fatalf("upstream main %s, want the merge %s", got, vars["merge.commit"])
	}
	git(t, work, "pull", "-q", "--ff-only", "origin", "main")
	if b, _ := os.ReadFile(filepath.Join(work, "greeting.txt")); string(b) != "hello\n" {
		t.Fatalf("published greeting.txt = %q", b)
	}
	step("cleanup", r.cleanup)
	if r.Boxes.Exists("run_1") {
		t.Fatal("cleanup left the environment")
	}
}
