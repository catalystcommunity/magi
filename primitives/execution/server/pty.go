package server

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
)

// PathPTY is the terminal route of the service:
// GET /v1/pty?environment=ID&cols=N&rows=N[&cwd=DIR], an HTTP upgrade with
// a bearer token. The caller must be the owner of the environment, or
// have execution:act-as or execution:admin. After the 101 answer, the
// connection carries the terminal frames of envd (envd/frame.go) to a
// shell in the environment.
const PathPTY = "/v1/pty"

// PTYHandler serves PathPTY.
func (s *Service) PTYHandler(ver *authn.Verifier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected an upgrade", http.StatusBadRequest)
			return
		}
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		p, err := ver.Verify(tok)
		if err != nil {
			http.Error(w, "a valid token is required", http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		ctx := authn.With(r.Context(), p)
		c, rec, err := s.envdClient(ctx, q.Get("environment"))
		if err != nil {
			code := rpc.CodeOf(err)
			if code == 0 {
				code = http.StatusInternalServerError
			}
			http.Error(w, err.Error(), code)
			return
		}
		cols, _ := strconv.ParseUint(q.Get("cols"), 10, 16)
		rows, _ := strconv.ParseUint(q.Get("rows"), 10, 16)
		req := envd.PTYRequest{Cwd: q.Get("cwd"), Cols: uint16(cols), Rows: uint16(rows)}
		// The shell lives as long as the connection, not the request.
		pty, err := c.PTY(context.WithoutCancel(ctx), req)
		if err != nil && req.Cwd != "" {
			req.Cwd = ""
			pty, err = c.PTY(context.WithoutCancel(ctx), req)
		}
		if err != nil {
			e := s.agentError(ctx, rec, err)
			http.Error(w, e.Error(), rpc.CodeOf(e))
			return
		}
		defer pty.Close()
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
		start := time.Now()
		s.log().Info("terminal opened", "environment", rec.Env.Id, "owner", rec.Env.Owner, "by", p.Subject)
		envd.Pipe(&envd.BufConn{Conn: conn, R: brw.Reader}, pty)
		s.log().Info("terminal closed", "environment", rec.Env.Id, "by", p.Subject, "seconds", int(time.Since(start).Seconds()))
	})
}
