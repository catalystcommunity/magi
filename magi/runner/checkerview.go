package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// The checker command runs on the runner host, as the user of the runner.
// It runs code of the change, and it is a program of an outside company.
// So the runner gives it a view of the host (bubblewrap) that has the
// system programs, the work directory of the check, and the paths that the
// configuration names. The home directory of the user is empty in the
// view: no keys, no repositories, no shell history. The view keeps the
// network, because the command calls its service. The environment
// variables of the runner do not go to the command.

// checkerEtc are the files of /etc that a program needs to resolve names
// and to check certificates.
var checkerEtc = []string{"resolv.conf", "hosts", "nsswitch.conf", "passwd", "group", "ssl", "ca-certificates", "pki",
	"alternatives", "ld.so.cache", "localtime"}

// checkerView gives the command that runs argv in the view. dir is the
// directory of the check (read-write). writable and readOnly are the paths
// of the configuration. The command gets the environment of the bubblewrap
// process, so the caller gives that process only the checker environment:
// a value in the argument list of bubblewrap is public on the host.
func checkerView(bwrap, home, dir string, argv, writable, readOnly []string) ([]string, error) {
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, err
	}
	// The command can be a link into a directory that the view does not
	// have (~/.local/bin). The view gets the file itself.
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return nil, err
	}
	if bin, err = filepath.Abs(bin); err != nil {
		return nil, err
	}
	a := []string{bwrap, "--unshare-all", "--share-net", "--die-with-parent", "--new-session",
		"--ro-bind", "/usr", "/usr", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run"}
	for _, p := range []string{"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/opt"} {
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
		case fi.Mode()&os.ModeSymlink != 0:
			if to, err := os.Readlink(p); err == nil {
				a = append(a, "--symlink", to, p)
			}
		default:
			a = append(a, "--ro-bind", p, p)
		}
	}
	for _, f := range checkerEtc {
		// A link (resolv.conf of systemd) gets the file that it names.
		if p, err := filepath.EvalSymlinks(filepath.Join("/etc", f)); err == nil {
			a = append(a, "--ro-bind", p, filepath.Join("/etc", f))
		}
	}
	if home != "" {
		a = append(a, "--tmpfs", home)
	}
	a = append(a, "--ro-bind", bin, bin)
	for _, p := range readOnly {
		a = append(a, "--ro-bind", p, p)
	}
	for _, p := range writable {
		a = append(a, "--bind", p, p)
	}
	a = append(a, "--bind", dir, dir)
	return append(append(a, "--", bin), argv[1:]...), nil
}

// checkerEnv is the environment of the checker command: a small fixed set,
// the variables of the configuration, and the variables whose value is in
// a file (a token). The variables of the runner (tokens, agent sockets)
// are not in it.
func checkerEnv(home string, more, files map[string]string) ([]string, error) {
	env := map[string]string{"HOME": home, "PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "TERM": "dumb"}
	if u := os.Getenv("USER"); u != "" {
		env["USER"] = u
	}
	for k, v := range more {
		env[k] = v
	}
	for k, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("checker env_files %s: %w", k, err)
		}
		env[k] = strings.TrimSpace(string(b))
	}
	list := make([]string, 0, len(env))
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	sort.Strings(list)
	return list, nil
}

// checkerCommand gives the command of the checker for this host and its
// environment. Linux: the view. A host with no bubblewrap gets an error,
// not a command with no view. macOS has no view yet: the command runs as
// it is, with the small environment.
func (r *Runner) checkerCommand(dir string, argv []string) ([]string, []string, error) {
	return agentCommand(r.Cfg.Checker, dir, argv)
}

// agentCommand is checkerCommand for the checker or the planner.
func agentCommand(c AgentCommand, dir string, argv []string) ([]string, []string, error) {
	home, _ := os.UserHomeDir()
	env, err := checkerEnv(home, c.Env, c.EnvFiles)
	if err != nil {
		return nil, nil, err
	}
	if runtime.GOOS != "linux" {
		return argv, env, nil
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, nil, fmt.Errorf("the checker needs bubblewrap (bwrap) on this host: %w", err)
	}
	for _, p := range append(append([]string{}, c.Writable...), c.ReadOnly...) {
		if !filepath.IsAbs(p) || strings.Contains(p, "..") {
			return nil, nil, fmt.Errorf("checker path %q: use an absolute path or ~/", p)
		}
	}
	out, err := checkerView(bwrap, home, dir, argv, c.Writable, c.ReadOnly)
	return out, env, err
}
