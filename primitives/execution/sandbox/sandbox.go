// Package sandbox runs envd for one environment in a sandbox on this
// machine: a Cloud Hypervisor VM (see VM) or bubblewrap on Linux, Seatbelt
// (sandbox-exec) on macOS.
//
// The sandbox sees only its work directory, read-only system directories,
// and the directories in Config.ReadOnly and Config.Shared. It does not see
// the home directory of the user that runs it, so it cannot read that
// user's credentials. Nothing here listens on the network: a runner calls
// envd and connects outward to Magi.
//
// With bubblewrap, envd listens on a unix socket in the state directory.
// An environment that starts with a Proxy has its own network namespace
// with no real interface; its one route out is the unix socket ProxySocket
// on the host, which the runner sends to the egress proxy. Commands reach
// it at the port ProxyAddr, and also with no proxy settings: "envd netns"
// sets up the network namespace as in a task VM (a firewall sends each TCP
// connection to envd, and envd answers DNS), and then starts bubblewrap
// in it, with a user namespace of its own, so commands have no capability
// in the network namespace. Without a Proxy the sandbox shares the host
// network. Seatbelt shares the host network, and envd listens on loopback.
//
// With bubblewrap the work directory is /work in the sandbox. Seatbelt
// cannot move directories, so there the sandbox sees the host path. Use
// Work for the path that commands in the sandbox see.
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/primitives/execution/envd"
)

// Config is the sandbox configuration.
type Config struct {
	// Root holds one directory per environment.
	Root string
	// Envd is the envd binary. It is mounted read-only in the sandbox.
	// EnvdArgs are put before the envd flags (for example ["envd"] when
	// the binary is a command that also runs as envd).
	Envd     string
	EnvdArgs []string
	// Bwrap is the bubblewrap binary (Linux).
	Bwrap string
	// SandboxExec is the sandbox-exec binary (macOS). When it is set,
	// environments use Seatbelt instead of bubblewrap.
	SandboxExec string
	// ReadOnly lists host paths that the sandbox sees read-only at the same
	// path, for example a toolchain directory.
	ReadOnly []string
	// Shared lists host paths that the sandbox can write, shared by all
	// environments, for example a Go module cache.
	Shared []string
	// Env is the base environment of envd.
	Env map[string]string
	// VM makes each environment a task VM (Linux). Bwrap is still needed:
	// virtiofsd runs in bubblewrap.
	VM *VM
}

// Proxy is the egress proxy of an environment (see the package comment).
type Proxy struct {
	// CA is the CA certificate of the proxy (PEM). Commands see it at
	// ProxyCA.
	CA []byte
	// SSHKnownHosts is the known_hosts file for the SSH host key of the
	// proxy. A VM sees it at /etc/ssh/ssh_known_hosts.
	SSHKnownHosts []byte
}

const (
	// ProxyAddr is the proxy port in a sandbox with a Proxy.
	ProxyAddr = "127.0.0.1:3128"
	// ProxyCA is the path of the proxy CA certificate in the sandbox.
	ProxyCA = "/run/magi/egress/ca.pem"
)

// ErrNoProxy means that this sandbox kind cannot isolate the network.
var ErrNoProxy = errors.New("seatbelt sandboxes cannot use a proxy: they share the host network")

