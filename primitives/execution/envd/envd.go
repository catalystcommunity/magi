// Package envd is the in-environment agent and its client.
//
// envd runs inside an environment: a container, a VM, or a host. It serves
// a small HTTP JSON API that runs commands and reads and writes files. Every
// request except GET /healthz needs "Authorization: Bearer <token>", where the
// token is the per-environment secret that the control plane made. Because
// the API does not depend on how the environment runs, every backend uses the
// same agent and the same client.
package envd

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
)

// Limits. Callers can ask for less, not more.
const (
	DefaultTimeout   = 2 * time.Minute
	MaxTimeout       = 2 * time.Hour
	DefaultOutput    = 1 << 20
	MaxOutput        = 8 << 20
	DefaultReadBytes = 1 << 20
	MaxReadBytes     = 48 << 20
	MaxWriteBytes    = 48 << 20
	MaxStdin         = 8 << 20
	DefaultListLimit = 1000
	MaxListLimit     = 10000
	DefaultListDepth = 1
	MaxListDepth     = 16
	maxRequest       = 72 << 20
)

// Paths of the API.
const (
	PathHealth = "/healthz"
	PathPing   = "/v1/ping"
	PathExec   = "/v1/exec"
	PathRead   = "/v1/read-file"
	PathWrite  = "/v1/write-file"
	PathList   = "/v1/list-files"
)

// Server is the agent. Token must not be empty.
type Server struct {
	Token string
	// Dir resolves relative paths and is the default command directory.
	Dir string
	// Env is the base environment of commands. Nil means os.Environ()
	// without MAGI_ENVD_ variables.
	Env []string
}

// Error is the JSON body of a failed request.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("envd %d: %s", e.Code, e.Message) }

func errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == PathHealth {
		w.Write([]byte("ok\n"))
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.Token == "" || !ok || subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
		writeJSON(w, http.StatusUnauthorized, errorf(http.StatusUnauthorized, "bad token"))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorf(http.StatusMethodNotAllowed, "POST only"))
		return
	}
	var resp any
	var err error
	switch r.URL.Path {
	case PathPing:
		resp = map[string]string{"status": "ok"}
	case PathExec:
		resp, err = handle(r, s.Exec)
	case PathRead:
		resp, err = handle(r, s.ReadFile)
	case PathWrite:
		resp, err = handle(r, s.WriteFile)
	case PathList:
		resp, err = handle(r, s.ListFiles)
	default:
		err = errorf(http.StatusNotFound, "unknown path %s", r.URL.Path)
	}
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = errorf(http.StatusInternalServerError, "%v", err)
		}
		writeJSON(w, e.Code, e)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func handle[Req, Resp any](r *http.Request, f func(context.Context, Req) (Resp, error)) (any, error) {
	var req Req
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequest))
	if err := dec.Decode(&req); err != nil {
		return nil, errorf(http.StatusBadRequest, "bad request: %v", err)
	}
	return f(r.Context(), req)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// CheckPath rejects paths that no backend can use safely.
func CheckPath(p string) error {
	if p == "" || strings.IndexByte(p, 0) >= 0 || len(p) > 4096 {
		return errorf(http.StatusBadRequest, "path must be 1-4096 bytes without NUL")
	}
	return nil
}

func (s *Server) resolve(p string) (string, error) {
	if err := CheckPath(p); err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.Dir, p)
	}
	return filepath.Clean(p), nil
}

func fsError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errorf(http.StatusNotFound, "%v", err)
	case errors.Is(err, fs.ErrPermission):
		return errorf(http.StatusForbidden, "%v", err)
	}
	return errorf(http.StatusBadRequest, "%v", err)
}

func limit(v *uint64, def, max uint64) uint64 {
	if v == nil || *v == 0 {
		return def
	}
	return min(*v, max)
}

// capped keeps the first n bytes and counts the rest.
type capped struct {
	buf bytes.Buffer
	n   int
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.n - c.buf.Len(); room < len(p) {
		c.buf.Write(p[:max(room, 0)])
		c.cut = true
	} else {
		c.buf.Write(p)
	}
	return len(p), nil
}

func (s *Server) baseEnv() []string {
	if s.Env != nil {
		return s.Env
	}
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MAGI_ENVD_") {
			out = append(out, kv)
		}
	}
	return out
}

