package compose

// Terminals: a person opens a shell in the sandbox of a running task.
//
// The client (the web client, the CLI) opens the WebSocket
// /api/terminal?task=RUN on Magi's public URL. Its first message is a
// JSON hello {"token", "cols", "rows"}; the token can also come in the
// Authorization header. After the hello, each WebSocket message is one
// terminal frame (envd/frame.go): the type byte and the payload.
//
// For a run on a runner, Magi cannot connect to the runner. Magi gives
// the session to the runner in its terminals long poll; the runner opens
// GET /runner/terminal?session=ID (an HTTP upgrade with its runner token)
// and copies that connection to the PTY route of envd in the sandbox.
// The meeting point is in the memory of this process: one Magi replica
// must hold both connections. With more replicas, a session that the
// runner opens on another replica fails after Terminals.Wait.
//
// For a run in the cluster (no runner), Magi opens /v1/pty on the
// execution service with its service token.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
	"golang.org/x/net/websocket"
)

// Terminals holds the terminal sessions that wait for a runner.
type Terminals struct {
	// Wait is how long a session waits for the runner (default 20 s).
	Wait time.Duration
	// Idle closes a session with no input for this time (default 30 min).
	Idle time.Duration
	// Max closes a session after this time (default 8 h).
	Max time.Duration
	// Ping is the interval of keepalive frames and of the time checks
	// (default 30 s): gateways close quiet connections.
	Ping time.Duration

	mu       sync.Mutex
	pending  map[string][]*termSession // runner name -> sessions not given yet
	wake     map[string]chan struct{}  // closed when a runner has a new session
	sessions map[string]*termSession   // session id -> waits for its runner
}

// NewTerminals returns Terminals with the default times.
func NewTerminals() *Terminals {
	return &Terminals{Wait: 20 * time.Second, Idle: 30 * time.Minute, Max: 8 * time.Hour, Ping: 30 * time.Second}
}

type termSession struct {
	id, runner string
	info       magiapi.RunnerTerminal
	conn       chan net.Conn // buffer 1: the connection of the runner
	done       bool          // the client stopped waiting
}

// open adds a session for runner and wakes its long poll.
func (t *Terminals) open(runner string, info magiapi.RunnerTerminal) *termSession {
	b := make([]byte, 16)
	rand.Read(b)
	s := &termSession{id: "term_" + hex.EncodeToString(b), runner: runner, info: info, conn: make(chan net.Conn, 1)}
	s.info.Session = s.id
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessions == nil {
		t.pending, t.wake, t.sessions = map[string][]*termSession{}, map[string]chan struct{}{}, map[string]*termSession{}
	}
	t.sessions[s.id] = s
	t.pending[runner] = append(t.pending[runner], s)
	if ch := t.wake[runner]; ch != nil {
		close(ch)
		delete(t.wake, runner)
	}
	return s
}

// await waits up to Wait for the runner of s. It returns nil when the
// runner did not come.
func (t *Terminals) await(ctx context.Context, s *termSession) net.Conn {
	timer := time.NewTimer(t.Wait)
	defer timer.Stop()
	select {
	case c := <-s.conn:
		return c
	case <-timer.C:
	case <-ctx.Done():
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s.done = true
	delete(t.sessions, s.id)
	t.pending[s.runner] = slices.DeleteFunc(t.pending[s.runner], func(x *termSession) bool { return x == s })
	select {
	case c := <-s.conn: // it came at the last moment
		return c
	default:
		return nil
	}
}

// take returns the new sessions of runner, and waits up to wait for one.
func (t *Terminals) take(ctx context.Context, runner string, wait time.Duration) []magiapi.RunnerTerminal {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		t.mu.Lock()
		if t.sessions == nil {
			t.pending, t.wake, t.sessions = map[string][]*termSession{}, map[string]chan struct{}{}, map[string]*termSession{}
		}
		if p := t.pending[runner]; len(p) > 0 {
			delete(t.pending, runner)
			t.mu.Unlock()
			out := make([]magiapi.RunnerTerminal, len(p))
			for i, s := range p {
				out[i] = s.info
			}
			return out
		}
		ch := t.wake[runner]
		if ch == nil {
			ch = make(chan struct{})
			t.wake[runner] = ch
		}
		t.mu.Unlock()
		select {
		case <-ch:
		case <-timer.C:
			return []magiapi.RunnerTerminal{}
		case <-ctx.Done():
			return []magiapi.RunnerTerminal{}
		}
	}
}

// claim takes the session id for runner, or gives an error.
func (t *Terminals) claim(runner, id string) (*termSession, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[id]
	if s == nil || s.runner != runner {
		return nil, rpc.NotFound("no terminal session %s waits for runner %s", id, runner)
	}
	delete(t.sessions, id)
	return s, nil
}

// deliver gives the runner connection to the client of s.
func (t *Terminals) deliver(s *termSession, c net.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s.done {
		c.Close()
		return
	}
	s.conn <- c
}

