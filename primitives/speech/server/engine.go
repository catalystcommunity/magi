package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
)

// Engine is an HTTP client for one speech engine process (engine/server.py).
// Any process that serves the same endpoints can replace it.
type Engine struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// NewEngine returns a client for the engine at url.
func NewEngine(url, token string) *Engine {
	return &Engine{URL: strings.TrimRight(url, "/"), Token: token, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

type engineResult struct {
	Body      []byte
	Header    http.Header
	AudioMs   uint64
	ElapsedMs uint64
}

func (e *Engine) do(ctx context.Context, method, path, ctype string, body []byte) (*engineResult, error) {
	if e == nil || e.URL == "" {
		return nil, rpc.Unavailable("speech engine for %s is not configured", path)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if e.Token != "" {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, rpc.Unavailable("speech engine %s unreachable: %v", path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, rpc.Unavailable("speech engine %s: read reply: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		var msg struct{ Error string }
		if json.Unmarshal(b, &msg) != nil || msg.Error == "" {
			msg.Error = http.StatusText(resp.StatusCode)
		}
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge {
			return nil, rpc.Invalid("speech engine: %s", msg.Error)
		}
		return nil, rpc.Unavailable("speech engine %s failed (%d): %s", path, resp.StatusCode, msg.Error)
	}
	r := &engineResult{Body: b, Header: resp.Header}
	r.AudioMs, _ = strconv.ParseUint(resp.Header.Get("X-Audio-Ms"), 10, 64)
	r.ElapsedMs, _ = strconv.ParseUint(resp.Header.Get("X-Elapsed-Ms"), 10, 64)
	return r, nil
}

func (e *Engine) json(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	r, err := e.do(ctx, method, path, "application/json", body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return rpc.Unavailable("speech engine %s: bad reply: %v", path, err)
	}
	return nil
}

// Health checks that the engine answers /health.
func (e *Engine) Health(ctx context.Context) error {
	var h struct{ OK bool }
	if err := e.json(ctx, http.MethodGet, "/health", nil, &h); err != nil {
		return err
	}
	if !h.OK {
		return fmt.Errorf("speech engine %s is not ready", e.URL)
	}
	return nil
}