// Sandboxes starts and removes environments.
type Sandboxes struct {
	Cfg  Config
	mu   sync.Mutex
	cmds map[string]*exec.Cmd
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Dir is the host directory of an environment.
func (s *Sandboxes) Dir(id string) string { return filepath.Join(s.Cfg.Root, id) }

// WorkDir is the host directory of the work directory of an environment.
func (s *Sandboxes) WorkDir(id string) string { return filepath.Join(s.Dir(id), "work") }

// Work is the path of the work directory in the sandbox.
func (s *Sandboxes) Work(id string) string {
	if s.macVM() {
		return macWork
	}
	if s.Cfg.SandboxExec != "" {
		// The real path: Seatbelt rules and the working directory of
		// commands use it.
		root := s.Cfg.Root
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		return filepath.Join(root, id, "work")
	}
	return "/work"
}

// environ returns the environment variables of envd.
func (s *Sandboxes) environ(id string, env map[string]string) map[string]string {
	tmp := "/tmp"
	if s.Cfg.SandboxExec != "" && !s.macVM() {
		tmp = filepath.Join(s.Dir(id), "tmp")
	}
	all := map[string]string{"HOME": s.Work(id) + "/home", "TMPDIR": tmp, "PATH": "/usr/local/bin:/usr/bin:/bin"}
	for k, v := range s.Cfg.Env {
		all[k] = v
	}
	for k, v := range env {
		all[k] = v
	}
	if s.Cfg.SandboxExec != "" {
		// The /usr/bin tools on macOS are shims that call xcrun, and xcrun
		// writes a cache in the per-user temp directory, which the sandbox
		// does not allow. Use the real tools first.
		if dev := developerPath(); dev != "" {
			all["PATH"] = dev + "/usr/bin:" + dev + "/Toolchains/XcodeDefault.xctoolchain/usr/bin:" + all["PATH"]
		}
	}
	return all
}

// args returns the bubblewrap arguments for one environment.
func (s *Sandboxes) args(d string, env map[string]string) []string {
	a := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--clearenv"}
	proxied := isFile(filepath.Join(d, "egress", "ca.pem"))
	if proxied {
		// "envd netns" made the network namespace (see bwrapCommand). A
		// user namespace of its own takes away the capabilities in it;
		// the IDs stay the IDs of the user.
		a = append(a, "--unshare-user", "--uid", strconv.Itoa(os.Getuid()), "--gid", strconv.Itoa(os.Getgid()))
	}
	// /tmp first: a configured directory can be below /tmp.
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	a = append(a, s.mounts(d, "", proxied)...)
	all := s.environ(filepath.Base(d), env)
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a = append(a, "--setenv", k, all[k])
	}
	a = append(a, "--chdir", "/work", "/run/magi/envd")
	a = append(a, s.Cfg.EnvdArgs...)
	return append(a, s.envdFlags(d, false)...)
}

// envdFlags returns the flags of envd in the sandbox of d.
func (s *Sandboxes) envdFlags(d string, vm bool) []string {
	listen := "unix:/run/magi/state/envd.sock"
	if vm {
		listen = "vsock:" + strconv.Itoa(vmEnvdPort)
	}
	a := []string{"-listen", listen, "-token-file", "/run/magi/token", "-dir", "/work", "-addr-file", "/run/magi/state/addr"}
	if isFile(filepath.Join(d, "egress", "ca.pem")) {
		if vm {
			up := "vsock:2:" + strconv.Itoa(vmProxyPort)
			a = append(a, "-forward", ProxyAddr+"="+up, "-transparent", up)
		} else {
			a = append(a, "-forward", ProxyAddr+"=/run/magi/egress/proxy.sock", "-transparent", "/run/magi/egress/proxy.sock")
		}
	}
	return a
}

// mounts returns the bubblewrap mounts of the sandbox view of d, with
// each path below p. A view with the transparent route (a VM, or a
// sandbox with a Proxy) has its own resolv.conf, the proxy CA in the
// system trust store, and the SSH known_hosts of the proxy, because
// commands reach the proxy also without proxy settings.
func (s *Sandboxes) mounts(d, p string, vm bool) []string {
	a := []string{"--ro-bind", "/usr", p + "/usr"}
	proxied := isFile(filepath.Join(d, "egress", "ca.pem"))
	// Merged-/usr systems link /bin, /lib, and so on into /usr.
	for _, x := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		if t, err := os.Readlink(x); err == nil {
			a = append(a, "--symlink", t, p+x)
		} else if _, err := os.Stat(x); err == nil {
			a = append(a, "--ro-bind", x, p+x)
		}
	}
	// Only the /etc files that programs need for names, users, and TLS, and
	// /etc/alternatives, where Debian links cc and other tools.
	for _, x := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/passwd", "/etc/group",
		"/etc/ssl", "/etc/ca-certificates", "/etc/pki", "/etc/localtime", "/etc/alternatives"} {
		if vm && x == "/etc/resolv.conf" {
			a = append(a, "--ro-bind", filepath.Join(d, "vm", "resolv.conf"), p+x)
			continue
		}
		if _, err := os.Stat(x); err == nil {
			a = append(a, "--ro-bind", x, p+x)
		}
	}
	if vm && proxied {
		for _, b := range trustBundles() {
			a = append(a, "--ro-bind", filepath.Join(d, "vm", "ca-bundle.pem"), p+b)
		}
		if kh := filepath.Join(d, "egress", "ssh_known_hosts"); isFile(kh) {
			a = append(a, "--ro-bind", kh, p+"/etc/ssh/ssh_known_hosts")
		}
	}
	a = append(a, "--bind", filepath.Join(d, "work"), p+"/work",
		"--ro-bind", filepath.Join(d, "token"), p+"/run/magi/token",
		"--bind", filepath.Join(d, "state"), p+"/run/magi/state",
		"--ro-bind", s.Cfg.Envd, p+"/run/magi/envd")
	if proxied {
		a = append(a, "--ro-bind", filepath.Join(d, "egress"), p+"/run/magi/egress")
	}
	for _, x := range s.Cfg.ReadOnly {
		a = append(a, "--ro-bind", x, p+x)
	}
	for _, x := range s.Cfg.Shared {
		a = append(a, "--bind", x, p+x)
	}
	return a
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// Start starts the sandbox of a new environment. token is the secret that
// envd requires. With proxy, the sandbox has no network but the proxy port;
// the caller serves ProxySocket. Start does not wait until envd is ready;
// see Wait.
func (s *Sandboxes) Start(id, token string, env map[string]string, proxy *Proxy) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("bad environment id %q", id)
	}
	if len(token) < 16 {
		return errors.New("the token must have at least 16 bytes")
	}
	if proxy != nil && s.Cfg.SandboxExec != "" {
		return ErrNoProxy
	}
	d := s.Dir(id)
	if _, err := os.Stat(d); err == nil {
		return fmt.Errorf("environment %s exists", id)
	}
	for _, sub := range []string{"work/home", "state", "tmp"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(d, "token"), []byte(token), 0o600); err != nil {
		return err
	}
	if proxy != nil {
		if err := os.Mkdir(filepath.Join(d, "egress"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(d, "egress", "ca.pem"), proxy.CA, 0o644); err != nil {
			return err
		}
		if len(proxy.SSHKnownHosts) > 0 {
			if err := os.WriteFile(filepath.Join(d, "egress", "ssh_known_hosts"), proxy.SSHKnownHosts, 0o644); err != nil {
				return err
			}
		}
	}
	return s.launch(id, env)
}