// Terminals is the terminals long poll of a runner.
func (r *Runners) Terminals(ctx context.Context, req magiapi.RunnerTerminalsRequest) (magiapi.RunnerTerminalsResponse, error) {
	name, err := runnerName(ctx)
	if err != nil {
		return magiapi.RunnerTerminalsResponse{}, err
	}
	wait := 20 * time.Second
	if req.WaitMs != nil {
		wait = time.Duration(min(*req.WaitMs, 25000)) * time.Millisecond
	}
	return magiapi.RunnerTerminalsResponse{Terminals: r.S.Terminals.take(ctx, name, wait)}, nil
}

// RunnerTerminal serves GET /runner/terminal?session=ID for runners: the
// runner side of a terminal session (see the top of this file).
func RunnerTerminal(ver *authn.Verifier, t *Terminals) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected an upgrade", http.StatusBadRequest)
			return
		}
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		p, err := ver.Verify(tok)
		name := ""
		if err == nil {
			name, err = runnerName(authn.With(r.Context(), p))
		}
		if err != nil {
			http.Error(w, "a runner token is required", http.StatusUnauthorized)
			return
		}
		s, err := t.claim(name, r.URL.Query().Get("session"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no upgrade on this connection", http.StatusInternalServerError)
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			return
		}
		conn.SetDeadline(time.Time{})
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(sum[:]))
		if err := brw.Flush(); err != nil {
			conn.Close()
			return
		}
		t.deliver(s, &envd.BufConn{Conn: conn, R: brw.Reader})
	})
}

// termTarget is the sandbox of a terminal.
type termTarget struct {
	run, owner, env, runner, cwd string
}

// terminalOpen reports whether the run has a sandbox now that a
// terminal can open.
func terminalOpen(run workflowapi.Run) bool {
	return run.Definition == CodeChange && (run.Status == "running" || run.Status == "waiting") && run.Vars["environment"] != ""
}

// terminalTarget checks that p can open a terminal in the sandbox of run
// id: p is a person, the owner of the task or an admin, and the task has
// a sandbox now.
func (s *System) terminalTarget(ctx context.Context, p authn.Principal, id string) (termTarget, error) {
	if p.Kind != authn.KindUser {
		return termTarget{}, rpc.Denied("a terminal needs the token of a person")
	}
	if id == "" {
		return termTarget{}, rpc.Invalid("task is required")
	}
	run, err := s.Workflow.GetRun(s.AsService(ctx), workflowapi.GetRunRequest{Id: id})
	if rpc.CodeOf(err) == http.StatusNotFound || err == nil && run.Definition != CodeChange {
		return termTarget{}, rpc.NotFound("no task %s", id)
	}
	if err != nil {
		return termTarget{}, err
	}
	if run.Owner != p.Subject && !slices.Contains(s.Cfg.Admins, p.Subject) {
		return termTarget{}, rpc.Denied("only the owner of task %s can open its terminal", id)
	}
	if !terminalOpen(run) {
		return termTarget{}, rpc.Conflict("task %s has no sandbox now (status %s)", id, run.Status)
	}
	return termTarget{run: id, owner: run.Owner, env: run.Vars["environment"], runner: run.Vars["runner"], cwd: run.Vars["workdir"]}, nil
}

// dialTerminal opens the terminal stream of the sandbox of tg.
func (s *System) dialTerminal(ctx context.Context, tg termTarget, cols, rows uint16) (io.ReadWriteCloser, error) {
	if tg.runner != "" {
		info := magiapi.RunnerTerminal{Run: tg.run, Environment: tg.env, Cols: uint64(cols), Rows: uint64(rows)}
		if tg.cwd != "" {
			info.Cwd = &tg.cwd
		}
		ses := s.Terminals.open(tg.runner, info)
		c := s.Terminals.await(ctx, ses)
		if c == nil {
			return nil, rpc.Unavailable("runner %s did not connect for the terminal in %s; it can be offline or run an older release", tg.runner, s.Terminals.Wait)
		}
		return c, nil
	}
	q := url.Values{"environment": {tg.env}, "cols": {fmt.Sprint(cols)}, "rows": {fmt.Sprint(rows)}}
	if tg.cwd != "" {
		q.Set("cwd", tg.cwd)
	}
	c, err := envd.DialUpgrade(ctx, s.Cfg.Execution, "/v1/pty?"+q.Encode(), s.Cfg.ServiceToken)
	if err != nil {
		return nil, rpc.Unavailable("the execution service did not open the terminal: %v", err)
	}
	return c, nil
}

