// Package revconn lets a machine that only connects out serve HTTP.
//
// The machine (the agent) keeps a few idle connections open to a server.
// It opens each one with an HTTP upgrade and then serves HTTP on it. The
// server keeps the idle connections of each agent in a Pool, and sends
// each request on one of them. Each connection carries one request; then
// it closes, and the agent opens a new one.
//
// The upgrade uses the WebSocket handshake, so that gateways pass it, but
// the bytes after it are plain HTTP/1.1, not WebSocket frames.
package revconn

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ErrNoAgent is returned when no idle connection of the agent comes in time.
var ErrNoAgent = errors.New("revconn: the agent is not connected")

// Pool holds the idle connections of each agent, on the server.
type Pool struct {
	// MaxAge closes an idle connection after this time, so that the agent
	// opens a new one. Gateways close connections that are idle for long.
	// Default 4 minutes.
	MaxAge time.Duration
	// Wait is how long a request waits for an idle connection. Default 2 s.
	Wait time.Duration
	Log  *slog.Logger

	mu     sync.Mutex
	idle   map[string][]*idleConn
	notify map[string]chan struct{}
	seen   map[string]time.Time // the last new connection of each agent
}

type idleConn struct {
	net.Conn
	at   time.Time
	stop chan struct{} // closed to stop the watch
	done chan struct{} // closed when the watch stopped
	dead bool
}

// Handler accepts connections of agents. name gives the agent name of a
// request (from its token), or an error, which refuses the connection.
func (p *Pool) Handler(name func(r *http.Request) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected an upgrade", http.StatusBadRequest)
			return
		}
		agent, err := name(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
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
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(sum[:]))
		if err := brw.Flush(); err != nil || brw.Reader.Buffered() > 0 {
			// The agent sends nothing before a request.
			conn.Close()
			return
		}
		p.add(agent, conn)
	})
}

func (p *Pool) add(agent string, conn net.Conn) {
	c := &idleConn{Conn: conn, at: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	p.mu.Lock()
	p.init()
	p.seen[agent] = c.at
	first := len(p.idle[agent]) == 0
	p.idle[agent] = append(p.idle[agent], c)
	if ch := p.notify[agent]; ch != nil {
		close(ch)
		delete(p.notify, agent)
	}
	p.mu.Unlock()
	if first && p.Log != nil {
		p.Log.Debug("revconn agent connection", "agent", agent)
	}
	go p.watch(agent, c)
}

// watch reads from an idle connection. The agent sends nothing while it
// waits, so a read that returns means that the connection closed. It also
// closes the connection at MaxAge.
func (p *Pool) watch(agent string, c *idleConn) {
	defer close(c.done)
	maxAge := p.MaxAge
	if maxAge <= 0 {
		maxAge = 4 * time.Minute
	}
	c.SetReadDeadline(c.at.Add(maxAge))
	read := make(chan struct{})
	go func() {
		var b [1]byte
		c.Read(b[:])
		close(read)
	}()
	select {
	case <-c.stop:
		// Taken for a request: end the read without closing.
		c.SetReadDeadline(time.Now())
		<-read
		c.SetReadDeadline(time.Time{})
		return
	case <-read:
	}
	if p.remove(agent, c) {
		c.Close()
		return
	}
	// A request took c at the same time: give it without the deadline.
	c.SetReadDeadline(time.Time{})
}

// remove takes c out of the idle list. It is false when a request took c.
func (p *Pool) remove(agent string, c *idleConn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.idle[agent]
	for i, x := range l {
		if x == c {
			p.idle[agent] = append(l[:i:i], l[i+1:]...)
			return true
		}
	}
	return false
}

// Connected gives the number of idle connections of each agent.
func (p *Pool) Connected() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for a, l := range p.idle {
		if len(l) > 0 {
			out[a] = len(l)
		}
	}
	return out
}

func (p *Pool) init() {
	if p.idle == nil {
		p.idle, p.notify, p.seen = map[string][]*idleConn{}, map[string]chan struct{}{}, map[string]time.Time{}
	}
}

