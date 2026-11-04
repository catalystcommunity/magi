package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/primitives/execution/envd"
)

// VM is the configuration of task VMs (Cloud Hypervisor, Linux). When it
// is set, each environment is a VM:
//
//   - The guest root is the sandbox view of bubblewrap, which virtiofsd
//     shares from inside a bubblewrap sandbox of its own. The guest sees
//     the same files as a bubblewrap sandbox, and nothing else of the host.
//   - envd is the init of the guest. The runner reaches it over vsock.
//   - The guest has no network card. With a Proxy, envd sends each TCP
//     connection of the guest over vsock to ProxySocket, also from tools
//     that have no proxy setting, and the guest trusts the proxy CA. With
//     no Proxy the guest has no network.
type VM struct {
	// Hypervisor is the cloud-hypervisor binary.
	Hypervisor string
	// Kernel is the guest kernel (tools/vm/build-kernel.sh): vmlinux on
	// x86_64, Image on arm64.
	Kernel string
	// Virtiofsd is the virtiofsd binary.
	Virtiofsd string
	// CPUs and MemoryMB size each VM (defaults 2 and 4096).
	CPUs     int
	MemoryMB int
	// Template is the macOS template directory (disk.img, aux.img,
	// vm.json; tools/vm/macos). When it is set, the VMs are macOS guests
	// and Hypervisor is magi-vz (see vm_mac.go).
	Template string
}

const (
	// vmEnvdPort is the vsock port of envd in the guest.
	vmEnvdPort = 1024
	// vmProxyPort is the host vsock port that the guest sends proxy
	// connections to.
	vmProxyPort = 3128
	// vmCID is the vsock address of the guest.
	vmCID = 3
)