// termHello is the first message of a terminal client.
type termHello struct {
	Token string `json:"token"`
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`
}

// Terminal serves the WebSocket /api/terminal?task=RUN for people.
func Terminal(ver *authn.Verifier, s *System) http.Handler {
	return websocket.Server{
		// The token is in a message or a header, never in a cookie: the
		// origin of the page does not matter.
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   func(ws *websocket.Conn) { s.serveTerminal(ver, ws) },
	}
}

func (s *System) serveTerminal(ver *authn.Verifier, ws *websocket.Conn) {
	defer ws.Close()
	ws.MaxPayloadBytes = envd.MaxFrame + 1
	var wmu sync.Mutex
	send := func(typ byte, p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
		return websocket.Message.Send(ws, append([]byte{typ}, p...))
	}
	req := ws.Request()
	run := req.URL.Query().Get("task")
	fail := func(err error) {
		msg := err.Error()
		var e *rpc.Error
		if errors.As(err, &e) {
			msg = e.Message
		}
		send(envd.FrameError, []byte(msg))
	}
	var hello termHello
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var b []byte
	if err := websocket.Message.Receive(ws, &b); err != nil {
		return
	}
	if err := json.Unmarshal(b, &hello); err != nil {
		fail(errors.New("the first message must be the JSON hello"))
		return
	}
	ws.SetReadDeadline(time.Time{})
	tok := hello.Token
	if h, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer "); ok {
		tok = h
	}
	p, err := ver.Verify(tok)
	if err != nil {
		fail(errors.New("sign in again: the token is not valid"))
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	tg, err := s.terminalTarget(ctx, p, run)
	if err != nil {
		s.Log.Warn("terminal refused", "run", run, "by", p.Subject, "err", err)
		fail(err)
		return
	}
	cols, rows := hello.Cols, hello.Rows
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	log := s.Log.With("run", tg.run, "owner", tg.owner, "by", p.Subject, "runner", tg.runner, "environment", tg.env)
	log.Info("terminal opening")
	up, err := s.dialTerminal(ctx, tg, cols, rows)
	if err != nil {
		log.Warn("terminal failed", "err", err)
		fail(err)
		return
	}
	start := time.Now()
	log.Info("terminal opened")
	reason, code := s.Terminals.pump(ws, up, send)
	log.Info("terminal closed", "reason", reason, "exit", code, "seconds", int(time.Since(start).Seconds()))
}

// pump copies frames between the client ws and the sandbox stream up
// until one side ends, the input is idle for Idle, or the session is
// older than Max. It returns why it ended and the exit status (-1 when
// the shell did not exit).
func (t *Terminals) pump(ws *websocket.Conn, up io.ReadWriteCloser, send func(byte, []byte) error) (string, int32) {
	defer up.Close()
	var umu sync.Mutex
	toUp := func(typ byte, p []byte) error {
		umu.Lock()
		defer umu.Unlock()
		return envd.WriteFrame(up, typ, p)
	}
	start := time.Now()
	var lastInput atomic.Int64
	lastInput.Store(start.UnixNano())
	code := atomic.Int32{}
	code.Store(-1)
	type end struct{ reason string }
	ended := make(chan end, 3)
	go func() { // sandbox to client
		br := bufio.NewReader(up)
		for {
			typ, p, err := envd.ReadFrame(br)
			if err != nil {
				ended <- end{"the sandbox closed the stream"}
				return
			}
			switch typ {
			case envd.FrameData:
				if send(typ, p) != nil {
					ended <- end{"the client went away"}
					return
				}
			case envd.FrameExit:
				if c, err := envd.ParseExit(p); err == nil {
					code.Store(c)
				}
				send(typ, p)
				ended <- end{"the shell exited"}
				return
			case envd.FrameError:
				send(typ, p)
				ended <- end{"error: " + string(p)}
				return
			}
		}
	}()
	go func() { // client to sandbox
		for {
			var b []byte
			if err := websocket.Message.Receive(ws, &b); err != nil {
				ended <- end{"the client closed the terminal"}
				return
			}
			if len(b) == 0 {
				continue
			}
			switch b[0] {
			case envd.FrameData, envd.FrameResize:
				lastInput.Store(time.Now().UnixNano())
				if toUp(b[0], b[1:]) != nil {
					ended <- end{"the sandbox closed the stream"}
					return
				}
			}
		}
	}()
	tick := time.NewTicker(t.Ping)
	defer tick.Stop()
	for {
		select {
		case e := <-ended:
			ws.Close()
			return e.reason, code.Load()
		case now := <-tick.C:
			send(envd.FramePing, nil)
			toUp(envd.FramePing, nil)
			if idle := now.Sub(time.Unix(0, lastInput.Load())); idle >= t.Idle {
				send(envd.FrameError, []byte(fmt.Sprintf("Magi closed the terminal: no input for %s.", t.Idle)))
				ws.Close()
				return "idle", code.Load()
			}
			if now.Sub(start) >= t.Max {
				send(envd.FrameError, []byte(fmt.Sprintf("Magi closed the terminal: the limit of a session is %s.", t.Max)))
				ws.Close()
				return "max", code.Load()
			}
		}
	}
}

// terminalLinkText is the result of the persona tool terminal_link.
func terminalLinkText(base string, run workflowapi.Run) string {
	if !terminalOpen(run) {
		return fmt.Sprintf("Task %s has no sandbox now (status %s), so no terminal can open. A terminal opens only while the task runs or waits for review.", run.Id, run.Status)
	}
	return fmt.Sprintf("Terminal link: %s/#/terminal/%s (it opens a shell in the sandbox of the task; only the person who owns the task can open it).", base, url.PathEscape(run.Id))
}
