package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
	"golang.org/x/net/websocket"
)

// termWorkflow knows the runs of the terminal tests.
type termWorkflow struct{ workflowapi.WorkflowService }

var termRuns = map[string]workflowapi.Run{
	"run_live": {Id: "run_live", Definition: CodeChange, Owner: "user:owner@example.local", Status: "waiting",
		Vars: map[string]string{"runner": "ht0", "environment": "run_live", "workdir": "/work/repo"}},
	"run_done": {Id: "run_done", Definition: CodeChange, Owner: "user:owner@example.local", Status: "succeeded",
		Vars: map[string]string{"runner": "ht0", "environment": "run_done"}},
	"run_new": {Id: "run_new", Definition: CodeChange, Owner: "user:owner@example.local", Status: "running",
		Vars: map[string]string{"runner": "ht0"}},
	"run_turn": {Id: "run_turn", Definition: PersonaTurn, Owner: "user:owner@example.local", Status: "running",
		Vars: map[string]string{"environment": "x"}},
}

func (termWorkflow) GetRun(_ context.Context, r workflowapi.GetRunRequest) (workflowapi.Run, error) {
	run, ok := termRuns[r.Id]
	if !ok {
		return workflowapi.Run{}, rpc.NotFound("no run %s", r.Id)
	}
	return run, nil
}

type termEnv struct {
	sys    *System
	ver    *authn.Verifier
	url    string // the Magi test server
	tok    func(sub, kind string, scopes ...string) string
	runner *magiapi.RunnerClient
}

func newTermEnv(t *testing.T) *termEnv {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok := func(sub, kind string, scopes ...string) string {
		s, err := iss.Issue(authn.Principal{Subject: sub, Kind: kind, Scopes: scopes}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wsrv := &rpc.Server{Verifier: ver, Log: log}
	wsrv.Register("WorkflowService", workflowapi.WorkflowServiceOps(termWorkflow{}))
	wf := httptest.NewServer(rpc.Mux(wsrv, nil))
	t.Cleanup(wf.Close)
	sys := New(Config{Workflow: wf.URL, ServiceToken: tok("service:magi", authn.KindService, "workflow:admin"),
		Admins: []string{"user:admin@example.local"}}, nil, log)
	sys.Terminals.Wait = 500 * time.Millisecond
	srv := &rpc.Server{Verifier: ver, Log: log}
	srv.Register("RunnerService", magiapi.RunnerServiceOps(&Runners{S: sys}))
	mux := rpc.Mux(srv, nil)
	mux.Handle("/api/terminal", Terminal(ver, sys))
	mux.Handle("/runner/terminal", RunnerTerminal(ver, sys.Terminals))
	h := httptest.NewServer(mux)
	t.Cleanup(h.Close)
	rtok := tok("runner:ht0", authn.KindService, ScopeRunner)
	return &termEnv{sys: sys, ver: ver, url: h.URL, tok: tok, runner: magiapi.NewRunnerClient(rpc.NewClient(h.URL, rpc.StaticToken(rtok)))}
}

// The access rules: the owner (or an admin) of a code-change task with a
// sandbox now; not another person, a service, or an ended task.
func TestTerminalTarget(t *testing.T) {
	e := newTermEnv(t)
	ctx := context.Background()
	user := func(s string) authn.Principal { return authn.Principal{Subject: s, Kind: authn.KindUser} }
	for _, c := range []struct {
		p    authn.Principal
		run  string
		code int // 0: allowed
	}{
		{user("user:owner@example.local"), "run_live", 0},
		{user("user:admin@example.local"), "run_live", 0},
		{user("user:other@example.local"), "run_live", 403},
		{authn.Principal{Subject: "user:owner@example.local", Kind: authn.KindService}, "run_live", 403},
		{user("user:owner@example.local"), "run_done", 409},
		{user("user:owner@example.local"), "run_new", 409}, // no sandbox yet
		{user("user:owner@example.local"), "run_turn", 404},
		{user("user:owner@example.local"), "run_none", 404},
		{user("user:owner@example.local"), "", 400},
	} {
		tg, err := e.sys.terminalTarget(ctx, c.p, c.run)
		if got := rpc.CodeOf(err); got != c.code {
			t.Errorf("%s on %q: code %d (%v), want %d", c.p.Subject, c.run, got, err, c.code)
		}
		if c.code == 0 && (tg.runner != "ht0" || tg.env != "run_live" || tg.cwd != "/work/repo" || tg.owner != "user:owner@example.local") {
			t.Errorf("target %+v", tg)
		}
	}
	if !terminalOpen(termRuns["run_live"]) || terminalOpen(termRuns["run_done"]) {
		t.Error("terminalOpen")
	}
}

// dialTerm opens /api/terminal and sends the hello.
func dialTerm(t *testing.T, base, run string, hello termHello, header string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig(strings.Replace(base, "http", "ws", 1)+"/api/terminal?task="+run, base)
	if err != nil {
		t.Fatal(err)
	}
	if header != "" {
		cfg.Header.Set("Authorization", "Bearer "+header)
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(hello)
	if err := websocket.Message.Send(ws, string(b)); err != nil {
		t.Fatal(err)
	}
	return ws
}

// readMsg reads one terminal message (type byte and payload).
func readMsg(t *testing.T, ws *websocket.Conn, d time.Duration) (byte, string) {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(d))
	var b []byte
	if err := websocket.Message.Receive(ws, &b); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("empty message")
	}
	return b[0], string(b[1:])
}

func TestTerminalRefusals(t *testing.T) {
	e := newTermEnv(t)
	for _, c := range []struct {
		name, run, token, header, want string
	}{
		{"another person", "run_live", e.tok("user:other@example.local", authn.KindUser), "", "only the owner"},
		{"bad token", "run_live", "magi1.bad", "", "sign in again"},
		{"ended task", "run_done", e.tok("user:owner@example.local", authn.KindUser), "", "no sandbox now"},
		{"a runner token", "run_live", "", e.tok("runner:ht0", authn.KindService, ScopeRunner), "token of a person"},
	} {
		ws := dialTerm(t, e.url, c.run, termHello{Token: c.token, Cols: 80, Rows: 24}, c.header)
		typ, msg := readMsg(t, ws, 5*time.Second)
		if typ != envd.FrameError || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %c %q, want an error with %q", c.name, typ, msg, c.want)
		}
		ws.Close()
	}
}

