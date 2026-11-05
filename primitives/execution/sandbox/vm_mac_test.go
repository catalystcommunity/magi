//go:build darwin

package sandbox_test

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
)

// A macOS task VM: a clone of the template, envd from the share, the
// proxy port over vsock. MAGI_TEST_MAC_TEMPLATE is the template directory.
func TestMacVM(t *testing.T) {
	tpl := os.Getenv("MAGI_TEST_MAC_TEMPLATE")
	if tpl == "" {
		t.Skip("MAGI_TEST_MAC_TEMPLATE is not set")
	}
	home, _ := os.UserHomeDir()
	self, _ := os.Executable()
	tools := t.TempDir()
	os.WriteFile(tools+"/tool.txt", []byte("tool"), 0o644)
	// A deep root: the socket paths are longer than the unix address limit.
	base := filepath.Join(home, ".cache", "magi-test-macvm")
	root := filepath.Join(base, strings.Repeat("d", 60), strings.Repeat("e", 30))
	os.RemoveAll(base)
	defer os.RemoveAll(base)
	s := &sandbox.Sandboxes{Cfg: sandbox.Config{Root: root, Envd: self, EnvdArgs: []string{"envd"}, ReadOnly: []string{tools},
		Env: map[string]string{"TOOL_FILE": tools + "/tool.txt", "PATH": tools + "/bin:/usr/bin:/bin"},
		VM:  &sandbox.VM{Template: tpl, Hypervisor: home + "/.local/share/magi-runner-tools/vm/magi-vz"}}}
	if err := s.Start("mac1", strings.Repeat("t", 32), map[string]string{"FROM_SPEC": "2", "HTTPS_PROXY": "http://env:cred@" + sandbox.ProxyAddr}, &sandbox.Proxy{CA: []byte("magi-test-ca\n"), SSHKnownHosts: []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMagiProxyTestKey\n")}); err != nil {
		t.Fatal(err)
	}
	defer s.Remove("mac1")
	ln, err := s.ListenProxy("mac1")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				if !strings.Contains(req.RequestURI, "example.") {
					// DNS queries that envd does not answer, and the
					// background connections of macOS (the transparent
					// route sends them to the proxy too).
					c.Write([]byte("HTTP/1.1 502 Not here\r\nContent-Length: 0\r\n\r\n"))
					return
				}
				got <- req.Method + " " + req.RequestURI
				if req.Method == http.MethodConnect {
					// The transparent route: then the request of the tool.
					c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
					if _, err := http.ReadRequest(br); err != nil {
						return
					}
					c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 15\r\nConnection: close\r\n\r\nvia transparent"))
					return
				}
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\nvia proxy"))
			}()
		}
	}()
	c, err := s.Client("mac1", 8*time.Minute)
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(s.Dir("mac1"), "envd.log"))
		g, _ := os.ReadFile(filepath.Join(s.Dir("mac1"), "state", "guest.log"))
		t.Fatalf("%v\nhost log: %s\nguest log: %s", err, b, g)
	}
	run := func(script string) api.ExecResult {
		t.Helper()
		r, err := c.Exec(context.Background(), api.ExecRequest{Argv: []string{"/bin/sh", "-c", script}})
		if err != nil {
			b, _ := os.ReadFile(filepath.Join(s.Dir("mac1"), "envd.log"))
			g, _ := os.ReadFile(filepath.Join(s.Dir("mac1"), "state", "guest.log"))
			t.Fatalf("%v\nhost log: %s\nguest log: %s", err, b, g)
		}
		return r
	}
	if r := run(`sw_vers -productVersion; echo "$FROM_SPEC"; pwd; cat "` + s.MacPath(tools) + `/tool.txt"; echo hi > out`); r.ExitCode != 0 ||
		!strings.HasSuffix(string(r.Stdout), "2\n/Users/Shared/work\ntool") {
		t.Fatalf("exec = %d %q %q", r.ExitCode, r.Stdout, r.Stderr)
	} else {
		t.Logf("guest: %q", r.Stdout)
	}
	if r := run(`cat "$TOOL_FILE"; echo; echo "$PATH"`); !strings.HasPrefix(string(r.Stdout), "tool\n"+s.MacPath(tools)+"/bin:") {
		t.Fatalf("host paths in the environment: %q %q", r.Stdout, r.Stderr)
	}
	if r := run(`cat /Users/Shared/work/out; stat -f %Su /Users/Shared/work`); string(r.Stdout) != "hi\nroot\n" {
		t.Fatalf("work dir on the guest disk: %q %q", r.Stdout, r.Stderr)
	}
	// Xcode is in the template (TestMacProvision), so git starts at once.
	if r := run(`set -e; cd /Users/Shared/work; git init -q g; cd g; git config user.name x; git config user.email x@x; git commit -q --allow-empty -m one; cd ..; git bundle create -q b.bundle -C g --all 2>/dev/null || (cd g && git bundle create -q ../b.bundle --all); git clone -q b.bundle c; cd c; git checkout -q -b t; git rev-parse --is-inside-work-tree`); string(r.Stdout) != "true\n" {
		t.Fatalf("git in the work share: %d %q %q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if r := run(`cat "` + s.ProxyCA() + `"`); string(r.Stdout) != "magi-test-ca\n" {
		t.Fatalf("CA: %q %q", r.Stdout, r.Stderr)
	}
	if r := run(`curl -sS --max-time 5 -x http://` + sandbox.ProxyAddr + ` http://example.invalid/x`); string(r.Stdout) != "via proxy" {
		g, _ := os.ReadFile(filepath.Join(s.Dir("mac1"), "state", "guest.log"))
		r2 := run(`tail -30 /var/log/magi-envd.log; netstat -an -p tcp | head -20; pfctl -s state 2>/dev/null | head`)
		t.Fatalf("proxy port: %d %q %q\nguest log: %s\n%s", r.ExitCode, r.Stdout, r.Stderr, g, r2.Stdout)
	}
	if line := <-got; line != "GET http://example.invalid/x" {
		t.Fatalf("the proxy got %q", line)
	}
	// The transparent route: a tool with no proxy settings, and a name
	// from the DNS of envd.
	if r := run(`dscacheutil -q host -a name example.invalid | grep ip_address`); !strings.Contains(string(r.Stdout), "198.18.") {
		t.Fatalf("guest DNS: %q %q", r.Stdout, r.Stderr)
	}
	if r := run(`curl -sS --max-time 10 --noproxy '*' http://example.invalid/t`); string(r.Stdout) != "via transparent" {
		b, _ := os.ReadFile(filepath.Join(s.Dir("mac1"), "envd.log"))
		t.Fatalf("transparent: %d %q %q\n%s", r.ExitCode, r.Stdout, r.Stderr, b)
	}
	if line := <-got; line != "CONNECT example.invalid:80" {
		t.Fatalf("the proxy got %q", line)
	}
	if r := run(`grep "the first destination" /var/log/magi-envd.log | tail -1; cat /etc/ssh/ssh_known_hosts`); !strings.Contains(string(r.Stdout), "MagiProxyTestKey") {
		t.Fatalf("known hosts: %q %q", r.Stdout, r.Stderr)
	} else {
		t.Logf("guest: %s", r.Stdout)
	}
	// No other way out: a connection that the proxy does not complete fails.
	if r := run(`curl -sS --max-time 5 https://example.org/ -o /dev/null`); r.ExitCode == 0 {
		t.Fatal("the VM reached the network directly")
	}
	if err := s.Remove("mac1"); err != nil || s.Exists("mac1") {
		t.Fatalf("remove: %v", err)
	}
}

