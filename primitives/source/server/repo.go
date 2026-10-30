package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/catalystcommunity/magi/lib/rpc"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// metaFile holds repository metadata inside the bare repository. Git does
// not read it, and git http-backend does not serve it.
const metaFile = "magi.json"

type meta struct {
	Owner         string `json:"owner"`
	DefaultBranch string `json:"default_branch"`
	CreatedMs     uint64 `json:"created_ms"`
	// Upstream is the Git URL that sync and publish use, or "".
	Upstream string `json:"upstream,omitempty"`
}

func validName(name string) error {
	if !nameRE.MatchString(name) || strings.HasSuffix(name, ".git") {
		return rpc.Invalid("repository name must match %s and not end with .git", nameRE)
	}
	return nil
}

func (s *Service) repoDir(name string) string { return filepath.Join(s.Dir, name+".git") }

func (s *Service) readMeta(name string) (meta, error) {
	var m meta
	if err := validName(name); err != nil {
		return m, err
	}
	b, err := os.ReadFile(filepath.Join(s.repoDir(name), metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return m, rpc.NotFound("repository %s not found", name)
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func writeMeta(dir string, m meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, metaFile), b, 0o640)
}

// gitError is a failed git command. Its message includes stderr.
type gitError struct {
	args   []string
	code   int
	stderr string
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", e.args[0], e.code, strings.TrimSpace(e.stderr))
}

func exitCode(err error) int {
	var g *gitError
	if errors.As(err, &g) {
		return g.code
	}
	return -1
}

// git runs git in the bare repository dir. Arguments go directly to the git
// process; no shell is used. out receives stdout; nil collects it.
func git(ctx context.Context, dir string, env []string, stdin io.Reader, out io.Writer, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_DIR="+dir, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if out != nil {
		cmd.Stdout = out
	}
	cmd.Stderr = &limited{w: &stderr, n: 64 << 10}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.Bytes(), &gitError{args: args, code: ee.ExitCode(), stderr: stderr.String()}
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// limited keeps the first n bytes and discards the rest.
type limited struct {
	w         io.Writer
	n         int
	truncated bool
}

func (l *limited) Write(p []byte) (int, error) {
	if len(p) > l.n {
		l.truncated = true
		l.w.Write(p[:l.n])
		l.n = 0
		return len(p), nil
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// resolve returns the commit that rev names. rev is a ref name or commit.
func resolve(ctx context.Context, dir, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") || strings.ContainsAny(rev, " \t\n\x00") {
		return "", rpc.Invalid("bad revision %q", rev)
	}
	out, err := git(ctx, dir, nil, nil, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		if exitCode(err) > 0 {
			return "", rpc.NotFound("revision %q not found", rev)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// validBranch checks a branch name with git check-ref-format.
func validBranch(ctx context.Context, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") {
		return rpc.Invalid("bad branch name %q", branch)
	}
	cmd := exec.CommandContext(ctx, "git", "check-ref-format", "refs/heads/"+branch)
	if err := cmd.Run(); err != nil {
		return rpc.Invalid("bad branch name %q", branch)
	}
	return nil
}

// isAncestor reports whether commit a is an ancestor of commit b.
func isAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	_, err := git(ctx, dir, nil, nil, nil, "merge-base", "--is-ancestor", a, b)
	if exitCode(err) == 1 {
		return false, nil
	}
	return err == nil, err
}
