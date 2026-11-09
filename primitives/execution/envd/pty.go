package envd

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// PathPTY starts a shell on a pseudo-terminal. The request is
// POST /v1/pty with "Connection: Upgrade", "Upgrade: magi-pty", and a
// JSON PTYRequest body. envd answers "101 Switching Protocols"; then the
// connection carries terminal frames (frame.go) until the shell exits.
const PathPTY = "/v1/pty"

// PTYUpgrade is the Upgrade token of PathPTY.
const PTYUpgrade = "magi-pty"

// PTYRequest is the body of PathPTY.
type PTYRequest struct {
	// Argv is the program. Default: "bash -l", or "sh -l" without bash.
	Argv []string `json:"argv,omitempty"`
	// Cwd is the directory. Default: the work directory of envd.
	Cwd  string            `json:"cwd,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	Cols uint16            `json:"cols,omitempty"`
	Rows uint16            `json:"rows,omitempty"`
}

// ptyGrace is the time between SIGHUP and SIGKILL when the client goes
// away, and the time that envd waits for the last output after the shell
// exits.
const ptyGrace = 2 * time.Second

func (s *Server) ptyCommand(req PTYRequest) (*exec.Cmd, error) {
	argv := req.Argv
	if len(argv) == 0 {
		argv = []string{"sh", "-l"}
		if _, err := exec.LookPath("bash"); err == nil {
			argv = []string{"bash", "-l"}
		}
	}
	if argv[0] == "" {
		return nil, errorf(http.StatusBadRequest, "argv[0] is empty")
	}
	for _, a := range argv {
		if strings.IndexByte(a, 0) >= 0 {
			return nil, errorf(http.StatusBadRequest, "argv must not contain NUL")
		}
	}
	dir := s.Dir
	if req.Cwd != "" {
		d, err := s.resolve(req.Cwd)
		if err != nil {
			return nil, err
		}
		dir = d
	}
	env := append([]string(nil), s.baseEnv()...)
	env = append(env, "TERM=xterm-256color")
	for k, v := range req.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.IndexByte(v, 0) >= 0 {
			return nil, errorf(http.StatusBadRequest, "bad environment variable %q", k)
		}
		env = append(env, k+"="+v)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	return cmd, nil
}

// servePTY serves PathPTY. The shell is the leader of a new session and
// process group; when the client goes away, envd sends SIGHUP to the
// group, and SIGKILL after ptyGrace.
func (s *Server) servePTY(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), PTYUpgrade) {
		writeJSON(w, http.StatusBadRequest, errorf(http.StatusBadRequest, "expected Upgrade: %s", PTYUpgrade))
		return
	}
	var req PTYRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorf(http.StatusBadRequest, "bad request: %v", err))
		return
	}
	cmd, err := s.ptyCommand(req)
	if err != nil {
		var e *Error
		errors.As(err, &e)
		writeJSON(w, e.Code, e)
		return
	}
	size := &pty.Winsize{Cols: req.Cols, Rows: req.Rows}
	if size.Cols == 0 || size.Rows == 0 {
		size.Cols, size.Rows = 80, 24
	}
	f, err := pty.StartWithSize(cmd, size)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorf(http.StatusBadRequest, "start %s: %v", cmd.Path, err))
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		killGroup(cmd, syscall.SIGKILL)
		f.Close()
		cmd.Wait()
		writeJSON(w, http.StatusInternalServerError, errorf(http.StatusInternalServerError, "no upgrade on this connection"))
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		killGroup(cmd, syscall.SIGKILL)
		f.Close()
		cmd.Wait()
		return
	}
	conn.SetDeadline(time.Time{})
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + PTYUpgrade + "\r\nConnection: Upgrade\r\n\r\n")
	if err := brw.Flush(); err != nil {
		conn.Close()
		killGroup(cmd, syscall.SIGKILL)
		f.Close()
		cmd.Wait()
		return
	}
	runPTY(cmd, f, conn, brw.Reader)
}

func killGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, sig)
	}
}

// runPTY copies frames between conn and the terminal f of cmd until cmd
// exits, then sends the exit frame and closes conn.
func runPTY(cmd *exec.Cmd, f *os.File, conn net.Conn, in *bufio.Reader) {
	defer conn.Close()
	exited := make(chan struct{})
	var once sync.Once
	hangup := func() {
		once.Do(func() {
			select {
			case <-exited:
				return // the group can be gone, and its ID used again
			default:
			}
			killGroup(cmd, syscall.SIGHUP)
			go func() {
				select {
				case <-exited:
				case <-time.After(ptyGrace):
					killGroup(cmd, syscall.SIGKILL)
				}
			}()
		})
	}
	// Input: data and resize frames from the client.
	go func() {
		defer hangup()
		for {
			typ, p, err := ReadFrame(in)
			if err != nil {
				return
			}
			switch typ {
			case FrameData:
				if _, err := f.Write(p); err != nil {
					return
				}
			case FrameResize:
				if c, r, err := ParseResize(p); err == nil && c > 0 && r > 0 {
					pty.Setsize(f, &pty.Winsize{Cols: c, Rows: r})
				}
			}
		}
	}()
	// Output: until the terminal closes (EIO when no process holds it).
	out := make(chan struct{})
	go func() {
		defer close(out)
		buf := make([]byte, 32<<10)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if WriteFrame(conn, FrameData, buf[:n]) != nil {
					hangup()
				}
			}
			if err != nil {
				return
			}
		}
	}()
	err := cmd.Wait()
	close(exited)
	// A background process can keep the terminal open: do not wait for
	// it for long.
	select {
	case <-out:
	case <-time.After(ptyGrace):
	}
	f.Close()
	<-out
	code := int32(0)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = int32(exitErr.ExitCode())
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int32(ws.Signal())
		}
	} else if err != nil {
		code = -1
	}
	WriteFrame(conn, FrameExit, ExitPayload(code))
}
