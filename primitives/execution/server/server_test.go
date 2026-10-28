package server_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/backend"
	"github.com/catalystcommunity/magi/primitives/execution/internal/testenv"
	"github.com/catalystcommunity/magi/primitives/execution/server"
	"github.com/catalystcommunity/magi/primitives/execution/store"
)

var envdDir string

func TestMain(m *testing.M) {
	platforms := []string{"linux-" + runtime.GOARCH}
	if os.Getenv("MAGI_TEST_KUBE") == "1" {
		platforms = backend.Platforms
	}
	dir, err := testenv.BuildEnvd(platforms...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	envdDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type harness struct {
	t    *testing.T
	svc  *server.Service
	url  string
	keys testenv.Keys
	// offset moves the service clock forward.
	offset atomic.Int64
}

func newHarness(t *testing.T, backends map[string]backend.Backend, def string) *harness {
	t.Helper()
	dbURL := testenv.Database(t)
	ctx := context.Background()
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	h := &harness{t: t, keys: testenv.NewKeys(t)}
	h.svc = &server.Service{DB: db, Backends: backends, Default: def, DefaultImage: "golang:1.26-bookworm",
		Now: func() time.Time { return time.Now().Add(time.Duration(h.offset.Load())) }}
	s := &rpc.Server{Verifier: h.keys.Verifier(t), MaxBody: server.MaxRequest}
	server.Register(s, h.svc)
	srv := httptest.NewServer(rpc.Mux(s, nil))
	t.Cleanup(srv.Close)
	h.url = srv.URL
	return h
}

func (h *harness) client(subject string, scopes ...string) *api.ExecutionClient {
	return api.NewExecutionClient(rpc.NewClient(h.url, rpc.StaticToken(h.keys.Token(h.t, subject, scopes...))))
}

func localBackend(t *testing.T) *backend.Local {
	return &backend.Local{Root: t.TempDir(), Envd: &backend.Binaries{Dir: envdDir}}
}

func u64(v uint64) *uint64 { return &v }
func str(v string) *string { return &v }

func wantCode(t *testing.T, err error, code int, what string) {
	t.Helper()
	if rpc.CodeOf(err) != code {
		t.Fatalf("%s: got %v, want code %d", what, err, code)
	}
}

const alice, bob = "user:alice@example.local", "user:bob@example.local"

func TestLocalOperations(t *testing.T) {
	h := newHarness(t, map[string]backend.Backend{"local": localBackend(t)}, "local")
	c := h.client(alice)
	ctx := context.Background()

	env, err := c.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{Env: map[string]string{"SECRET_VALUE": "hunter2"},
		Labels: map[string]string{"task": "t1"}}, WaitReadyMs: u64(10000)})
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != "ready" || env.Owner != alice || !strings.HasPrefix(env.Id, "env_") || *env.Spec.Backend != "local" {
		t.Fatalf("create: %+v", env)
	}
	if v, ok := env.Spec.Env["SECRET_VALUE"]; !ok || v != "" {
		t.Fatalf("env values must not be returned: %q", v)
	}
	got, err := c.Get(ctx, api.GetRequest{Id: env.Id})
	if err != nil || got.Id != env.Id || got.Status != "ready" || got.Spec.Labels["task"] != "t1" {
		t.Fatalf("get: %+v %v", got, err)
	}
	l, err := c.List(ctx, api.ListRequest{})
	if err != nil || len(l.Environments) != 1 {
		t.Fatalf("list: %+v %v", l, err)
	}

	// exec: env reaches the command; stdin, exit code, stderr.
	stdin := []byte("in")
	r, err := c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"sh", "-c", `printf "%s-" "$SECRET_VALUE"; cat; echo e >&2; exit 4`}, Stdin: &stdin})
	if err != nil || string(r.Stdout) != "hunter2-in" || string(r.Stderr) != "e\n" || r.ExitCode != 4 {
		t.Fatalf("exec: %+v %v", r, err)
	}
	start := time.Now()
	r, err = c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"sleep", "30"}, TimeoutMs: u64(200)})
	if err != nil || !r.TimedOut || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout: %+v %v", r, err)
	}
	r, err = c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"head", "-c", "100000", "/dev/zero"}, MaxOutputBytes: u64(10)})
	if err != nil || len(r.Stdout) != 10 || !r.Truncated {
		t.Fatalf("truncate: %+v %v", r, err)
	}
	wantCode(t, func() error { _, err := c.Exec(ctx, api.ExecRequest{Environment: env.Id}); return err }(), 400, "empty argv")

	// files
	w, err := c.WriteFile(ctx, api.WriteFileRequest{Environment: env.Id, Path: "src/main.go", Body: []byte("package main\n")})
	if err != nil || w.Size != 13 {
		t.Fatalf("write: %+v %v", w, err)
	}
	f, err := c.ReadFile(ctx, api.ReadFileRequest{Environment: env.Id, Path: "src/main.go", MaxBytes: u64(7)})
	if err != nil || string(f.Body) != "package" || !f.Truncated || f.Size != 13 {
		t.Fatalf("read: %+v %v", f, err)
	}
	lf, err := c.ListFiles(ctx, api.ListFilesRequest{Environment: env.Id, Path: ".", Depth: u64(3)})
	if err != nil || len(lf.Entries) != 2 || lf.Entries[1].Path != "src/main.go" {
		t.Fatalf("list-files: %+v %v", lf, err)
	}
	r, err = c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"cat", "main.go"}, Cwd: str("src")})
	if err != nil || string(r.Stdout) != "package main\n" {
		t.Fatalf("exec cwd: %+v %v", r, err)
	}
	_, err = c.ReadFile(ctx, api.ReadFileRequest{Environment: env.Id, Path: "nope"})
	wantCode(t, err, 404, "missing file")
	for _, p := range []string{"", "a\x00b"} {
		_, err = c.ReadFile(ctx, api.ReadFileRequest{Environment: env.Id, Path: p})
		wantCode(t, err, 400, "read bad path")
		_, err = c.WriteFile(ctx, api.WriteFileRequest{Environment: env.Id, Path: p})
		wantCode(t, err, 400, "write bad path")
		_, err = c.ListFiles(ctx, api.ListFilesRequest{Environment: env.Id, Path: p})
		wantCode(t, err, 400, "list bad path")
	}
	_, err = c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"true"}, Cwd: str("a\x00")})
	wantCode(t, err, 400, "bad cwd")

	// hold and extend
	e, err := c.Hold(ctx, api.HoldRequest{Id: env.Id, Held: true})
	if err != nil || !e.Held {
		t.Fatalf("hold: %+v %v", e, err)
	}
	e, err = c.Extend(ctx, api.ExtendRequest{Id: env.Id, TtlSeconds: 7200})
	if err != nil || e.ExpiresMs < uint64(time.Now().Add(119*time.Minute).UnixMilli()) {
		t.Fatalf("extend: %+v %v", e, err)
	}
	_, err = c.Extend(ctx, api.ExtendRequest{Id: env.Id, TtlSeconds: 0})
	wantCode(t, err, 400, "extend 0")

	// destroy
	e, err = c.Destroy(ctx, api.DestroyRequest{Id: env.Id})
	if err != nil || e.Status != "destroyed" {
		t.Fatalf("destroy: %+v %v", e, err)
	}
	if e, err = c.Destroy(ctx, api.DestroyRequest{Id: env.Id}); err != nil || e.Status != "destroyed" {
		t.Fatalf("destroy again: %+v %v", e, err)
	}
	_, err = c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"true"}})
	wantCode(t, err, 409, "exec after destroy")
	_, err = c.Hold(ctx, api.HoldRequest{Id: env.Id, Held: false})
	wantCode(t, err, 409, "hold after destroy")
	if l, _ := c.List(ctx, api.ListRequest{}); len(l.Environments) != 0 {
		t.Fatalf("destroyed env listed by default: %+v", l)
	}
	if l, _ := c.List(ctx, api.ListRequest{Status: str("destroyed")}); len(l.Environments) != 1 {
		t.Fatalf("destroyed filter: %+v", l)
	}

	// validation
	_, err = c.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{Backend: str("vm")}})
	wantCode(t, err, 400, "unknown backend")
	_, err = c.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{Network: str("internet")}})
	wantCode(t, err, 400, "bad network")
	_, err = c.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{Env: map[string]string{"1BAD": "x"}}})
	wantCode(t, err, 400, "bad env name")
	_, err = c.Get(ctx, api.GetRequest{Id: "env_missing"})
	wantCode(t, err, 404, "missing env")
}

