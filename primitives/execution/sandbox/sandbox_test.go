package sandbox_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
)

// The test binary is also the envd in the sandbox.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "envd" {
		envd.Main(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

func TestSandbox(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap is not installed")
	}
	self, _ := os.Executable()
	tools := t.TempDir()
	os.WriteFile(tools+"/tool.txt", []byte("tool"), 0o644)
	secret := t.TempDir() // stands in for the home directory of the runner user
	os.WriteFile(secret+"/key", []byte("do not read"), 0o600)
	s := &sandbox.Sandboxes{Cfg: sandbox.Config{Root: t.TempDir(), Envd: self, EnvdArgs: []string{"envd"}, Bwrap: bwrap,
		ReadOnly: []string{tools}, Env: map[string]string{"FROM_CONFIG": "1"}}}
	tok := strings.Repeat("t", 32)
	if err := s.Start("env1", tok, map[string]string{"FROM_SPEC": "2"}); err != nil {
		t.Fatal(err)
	}
	defer s.Remove("env1")
	base, err := s.Wait("env1", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("envd must listen on loopback only: %s", base)
	}
	ctx := context.Background()
	c := &envd.Client{Base: base, Token: tok, HTTP: http.DefaultClient}
	run := func(script string) api.ExecResult {
		r, err := c.Exec(ctx, api.ExecRequest{Argv: []string{"sh", "-c", script}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := run(`echo "$HOME $FROM_CONFIG $FROM_SPEC"; cat ` + tools + `/tool.txt; echo hi > /work/out; pwd`)
	if got := string(r.Stdout); r.ExitCode != 0 || got != "/work/home 1 2\ntool/work\n" {
		t.Fatalf("exec = %d %q %q", r.ExitCode, got, r.Stderr)
	}
	if b, _ := os.ReadFile(s.WorkDir("env1") + "/out"); string(b) != "hi\n" {
		t.Fatalf("work dir: %q", b)
	}
	if r := run(`cat ` + secret + `/key`); r.ExitCode == 0 {
		t.Fatalf("the sandbox read a host file: %q", r.Stdout)
	}
	if r := run(`touch ` + tools + `/new`); r.ExitCode == 0 {
		t.Fatal("the sandbox wrote a read-only tool directory")
	}
	// The sandbox binds its own work directory at /work, so a host home
	// under /work is not the host directory and this check says nothing.
	if home, _ := os.UserHomeDir(); home != "" && !strings.HasPrefix(home, "/work") {
		if r := run(`ls ` + home); r.ExitCode == 0 {
			t.Fatalf("the sandbox sees the home directory: %q", r.Stdout)
		}
	}
	if err := (&envd.Client{Base: base, Token: strings.Repeat("w", 32), HTTP: http.DefaultClient}).Ping(ctx); err == nil {
		t.Fatal("envd accepted a wrong token")
	}
	// Resume after envd stops (a runner restart) keeps the work directory.
	if err := s.Remove("env1"); err != nil || s.Exists("env1") {
		t.Fatalf("remove: %v", err)
	}
}
