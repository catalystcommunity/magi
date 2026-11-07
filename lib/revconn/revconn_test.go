package revconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func start(t *testing.T, p *Pool) (*httptest.Server, func(idle int, h http.Handler) context.CancelFunc) {
	t.Helper()
	srv := httptest.NewTLSServer(p.Handler(func(r *http.Request) (string, error) {
		if r.Header.Get("Authorization") != "Bearer good" {
			return "", errors.New("bad token")
		}
		return "box", nil
	}))
	t.Cleanup(srv.Close)
	agent := func(idle int, h http.Handler) context.CancelFunc {
		ctx, cancel := context.WithCancel(context.Background())
		a := &Agent{URL: srv.URL + "/connect", Token: func() string { return "good" }, Idle: idle, HTTP: srv.Client()}
		done := make(chan struct{})
		go func() { a.Serve(ctx, h); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		return cancel
	}
	return srv, agent
}

func waitIdle(t *testing.T, p *Pool, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if p.Connected()["box"] >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("idle %v, want %d", p.Connected(), n)
}

func TestRequestsGoToTheAgent(t *testing.T) {
	p := &Pool{}
	_, agent := start(t, p)
	agent(2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s %s %s", r.Method, r.URL.Path, r.Header.Get("Authorization"), b)
	}))
	waitIdle(t, p, 2)
	c := &http.Client{Transport: p.Transport(nil)}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", "http://box.revconn/v1/chat/completions", strings.NewReader(fmt.Sprint(i)))
			req.Header.Set("Authorization", "Bearer caller")
			res, err := c.Do(req)
			if err != nil {
				errs <- err
				return
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if want := fmt.Sprintf("POST /v1/chat/completions Bearer caller %d", i); string(b) != want {
				errs <- fmt.Errorf("got %q, want %q", b, want)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	// The agent opens new idle connections after the requests.
	waitIdle(t, p, 2)
}

func TestBadTokenIsRefused(t *testing.T) {
	p := &Pool{}
	srv, _ := start(t, p)
	a := &Agent{URL: srv.URL + "/connect", Token: func() string { return "bad" }, HTTP: srv.Client()}
	if _, err := a.open(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err %v", err)
	}
	if len(p.Connected()) != 0 {
		t.Fatal("a refused agent must not be in the pool")
	}
}

func TestNoAgentFailsFast(t *testing.T) {
	p := &Pool{Wait: 5 * time.Second}
	c := &http.Client{Transport: p.Transport(nil)}
	start := time.Now()
	_, err := c.Get("http://box.revconn/x")
	if !errors.Is(err, ErrNoAgent) || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestClosedAgentLeavesThePool(t *testing.T) {
	p := &Pool{}
	_, agent := start(t, p)
	cancel := agent(2, http.NotFoundHandler())
	waitIdle(t, p, 2)
	cancel()
	for i := 0; i < 200 && len(p.Connected()) > 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if len(p.Connected()) != 0 {
		t.Fatalf("idle %v after the agent stopped", p.Connected())
	}
}

func TestMaxAgeRenewsConnections(t *testing.T) {
	p := &Pool{MaxAge: 100 * time.Millisecond}
	_, agent := start(t, p)
	agent(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	waitIdle(t, p, 1)
	time.Sleep(350 * time.Millisecond) // more than one renewal
	waitIdle(t, p, 1)
	res, err := (&http.Client{Transport: p.Transport(nil)}).Get("http://box.revconn/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "ok" {
		t.Fatalf("got %q", b)
	}
}

func TestOtherHostsUseNext(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "next") }))
	defer other.Close()
	res, err := (&http.Client{Transport: (&Pool{}).Transport(nil)}).Get(other.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "next" {
		t.Fatalf("got %q", b)
	}
}

// TestBusyAgentWaits: when all connections of a connected agent are in
// use, a request waits for the next one.
func TestBusyAgentWaits(t *testing.T) {
	p := &Pool{}
	_, agent := start(t, p)
	release := make(chan struct{})
	agent(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-release
		}
		io.WriteString(w, "ok")
	}))
	waitIdle(t, p, 1)
	c := &http.Client{Transport: p.Transport(nil)}
	go c.Get("http://box.revconn/slow")
	for i := 0; i < 200 && p.Connected()["box"] > 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	go func() { time.Sleep(100 * time.Millisecond); close(release) }()
	res, err := c.Get("http://box.revconn/fast")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
}

// TestCloseMovesAgents: after Close, the agent connects to the next server.
func TestCloseMovesAgents(t *testing.T) {
	p1, p2 := &Pool{}, &Pool{}
	cur := p1
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		p := cur
		mu.Unlock()
		p.Handler(func(*http.Request) (string, error) { return "box", nil }).ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &Agent{URL: srv.URL, Idle: 1, HTTP: srv.Client()}
	go a.Serve(ctx, http.NotFoundHandler())
	waitIdle(t, p1, 1)
	mu.Lock()
	cur = p2
	mu.Unlock()
	p1.Close()
	waitIdle(t, p2, 1)
}