// Exec runs one command. On timeout it kills the process group.
func (s *Server) Exec(ctx context.Context, req api.ExecRequest) (api.ExecResult, error) {
	var res api.ExecResult
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return res, errorf(http.StatusBadRequest, "argv is required")
	}
	for _, a := range req.Argv {
		if strings.IndexByte(a, 0) >= 0 {
			return res, errorf(http.StatusBadRequest, "argv must not contain NUL")
		}
	}
	dir := s.Dir
	if req.Cwd != nil && *req.Cwd != "" {
		d, err := s.resolve(*req.Cwd)
		if err != nil {
			return res, err
		}
		dir = d
	}
	env := append([]string(nil), s.baseEnv()...)
	for k, v := range req.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.IndexByte(v, 0) >= 0 {
			return res, errorf(http.StatusBadRequest, "bad environment variable %q", k)
		}
		env = append(env, k+"="+v)
	}
	timeout := time.Duration(limit(req.TimeoutMs, uint64(DefaultTimeout.Milliseconds()), uint64(MaxTimeout.Milliseconds()))) * time.Millisecond
	out := int(limit(req.MaxOutputBytes, DefaultOutput, MaxOutput))

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	stdout, stderr := &capped{n: out}, &capped{n: out}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if req.Stdin != nil {
		if len(*req.Stdin) > MaxStdin {
			return res, errorf(http.StatusBadRequest, "stdin is larger than %d bytes", MaxStdin)
		}
		cmd.Stdin = bytes.NewReader(*req.Stdin)
	}
	start := time.Now()
	err := cmd.Run()
	res.DurationMs = uint64(time.Since(start).Milliseconds())
	res.Stdout, res.Stderr = stdout.buf.Bytes(), stderr.buf.Bytes()
	res.Truncated = stdout.cut || stderr.cut
	res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = int64(exitErr.ExitCode())
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.ExitCode = 128 + int64(ws.Signal())
		}
	case res.TimedOut || errors.Is(err, exec.ErrWaitDelay):
		res.ExitCode = -1
	default:
		// The command did not start, for example an unknown program.
		res.ExitCode = 127
		res.Stderr = append(res.Stderr, []byte(err.Error()+"\n")...)
	}
	return res, nil
}

// ReadFile returns up to max_bytes of a regular file.
func (s *Server) ReadFile(_ context.Context, req api.ReadFileRequest) (api.FileContent, error) {
	p, err := s.resolve(req.Path)
	if err != nil {
		return api.FileContent{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return api.FileContent{}, fsError(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return api.FileContent{}, fsError(err)
	}
	if !st.Mode().IsRegular() {
		return api.FileContent{}, errorf(http.StatusBadRequest, "%s is not a regular file", req.Path)
	}
	n := limit(req.MaxBytes, DefaultReadBytes, MaxReadBytes)
	body, err := io.ReadAll(io.LimitReader(f, int64(n)))
	if err != nil {
		return api.FileContent{}, fsError(err)
	}
	return api.FileContent{Path: p, Body: body, Size: uint64(st.Size()), Truncated: uint64(st.Size()) > uint64(len(body))}, nil
}

// WriteFile replaces a file atomically and makes missing parent directories.
func (s *Server) WriteFile(_ context.Context, req api.WriteFileRequest) (api.WriteFileResponse, error) {
	p, err := s.resolve(req.Path)
	if err != nil {
		return api.WriteFileResponse{}, err
	}
	if len(req.Body) > MaxWriteBytes {
		return api.WriteFileResponse{}, errorf(http.StatusBadRequest, "body is larger than %d bytes", MaxWriteBytes)
	}
	mode := fs.FileMode(0o644)
	if req.Mode != nil && *req.Mode != 0 {
		if *req.Mode > 0o7777 {
			return api.WriteFileResponse{}, errorf(http.StatusBadRequest, "mode must be at most 07777")
		}
		mode = fs.FileMode(*req.Mode)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return api.WriteFileResponse{}, fsError(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".envd-*")
	if err != nil {
		return api.WriteFileResponse{}, fsError(err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(req.Body)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), p)
	}
	if err != nil {
		return api.WriteFileResponse{}, fsError(err)
	}
	return api.WriteFileResponse{Path: p, Size: uint64(len(req.Body))}, nil
}

// ListFiles walks a directory to depth levels. It does not follow links.
func (s *Server) ListFiles(_ context.Context, req api.ListFilesRequest) (api.ListFilesResponse, error) {
	resp := api.ListFilesResponse{Entries: []api.FileEntry{}}
	root, err := s.resolve(req.Path)
	if err != nil {
		return resp, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return resp, fsError(err)
	}
	if !st.IsDir() {
		resp.Entries = append(resp.Entries, api.FileEntry{Path: root, Size: uint64(st.Size())})
		return resp, nil
	}
	depth := int(limit(req.Depth, DefaultListDepth, MaxListDepth))
	n := int(limit(req.Limit, DefaultListLimit, MaxListLimit))
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if p == root {
			return err
		}
		if err != nil {
			return nil // Unreadable entries are skipped.
		}
		if len(resp.Entries) >= n {
			resp.Truncated = true
			return fs.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		e := api.FileEntry{Path: rel, Dir: d.IsDir()}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			e.Size = uint64(info.Size())
		}
		resp.Entries = append(resp.Entries, e)
		if d.IsDir() && strings.Count(rel, string(filepath.Separator))+1 >= depth {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return resp, fsError(err)
	}
	return resp, nil
}