func TestCreateWithoutWait(t *testing.T) {
	h := newHarness(t, map[string]backend.Backend{"local": localBackend(t)}, "local")
	c := h.client(alice)
	ctx := context.Background()
	env, err := c.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{}})
	if err != nil || env.Status != "creating" {
		t.Fatalf("create: %+v %v", env, err)
	}
	for i := 0; i < 100 && env.Status == "creating"; i++ {
		time.Sleep(50 * time.Millisecond)
		if env, err = c.Get(ctx, api.GetRequest{Id: env.Id}); err != nil {
			t.Fatal(err)
		}
	}
	if env.Status != "ready" {
		t.Fatalf("not ready: %+v", env)
	}
	c.Destroy(ctx, api.DestroyRequest{Id: env.Id})
}

func TestAccess(t *testing.T) {
	h := newHarness(t, map[string]backend.Backend{"local": localBackend(t)}, "local")
	ctx := context.Background()
	a, b := h.client(alice), h.client(bob)
	svc := h.client("service:magi", server.ScopeActAs)
	admin := h.client("user:root@example.local", server.ScopeAdmin)

	_, err := b.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{}, Owner: str(alice)})
	wantCode(t, err, 403, "owner override without act-as")
	env, err := svc.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{}, Owner: str(alice), WaitReadyMs: u64(10000)})
	if err != nil || env.Owner != alice || env.Status != "ready" {
		t.Fatalf("act-as create: %+v %v", env, err)
	}
	if _, err := a.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"true"}}); err != nil {
		t.Fatalf("owner exec: %v", err)
	}
	if _, err := svc.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"true"}}); err != nil {
		t.Fatalf("act-as exec: %v", err)
	}
	for name, call := range map[string]func() error{
		"get": func() error { _, err := b.Get(ctx, api.GetRequest{Id: env.Id}); return err },
		"exec": func() error {
			_, err := b.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"true"}})
			return err
		},
		"read-file": func() error {
			_, err := b.ReadFile(ctx, api.ReadFileRequest{Environment: env.Id, Path: "x"})
			return err
		},
		"write-file": func() error {
			_, err := b.WriteFile(ctx, api.WriteFileRequest{Environment: env.Id, Path: "x"})
			return err
		},
		"list-files": func() error {
			_, err := b.ListFiles(ctx, api.ListFilesRequest{Environment: env.Id, Path: "."})
			return err
		},
		"hold":       func() error { _, err := b.Hold(ctx, api.HoldRequest{Id: env.Id, Held: true}); return err },
		"extend":     func() error { _, err := b.Extend(ctx, api.ExtendRequest{Id: env.Id, TtlSeconds: 60}); return err },
		"destroy":    func() error { _, err := b.Destroy(ctx, api.DestroyRequest{Id: env.Id}); return err },
		"list owner": func() error { _, err := b.List(ctx, api.ListRequest{Owner: str(alice)}); return err },
	} {
		wantCode(t, call(), 403, "bob "+name)
	}
	if l, err := b.List(ctx, api.ListRequest{}); err != nil || len(l.Environments) != 0 {
		t.Fatalf("bob list: %+v %v", l, err)
	}
	if l, err := svc.List(ctx, api.ListRequest{Owner: str(alice)}); err != nil || len(l.Environments) != 1 {
		t.Fatalf("act-as list: %+v %v", l, err)
	}
	if l, err := admin.List(ctx, api.ListRequest{}); err != nil || len(l.Environments) != 1 {
		t.Fatalf("admin list: %+v %v", l, err)
	}
	anon := api.NewExecutionClient(rpc.NewClient(h.url, nil))
	_, err = anon.Get(ctx, api.GetRequest{Id: env.Id})
	wantCode(t, err, 401, "anonymous")
	if _, err := admin.Destroy(ctx, api.DestroyRequest{Id: env.Id}); err != nil {
		t.Fatalf("admin destroy: %v", err)
	}
}

