package sandbox_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
)

// vmConfig returns the task VM configuration of this host, or skips the
// test. MAGI_TEST_VM_KERNEL is the guest kernel (tools/vm/build-kernel.sh).
func vmConfig(t *testing.T) *sandbox.VM {
	k := os.Getenv("MAGI_TEST_VM_KERNEL")
	if k == "" {
		t.Skip("MAGI_TEST_VM_KERNEL is not set")
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		t.Skipf("no access to /dev/kvm: %v", err)
	} else {
		f.Close()
	}
	v := &sandbox.VM{Kernel: k, CPUs: 2, MemoryMB: 1024}
	home, _ := os.UserHomeDir()
	for _, p := range []string{os.Getenv("MAGI_TEST_VM_HYPERVISOR"), home + "/.local/share/magi-runner-tools/vm/cloud-hypervisor"} {
		if p != "" {
			v.Hypervisor = p
			break
		}
	}
	if p, err := exec.LookPath("cloud-hypervisor"); err == nil && v.Hypervisor == "" {
		v.Hypervisor = p
	}
	for _, p := range []string{"/usr/lib/virtiofsd", "/usr/libexec/virtiofsd", home + "/.local/share/magi-runner-tools/vm/virtiofsd"} {
		if _, err := os.Stat(p); err == nil {
			v.Virtiofsd = p
		}
	}
	if v.Virtiofsd == "" {
		t.Skip("virtiofsd is not installed")
	}
	return v
}