// trustBundles returns the paths of the system CA bundle on this host,
// with links resolved.
func trustBundles() []string {
	var out []string
	seen := map[string]bool{}
	for _, b := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"} {
		r, err := filepath.EvalSymlinks(b)
		if err != nil || seen[r] || !isFile(r) {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// vmDir is the host directory of the VM files of an environment: the
// sockets, the console log, and the files of the guest view.
func (s *Sandboxes) vmDir(id string) string { return filepath.Join(s.Dir(id), "vm") }

// prepareVM writes the files of the guest: the envd configuration, the
// resolv.conf, and the CA bundle with the proxy CA.
func (s *Sandboxes) prepareVM(id string, env map[string]string) error {
	d, vd := s.Dir(id), s.vmDir(id)
	// Mkdir, not MkdirAll: an environment that was removed stays removed.
	if err := os.Mkdir(vd, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	for _, f := range []string{"fs.sock", "vsock.sock"} {
		os.Remove(filepath.Join(vd, f))
	}
	if err := s.writeView(id); err != nil {
		return err
	}
	b, err := json.Marshal(envd.VMConfigFile{Args: s.envdFlags(d, true), Env: s.environ(id, env)})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "state", "vm.json"), b, 0o600)
}

// writeView writes the files of a view with the transparent route (see
// mounts) to the vm directory: the resolv.conf and the CA bundle with the
// proxy CA.
func (s *Sandboxes) writeView(id string) error {
	d, vd := s.Dir(id), s.vmDir(id)
	if err := os.Mkdir(vd, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := os.WriteFile(filepath.Join(vd, "resolv.conf"), []byte("nameserver 127.0.0.53\noptions edns0\n"), 0o644); err != nil {
		return err
	}
	if s.Proxied(id) {
		ca, err := os.ReadFile(filepath.Join(d, "egress", "ca.pem"))
		if err != nil {
			return err
		}
		var bundle []byte
		if bs := trustBundles(); len(bs) > 0 {
			if bundle, err = os.ReadFile(bs[0]); err != nil {
				return err
			}
		}
		bundle = append(append(bundle, '\n'), ca...)
		if err := os.WriteFile(filepath.Join(vd, "ca-bundle.pem"), bundle, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// virtiofsdArgs returns the bubblewrap command that runs virtiofsd for the
// guest root. virtiofsd sees only the view at /view, its own binary and
// libraries, and the socket directory.
func (s *Sandboxes) virtiofsdArgs(id string) []string {
	d := s.Dir(id)
	a := []string{"--die-with-parent", "--new-session", "--unshare-all", "--clearenv", "--ro-bind", "/usr", "/usr"}
	for _, x := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		if t, err := os.Readlink(x); err == nil {
			a = append(a, "--symlink", t, x)
		} else if _, err := os.Stat(x); err == nil {
			a = append(a, "--ro-bind", x, x)
		}
	}
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		"--ro-bind", s.Cfg.VM.Virtiofsd, "/run/virtiofsd", "--bind", s.vmDir(id), "/run/vfs", "--tmpfs", "/view")
	for _, x := range []string{"/proc", "/dev", "/sys", "/root"} {
		a = append(a, "--dir", "/view"+x)
	}
	// /tmp is a host directory of the environment (not a guest tmpfs), so
	// that a configured directory below /tmp stays visible.
	a = append(a, "--bind", filepath.Join(d, "tmp"), "/view/tmp")
	a = append(a, s.mounts(d, "/view", true)...)
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	return append(a, "--remount-ro", "/view", "/run/virtiofsd", "--socket-path=/run/vfs/fs.sock", "--shared-dir=/view",
		"--sandbox=none", "--cache=auto", "--translate-uid=map:0:"+uid+":1", "--translate-gid=map:0:"+gid+":1")
}

// hypervisorArgs returns the cloud-hypervisor arguments of the VM. They
// run in the VM directory, so that the socket paths are short.
func (s *Sandboxes) hypervisorArgs() []string {
	v := s.Cfg.VM
	cpus, mem := v.CPUs, v.MemoryMB
	if cpus <= 0 {
		cpus = 2
	}
	if mem <= 0 {
		mem = 4096
	}
	cmdline := "console=hvc0 root=root rootfstype=virtiofs rw quiet panic=-1 init=/run/magi/envd -- " +
		strings.Join(append(append([]string{}, s.Cfg.EnvdArgs...), "-vm"), " ")
	return []string{"--kernel", v.Kernel, "--cmdline", cmdline,
		"--cpus", "boot=" + strconv.Itoa(cpus), "--memory", "size=" + strconv.Itoa(mem) + "M,shared=on",
		"--fs", "tag=root,socket=fs.sock", "--vsock", "cid=" + strconv.Itoa(vmCID) + ",socket=vsock.sock",
		"--console", "file=console.log", "--serial", "off"}
}

// launchVM starts virtiofsd and the VM of an environment. Both are in one
// process group, the group of virtiofsd.
func (s *Sandboxes) launchVM(id string, env map[string]string) (*exec.Cmd, error) {
	if s.macVM() {
		return s.launchMacVM(id, env)
	}
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("task VMs with Cloud Hypervisor need Linux")
	}
	if err := s.prepareVM(id, env); err != nil {
		return nil, err
	}
	vd := s.vmDir(id)
	log, err := os.OpenFile(filepath.Join(s.Dir(id), "envd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	fsd := exec.Command(s.Cfg.Bwrap, s.virtiofsdArgs(id)...)
	fsd.Stdout, fsd.Stderr = log, log
	fsd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := fsd.Start(); err != nil {
		return nil, fmt.Errorf("virtiofsd: %w", err)
	}
	go fsd.Wait()
	for i := 0; !exists(filepath.Join(vd, "fs.sock")); i++ {
		if i == 200 {
			syscall.Kill(-fsd.Process.Pid, syscall.SIGKILL)
			return nil, fmt.Errorf("virtiofsd did not start; see %s", filepath.Join(s.Dir(id), "envd.log"))
		}
		time.Sleep(25 * time.Millisecond)
	}
	ch := exec.Command(s.Cfg.VM.Hypervisor, s.hypervisorArgs()...)
	ch.Dir = vd
	ch.Stdout, ch.Stderr = log, log
	ch.SysProcAttr = vmProcAttr(fsd.Process.Pid)
	if err := ch.Start(); err != nil {
		syscall.Kill(-fsd.Process.Pid, syscall.SIGKILL)
		return nil, fmt.Errorf("cloud-hypervisor: %w", err)
	}
	return ch, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// dialVM connects to a vsock port of the guest of an environment through
// the hybrid vsock socket of Cloud Hypervisor.
func (s *Sandboxes) dialVM(ctx context.Context, id string, port int) (net.Conn, error) {
	var c net.Conn
	err := unixAt(s.vmDir(id), "vsock.sock", func(p string) (err error) {
		var d net.Dialer
		c, err = d.DialContext(ctx, "unix", p)
		return err
	})
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	} else {
		c.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err != nil {
		c.Close()
		return nil, err
	}
	// The answer is "OK <host port>\n". Read it one byte at a time, so that
	// no bytes of the stream are read here.
	var line []byte
	b := make([]byte, 1)
	for len(line) < 64 {
		if _, err := c.Read(b); err != nil {
			c.Close()
			return nil, fmt.Errorf("vsock connect: %w", err)
		}
		if b[0] == '\n' {
			break
		}
		line = append(line, b[0])
	}
	if !strings.HasPrefix(string(line), "OK ") {
		c.Close()
		return nil, fmt.Errorf("vsock connect: %q", line)
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

// consoleTail returns the last lines of the guest console.
func (s *Sandboxes) consoleTail(id string) string {
	f, err := os.Open(filepath.Join(s.vmDir(id), "console.log"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > 20 {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n")
}