// Dial takes an idle connection of agent. It waits up to Wait for one, but
// only when the agent opened a connection in the last minute: else the
// agent is not connected, and Dial fails at once, so that the caller uses
// another provider.
func (p *Pool) Dial(ctx context.Context, agent string) (net.Conn, error) {
	wait := p.Wait
	if wait <= 0 {
		wait = 2 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		p.init()
		if l := p.idle[agent]; len(l) > 0 {
			c := l[len(l)-1] // the newest
			p.idle[agent] = l[:len(l)-1]
			p.mu.Unlock()
			close(c.stop)
			<-c.done
			return c.Conn, nil
		}
		if time.Since(p.seen[agent]) > time.Minute {
			p.mu.Unlock()
			return nil, fmt.Errorf("%w: %s", ErrNoAgent, agent)
		}
		ch := p.notify[agent]
		if ch == nil {
			ch = make(chan struct{})
			p.notify[agent] = ch
		}
		p.mu.Unlock()
		select {
		case <-ch:
		case <-deadline.C:
			return nil, fmt.Errorf("%w: %s", ErrNoAgent, agent)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Suffix is the host name suffix of an agent in a URL: http://<agent>.revconn/...
const Suffix = ".revconn"

// Transport sends requests for hosts "<agent>.revconn" through the pool,
// and other requests through next (http.DefaultTransport when nil).
func (p *Pool) Transport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			return p.Dial(ctx, strings.TrimSuffix(host, Suffix))
		},
		// One request per connection: the pool, not the transport, keeps
		// the connections.
		DisableKeepAlives: true,
	}
	return roundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Hostname(), Suffix) {
			if r.URL.Scheme != "http" {
				return nil, errors.New("revconn: use http:// for an agent (the connection to the agent is already TLS)")
			}
			return t.RoundTrip(r)
		}
		return next.RoundTrip(r)
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Agent opens connections to a server and serves HTTP on them.
type Agent struct {
	URL   string        // the Handler of the server, https://...
	Token func() string // bearer token for each connection
	Idle  int           // idle connections to keep; default 2
	Log   *slog.Logger
	// HTTP opens the connections. It must use HTTP/1.1. The default does.
	HTTP *http.Client
}

// Serve keeps Idle connections open and serves h on them until ctx ends.
func (a *Agent) Serve(ctx context.Context, h http.Handler) error {
	idle := a.Idle
	if idle <= 0 {
		idle = 2
	}
	log := a.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 0}
	slots := make(chan struct{}, idle)
	for range idle {
		slots <- struct{}{}
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	backoff := time.Second
	connected := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-slots:
		}
		conn, err := a.open(ctx)
		if err != nil {
			slots <- struct{}{}
			if ctx.Err() != nil {
				return nil
			}
			if connected {
				log.Warn("revconn disconnected", "url", a.URL, "err", err)
				connected = false
			} else {
				log.Debug("revconn connect", "url", a.URL, "err", err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		if !connected {
			log.Info("revconn connected", "url", a.URL)
			connected = true
		}
		backoff = time.Second
		// The slot is free again at the first byte of a request, or when
		// the server closes the idle connection.
		var once sync.Once
		free := func() { once.Do(func() { slots <- struct{}{} }) }
		c := &firstRead{Conn: conn, first: free}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer free()
			l := &oneConn{c: c, closed: make(chan struct{})}
			stop := context.AfterFunc(ctx, func() { c.Close() })
			defer stop()
			srv.Serve(l)
		}()
	}
}

// open dials the server and does the upgrade.
func (a *Agent) open(ctx context.Context) (net.Conn, error) {
	key := make([]byte, 16)
	rand.Read(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(key))
	if a.Token != nil {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.Token()))
	}
	hc := a.HTTP
	if hc == nil {
		// An upgrade needs HTTP/1.1: the empty TLSNextProto turns HTTP/2 off.
		hc = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 10 * time.Second,
			TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}}
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := hc.Do(req.WithContext(dctx))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		res.Body.Close()
		return nil, fmt.Errorf("upgrade: %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	rwc, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		res.Body.Close()
		return nil, errors.New("upgrade: the response body is not a connection")
	}
	return &rwConn{ReadWriteCloser: rwc}, nil
}

// firstRead calls first at the first byte that it reads.
type firstRead struct {
	net.Conn
	first func()
	seen  bool
}

func (c *firstRead) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && !c.seen {
		c.seen = true
		c.first()
	}
	return n, err
}

// oneConn is a listener that gives one connection, then waits until it closes.
type oneConn struct {
	mu     sync.Mutex
	c      net.Conn
	closed chan struct{}
}

func (l *oneConn) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.c
	l.c = nil
	l.mu.Unlock()
	if c != nil {
		return &notify{Conn: c, l: l}, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConn) Close() error   { return nil }
func (l *oneConn) Addr() net.Addr { return addr{} }

type notify struct {
	net.Conn
	l    *oneConn
	once sync.Once
}

func (n *notify) Close() error {
	n.once.Do(func() { close(n.l.closed) })
	return n.Conn.Close()
}

// rwConn is a net.Conn over the body of an upgraded response.
type rwConn struct{ io.ReadWriteCloser }

func (rwConn) LocalAddr() net.Addr              { return addr{} }
func (rwConn) RemoteAddr() net.Addr             { return addr{} }
func (rwConn) SetDeadline(time.Time) error      { return nil }
func (rwConn) SetReadDeadline(time.Time) error  { return nil }
func (rwConn) SetWriteDeadline(time.Time) error { return nil }

type addr struct{}

func (addr) Network() string { return "revconn" }
func (addr) String() string  { return "revconn" }