// A task VM sees the sandbox view, and nothing else of the host. Each TCP
// connection of the guest, also from a tool with no proxy setting, goes to
// the proxy socket as a CONNECT request with the name of the destination.
func TestVM(t *testing.T) {
	vm := vmConfig(t)
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap is not installed")
	}
	self, _ := os.Executable()
	tools := t.TempDir()
	os.WriteFile(tools+"/tool.txt", []byte("tool"), 0o644)
	secret := t.TempDir()
	os.WriteFile(secret+"/key", []byte("do not read"), 0o600)
	root := filepath.Join(t.TempDir(), strings.Repeat("d", 60))
	s := &sandbox.Sandboxes{Cfg: sandbox.Config{Root: root, Envd: self, EnvdArgs: []string{"envd"}, Bwrap: bwrap,
		ReadOnly: []string{tools}, Env: map[string]string{"FROM_CONFIG": "1"}, VM: vm}}
	id := "run_" + strings.Repeat("v", 26)
	if err := s.Start(id, strings.Repeat("t", 32), map[string]string{"HTTPS_PROXY": "http://env:cred@" + sandbox.ProxyAddr},
		&sandbox.Proxy{CA: []byte("magi-test-ca\n"), SSHKnownHosts: []byte("* ssh-ed25519 AAAAtest\n")}); err != nil {
		t.Fatal(err)
	}
	defer s.Remove(id)
	ln, err := s.ListenProxy(id)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 10)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				got <- req.Method + " " + req.RequestURI + " " + req.Header.Get("Proxy-Authorization")
				if req.Method == http.MethodConnect {
					conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\nhello from proxy\n"))
					return
				}
				conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\nvia proxy"))
			}()
		}
	}()
	c, err := s.Client(id, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	run := func(script string) api.ExecResult {
		t.Helper()
		r, err := c.Exec(context.Background(), api.ExecRequest{Argv: []string{"sh", "-c", script}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	host, _ := exec.Command("uname", "-r").Output()
	if r := run(`uname -r`); r.ExitCode != 0 || string(r.Stdout) == string(host) {
		t.Fatalf("not a VM kernel: %q %q", r.Stdout, r.Stderr)
	}
	if r := run(`getent ahostsv4 example.org`); !strings.HasPrefix(string(r.Stdout), "198.18.") {
		t.Fatalf("guest DNS: %q %q", r.Stdout, r.Stderr)
	}
	if r := run(`echo "$HOME $FROM_CONFIG"; cat ` + tools + `/tool.txt; echo hi > /work/out; pwd`); r.ExitCode != 0 ||
		string(r.Stdout) != "/work/home 1\ntool/work\n" {
		t.Fatalf("exec = %d %q %q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if b, _ := os.ReadFile(s.WorkDir(id) + "/out"); string(b) != "hi\n" {
		t.Fatalf("work dir: %q", b)
	}
	if r := run(`cat ` + secret + `/key`); r.ExitCode == 0 {
		t.Fatalf("the VM read a host file: %q", r.Stdout)
	}
	if r := run(`touch ` + tools + `/new /usr/new`); r.ExitCode == 0 {
		t.Fatal("the VM wrote a read-only directory")
	}
	if r := run(`cat ` + sandbox.ProxyCA + `; grep -l magi-test-ca /etc/ssl/certs/ca-certificates.crt >/dev/null && echo trusted`); !strings.HasPrefix(string(r.Stdout), "magi-test-ca\n") {
		t.Fatalf("CA in the VM: %q %q", r.Stdout, r.Stderr)
	} else if _, err := os.Stat("/etc/ssl/certs/ca-certificates.crt"); err == nil && !strings.Contains(string(r.Stdout), "trusted") {
		t.Fatalf("the system trust store of the VM has no proxy CA: %q", r.Stdout)
	}
	if r := run(`cat /etc/ssh/ssh_known_hosts`); string(r.Stdout) != "* ssh-ed25519 AAAAtest\n" {
		t.Fatalf("known_hosts in the VM: %q %q", r.Stdout, r.Stderr)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	// A plain socket with no proxy setting: the name reaches the proxy.
	r := run(`python3 -c 'import socket; s = socket.create_connection(("example.org", 443), 10); print(s.recv(100).decode(), end="")'`)
	if string(r.Stdout) != "hello from proxy\n" {
		t.Fatalf("transparent route: %d %q %q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if line := <-got; line != "CONNECT example.org:443 Basic ZW52OmNyZWQ=" {
		t.Fatalf("the proxy got %q", line)
	}
	// An address of the host network also goes to the proxy, not to the
	// host.
	hl, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hl.Close()
	accepted := make(chan bool, 1)
	go func() {
		if c, err := hl.Accept(); err == nil {
			c.Close()
			accepted <- true
		}
	}()
	port := hl.Addr().(*net.TCPAddr).Port
	addr := hostAddr(t)
	r = run(`python3 -c 'import socket; s = socket.create_connection(("` + addr + `", ` + strconv.Itoa(port) + `), 10); print(s.recv(100).decode(), end="")'`)
	if string(r.Stdout) != "hello from proxy\n" {
		t.Fatalf("host address: %d %q %q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if line := <-got; line != "CONNECT "+net.JoinHostPort(addr, strconv.Itoa(port))+" Basic ZW52OmNyZWQ=" {
		t.Fatalf("the proxy got %q", line)
	}
	select {
	case <-accepted:
		t.Fatal("the VM reached the host network")
	default:
	}
	// A tool with the proxy setting uses the proxy port.
	if _, err := exec.LookPath("curl"); err == nil {
		r = run(`curl -sS --max-time 5 -x http://` + sandbox.ProxyAddr + ` http://example.invalid/x`)
		if string(r.Stdout) != "via proxy" {
			t.Fatalf("proxy port: %d %q %q", r.ExitCode, r.Stdout, r.Stderr)
		}
		if line := <-got; line != "GET http://example.invalid/x " {
			t.Fatalf("the proxy got %q", line)
		}
	}
	if err := s.Remove(id); err != nil || s.Exists(id) {
		t.Fatalf("remove: %v", err)
	}
}

// hostAddr returns a non-loopback IPv4 address of this host.
func hostAddr(t *testing.T) string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	t.Skip("no IPv4 address on this host")
	return ""
}
