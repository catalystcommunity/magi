package host

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
)

// The test binary is also the fake engine.
func TestMain(m *testing.M) {
	if port := os.Getenv("FAKE_ENGINE_PORT"); port != "" {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {})
		http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, `{"echo":%q,"auth":%q}`, b, r.Header.Get("Authorization"))
		})
		http.ListenAndServe("127.0.0.1:"+port, nil)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeSampler struct {
	mu sync.Mutex
	s  Sample
}

func (f *fakeSampler) Sample(context.Context, int) Sample {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.s
}

func (f *fakeSampler) set(s Sample) { f.mu.Lock(); f.s = s; f.mu.Unlock() }

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type env struct {
	h    *Host
	s    *fakeSampler
	srv  *httptest.Server
	tok  func(scopes ...string) string
	now  time.Time
	mu   sync.Mutex
	stop context.CancelFunc
}

func setup(t *testing.T, startWait time.Duration) *env {
	pub, priv, _ := ed25519.GenerateKey(nil)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	port := freePort(t)
	os.Setenv("FAKE_ENGINE_PORT", fmt.Sprint(port))
	t.Cleanup(func() { os.Unsetenv("FAKE_ENGINE_PORT") })
	e := &env{s: &fakeSampler{s: Sample{GPUUtil: 0, GPUUsedMiB: 1000, GPUTotalMiB: 16000}}, now: time.Now()}
	rules := DefaultRules()
	rules.QuietFor = Duration(30 * time.Second)
	e.h = New(Config{
		Engine: Engine{Command: []string{os.Args[0]}, Port: port, Model: "local"}, Rules: rules,
		IdleStop: 10 * time.Minute, StartWait: startWait, Interval: time.Hour,
	}, e.s, ver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.h.now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
	ctx, cancel := context.WithCancel(context.Background())
	e.stop = cancel
	done := make(chan struct{})
	go func() { e.h.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; time.Sleep(100 * time.Millisecond) })
	e.srv = httptest.NewServer(e.h.OpenAI())
	t.Cleanup(e.srv.Close)
	e.tok = func(scopes ...string) string {
		tok, err := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: scopes}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	return e
}

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func (e *env) chat(t *testing.T, tok string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"local"}`))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Error struct{ Type string } `json:"error"`
	}
	json.Unmarshal(b, &out)
	return resp.StatusCode, out.Error.Type, string(b)
}

func (e *env) state() string {
	e.h.mu.Lock()
	defer e.h.mu.Unlock()
	return e.h.statusLocked().State
}

func waitState(t *testing.T, e *env, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if e.state() == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("state %s, want %s", e.state(), want)
}

func TestAuth(t *testing.T) {
	e := setup(t, 0)
	if code, _, _ := e.chat(t, ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _, _ := e.chat(t, e.tok("other:scope")); code != 403 {
		t.Fatalf("no scope: %d", code)
	}
	if e.state() != "off" {
		t.Fatalf("refused requests must not start the engine: %s", e.state())
	}
}

func TestColdStartThenServe(t *testing.T) {
	e := setup(t, 0)
	tok := e.tok(Scope)
	if code, kind, _ := e.chat(t, tok); code != 503 || kind != "warming" {
		t.Fatalf("cold: %d %s", code, kind)
	}
	waitState(t, e, "ready")
	code, _, body := e.chat(t, tok)
	if code != 200 || !strings.Contains(body, `"auth":""`) {
		t.Fatalf("ready: %d %s (the caller's token must not reach the engine)", code, body)
	}
	// Idle stop.
	e.advance(11 * time.Minute)
	e.h.check(context.Background())
	waitState(t, e, "off")
}

func TestWaitForColdStart(t *testing.T) {
	e := setup(t, 20*time.Second)
	if code, _, body := e.chat(t, e.tok(Scope)); code != 200 {
		t.Fatalf("waited start: %d %s", code, body)
	}
}

func TestBusyMachineYields(t *testing.T) {
	e := setup(t, 20*time.Second)
	tok := e.tok(Scope)
	if code, _, _ := e.chat(t, tok); code != 200 {
		t.Fatal("first request")
	}
	// A compile starts: the idle engine stops and new work is refused.
	e.s.set(Sample{CPULoad: 0.1, GPUUtil: 0, GPUUsedMiB: 7000, GPUTotalMiB: 16000, Processes: []string{"bash", "rustc"}})
	e.h.check(context.Background())
	waitState(t, e, "busy")
	if code, kind, _ := e.chat(t, tok); code != 503 || kind != "host_busy" {
		t.Fatalf("busy: %d %s", code, kind)
	}
	// Quiet again, but not for long enough.
	e.s.set(Sample{GPUUtil: 0, GPUUsedMiB: 1000, GPUTotalMiB: 16000})
	e.h.check(context.Background())
	e.advance(10 * time.Second)
	e.h.check(context.Background())
	if e.state() != "busy" {
		t.Fatalf("quiet too soon: %s", e.state())
	}
	e.advance(25 * time.Second)
	e.h.check(context.Background())
	if code, _, body := e.chat(t, tok); code != 200 {
		t.Fatalf("after quiet: %d %s", code, body)
	}
}

// The engine's own GPU use while it loads and just after an answer does not
// make the machine busy. Other GPU use after that does.
func TestOwnGPUUseIsNotBusy(t *testing.T) {
	e := setup(t, 20*time.Second)
	e.s.set(Sample{GPUUtil: 90, GPUUsedMiB: 9000, GPUTotalMiB: 16000})
	e.h.mu.Lock()
	e.h.startLocked()
	e.h.mu.Unlock()
	e.h.check(context.Background()) // loading
	if st := e.state(); st == "busy" || st == "off" {
		t.Fatalf("loading engine stopped: %s", st)
	}
	e.s.set(Sample{GPUUtil: 0, GPUUsedMiB: 9000, GPUTotalMiB: 16000})
	if code, _, body := e.chat(t, e.tok(Scope)); code != 200 {
		t.Fatalf("chat: %d %s", code, body)
	}
	e.s.set(Sample{GPUUtil: 90, GPUUsedMiB: 9000, GPUTotalMiB: 16000})
	e.advance(5 * time.Second)
	e.h.check(context.Background()) // just after the answer
	if st := e.state(); st != "ready" {
		t.Fatalf("after answer: %s", st)
	}
	e.advance(30 * time.Second)
	e.h.check(context.Background()) // someone else uses the GPU
	waitState(t, e, "busy")
}

func TestRules(t *testing.T) {
	r := DefaultRules()
	cases := []struct {
		s               Sample
		running, active bool
		busy            bool
	}{
		{Sample{CPULoad: 0.9, GPUUtil: -1, GPUTotalMiB: -1}, false, false, true},
		{Sample{GPUUtil: 90, GPUUsedMiB: 2000, GPUTotalMiB: 16000}, false, false, true}, // a game
		{Sample{GPUUtil: 90, GPUUsedMiB: 9000, GPUTotalMiB: 16000}, true, true, false},  // the engine itself
		{Sample{GPUUtil: 0, GPUUsedMiB: 12000, GPUTotalMiB: 16000}, false, false, true}, // no room to start
		{Sample{GPUUtil: 0, GPUUsedMiB: 12000, GPUTotalMiB: 16000}, true, false, false}, // the room is the engine's
		{Sample{GPUUtil: -1, GPUUsedMiB: -1, GPUTotalMiB: -1}, false, false, false},     // no GPU data
	}
	for i, c := range cases {
		if got := r.busyReason(c.s, c.running, c.active) != ""; got != c.busy {
			t.Errorf("case %d: busy = %v", i, got)
		}
	}
}
