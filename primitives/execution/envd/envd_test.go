package envd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
)

const token = "0123456789abcdef0123456789abcdef"

func setup(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	srv := httptest.NewServer(&Server{Token: token, Dir: dir, Env: []string{"PATH=" + os.Getenv("PATH"), "BASE=1"}})
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, Token: token}, dir
}

func code(err error) int {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return 0
}

func u64(v uint64) *uint64 { return &v }
func str(v string) *string { return &v }

func TestAuth(t *testing.T) {
	c, _ := setup(t)
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", "wrong", token + "x"} {
		bad := &Client{Base: c.Base, Token: tok}
		if err := bad.Ping(ctx); code(err) != http.StatusUnauthorized {
			t.Fatalf("token %q: got %v, want 401", tok, err)
		}
		if _, err := bad.Exec(ctx, api.ExecRequest{Argv: []string{"true"}}); code(err) != http.StatusUnauthorized {
			t.Fatalf("exec with token %q: got %v", tok, err)
		}
	}
	res, err := http.Get(c.Base + PathHealth)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("health: %v %v", res, err)
	}
}

func TestExec(t *testing.T) {
	c, dir := setup(t)
	ctx := context.Background()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	stdin := []byte("from stdin")
	r, err := c.Exec(ctx, api.ExecRequest{
		Argv: []string{"sh", "-c", `echo "$BASE $EXTRA $(pwd)"; cat; echo err >&2; exit 3`},
		Cwd:  str("sub"), Env: map[string]string{"EXTRA": "x"}, Stdin: &stdin,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "1 x " + filepath.Join(dir, "sub") + "\nfrom stdin"
	if r.ExitCode != 3 || string(r.Stdout) != want || string(r.Stderr) != "err\n" || r.TimedOut || r.Truncated {
		t.Fatalf("got %+v stdout=%q", r, r.Stdout)
	}

	r, err = c.Exec(ctx, api.ExecRequest{Argv: []string{"sh", "-c", "head -c 5000 /dev/zero"}, MaxOutputBytes: u64(100)})
	if err != nil || len(r.Stdout) != 100 || !r.Truncated || r.ExitCode != 0 {
		t.Fatalf("truncate: %+v %v", r, err)
	}

	// A background child holds the pipes. The process group is killed.
	start := time.Now()
	r, err = c.Exec(ctx, api.ExecRequest{Argv: []string{"sh", "-c", "sleep 30 & echo started; sleep 30"}, TimeoutMs: u64(300)})
	if err != nil || !r.TimedOut || string(r.Stdout) != "started\n" || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout: %+v %v after %v", r, err, time.Since(start))
	}

	r, err = c.Exec(ctx, api.ExecRequest{Argv: []string{"no-such-program-x"}})
	if err != nil || r.ExitCode != 127 {
		t.Fatalf("missing program: %+v %v", r, err)
	}
	for _, bad := range []api.ExecRequest{{}, {Argv: []string{""}}, {Argv: []string{"a\x00b"}}, {Argv: []string{"true"}, Env: map[string]string{"A=B": "x"}}} {
		if _, err := c.Exec(ctx, bad); code(err) != 400 {
			t.Fatalf("%+v: got %v, want 400", bad, err)
		}
	}
}

func TestFiles(t *testing.T) {
	c, dir := setup(t)
	ctx := context.Background()
	w, err := c.WriteFile(ctx, api.WriteFileRequest{Path: "a/b/c.txt", Body: []byte("hello world"), Mode: u64(0o600)})
	if err != nil || w.Size != 11 || w.Path != filepath.Join(dir, "a/b/c.txt") {
		t.Fatalf("write: %+v %v", w, err)
	}
	if st, _ := os.Stat(w.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	f, err := c.ReadFile(ctx, api.ReadFileRequest{Path: w.Path})
	if err != nil || string(f.Body) != "hello world" || f.Truncated || f.Size != 11 {
		t.Fatalf("read: %+v %v", f, err)
	}
	f, err = c.ReadFile(ctx, api.ReadFileRequest{Path: "a/b/c.txt", MaxBytes: u64(5)})
	if err != nil || string(f.Body) != "hello" || !f.Truncated || f.Size != 11 {
		t.Fatalf("read cut: %+v %v", f, err)
	}
	if _, err := c.ReadFile(ctx, api.ReadFileRequest{Path: "missing"}); code(err) != 404 {
		t.Fatalf("missing: %v", err)
	}
	if _, err := c.ReadFile(ctx, api.ReadFileRequest{Path: "a"}); code(err) != 400 {
		t.Fatalf("dir read: %v", err)
	}
	c.WriteFile(ctx, api.WriteFileRequest{Path: "top.txt", Body: []byte("x")})

	l, err := c.ListFiles(ctx, api.ListFilesRequest{Path: "."})
	if err != nil || len(l.Entries) != 2 || l.Truncated {
		t.Fatalf("list depth 1: %+v %v", l, err)
	}
	l, err = c.ListFiles(ctx, api.ListFilesRequest{Path: ".", Depth: u64(5)})
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Path)
	}
	if err != nil || strings.Join(names, ",") != "a,a/b,a/b/c.txt,top.txt" || l.Entries[2].Size != 11 || !l.Entries[0].Dir {
		t.Fatalf("list depth 5: %v %+v %v", names, l, err)
	}
	l, err = c.ListFiles(ctx, api.ListFilesRequest{Path: ".", Depth: u64(5), Limit: u64(2)})
	if err != nil || len(l.Entries) != 2 || !l.Truncated {
		t.Fatalf("list limit: %+v %v", l, err)
	}
	for _, p := range []string{"", "a\x00b"} {
		if _, err := c.ReadFile(ctx, api.ReadFileRequest{Path: p}); code(err) != 400 {
			t.Fatalf("path %q read: %v", p, err)
		}
		if _, err := c.WriteFile(ctx, api.WriteFileRequest{Path: p}); code(err) != 400 {
			t.Fatalf("path %q write: %v", p, err)
		}
		if _, err := c.ListFiles(ctx, api.ListFilesRequest{Path: p}); code(err) != 400 {
			t.Fatalf("path %q list: %v", p, err)
		}
	}
}
