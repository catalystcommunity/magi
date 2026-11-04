package sandbox_test

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	cfg := sandbox.Config{}
	if p, err := exec.LookPath("bwrap"); err == nil {
		cfg.Bwrap = p
	} else if p, err := exec.LookPath("sandbox-exec"); err == nil {
		cfg.SandboxExec = p
	} else {
		t.Skip("neither bubblewrap nor sandbox-exec is installed")
	}
	self, _ := os.Executable()
	tools := t.TempDir()
	os.WriteFile(tools+"/tool.txt", []byte("tool"), 0o644)
	secret := t.TempDir() // stands in for the home directory of the runner user
	os.WriteFile(secret+"/key", []byte("do not read"), 0o600)
	cfg.Root, cfg.Envd, cfg.EnvdArgs = t.TempDir(), self, []string{"envd"}
	cfg.ReadOnly, cfg.Env = []string{tools}, map[string]string{"FROM_CONFIG": "1"}
	s := &sandbox.Sandboxes{Cfg: cfg}
	tok := strings.Repeat("t", 32)
	if err := s.Start("env1", tok, map[string]string{"FROM_SPEC": "2"}, nil); err != nil {
		t.Fatal(err)
	}
	defer s.Remove("env1")
	base, err := s.Wait("env1", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") && !strings.HasPrefix(base, "unix:") {
		t.Fatalf("envd must listen on loopback or a unix socket only: %s", base)
	}
	ctx := context.Background()
	c, err := s.Client("env1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	run := func(script string) api.ExecResult {
		r, err := c.Exec(ctx, api.ExecRequest{Argv: []string{"sh", "-c", script}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	w := s.Work("env1")
	r := run(`echo "$HOME $FROM_CONFIG $FROM_SPEC"; cat ` + tools + `/tool.txt; echo hi > ` + w + `/out; pwd`)
	if got := string(r.Stdout); r.ExitCode != 0 || got != w+"/home 1 2\ntool"+w+"\n" {
		t.Fatalf("exec = %d %q %q", r.ExitCode, got, r.Stderr)
	}
	if b, _ := os.ReadFile(s.WorkDir("env1") + "/out"); string(b) != "hi\n" {
		t.Fatalf("work dir: %q", b)
	}
	if _, err := exec.LookPath("git"); err == nil {
		// No stderr: on macOS a blocked xcrun cache write prints errors.
		if r := run(`git --version`); r.ExitCode != 0 || len(r.Stderr) > 0 {
			t.Fatalf("git in the sandbox: %q %q", r.Stdout, r.Stderr)
		}
	}
	if r := run(`cat ` + secret + `/key`); r.ExitCode == 0 {
		t.Fatalf("the sandbox read a host file: %q", r.Stdout)
	}
	if r := run(`touch ` + tools + `/new`); r.ExitCode == 0 {
		t.Fatal("the sandbox wrote a read-only tool directory")
	}
	// In a runner sandbox HOME is the home of that sandbox, under its work
	// directory, so this check says nothing there.
	if home, _ := os.UserHomeDir(); home != "" && !strings.HasPrefix(home, w) && !strings.HasPrefix(home, "/work") {
		if r := run(`ls ` + home); r.ExitCode == 0 {
			t.Fatalf("the sandbox sees the home directory: %q", r.Stdout)
		}
	}
	if err := (&envd.Client{Base: c.Base, Token: strings.Repeat("w", 32), HTTP: c.HTTP}).Ping(ctx); err == nil {
		t.Fatal("envd accepted a wrong token")
	}
	// Resume after envd stops (a runner restart) keeps the work directory.
	if err := s.Remove("env1"); err != nil || s.Exists("env1") {
		t.Fatalf("remove: %v", err)
	}
}

// A sandbox with a Proxy has no network; its only route out is the proxy
// socket on the host: through the proxy port, and through the transparent
// route with no proxy settings. The root is deep, so the socket paths are
// longer than the unix address limit.
func TestSandboxProxy(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap is not installed")
	}
	self, _ := os.Executable()
	root := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 40))
	s := &sandbox.Sandboxes{Cfg: sandbox.Config{Root: root, Envd: self, EnvdArgs: []string{"envd"}, Bwrap: bwrap}}
	id := "run_" + strings.Repeat("x", 26)
	// envd finds the proxy user of the transparent route in HTTPS_PROXY.
	env := map[string]string{"HTTPS_PROXY": "http://" + id + ":secret@" + sandbox.ProxyAddr}
	if err := s.Start(id, strings.Repeat("t", 32), env, &sandbox.Proxy{CA: []byte("ca"), SSHKnownHosts: []byte("* ssh-ed25519 AAAA\n")}); err != nil {
		t.Fatal(err)
	}
	defer s.Remove(id)
	ln, err := s.ListenProxy(id)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// A fake proxy: it answers a request, or a CONNECT and then the request
	// in the tunnel.
	got := make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				line, _ := br.ReadString('\n')
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "CONNECT ") {
					for h, _ := br.ReadString('\n'); strings.TrimSpace(h) != ""; h, _ = br.ReadString('\n') {
						if strings.HasPrefix(strings.ToLower(h), "proxy-authorization:") {
							line += " auth"
						}
					}
					conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
					in, _ := br.ReadString('\n')
					line += " | " + strings.TrimSpace(in)
				}
				got <- line
				conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\nvia proxy"))
			}()
		}
	}()
	// A host port that the sandbox must not reach.
	host, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	c, err := s.Client(id, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	run := func(script string) api.ExecResult {
		r, err := c.Exec(context.Background(), api.ExecRequest{Argv: []string{"sh", "-c", script}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(`cat ` + sandbox.ProxyCA); string(r.Stdout) != "ca" {
		t.Fatalf("CA in the sandbox: %q %q", r.Stdout, r.Stderr)
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	if r := run(`curl -sS --max-time 3 http://` + host.Addr().String() + `/`); r.ExitCode == 0 {
		t.Fatalf("the sandbox reached the host network: %q", r.Stdout)
	}
	r := run(`curl -sS --max-time 5 -x http://` + sandbox.ProxyAddr + ` http://example.invalid/x`)
	if string(r.Stdout) != "via proxy" {
		t.Fatalf("proxy route: %d %q %q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if line := <-got; line != "GET http://example.invalid/x HTTP/1.1" {
		t.Fatalf("the proxy got %q", line)
	}
	// No proxy settings: envd answers the name with a fake address, the
	// firewall sends the connection to envd, and envd sends a CONNECT.
	r = run(`curl -sS --max-time 5 --noproxy '*' http://example.invalid/y`)
	if string(r.Stdout) != "via proxy" {
		t.Fatalf("transparent route: %d %q %q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if line := <-got; line != "CONNECT example.invalid:80 HTTP/1.1 auth | GET /y HTTP/1.1" {
		t.Fatalf("the proxy got %q", line)
	}
	// The view has the proxy CA in the system bundle and the SSH host key.
	if r := run(`grep -c . /etc/ssh/ssh_known_hosts; cat /etc/resolv.conf; grep -q '^ca$' /etc/ssl/certs/ca-certificates.crt /etc/ssl/cert.pem 2>/dev/null && echo bundle`); !strings.Contains(string(r.Stdout), "nameserver 127.0.0.53") || !strings.Contains(string(r.Stdout), "bundle") {
		t.Fatalf("view: %q %q", r.Stdout, r.Stderr)
	}
	// Commands have no capability in the network namespace.
	if r := run(`grep CapEff /proc/self/status`); !strings.Contains(string(r.Stdout), "0000000000000000") {
		t.Fatalf("capabilities: %q", r.Stdout)
	}
}
