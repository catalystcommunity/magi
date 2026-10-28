// Package host runs a model engine on demand on a machine that also has
// other work, and gives it to callers only while the machine is quiet.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
)

// Scope is needed for every request.
const Scope = "inference:use"

// Engine is the model server that the host starts. Command must serve the
// OpenAI API on 127.0.0.1:Port, with GET /health.
type Engine struct {
	Command []string `json:"command"`
	Port    int      `json:"port"`
	Model   string   `json:"model"`
}

type Config struct {
	Engine     Engine
	Rules      Rules
	IdleStop   time.Duration // stop the engine after this time without requests
	StartWait  time.Duration // a request waits this long for a cold engine; 0 refuses at once
	Interval   time.Duration // how often to sample the machine
	StartLimit time.Duration // the engine must be ready in this time
	MaxBody    int64
	LogFile    string // engine output; empty discards it
}

type Host struct {
	cfg     Config
	sampler Sampler
	ver     *authn.Verifier
	log     *slog.Logger
	now     func() time.Time

	mu        sync.Mutex
	cmd       *exec.Cmd
	exited    chan struct{}
	ready     bool
	readyCh   chan struct{} // closed when ready or when the start failed
	inFlight  int
	lastUsed  time.Time
	started   time.Time
	busy      string    // current busy reason, or ""
	quietFrom time.Time // when the machine became quiet
	reason    string    // last reason the engine stopped or failed
}

func New(cfg Config, s Sampler, v *authn.Verifier, log *slog.Logger) *Host {
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.StartLimit == 0 {
		cfg.StartLimit = 3 * time.Minute
	}
	if cfg.MaxBody == 0 {
		cfg.MaxBody = 16 << 20
	}
	return &Host{cfg: cfg, sampler: s, ver: v, log: log, now: time.Now}
}

// Run samples the machine until ctx ends, then stops the engine.
func (h *Host) Run(ctx context.Context) {
	h.check(ctx)
	t := time.NewTicker(h.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			h.stopLocked("host stopped")
			h.mu.Unlock()
			return
		case <-t.C:
			h.check(ctx)
		}
	}
}

func (h *Host) check(ctx context.Context) {
	h.mu.Lock()
	pid := 0
	if h.cmd != nil && h.cmd.Process != nil {
		pid = h.cmd.Process.Pid
	}
	running, active := h.cmd != nil, h.inFlight > 0
	h.mu.Unlock()
	s := h.sampler.Sample(ctx, pid)
	why := h.cfg.Rules.busyReason(s, running, active)

	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	switch {
	case why != "":
		if h.busy == "" {
			h.log.Info("machine busy", "reason", why)
		}
		h.busy, h.quietFrom = why, time.Time{}
		// Give the GPU back while no request uses the engine.
		if h.cmd != nil && h.inFlight == 0 {
			h.stopLocked("machine busy: " + why)
		}
	case h.busy != "":
		if h.quietFrom.IsZero() {
			h.quietFrom = now
		}
		if now.Sub(h.quietFrom) >= time.Duration(h.cfg.Rules.QuietFor) {
			h.log.Info("machine quiet")
			h.busy = ""
		}
	}
	if h.cmd != nil && h.inFlight == 0 && h.cfg.IdleStop > 0 && now.Sub(h.lastUsed) >= h.cfg.IdleStop {
		h.stopLocked("idle")
	}
}

// startLocked starts the engine if it does not run. h.mu is held.
func (h *Host) startLocked() {
	if h.cmd != nil || h.busy != "" {
		return
	}
	c := h.cfg.Engine.Command
	cmd := exec.Command(c[0], c[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if h.cfg.LogFile != "" {
		if f, err := os.OpenFile(h.cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close()
		}
	}
	if err := cmd.Start(); err != nil {
		h.reason = "engine did not start: " + err.Error()
		h.log.Error("engine start", "err", err)
		return
	}
	h.cmd, h.ready, h.readyCh, h.exited = cmd, false, make(chan struct{}), make(chan struct{})
	h.started, h.lastUsed = h.now(), h.now()
	h.log.Info("engine starting", "pid", cmd.Process.Pid)
	exited, readyCh := h.exited, h.readyCh
	go func() {
		err := cmd.Wait()
		close(exited)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.cmd == cmd {
			h.reason = fmt.Sprintf("engine exited: %v", err)
			h.log.Warn("engine exited", "err", err)
			h.cmd, h.ready = nil, false
			closeOnce(readyCh)
		}
	}()
	go h.waitReady(cmd, readyCh, exited)
}

func (h *Host) waitReady(cmd *exec.Cmd, readyCh, exited chan struct{}) {
	deadline := time.Now().Add(h.cfg.StartLimit)
	hc := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return
		case <-time.After(500 * time.Millisecond):
		}
		resp, err := hc.Get(fmt.Sprintf("http://127.0.0.1:%d/health", h.cfg.Engine.Port))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				h.mu.Lock()
				if h.cmd == cmd {
					h.ready = true
					h.log.Info("engine ready", "after_ms", time.Since(h.started).Milliseconds())
				}
				h.mu.Unlock()
				closeOnce(readyCh)
				return
			}
		}
	}
	h.mu.Lock()
	if h.cmd == cmd {
		h.stopLocked("engine was not ready in time")
	}
	h.mu.Unlock()
}