// The runner does not come: the client gets an error after Wait, not a
// connection that hangs.
func TestTerminalRunnerTimeout(t *testing.T) {
	e := newTermEnv(t)
	start := time.Now()
	ws := dialTerm(t, e.url, "run_live", termHello{Token: e.tok("user:owner@example.local", authn.KindUser)}, "")
	defer ws.Close()
	typ, msg := readMsg(t, ws, 5*time.Second)
	if typ != envd.FrameError || !strings.Contains(msg, "did not connect") {
		t.Fatalf("%c %q", typ, msg)
	}
	if d := time.Since(start); d < e.sys.Terminals.Wait || d > 3*time.Second {
		t.Fatalf("the error came after %s", d)
	}
	// The session is gone: a late runner cannot claim it.
	e.sys.Terminals.mu.Lock()
	n := len(e.sys.Terminals.sessions) + len(e.sys.Terminals.pending["ht0"])
	e.sys.Terminals.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d sessions left", n)
	}
}

// A runner that takes the session and connects: the frames go through
// Magi in both directions; the idle limit closes the session.
func TestTerminalThroughRunner(t *testing.T) {
	e := newTermEnv(t)
	e.sys.Terminals.Wait = 5 * time.Second
	e.sys.Terminals.Ping, e.sys.Terminals.Idle = 50*time.Millisecond, 600*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The runner side: the long poll, then the upgrade; a fake sandbox
	// that echoes data frames and reports resizes.
	go func() {
		res, err := e.runner.Terminals(ctx, magiapi.RunnerTerminalsRequest{WaitMs: ptr(uint64(5000))})
		if err != nil || len(res.Terminals) != 1 {
			t.Errorf("terminals: %+v %v", res, err)
			return
		}
		rt := res.Terminals[0]
		if rt.Run != "run_live" || rt.Environment != "run_live" || deref(rt.Cwd) != "/work/repo" || rt.Cols != 100 || rt.Rows != 30 {
			t.Errorf("terminal %+v", rt)
		}
		// Another runner cannot take the session.
		other := e.tok("runner:other", authn.KindService, ScopeRunner)
		if c, err := envd.DialUpgrade(ctx, e.url, "/runner/terminal?session="+rt.Session, other); err == nil {
			c.Close()
			t.Error("another runner claimed the session")
		}
		conn, err := envd.DialUpgrade(ctx, e.url, "/runner/terminal?session="+rt.Session, e.tok("runner:ht0", authn.KindService, ScopeRunner))
		if err != nil {
			t.Errorf("runner upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			typ, p, err := envd.ReadFrame(conn)
			if err != nil {
				return
			}
			switch typ {
			case envd.FrameData:
				if string(p) == "exit\n" {
					envd.WriteFrame(conn, envd.FrameExit, envd.ExitPayload(3))
					continue
				}
				envd.WriteFrame(conn, envd.FrameData, append([]byte("echo:"), p...))
			case envd.FrameResize:
				c, r, _ := envd.ParseResize(p)
				envd.WriteFrame(conn, envd.FrameData, []byte("size:"+itoa(int(c))+"x"+itoa(int(r))))
			}
		}
	}()
	ws := dialTerm(t, e.url, "run_live", termHello{Cols: 100, Rows: 30}, e.tok("user:owner@example.local", authn.KindUser))
	defer ws.Close()
	next := func() (byte, string) { // skips pings
		for {
			typ, msg := readMsg(t, ws, 5*time.Second)
			if typ != envd.FramePing {
				return typ, msg
			}
		}
	}
	websocket.Message.Send(ws, []byte("dhello"))
	if typ, msg := next(); typ != envd.FrameData || msg != "echo:hello" {
		t.Fatalf("echo: %c %q", typ, msg)
	}
	websocket.Message.Send(ws, append([]byte{envd.FrameResize}, envd.ResizePayload(120, 40)...))
	if typ, msg := next(); typ != envd.FrameData || msg != "size:120x40" {
		t.Fatalf("resize: %c %q", typ, msg)
	}
	websocket.Message.Send(ws, []byte("dexit\n"))
	if typ, msg := next(); typ != envd.FrameExit || msg != string(envd.ExitPayload(3)) {
		t.Fatalf("exit: %c %q", typ, msg)
	}

	// A second session that gets no input: the idle limit ends it.
	go func() {
		res, err := e.runner.Terminals(ctx, magiapi.RunnerTerminalsRequest{WaitMs: ptr(uint64(5000))})
		if err != nil || len(res.Terminals) != 1 {
			return
		}
		conn, err := envd.DialUpgrade(ctx, e.url, "/runner/terminal?session="+res.Terminals[0].Session, e.tok("runner:ht0", authn.KindService, ScopeRunner))
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn)
	}()
	ws2 := dialTerm(t, e.url, "run_live", termHello{}, e.tok("user:owner@example.local", authn.KindUser))
	defer ws2.Close()
	for {
		typ, msg := readMsg(t, ws2, 5*time.Second)
		if typ == envd.FramePing {
			continue
		}
		if typ != envd.FrameError || !strings.Contains(msg, "no input") {
			t.Fatalf("idle: %c %q", typ, msg)
		}
		break
	}
}

func TestTerminalLink(t *testing.T) {
	if got := terminalLinkText("https://magi.example.local", termRuns["run_live"]); !strings.Contains(got, "https://magi.example.local/#/terminal/run_live") {
		t.Fatalf("live: %s", got)
	}
	if got := terminalLinkText("https://magi.example.local", termRuns["run_done"]); strings.Contains(got, "#/terminal") {
		t.Fatalf("ended: %s", got)
	}
	if _, ok := toolDefs()["terminal_link"]; !ok || !slices.Contains(profiles[Engineering].tools, "terminal_link") {
		t.Fatal("the engineering persona has no terminal_link tool")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
