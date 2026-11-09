package runner

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/compose"
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
	proxy     *magiapi.RunnerEgressProxy
}

func (f *fakeMagi) EgressAccess(context.Context, magiapi.RunnerStep) (magiapi.RunnerEgressAccessResponse, error) {
	return magiapi.RunnerEgressAccessResponse{Proxy: f.proxy}, nil
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
func (f *fakeMagi) Hello(context.Context, magiapi.RunnerHelloRequest) (magiapi.RunnerHelloResponse, error) {
	return magiapi.RunnerHelloResponse{}, nil
}
func (f *fakeMagi) GetPackage(context.Context, magiapi.RunnerGetPackageRequest) (magiapi.RunnerGetPackageResponse, error) {
	return magiapi.RunnerGetPackageResponse{}, rpc.NotFound("no package")
}
func (f *fakeMagi) GetArtifact(_ context.Context, req magiapi.RunnerGetArtifactRequest) (magiapi.RunnerGetArtifactResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.artifacts[req.Digest]
	if !ok {
		return magiapi.RunnerGetArtifactResponse{}, rpc.NotFound("no artifact %s", req.Digest)
	}
	return magiapi.RunnerGetArtifactResponse{MediaType: "application/octet-stream", Body: b}, nil
}
func (f *fakeMagi) Terminals(ctx context.Context, _ magiapi.RunnerTerminalsRequest) (magiapi.RunnerTerminalsResponse, error) {
	select { // a long poll with no session
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	return magiapi.RunnerTerminalsResponse{Terminals: []magiapi.RunnerTerminal{}}, nil
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
	box := sandbox.Config{}
	if p, err := exec.LookPath("bwrap"); err == nil {
		box.Bwrap = p
	} else if p, err := exec.LookPath("sandbox-exec"); err == nil {
		box.SandboxExec = p
	} else {
		t.Skip("neither bubblewrap nor sandbox-exec is installed")
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

	// The apprentice's tools reach the sandbox through the loopback API.
	if !strings.HasPrefix(r.execURL, "http://127.0.0.1:") {
		t.Fatalf("the execution API must be on loopback: %s", r.execURL)
	}
	b, _ := os.ReadFile(r.execTokenFile)
	ex := executionapi.NewExecutionClient(rpc.NewClient(r.execURL, rpc.StaticToken(string(b))))
	if _, err := ex.WriteFile(ctx, executionapi.WriteFileRequest{Environment: "run_1", Path: vars["workdir"] + "/greeting.txt", Body: []byte("hello\n")}); err != nil {
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
	if got := git(t, up, "log", "-1", "--format=%an <%ae>", "main^2"); got != "Magi's runner <magi@test.example>" {
		t.Fatalf("change author = %q", got)
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

// With an egress proxy from Magi, the sandbox has no network, and its
// proxy port reaches the proxy through GET /runner/egress on Magi.
// With MAGI_TEST_VM_KERNEL, the same test runs with a task VM.
func TestSandboxEgressThroughMagi(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap is not installed")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	t.Run("bubblewrap", func(t *testing.T) { egressThroughMagi(t, bwrap, nil) })
	t.Run("vm", func(t *testing.T) {
		k := os.Getenv("MAGI_TEST_VM_KERNEL")
		if k == "" {
			t.Skip("MAGI_TEST_VM_KERNEL is not set")
		}
		home, _ := os.UserHomeDir()
		hv := filepath.Join(home, ".local", "share", "magi-runner-tools", "vm", "cloud-hypervisor")
		fsd := "/usr/lib/virtiofsd"
		if _, err := os.Stat("/usr/libexec/virtiofsd"); err == nil {
			fsd = "/usr/libexec/virtiofsd"
		}
		egressThroughMagi(t, bwrap, &sandbox.VM{Kernel: k, Hypervisor: hv, Virtiofsd: fsd, MemoryMB: 1024})
	})
}

func egressThroughMagi(t *testing.T, bwrap string, vm *sandbox.VM) {
	// The egress proxy: it answers each request with the request line and
	// the proxy user.
	pl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pl.Close()
	go func() {
		for {
			c, err := pl.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				body := req.Method + " " + req.RequestURI + " " + req.Header.Get("Proxy-Authorization")
				io.WriteString(c, "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: "+itoa(len(body))+"\r\n\r\n"+body)
			}()
		}
	}()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	fake := &fakeMagi{artifacts: map[string][]byte{}, proxy: &magiapi.RunnerEgressProxy{Environment: "run_2", Credential: "cred", Ca: []byte("ca")}}
	srv := &rpc.Server{Verifier: ver, MaxBody: 64 << 20}
	srv.Register("RunnerService", magiapi.RunnerServiceOps(fake))
	mux := rpc.Mux(srv, nil)
	mux.Handle("/runner/egress", compose.EgressTunnel(ver, &compose.EgressProxy{Addr: pl.Addr().String()}))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	tok, _ := iss.Issue(authn.Principal{Subject: "runner:test", Kind: authn.KindService, Scopes: []string{"magi:runner"}}, time.Hour)
	state := t.TempDir()
	os.WriteFile(filepath.Join(state, "token"), []byte(tok), 0o600)

	up := t.TempDir()
	git(t, up, "init", "-q", "--bare", "--initial-branch=main", up)
	work := t.TempDir()
	git(t, work, "clone", "-q", up, work)
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "start")
	git(t, work, "push", "-q", "origin", "HEAD:main")

	self, _ := os.Executable()
	box := sandbox.Config{Root: filepath.Join(state, "envs"), Envd: self, EnvdArgs: []string{"envd"}, Bwrap: bwrap, VM: vm}
	cfg := Config{Server: ts.URL, Name: "test", StateDir: state, TokenFile: filepath.Join(state, "token"),
		Repos: map[string]Repo{"r": {Upstream: "file://" + up, DefaultBranch: "main"}}}
	r := &Runner{Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Magi:   magiapi.NewRunnerClient(rpc.NewClient(ts.URL, rpc.StaticToken(tok))),
		Boxes:  &sandbox.Sandboxes{Cfg: box},
		Source: &sourceserver.Service{Dir: filepath.Join(state, "repos"), AllowFileUpstream: true}}
	ctx := context.Background()
	if err := r.ensureRepos(ctx); err != nil {
		t.Fatal(err)
	}
	a := magiapi.RunnerAssignment{Run: "run_2", Step: "prepare", Claim: "c", Owner: "user:x@example.local", Vars: map[string]string{"repo": "r", "title": "t"}}
	if _, err := r.prepare(ctx, a, magiapi.RunnerStep{Run: "run_2", Step: "prepare", Claim: "c"}); err != nil {
		t.Fatal(err)
	}
	defer r.removeBox("run_2")
	res, err := r.exec(ctx, "run_2", "/work", `curl -sS --max-time 10 http://example.invalid/x; echo; cat "$SSL_CERT_FILE"`, 30000)
	if err != nil {
		t.Fatal(err)
	}
	want := "GET http://example.invalid/x Basic " + base64.StdEncoding.EncodeToString([]byte("run_2:cred")) + "\nca"
	if got := string(res.Stdout); got != want {
		t.Fatalf("through the proxy: %q %q, want %q", got, res.Stderr, want)
	}
	if res, _ := r.exec(ctx, "run_2", "/work", `curl -sS --noproxy '*' --max-time 3 `+ts.URL+`/`, 30000); res.ExitCode == 0 {
		t.Fatalf("the sandbox reached the network directly: %q", res.Stdout)
	}
	// A restarted runner serves the proxy of a kept environment again.
	// The old envd stops with the old runner (bubblewrap --die-with-parent;
	// a VM stops with its process group).
	r.mu.Lock()
	r.proxies["run_2"].Close()
	r.mu.Unlock()
	pid, _ := os.ReadFile(filepath.Join(r.Boxes.Dir("run_2"), "pid"))
	n, _ := strconv.Atoi(strings.TrimSpace(string(pid)))
	syscall.Kill(-n, syscall.SIGKILL)
	for r.Boxes.Alive("run_2") {
		time.Sleep(10 * time.Millisecond)
	}
	r2 := &Runner{Cfg: r.Cfg, Log: r.Log, Magi: r.Magi, Boxes: &sandbox.Sandboxes{Cfg: box}, Source: r.Source}
	defer r2.removeBox("run_2")
	res, err = r2.exec(ctx, "run_2", "/work", `curl -sS --max-time 10 http://example.invalid/y`, 30000)
	if err != nil || !strings.HasPrefix(string(res.Stdout), "GET http://example.invalid/y Basic ") {
		t.Fatalf("after a restart: %v %q %q", err, res.Stdout, res.Stderr)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// The tunnel needs a runner token.
func TestEgressTunnelNeedsRunner(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	ts := httptest.NewServer(compose.EgressTunnel(ver, &compose.EgressProxy{Addr: "127.0.0.1:1"}))
	defer ts.Close()
	user, _ := iss.Issue(authn.Principal{Subject: "user:x@example.local", Kind: authn.KindUser}, time.Hour)
	for _, tok := range []string{"", user} {
		req, _ := http.NewRequest("GET", ts.URL+"/runner/egress", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: %s", tok, res.Status)
		}
	}
}