// Provisions the macOS template for iOS work, in a clone: the host Xcode
// (shared read only) is copied into the guest, which then accepts its
// licence and installs its first-launch packages, and the simulator
// runtime in MAGI_MAC_RUNTIME_DMG is added. Xcode is in the guest disk, not
// a share: macOS checks the signature of the whole bundle at the first
// start of a program in it (about two minutes over the share), and the
// check of the template stays valid in each clone.
// When each step works, the clone becomes the template. Run it only on
// purpose: MAGI_MAC_PROVISION=1.
func TestMacProvision(t *testing.T) {
	tpl, dmg := os.Getenv("MAGI_TEST_MAC_TEMPLATE"), os.Getenv("MAGI_MAC_RUNTIME_DMG")
	if os.Getenv("MAGI_MAC_PROVISION") != "1" || tpl == "" || dmg == "" {
		t.Skip("MAGI_MAC_PROVISION=1, MAGI_TEST_MAC_TEMPLATE, and MAGI_MAC_RUNTIME_DMG are needed")
	}
	home, _ := os.UserHomeDir()
	self, _ := os.Executable()
	root := filepath.Join(home, ".cache", "magi-test-macprov")
	os.RemoveAll(root)
	defer os.RemoveAll(root)
	// The share gives symlinks badly ("too many levels of symbolic
	// links"), so Xcode goes to the guest as a tar archive.
	ver, err := exec.Command("defaults", "read", "/Applications/Xcode.app/Contents/Info.plist", "CFBundleVersion").Output()
	if err != nil {
		t.Fatal(err)
	}
	xdir := filepath.Join(home, ".cache", "magi-xcode")
	archive := filepath.Join(xdir, "Xcode-"+strings.TrimSpace(string(ver))+".tar")
	if _, err := os.Stat(archive); err != nil {
		os.RemoveAll(xdir)
		os.MkdirAll(xdir, 0o755)
		if out, err := exec.Command("tar", "-cf", archive+".part", "-C", "/Applications", "Xcode.app").CombinedOutput(); err != nil {
			t.Fatalf("tar: %v %s", err, out)
		}
		os.Rename(archive+".part", archive)
	}
	s := &sandbox.Sandboxes{Cfg: sandbox.Config{Root: root, Envd: self, EnvdArgs: []string{"envd"},
		ReadOnly: []string{xdir, filepath.Dir(dmg)},
		VM:       &sandbox.VM{Template: tpl, Hypervisor: home + "/.local/share/magi-runner-tools/vm/magi-vz"}}}
	if err := s.Start("prov", strings.Repeat("t", 32), nil, nil); err != nil {
		t.Fatal(err)
	}
	defer s.Remove("prov")
	c, err := s.Client("prov", 8*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	src := s.MacPath(xdir) + "/" + filepath.Base(archive)
	share := s.MacPath(filepath.Dir(dmg)) + "/" + filepath.Base(dmg)
	timeout := uint64(60 * 60 * 1000)
	for _, step := range []string{
		`set -e; rm -rf /Applications/Xcode.app; tar -xf "` + src + `" -C /Applications; ls /Applications/Xcode.app/Contents/Developer/usr/bin/git`,
		`xcode-select -s /Applications/Xcode.app/Contents/Developer`,
		`xcodebuild -version && git --version && xcrun swift --version`,
		`xcodebuild -license accept`,
		`xcodebuild -runFirstLaunch`,
		`xcrun simctl runtime list | grep -q "(Ready)" || xcrun simctl runtime add "` + share + `"`,
		`xcrun simctl runtime list`,
		`id=$(xcrun simctl create magi-check "iPhone 16" 2>/dev/null || xcrun simctl create magi-check "$(xcrun simctl list devicetypes | awk -F' [(]' '/iPhone/{print $1; exit}')") && xcrun simctl boot "$id" && xcrun simctl bootstatus "$id" -b && xcrun simctl shutdown "$id" && xcrun simctl delete "$id" && echo simulator ok`,
	} {
		r, err := c.Exec(context.Background(), api.ExecRequest{Argv: []string{"/bin/sh", "-c", step}, TimeoutMs: &timeout})
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		t.Logf("%s: exit %d\n%s%s", step, r.ExitCode, tail(r.Stdout), tail(r.Stderr))
		if r.ExitCode != 0 {
			t.Fatalf("%s failed", step)
		}
	}
	c.Exec(context.Background(), api.ExecRequest{Argv: []string{"/sbin/shutdown", "-h", "now"}})
	for i := 0; s.Alive("prov"); i++ {
		if i > 600 {
			t.Fatal("the guest did not stop")
		}
		time.Sleep(time.Second)
	}
	m := filepath.Join(s.Dir("prov"), "vm", "machine")
	for _, f := range []string{"disk.img", "aux.img"} {
		if err := os.Rename(filepath.Join(m, f), filepath.Join(tpl, f+".new")); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"disk.img", "aux.img"} {
		os.Rename(filepath.Join(tpl, f), filepath.Join(tpl, f+".old"))
		if err := os.Rename(filepath.Join(tpl, f+".new"), filepath.Join(tpl, f)); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("the template is provisioned; the old disk is disk.img.old")
}

func tail(b []byte) string {
	if len(b) > 1500 {
		b = b[len(b)-1500:]
	}
	return string(b)
}

// Runs MAGI_MAC_RUN in a clone of the template, with the host directories
// in MAGI_MAC_SHARES (separated by ":") shared read only. A tool for
// template work and for debugging.
func TestMacRun(t *testing.T) {
	tpl, script := os.Getenv("MAGI_TEST_MAC_TEMPLATE"), os.Getenv("MAGI_MAC_RUN")
	if tpl == "" || script == "" {
		t.Skip("MAGI_TEST_MAC_TEMPLATE and MAGI_MAC_RUN are needed")
	}
	home, _ := os.UserHomeDir()
	self, _ := os.Executable()
	root := filepath.Join(home, ".cache", "magi-test-macrun")
	os.RemoveAll(root)
	defer os.RemoveAll(root)
	var ro []string
	if v := os.Getenv("MAGI_MAC_SHARES"); v != "" {
		ro = strings.Split(v, ":")
	}
	s := &sandbox.Sandboxes{Cfg: sandbox.Config{Root: root, Envd: self, EnvdArgs: []string{"envd"}, ReadOnly: ro,
		VM: &sandbox.VM{Template: tpl, Hypervisor: home + "/.local/share/magi-runner-tools/vm/magi-vz"}}}
	if err := s.Start("run", strings.Repeat("t", 32), nil, nil); err != nil {
		t.Fatal(err)
	}
	defer s.Remove("run")
	c, err := s.Client("run", 8*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	timeout := uint64(60 * 60 * 1000)
	r, err := c.Exec(context.Background(), api.ExecRequest{Argv: []string{"/bin/sh", "-c", script}, TimeoutMs: &timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exit %d\n%s%s", r.ExitCode, tail(r.Stdout), tail(r.Stderr))
}
