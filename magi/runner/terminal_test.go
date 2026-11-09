package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/compose"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
	"golang.org/x/net/websocket"
)

type termWorkflow struct{ workflowapi.WorkflowService }

func (termWorkflow) GetRun(_ context.Context, r workflowapi.GetRunRequest) (workflowapi.Run, error) {
	switch r.Id {
	case "run_t", "run_gone":
		return workflowapi.Run{Id: r.Id, Definition: compose.CodeChange, Owner: "user:owner@example.local", Status: "running",
			Vars: map[string]string{"runner": "test", "environment": r.Id, "workdir": "/work/repo"}}, nil
	}
	return workflowapi.Run{}, rpc.NotFound("no run %s", r.Id)
}

// The whole path: a WebSocket client on Magi, the runner long poll and
// upgrade, and a shell of envd in a bubblewrap sandbox.
func TestTerminalInSandbox(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap is not installed")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok := func(sub, kind string, scopes ...string) string {
		s, _ := iss.Issue(authn.Principal{Subject: sub, Kind: kind, Scopes: scopes}, time.Hour)
		return s
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wsrv := &rpc.Server{Verifier: ver, Log: log}
	wsrv.Register("WorkflowService", workflowapi.WorkflowServiceOps(termWorkflow{}))
	wf := httptest.NewServer(rpc.Mux(wsrv, nil))
	defer wf.Close()
	sys := compose.New(compose.Config{Workflow: wf.URL, ServiceToken: tok("service:magi", authn.KindService)}, nil, log)
	sys.Terminals.Wait = 10 * time.Second
	srv := &rpc.Server{Verifier: ver, Log: log}
	srv.Register("RunnerService", magiapi.RunnerServiceOps(&compose.Runners{S: sys}))
	mux := rpc.Mux(srv, nil)
	mux.Handle("/api/terminal", compose.Terminal(ver, sys))
	mux.Handle("/runner/terminal", compose.RunnerTerminal(ver, sys.Terminals))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	state := t.TempDir()
	rtok := tok("runner:test", authn.KindService, compose.ScopeRunner)
	os.WriteFile(filepath.Join(state, "token"), []byte(rtok), 0o600)
	self, _ := os.Executable()
	box := sandbox.Config{Root: filepath.Join(state, "envs"), Envd: self, EnvdArgs: []string{"envd"}, Bwrap: bwrap}
	r := &Runner{Cfg: Config{Server: ts.URL, Name: "test", StateDir: state, TokenFile: filepath.Join(state, "token")}, Log: log,
		Magi: magiapi.NewRunnerClient(rpc.NewClient(ts.URL, rpc.StaticToken(rtok))), Boxes: &sandbox.Sandboxes{Cfg: box}}
	if err := r.Boxes.Start("run_t", "0123456789abcdef0123456789abcdef", r.sandboxEnv(), nil); err != nil {
		t.Fatal(err)
	}
	defer r.removeBox("run_t")
	if _, err := r.Boxes.Wait("run_t", r.Boxes.StartTimeout()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.terminalLoop(ctx)

	open := func(run string) *websocket.Conn {
		cfg, _ := websocket.NewConfig(strings.Replace(ts.URL, "http", "ws", 1)+"/api/terminal?task="+run, ts.URL)
		ws, err := websocket.DialConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"token": tok("user:owner@example.local", authn.KindUser), "cols": 80, "rows": 24})
		websocket.Message.Send(ws, string(b))
		return ws
	}
	var out strings.Builder
	var exit *int32
	read := func(ws *websocket.Conn, want string) {
		t.Helper()
		for !strings.Contains(out.String(), want) {
			ws.SetReadDeadline(time.Now().Add(15 * time.Second))
			var b []byte
			if err := websocket.Message.Receive(ws, &b); err != nil {
				t.Fatalf("waiting for %q: %v; output %q", want, err, out.String())
			}
			switch b[0] {
			case envd.FrameData:
				out.Write(b[1:])
			case envd.FrameExit:
				c, _ := envd.ParseExit(b[1:])
				exit = &c
				return
			case envd.FrameError:
				t.Fatalf("error: %s", b[1:])
			}
		}
	}
	ws := open("run_t")
	defer ws.Close()
	send := func(s string) { websocket.Message.Send(ws, append([]byte{envd.FrameData}, s...)) }
	// The shell starts in the work directory of envd: /work/repo does
	// not exist in this sandbox.
	home, _ := os.UserHomeDir()
	send("echo hi-$((40+2)) $(pwd); test -e " + home + " && echo home-seen || echo home-hidden\n")
	read(ws, "hi-42 /work")
	read(ws, "home-hidden")
	websocket.Message.Send(ws, append([]byte{envd.FrameResize}, envd.ResizePayload(100, 30)...))
	send("echo size-$(stty size | tr ' ' x)\n")
	read(ws, "size-30x100")
	send("exit 7\n")
	read(ws, "\x00never")
	if exit == nil || *exit != 7 {
		t.Fatalf("exit %v; output %q", exit, out.String())
	}

	// A run whose sandbox is not on the runner: a clear error.
	ws2 := open("run_gone")
	defer ws2.Close()
	ws2.SetReadDeadline(time.Now().Add(15 * time.Second))
	var b []byte
	if err := websocket.Message.Receive(ws2, &b); err != nil || b[0] != envd.FrameError || !strings.Contains(string(b), "not on runner test") {
		t.Fatalf("missing sandbox: %q %v", b, err)
	}
}