// CanProxy reports whether this sandbox kind can start with a Proxy.
func (s *Sandboxes) CanProxy() bool { return s.Cfg.SandboxExec == "" }

// Proxied reports whether the environment started with a Proxy.
func (s *Sandboxes) Proxied(id string) bool {
	return isFile(filepath.Join(s.Dir(id), "egress", "ca.pem"))
}

// ListenProxy listens on the proxy socket of an environment that started
// with a Proxy. Each connection is one connection of the sandbox to
// ProxyAddr.
func (s *Sandboxes) ListenProxy(id string) (net.Listener, error) {
	if !s.Proxied(id) {
		return nil, fmt.Errorf("environment %s has no proxy", id)
	}
	dir, name := filepath.Join(s.Dir(id), "egress"), "proxy.sock"
	if s.Cfg.VM != nil {
		// Cloud Hypervisor sends a guest connection to host port P to the
		// socket <vsock socket>_P.
		dir, name = s.vmDir(id), "vsock.sock_"+strconv.Itoa(vmProxyPort)
	}
	os.Remove(filepath.Join(dir, name))
	var ln net.Listener
	err := unixAt(dir, name, func(p string) (err error) {
		ln, err = net.Listen("unix", p)
		return err
	})
	return ln, err
}

// unixAt calls f with a path of the socket name in dir that fits the
// length limit of a unix socket address (about 108 bytes): a deep
// directory is reached through a file descriptor.
func unixAt(dir, name string, f func(path string) error) error {
	if p := filepath.Join(dir, name); len(p) < 100 {
		return f(p)
	}
	if runtime.GOOS != "linux" {
		// No /proc: a short link in /tmp names the directory.
		h := sha256.Sum256([]byte(dir))
		link := "/tmp/magi-" + hex.EncodeToString(h[:6])
		if t, err := os.Readlink(link); err != nil || t != dir {
			os.Remove(link)
			if err := os.Symlink(dir, link); err != nil {
				return err
			}
		}
		return f(link + "/" + name)
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return f(fmt.Sprintf("/proc/self/fd/%d/%s", d.Fd(), name))
}

// Resume starts envd again in an existing environment, for example after
// the runner restarted. The work directory is kept.
func (s *Sandboxes) Resume(id string, env map[string]string) error {
	if s.Alive(id) {
		return nil
	}
	if !s.Exists(id) {
		return fmt.Errorf("environment %s does not exist", id)
	}
	os.Remove(filepath.Join(s.Dir(id), "state", "addr"))
	os.Remove(filepath.Join(s.Dir(id), "exited"))
	return s.launch(id, env)
}

func (s *Sandboxes) launch(id string, env map[string]string) error {
	d := s.Dir(id)
	s.killStale(id)
	var cmd *exec.Cmd
	pgid := 0
	if s.Cfg.VM != nil {
		var err error
		if cmd, err = s.launchVM(id, env); err != nil {
			return err
		}
		pgid = cmd.SysProcAttr.Pgid
		if pgid == 0 {
			pgid = cmd.Process.Pid
		}
	} else {
		if s.Cfg.SandboxExec != "" {
			var err error
			if cmd, err = s.seatbelt(id, env); err != nil {
				return err
			}
		} else {
			var err error
			if cmd, err = s.bwrapCommand(id, env); err != nil {
				return err
			}
		}
		log, err := os.OpenFile(filepath.Join(d, "envd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer log.Close()
		cmd.Stdout, cmd.Stderr = log, log
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Setpgid = true
		if err := cmd.Start(); err != nil {
			return err
		}
		pgid = cmd.Process.Pid
	}
	// pid is the process group: a VM has virtiofsd in its group.
	os.WriteFile(filepath.Join(d, "pid"), []byte(strconv.Itoa(pgid)), 0o600)
	s.mu.Lock()
	if s.cmds == nil {
		s.cmds = map[string]*exec.Cmd{}
	}
	s.cmds[id] = cmd
	s.mu.Unlock()
	go func() {
		cmd.Wait()
		if s.Cfg.VM != nil {
			syscall.Kill(-pgid, syscall.SIGKILL)
		}
		os.WriteFile(filepath.Join(d, "exited"), nil, 0o600)
	}()
	return nil
}

// bwrapCommand is the bubblewrap command of an environment. With a Proxy,
// "envd netns" starts it in a new user and network namespace, where envd
// is root: it sets up the network, and then it runs bubblewrap.
func (s *Sandboxes) bwrapCommand(id string, env map[string]string) (*exec.Cmd, error) {
	d := s.Dir(id)
	if !s.Proxied(id) {
		return exec.Command(s.Cfg.Bwrap, s.args(d, env)...), nil
	}
	if err := s.writeView(id); err != nil {
		return nil, err
	}
	bw, err := exec.LookPath(s.Cfg.Bwrap)
	if err != nil {
		return nil, err
	}
	a := append(append(append([]string{}, s.Cfg.EnvdArgs...), "netns", "--", bw), s.args(d, env)...)
	cmd := exec.Command(s.Cfg.Envd, a...)
	netns(cmd)
	return cmd, nil
}

// Exists reports whether the environment directory exists.
func (s *Sandboxes) Exists(id string) bool {
	_, err := os.Stat(s.Dir(id))
	return idRE.MatchString(id) && err == nil
}

// Alive reports whether envd of the environment runs.
func (s *Sandboxes) Alive(id string) bool {
	s.mu.Lock()
	cmd := s.cmds[id]
	s.mu.Unlock()
	if cmd == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.Dir(id), "exited"))
	return errors.Is(err, os.ErrNotExist)
}

// Token returns the envd token of an environment.
func (s *Sandboxes) Token(id string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "token"))
	return string(b), err
}

