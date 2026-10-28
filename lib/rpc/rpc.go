// Package rpc carries CSIL-encoded calls over HTTP.
//
// A call is POST /rpc/{Service}/{op} with a CBOR request body. A success
// returns 200 and a CBOR response body. A failure returns a non-2xx status
// and a JSON Error body. Generated CSIL clients use Transport; servers
// register the operation maps that dispatchgen writes.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
)

// Error is a service error. Code uses HTTP status values.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func Invalid(format string, args ...any) *Error {
	return Errorf(http.StatusBadRequest, format, args...)
}
func NotFound(format string, args ...any) *Error {
	return Errorf(http.StatusNotFound, format, args...)
}
func Denied(format string, args ...any) *Error {
	return Errorf(http.StatusForbidden, format, args...)
}
func Conflict(format string, args ...any) *Error {
	return Errorf(http.StatusConflict, format, args...)
}
func Unavailable(format string, args ...any) *Error {
	return Errorf(http.StatusServiceUnavailable, format, args...)
}

// CodeOf returns the service error code of err, or 0.
func CodeOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

// Op handles one encoded request. It is an alias, so the tables that
// dispatchgen writes are map[string]Op without conversion.
type Op = func(ctx context.Context, req []byte) ([]byte, error)

// Server dispatches calls to registered services.
type Server struct {
	// Verifier authenticates bearer tokens. A nil Verifier rejects every call.
	Verifier *authn.Verifier
	// Anonymous lists "Service/op" names that need no token.
	Anonymous map[string]bool
	// MaxBody limits request size. The default is 16 MiB.
	MaxBody  int64
	Log      *slog.Logger
	services map[string]map[string]Op
}

// Register adds the operations of one service. ops is usually the table
// that dispatchgen writes, such as api.ArtifactServiceOps(impl).
func (s *Server) Register(service string, ops map[string]Op) {
	if s.services == nil {
		s.services = map[string]map[string]Op{}
	}
	s.services[service] = ops
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/rpc/"), "/")
	if r.Method != http.MethodPost || len(parts) != 2 {
		writeError(w, Invalid("expected POST /rpc/{service}/{op}"))
		return
	}
	op, ok := s.services[parts[0]][parts[1]]
	if !ok {
		writeError(w, NotFound("unknown operation %s/%s", parts[0], parts[1]))
		return
	}
	ctx := r.Context()
	if !s.Anonymous[parts[0]+"/"+parts[1]] {
		p, err := s.authenticate(r)
		if err != nil {
			writeError(w, Errorf(http.StatusUnauthorized, "%v", err))
			return
		}
		ctx = authn.With(ctx, p)
	}
	limit := s.MaxBody
	if limit == 0 {
		limit = 16 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		writeError(w, Invalid("request body unreadable or too large"))
		return
	}
	start := time.Now()
	resp, err := op(ctx, body)
	if s.Log != nil {
		s.Log.Info("rpc", "op", parts[0]+"/"+parts[1], "ms", time.Since(start).Milliseconds(), "code", CodeOf(err), "err", errString(err))
	}
	if err != nil {
		var e *Error
		var sc interface{ StatusCode() int }
		if !errors.As(err, &e) && errors.As(err, &sc) {
			e = Errorf(sc.StatusCode(), "%v", err)
		}
		if e == nil {
			e = Errorf(http.StatusInternalServerError, "internal error")
			if s.Log != nil {
				s.Log.Error("rpc internal", "op", parts[0]+"/"+parts[1], "err", err)
			}
		}
		writeError(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.Write(resp)
}

func (s *Server) authenticate(r *http.Request) (authn.Principal, error) {
	if s.Verifier == nil {
		return authn.Principal{}, errors.New("authentication is not configured")
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return authn.Principal{}, errors.New("bearer token required")
	}
	return s.Verifier.Verify(strings.TrimPrefix(h, "Bearer "))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeError(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Code)
	json.NewEncoder(w).Encode(e)
}

// Caller returns the authenticated principal of a call.
func Caller(ctx context.Context) (authn.Principal, error) {
	p, ok := authn.From(ctx)
	if !ok {
		return p, Errorf(http.StatusUnauthorized, "no caller")
	}
	return p, nil
}

type tokenKey struct{}

// WithToken makes calls with ctx use token. A service uses it to forward
// the caller's own token, so the next service authorizes the original caller.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

// Forward returns a context that forwards the current caller's token.
func Forward(ctx context.Context) context.Context {
	if p, ok := authn.From(ctx); ok && p.Token != "" {
		return WithToken(ctx, p.Token)
	}
	return ctx
}

// Client is an HTTP Transport for generated CSIL clients.
type Client struct {
	Base  string
	Token func() string
	HTTP  *http.Client
}

// NewClient returns a client for base, such as "http://artifacts:8080".
func NewClient(base string, token func() string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// StaticToken returns a token function for a fixed token.
func StaticToken(t string) func() string { return func() string { return t } }

func (c *Client) Call(ctx context.Context, service, op string, req []byte) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/rpc/"+service+"/"+op, bytes.NewReader(req))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/cbor")
	tok, _ := ctx.Value(tokenKey{}).(string)
	if tok == "" && c.Token != nil {
		tok = c.Token()
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.HTTP.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		e := &Error{}
		if json.Unmarshal(body, e) != nil || e.Code == 0 {
			e = Errorf(resp.StatusCode, "%s", strings.TrimSpace(string(body)))
		}
		return nil, e
	}
	return body, nil
}

// Mux returns a handler with the RPC server at /rpc/ and a health check at
// /healthz. ready reports readiness; nil means always ready.
func Mux(s *Server, ready func() error) *http.ServeMux {
	m := http.NewServeMux()
	m.Handle("/rpc/", s)
	m.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			if err := ready(); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		w.Write([]byte("ok\n"))
	})
	return m
}

// ListenAndServe serves h until ctx ends.
func ListenAndServe(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}
