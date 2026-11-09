package envd

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DialUpgrade opens an HTTP/1.1 upgrade to GET server+path (path can have
// a query) with a bearer token, and returns the connection after the 101
// answer. The upgrade has the form of a WebSocket upgrade, so that HTTP
// gateways pass it; the bytes after it are not WebSocket frames. server
// is an http or https base URL; the dial and the answer must come within
// 15 s.
func DialUpgrade(ctx context.Context, server, path, token string) (net.Conn, error) {
	u, err := url.Parse(server)
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
		return nil, fmt.Errorf("server %q: expected http or https", server)
	}
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	key := make([]byte, 16)
	rand.Read(key)
	req := fmt.Sprintf("GET %s%s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nAuthorization: Bearer %s\r\n\r\n",
		strings.TrimRight(u.Path, "/"), path, u.Host, base64.StdEncoding.EncodeToString(key), strings.TrimSpace(token))
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
		return nil, fmt.Errorf("upgrade %s: %s: %s", path, res.Status, strings.TrimSpace(string(b)))
	}
	conn.SetDeadline(time.Time{})
	return &BufConn{Conn: conn, R: br}, nil
}

// BufConn is a connection with bytes that a reader already buffered.
type BufConn struct {
	net.Conn
	R *bufio.Reader
}

func (c *BufConn) Read(p []byte) (int, error) { return c.R.Read(p) }

func (c *BufConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// Pipe copies a to b and b to a. When one direction ends, it closes both,
// and returns when both copies end. A terminal stream needs no half close.
func Pipe(a, b io.ReadWriteCloser) {
	var once sync.Once
	stop := func() { once.Do(func() { a.Close(); b.Close() }) }
	done := make(chan struct{}, 2)
	cp := func(dst io.Writer, src io.Reader) {
		io.Copy(dst, src)
		stop()
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