// Client returns a client of the envd of an environment once envd
// listens.
func (s *Sandboxes) Client(id string, timeout time.Duration) (*envd.Client, error) {
	base, err := s.Wait(id, timeout)
	if err != nil {
		return nil, err
	}
	tok, err := s.Token(id)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 30 * time.Minute}
	if port, ok := strings.CutPrefix(base, "vsock:"); ok {
		p, err := strconv.Atoi(port)
		if err != nil {
			return nil, fmt.Errorf("envd address %q", base)
		}
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return s.dialVM(ctx, id, p)
		}}
		base = "http://envd"
	} else if path, ok := strings.CutPrefix(base, "unix:"); ok {
		dir, name := filepath.Split(path)
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var c net.Conn
			err := unixAt(dir, name, func(p string) (err error) {
				var d net.Dialer
				c, err = d.DialContext(ctx, "unix", p)
				return err
			})
			return c, err
		}}
		base = "http://envd"
	}
	return &envd.Client{Base: base, Token: tok, HTTP: hc}, nil
}

// Wait returns the envd address once envd listens, or an error: an HTTP
// base URL, "unix:PATH" for a unix socket, or "vsock:PORT" for a port of
// the guest of a VM (see Client).
func (s *Sandboxes) Wait(id string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !s.Alive(id) {
			b, _ := os.ReadFile(filepath.Join(s.Dir(id), "envd.log"))
			msg := strings.TrimSpace(string(b))
			if c := s.consoleTail(id); c != "" {
				msg += "\nconsole:\n" + c
			}
			return "", fmt.Errorf("envd exited: %s", msg)
		}
		if b, err := os.ReadFile(filepath.Join(s.Dir(id), "state", "addr")); err == nil && len(b) > 0 {
			addr := strings.TrimSpace(string(b))
			if _, ok := strings.CutPrefix(addr, "unix:"); ok {
				return "unix:" + filepath.Join(s.Dir(id), "state", "envd.sock"), nil
			}
			if strings.HasPrefix(addr, "vsock:") {
				return addr, nil
			}
			return "http://" + addr, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("envd of %s did not start in %s", id, timeout)
}

// Remove stops the environment and deletes its directory.
func (s *Sandboxes) Remove(id string) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("bad environment id %q", id)
	}
	s.mu.Lock()
	cmd := s.cmds[id]
	delete(s.cmds, id)
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		pgid := cmd.Process.Pid
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.Pgid != 0 {
			pgid = cmd.SysProcAttr.Pgid
		}
		syscall.Kill(-pgid, syscall.SIGKILL)
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(filepath.Join(s.Dir(id), "exited")); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	} else {
		s.killStale(id)
	}
	return os.RemoveAll(s.Dir(id))
}