func TestReaperAndLoss(t *testing.T) {
	local := localBackend(t)
	h := newHarness(t, map[string]backend.Backend{"local": local}, "local")
	c := h.client(alice)
	ctx := context.Background()
	mk := func() api.Environment {
		e, err := c.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{TtlSeconds: u64(60)}, WaitReadyMs: u64(10000)})
		if err != nil || e.Status != "ready" {
			t.Fatalf("create: %+v %v", e, err)
		}
		return e
	}
	held, free, lost := mk(), mk(), mk()
	if _, err := c.Hold(ctx, api.HoldRequest{Id: held.Id, Held: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if e, _ := c.Get(ctx, api.GetRequest{Id: free.Id}); e.Status != "ready" {
		t.Fatalf("reaped too early: %+v", e)
	}

	// Kill envd of one environment. The next call reports it as lost.
	pid, _ := os.ReadFile(filepath.Join(local.Root, lost.Id, "pid"))
	var n int
	fmt.Sscan(string(pid), &n)
	syscall.Kill(-n, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
	_, err := c.Exec(ctx, api.ExecRequest{Environment: lost.Id, Argv: []string{"true"}})
	wantCode(t, err, 503, "exec on lost env")
	if e, _ := c.Get(ctx, api.GetRequest{Id: lost.Id}); e.Status != "failed" {
		t.Fatalf("lost env status: %+v", e)
	}

	h.offset.Store(int64(2 * time.Minute))
	if err := h.svc.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{held.Id: "ready", free.Id: "destroyed", lost.Id: "destroyed"} {
		if e, _ := c.Get(ctx, api.GetRequest{Id: id}); e.Status != want {
			t.Fatalf("%s: %s, want %s", id, e.Status, want)
		}
	}
	if _, err := os.Stat(filepath.Join(local.Root, free.Id)); !os.IsNotExist(err) {
		t.Fatalf("reaped environment directory remains: %v", err)
	}
	c.Destroy(ctx, api.DestroyRequest{Id: held.Id})
}
