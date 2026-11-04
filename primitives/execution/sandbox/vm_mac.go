package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/primitives/execution/envd"
)

// macOS task VMs (VM.Template set; Apple silicon only). Each environment
// is an APFS clone of the template (tools/vm/macos), run by magi-vz
// (VM.Hypervisor) with no network card:
//
//   - The guest is a full macOS. It sees the host only through shares at
//     /Volumes/My Shared Files: state, magi (envd, the token, and the
//     proxy CA; read only), and each ReadOnly and Shared directory by its
//     base name. The work directory is on the guest disk (files on a share
//     have no owner, and Git refuses such repositories); use envd to read
//     it. It stays while the environment stays.
//   - launchd starts envd at boot (the boot script of the template). The
//     runner reaches envd over vsock, as with Cloud Hypervisor.
//   - With a Proxy, tools use the proxy port 127.0.0.1:3128, which envd
//     copies over vsock to the host. macOS has no transparent route.
//   - The macOS licence permits at most two macOS VMs at a time on a Mac.

// MaxMacVMs is the number of macOS VMs that can run at a time.
const MaxMacVMs = 2

const macWork = "/Users/Shared/work"

func (s *Sandboxes) macVM() bool { return s.Cfg.VM != nil && s.Cfg.VM.Template != "" }

// StartTimeout is how long envd of a new environment can take to start: a
// macOS guest boots a whole system.
func (s *Sandboxes) StartTimeout() time.Duration {
	if s.macVM() {
		return 4 * time.Minute
	}
	return 30 * time.Second
}

// ProxyCA is the path of the proxy CA certificate that commands in the
// sandbox see.
func (s *Sandboxes) ProxyCA() string {
	if s.macVM() {
		return envd.MacShares + "/magi/egress/ca.pem"
	}
	return ProxyCA
}

// macShare is the name of a configured directory in the guest.
func macShare(p string) string { return filepath.Base(filepath.Clean(p)) }

// macPaths changes each host path of a configured directory in v (for
// example in PATH) to its guest path.
func (s *Sandboxes) macPaths(v string) string {
	dirs := append(append([]string{}, s.Cfg.ReadOnly...), s.Cfg.Shared...)
	// Longer paths first, so that a directory below another one wins.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	parts := strings.Split(v, ":")
	for i, p := range parts {
		for _, d := range dirs {
			if p == d || strings.HasPrefix(p, d+"/") {
				parts[i] = s.MacPath(d) + p[len(d):]
				break
			}
		}
	}
	return strings.Join(parts, ":")
}

// MacPath returns the guest path of a configured host directory.
func (s *Sandboxes) MacPath(p string) string { return envd.MacShares + "/" + macShare(p) }

func (s *Sandboxes) runningMac() int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.cmds))
	for id := range s.cmds {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	n := 0
	for _, id := range ids {
		if s.Alive(id) {
			n++
		}
	}
	return n
}

// clone copies a file as an APFS clone (no data is copied).
func clone(src, dst string) error {
	if out, err := exec.Command("cp", "-c", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("clone %s: %v: %s", src, err, out)
	}
	return nil
}

func (s *Sandboxes) launchMacVM(id string, env map[string]string) (*exec.Cmd, error) {
	if s.runningMac() >= MaxMacVMs {
		return nil, fmt.Errorf("%d macOS VMs run already; the macOS licence permits at most %d", MaxMacVMs, MaxMacVMs)
	}
	d, vd := s.Dir(id), s.vmDir(id)
	machine, share := filepath.Join(vd, "machine"), filepath.Join(vd, "magi")
	for _, p := range []string{vd, machine, share} {
		if err := os.Mkdir(p, 0o700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	for _, f := range []string{"disk.img", "aux.img", "vm.json"} {
		if _, err := os.Stat(filepath.Join(machine, f)); errors.Is(err, os.ErrNotExist) {
			if err := clone(filepath.Join(s.Cfg.VM.Template, f), filepath.Join(machine, f)); err != nil {
				return nil, err
			}
		}
	}
	os.Remove(filepath.Join(share, "envd"))
	if err := clone(s.Cfg.Envd, filepath.Join(share, "envd")); err != nil {
		return nil, err
	}
	tok, err := os.ReadFile(filepath.Join(d, "token"))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(share, "token"), tok, 0o600); err != nil {
		return nil, err
	}
	if s.Proxied(id) {
		os.MkdirAll(filepath.Join(share, "egress"), 0o700)
		ca, err := os.ReadFile(filepath.Join(d, "egress", "ca.pem"))
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(share, "egress", "ca.pem"), ca, 0o644); err != nil {
			return nil, err
		}
	}
	m := envd.MacShares
	flags := []string{"-listen", "vsock:" + strconv.Itoa(vmEnvdPort), "-token-file", m + "/magi/token", "-dir", macWork,
		"-addr-file", m + "/state/addr"}
	if s.Proxied(id) {
		flags = append(flags, "-forward", ProxyAddr+"=vsock:2:"+strconv.Itoa(vmProxyPort))
	}
	all := s.environ(id, env)
	// Host paths of shared directories become their guest paths.
	for k, v := range all {
		all[k] = s.macPaths(v)
	}
	all["PATH"] += ":/usr/sbin:/sbin"
	// The real path: /tmp is a link to /private/tmp, and tools that
	// compare paths see the real one.
	all["TMPDIR"] = "/private/tmp"
	b, err := json.Marshal(envd.VMConfigFile{Args: flags, Env: all})
	if err != nil {
		return nil, err
	}
	os.Remove(filepath.Join(d, "state", "addr"))
	if err := os.WriteFile(filepath.Join(d, "state", "vm.json"), b, 0o600); err != nil {
		return nil, err
	}
	a := []string{"run", "--dir", machine, "--sockets", vd, "--host-port", strconv.Itoa(vmProxyPort),
		"--share", "state=" + filepath.Join(d, "state"), "--share", "magi=" + share + ":ro"}
	for _, p := range s.Cfg.ReadOnly {
		a = append(a, "--share", macShare(p)+"="+p+":ro")
	}
	for _, p := range s.Cfg.Shared {
		a = append(a, "--share", macShare(p)+"="+p)
	}
	log, err := os.OpenFile(filepath.Join(d, "envd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := exec.Command(s.Cfg.VM.Hypervisor, a...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("magi-vz: %w", err)
	}
	return cmd, nil
}