// stopLocked stops the engine process group. h.mu is held.
func (h *Host) stopLocked(why string) {
	if h.cmd == nil {
		return
	}
	cmd, exited := h.cmd, h.exited
	h.cmd, h.ready, h.reason = nil, false, why
	closeOnce(h.readyCh)
	h.log.Info("engine stopping", "reason", why)
	pgid := cmd.Process.Pid
	syscall.Kill(-pgid, syscall.SIGTERM)
	go func() {
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()
}

func closeOnce(c chan struct{}) {
	if c == nil {
		return
	}
	select {
	case <-c:
	default:
		close(c)
	}
}

func (h *Host) statusLocked() api.Status {
	st := api.Status{Model: h.cfg.Engine.Model, InFlight: uint64(h.inFlight)}
	switch {
	case h.busy != "" && h.cmd == nil:
		st.State, st.Reason = "busy", ptr(h.busy)
	case h.cmd == nil:
		st.State = "off"
		if h.reason != "" {
			st.Reason = ptr(h.reason)
		}
	case !h.ready:
		st.State = "starting"
	default:
		st.State = "ready"
	}
	if !h.lastUsed.IsZero() {
		st.LastUsedMs = ptr(uint64(h.lastUsed.UnixMilli()))
	}
	if h.cmd != nil {
		st.StartedMs = ptr(uint64(h.started.UnixMilli()))
	}
	return st
}

// Status and Warm implement InferenceService.
func (h *Host) Status(ctx context.Context, _ api.StatusRequest) (api.Status, error) {
	if err := h.allowed(ctx); err != nil {
		return api.Status{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statusLocked(), nil
}

func (h *Host) Warm(ctx context.Context, _ api.WarmRequest) (api.Status, error) {
	if err := h.allowed(ctx); err != nil {
		return api.Status{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.startLocked()
	h.lastUsed = h.now()
	return h.statusLocked(), nil
}

func (h *Host) allowed(ctx context.Context) error {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return err
	}
	if !p.Has(Scope) {
		return rpc.Denied("%s is required", Scope)
	}
	return nil
}

// OpenAI serves /v1/ by forwarding to the engine.
func (h *Host) OpenAI() http.Handler {
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", h.cfg.Engine.Port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.log.Warn("engine request", "err", err)
		refuse(w, "engine_error", "the engine did not answer", 0)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		p, err := h.ver.Verify(tok)
		if !ok || err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid Magi token is required")
			return
		}
		if !p.Has(Scope) {
			writeError(w, http.StatusForbidden, "forbidden", Scope+" is required")
			return
		}
		if err := h.acquire(r.Context()); err != nil {
			var b *refusal
			if errors.As(err, &b) {
				refuse(w, b.kind, b.msg, b.retry)
				return
			}
			refuse(w, "engine_error", err.Error(), 0)
			return
		}
		defer h.release()
		r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBody)
		r.Header.Del("Authorization")
		proxy.ServeHTTP(w, r)
	})
}

type refusal struct {
	kind, msg string
	retry     int
}

func (r *refusal) Error() string { return r.msg }

// acquire counts a request in flight once the engine is ready.
func (h *Host) acquire(ctx context.Context) error {
	h.mu.Lock()
	if h.busy != "" && h.cmd == nil {
		why := h.busy
		h.mu.Unlock()
		return &refusal{"host_busy", "the host is busy: " + why, 60}
	}
	h.startLocked()
	if h.cmd == nil {
		why := h.reason
		h.mu.Unlock()
		return &refusal{"engine_error", why, 30}
	}
	h.lastUsed = h.now()
	if h.ready {
		h.inFlight++
		h.mu.Unlock()
		return nil
	}
	readyCh := h.readyCh
	h.mu.Unlock()
	if h.cfg.StartWait <= 0 {
		return &refusal{"warming", "the engine is starting", 15}
	}
	select {
	case <-readyCh:
	case <-time.After(h.cfg.StartWait):
		return &refusal{"warming", "the engine is starting", 15}
	case <-ctx.Done():
		return ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ready {
		return &refusal{"engine_error", "the engine did not start: " + h.reason, 30}
	}
	h.inFlight++
	return nil
}

func (h *Host) release() {
	h.mu.Lock()
	h.inFlight--
	h.lastUsed = h.now()
	h.mu.Unlock()
}

func refuse(w http.ResponseWriter, kind, msg string, retry int) {
	if retry > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(retry))
	}
	writeError(w, http.StatusServiceUnavailable, kind, msg)
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": kind, "message": msg}})
}

func ptr[T any](v T) *T { return &v }