// List returns the environment ids on disk.
func (s *Sandboxes) List() []string {
	entries, _ := os.ReadDir(s.Cfg.Root)
	var out []string
	for _, e := range entries {
		if e.IsDir() && idRE.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// killStale stops an envd that an earlier runner process started, for
// example after the runner restarted. On Linux bubblewrap stops it with
// its parent; Seatbelt does not.
func (s *Sandboxes) killStale(id string) {
	s.mu.Lock()
	running := s.cmds[id] != nil
	s.mu.Unlock()
	if running {
		return
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "pid"))
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
	os.Remove(filepath.Join(s.Dir(id), "pid"))
}

// seatbelt returns the sandbox-exec command of an environment. The profile
// denies everything, then allows the system, the work, state, and tmp
// directories of the environment, and the configured directories. Other
// files, such as the home directory of the user, stay unreadable; only
// their metadata (names that a path search sees) is visible.
func (s *Sandboxes) seatbelt(id string, env map[string]string) (*exec.Cmd, error) {
	d, err := filepath.EvalSymlinks(s.Dir(id))
	if err != nil {
		return nil, err
	}
	q := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
	}
	var b strings.Builder
	b.WriteString(`(version 1)
(deny default)
(allow process-exec process-fork signal sysctl-read mach-lookup ipc-posix-shm ipc-posix-sem network* file-read-metadata)
(allow pseudo-tty)
(allow file-read* (literal "/") (subpath "/usr") (subpath "/bin") (subpath "/sbin") (subpath "/System") (subpath "/Library")
  (subpath "/private/etc") (subpath "/private/var/db/timezone") (subpath "/opt") (subpath "/dev"))
(allow file-write* (literal "/dev/null") (literal "/dev/zero") (literal "/dev/tty") (regex #"^/dev/fd/") (regex #"^/dev/ttys"))
`)
	fmt.Fprintf(&b, "(allow file-read* file-write* (subpath %s) (subpath %s) (subpath %s))\n",
		q(filepath.Join(d, "work")), q(filepath.Join(d, "state")), q(filepath.Join(d, "tmp")))
	fmt.Fprintf(&b, "(allow file-read* (literal %s) (literal %s))\n", q(filepath.Join(d, "token")), q(s.Cfg.Envd))
	// git, cc, and make in /usr/bin are shims that load the active
	// developer directory, often in /Applications/Xcode.app.
	if dev := developerDir(); dev != "" {
		fmt.Fprintf(&b, "(allow file-read* (subpath %s))\n", q(dev))
	}
	for _, p := range s.Cfg.ReadOnly {
		fmt.Fprintf(&b, "(allow file-read* (subpath %s))\n", q(p))
	}
	for _, p := range s.Cfg.Shared {
		fmt.Fprintf(&b, "(allow file-read* file-write* (subpath %s))\n", q(p))
	}
	args := append([]string{"-p", b.String(), s.Cfg.Envd}, s.Cfg.EnvdArgs...)
	args = append(args, "-listen", "127.0.0.1:0", "-token-file", filepath.Join(d, "token"), "-dir", filepath.Join(d, "work"),
		"-addr-file", filepath.Join(d, "state", "addr"))
	cmd := exec.Command(s.Cfg.SandboxExec, args...)
	cmd.Dir = filepath.Join(d, "work")
	for k, v := range s.environ(id, env) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd, nil
}

// developerPath returns the developer directory that xcode-select names
// (Xcode or the Command Line Tools), or "".
func developerPath() string {
	out, err := exec.Command("xcode-select", "-p").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// developerDir returns the directory that the sandbox must read for the
// developer tools: the .app bundle when the developer directory is in one.
func developerDir() string {
	dev := developerPath()
	if i := strings.Index(dev, ".app/"); i >= 0 {
		dev = dev[:i+4]
	}
	return dev
}
