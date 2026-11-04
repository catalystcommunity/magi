package runner

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/primitives/execution/egressenv"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
)

// The egress proxy route of a sandbox: a command in the sandbox connects
// to sandbox.ProxyAddr, envd copies the connection to the proxy socket,
// and the runner sends it through GET /runner/egress on Magi to the egress
// proxy. The runner machine connects only to Magi.

// egressAccess is the proxy user of an environment. It is kept next to
// the environment, out of the sandbox, so that a restarted runner can
// serve the environment again.
type egressAccess struct {
	Environment string `json:"environment"`
	Credential  string `json:"credential"`
}

func (r *Runner) accessFile(id string) string {
	return filepath.Join(r.Boxes.Dir(id), "egress-access.json")
}

// egressProxy asks Magi how the sandbox of the step reaches the egress
// proxy. nil means that the sandbox uses the host network: Magi has no
// egress proxy for runners, or this sandbox kind cannot isolate the
// network.
func (r *Runner) egressProxy(ctx context.Context, st magiapi.RunnerStep) (*magiapi.RunnerEgressProxy, error) {
	res, err := r.Magi.EgressAccess(ctx, st)
	if rpc.CodeOf(err) == 404 {
		r.Log.Warn("Magi has no egress-access operation (an older release); the sandbox uses the host network")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("egress access: %w", err)
	}
	if res.Proxy == nil {
		return nil, nil
	}
	if !r.Boxes.CanProxy() {
		r.Log.Warn("this sandbox kind shares the host network; the egress proxy is not used")
		return nil, nil
	}
	return res.Proxy, nil
}

// startBox starts the sandbox of env, with the egress proxy when p is set.
func (r *Runner) startBox(env, token string, p *magiapi.RunnerEgressProxy) error {
	if p == nil {
		return r.Boxes.Start(env, token, r.sandboxEnv(), nil)
	}
	sp := &sandbox.Proxy{CA: p.Ca}
	if p.SshKnownHosts != nil {
		sp.SSHKnownHosts = []byte(*p.SshKnownHosts + "\n")
	}
	if err := r.Boxes.Start(env, token, r.proxyEnv(p.Environment, p.Credential), sp); err != nil {
		return err
	}
	b, _ := json.Marshal(egressAccess{Environment: p.Environment, Credential: p.Credential})
	if err := os.WriteFile(r.accessFile(env), b, 0o600); err != nil {
		return err
	}
	return r.serveProxy(env)
}

func (r *Runner) proxyEnv(env, credential string) map[string]string {
	all := r.sandboxEnv()
	for k, v := range egressenv.Vars(egressenv.URL(sandbox.ProxyAddr, env, credential), r.Boxes.ProxyCA()) {
		all[k] = v
	}
	return all
}

// resumeBox starts envd again in an existing environment, and its proxy.
func (r *Runner) resumeBox(id string) error {
	if !r.Boxes.Proxied(id) {
		return r.Boxes.Resume(id, r.sandboxEnv())
	}
	b, err := os.ReadFile(r.accessFile(id))
	if err != nil {
		return err
	}
	var a egressAccess
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if err := r.serveProxy(id); err != nil {
		return err
	}
	return r.Boxes.Resume(id, r.proxyEnv(a.Environment, a.Credential))
}

// serveProxy serves the proxy socket of an environment until removeBox.
func (r *Runner) serveProxy(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proxies[id] != nil {
		return nil
	}
	ln, err := r.Boxes.ListenProxy(id)
	if err != nil {
		return err
	}
	if r.proxies == nil {
		r.proxies = map[string]net.Listener{}
	}
	r.proxies[id] = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := r.dialEgress(context.Background())
				if err != nil {
					r.Log.Warn("egress tunnel", "env", id, "err", err)
					return
				}
				defer up.Close()
				envd.Splice(c, up)
			}()
		}
	}()
	return nil
}

// removeBox stops the proxy of an environment and removes it.
func (r *Runner) removeBox(id string) error {
	r.mu.Lock()
	if ln := r.proxies[id]; ln != nil {
		ln.Close()
		delete(r.proxies, id)
	}
	r.mu.Unlock()
	return r.Boxes.Remove(id)
}

// dialEgress opens a tunnel to the egress proxy: an HTTP/1.1 upgrade on
// GET /runner/egress with the runner token. The upgrade has the form of a
// WebSocket upgrade, so that HTTP gateways pass it; after the 101 answer
// the connection carries the proxy protocol, not WebSocket frames.
func (r *Runner) dialEgress(ctx context.Context) (net.Conn, error) {
	u, err := url.Parse(r.Cfg.Server)
	if err != nil {
		return nil, err
	}
	tok, err := os.ReadFile(r.Cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), map[string]string{"https": "443", "http": "80"}[u.Scheme])
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var conn net.Conn
	switch u.Scheme {
	case "https":
		d := &tls.Dialer{Config: &tls.Config{ServerName: u.Hostname(), NextProtos: []string{"http/1.1"}}}
		conn, err = d.DialContext(ctx, "tcp", host)
	case "http":
		var d net.Dialer
		conn, err = d.DialContext(ctx, "tcp", host)
	default:
		return nil, fmt.Errorf("server %q: expected http or https", r.Cfg.Server)
	}
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	key := make([]byte, 16)
	rand.Read(key)
	req := fmt.Sprintf("GET %s/runner/egress HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nAuthorization: Bearer %s\r\n\r\n",
		strings.TrimRight(u.Path, "/"), u.Host, base64.StdEncoding.EncodeToString(key), strings.TrimSpace(string(tok)))
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		conn.Close()
		return nil, fmt.Errorf("egress tunnel: %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	conn.SetDeadline(time.Time{})
	return &bufConn{Conn: conn, r: br}, nil
}

// bufConn is a connection with bytes that a reader already buffered.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *bufConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}
