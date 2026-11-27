// Package host runs a model engine on demand on a machine that also has
// other work, and gives it to callers only while the machine is quiet.
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	// Description tells what the base model is for (GET /v1/models).
	Description string `json:"description,omitempty"`
	// Decide is how the engine answers decide. "" reads the probabilities
	// of the answer labels of a language model. "head" is for a decision
	// model: a model with a scoring head in place of the language head
	// (llama-server /v1/systemone). Such a model answers only the
	// questions that it was trained on, and it has no chat.
	Decide string `json:"decide,omitempty"`
	// Models are more model names of the engine: the base model with an
	// adapter (llama-server per-request LoRA; start the engine with
	// --lora FILE --lora-init-without-apply).
	Models map[string]Variant `json:"models,omitempty"`
}

// Variant is a model name of the engine with an adapter.
type Variant struct {
	// Lora is the "lora" field of a llama-server request: the adapter ids
	// (in the order of --lora) and their scales.
	Lora        []LoraScale `json:"lora"`
	Description string      `json:"description,omitempty"`
}

type LoraScale struct {
	Id    int     `json:"id"`
	Scale float64 `json:"scale"`
}

type Config struct {
	Engine Engine
	// Engines are more engines of the host. One engine runs at a time:
	// Engine, until Magi loads a different one (Load). After the hold of a
	// load ends, the host goes back to Engine.
	Engines  []Engine
	Rules    Rules
	IdleStop time.Duration // stop the engine after this time without requests
	// KeepWarm: start the engine when the host starts and each time the
	// machine becomes quiet, and do not stop it for idle time. Then the
	// first request does not wait for the model to load.
	KeepWarm   bool
	StartWait  time.Duration // a request waits this long for a cold engine; 0 refuses at once
	Interval   time.Duration // how often to sample the machine
	StartLimit time.Duration // the engine must be ready in this time
	MaxBody    int64
	LogFile    string // engine output; empty discards it
}

type Host struct {
	cfg     Config
	sampler Sampler
	// sample is the last look at the machine, for the detail of the status.
	sample Sample
	ver    *authn.Verifier
	log    *slog.Logger
	now    func() time.Time

	// engines is Engine and then Engines; proxies has the reverse proxy of
	// each one.
	engines []Engine
	proxies []*httputil.ReverseProxy

	mu  sync.Mutex
	cmd *exec.Cmd
	// active is the engine that the host runs, or starts next. running is
	// the engine of cmd: it is not active while the engine before a load
	// still answers its last requests.
	active, running int
	// stopped is the exit of the engine that the host stopped last. The
	// next engine starts after it, because the old process holds the GPU
	// memory until it ends.
	stopped chan struct{}
	// pending: the active engine starts as soon as the engine before it ended.
	pending bool
	// holdFor and holdUntil keep an engine that is not the first one: each
	// request moves holdUntil to holdFor after the request.
	holdFor   time.Duration
	holdUntil time.Time
	exited    chan struct{}
	ready     bool
	readyCh   chan struct{} // closed when ready or when the start failed
	inFlight  int
	lastUsed  time.Time
	started   time.Time
	busy      string    // current busy reason, or ""
	quietFrom time.Time // when the machine became quiet
	reason    string    // last reason the engine stopped or failed
	failed    time.Time // when the engine last failed to start or exited
	ec        *engineClient
}

// retryFail is how long KeepWarm waits after the engine failed before it
// starts the engine again.
const retryFail = time.Minute

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
	h := &Host{cfg: cfg, sampler: s, ver: v, log: log, now: time.Now}
	h.engines = append([]Engine{cfg.Engine}, cfg.Engines...)
	for _, e := range h.engines {
		target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", e.Port))
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.FlushInterval = -1
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			h.log.Warn("engine request", "err", err)
			refuse(w, "engine_error", "the engine did not answer", 0)
		}
		h.proxies = append(h.proxies, proxy)
	}
	return h
}

// Hold limits of Load.
const (
	defaultHold = 15 * time.Minute
	maxHold     = 4 * time.Hour
)

// engineOf gives the engine that serves the model name: a base model or a
// name of Models. An empty name is the active engine. h.mu is held.
func (h *Host) engineOf(model string) (int, bool) {
	if model == "" {
		return h.active, true
	}
	for i, e := range h.engines {
		if e.Model == model {
			return i, true
		}
		if _, ok := e.Models[model]; ok {
			return i, true
		}
	}
	return 0, false
}

// baseOf gives the base model of the engine that serves the model name.
func (h *Host) baseOf(model string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	i, _ := h.engineOf(model)
	return h.engines[i].Model
}

// CheckEngines tells what is wrong with the engines of a configuration:
// the first engine and the others. With more than one engine, a request
// finds its engine by the model name, so each name must be there one time.
func CheckEngines(first Engine, more []Engine) error {
	if len(more) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for i, e := range append([]Engine{first}, more...) {
		if len(e.Command) == 0 || e.Port == 0 || e.Model == "" {
			return fmt.Errorf("engine %d: command, port, and model are required when the host has more than one engine", i)
		}
		for _, n := range e.names() {
			if seen[n] {
				return fmt.Errorf("model name %q is in more than one engine", n)
			}
			seen[n] = true
		}
	}
	return nil
}

// switchLocked makes engine i the active one. The engine that runs stops
// when no request uses it; the new engine starts after the old one ended.
// h.mu is held.
func (h *Host) switchLocked(i int, start bool, why string) {
	if h.active == i {
		return
	}
	h.log.Info("engine change", "from", h.engines[h.active].Model, "to", h.engines[i].Model, "reason", why)
	h.active, h.pending = i, start
	h.drainLocked(why)
}

// drainLocked stops an engine that is not the active one when no request
// uses it, and starts the active engine after it. h.mu is held.
func (h *Host) drainLocked(why string) {
	if h.cmd == nil || h.running == h.active || h.inFlight > 0 {
		return
	}
	h.stopLocked(why)
	if h.pending {
		go h.startAfter(h.stopped)
	}
}

// startAfter starts the active engine when the stopped engine ended.
func (h *Host) startAfter(stopped chan struct{}) {
	<-stopped
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending = false
	h.startLocked()
}

// Load implements InferenceService: the engine of req.Model becomes the
// engine of the host. It does not wait for the engine to be ready.
func (h *Host) Load(ctx context.Context, req api.LoadRequest) (api.Status, error) {
	if err := h.allowed(ctx); err != nil {
		return api.Status{}, err
	}
	hold := defaultHold
	if req.HoldSeconds != nil && *req.HoldSeconds > 0 {
		hold = min(time.Duration(*req.HoldSeconds)*time.Second, maxHold)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	i, ok := h.engineOf(req.Model)
	if !ok || req.Model == "" {
		return api.Status{}, rpc.NotFound("no model %q on this host", req.Model)
	}
	h.holdFor, h.holdUntil = hold, h.now().Add(hold)
	h.switchLocked(i, true, "load "+req.Model)
	if h.cmd == nil {
		h.startLocked()
	}
	h.lastUsed = h.now()
	st := h.statusLocked()
	h.detailLocked(&st)
	return st, nil
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
	// GPU use is the engine's own while it loads, while it answers, and
	// for a short time after an answer (the utilization sample lags).
	running := h.cmd != nil
	active := h.inFlight > 0 || (running && !h.ready) || (running && h.now().Sub(h.lastUsed) < ownUseGrace)
	h.mu.Unlock()
	s := h.sampler.Sample(ctx, pid)
	why := h.cfg.Rules.busyReason(s, running, active)

	h.mu.Lock()
	defer h.mu.Unlock()
	h.sample = s
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
	if h.active != 0 && h.inFlight == 0 && !now.Before(h.holdUntil) {
		// The hold of a load ended: the first engine is the engine of the
		// host again.
		// It starts by the rules of the host: at once with KeepWarm, else
		// with a request.
		h.switchLocked(0, h.cfg.KeepWarm, "the hold ended")
	}
	if !h.cfg.KeepWarm && h.cmd != nil && h.inFlight == 0 && h.cfg.IdleStop > 0 && now.Sub(h.lastUsed) >= h.cfg.IdleStop {
		h.stopLocked("idle")
	}
	if h.cfg.KeepWarm && h.cmd == nil && h.busy == "" && now.Sub(h.failed) >= retryFail {
		h.log.Info("keep warm: starting the engine")
		h.startLocked()
	}
}

// stoppingLocked tells if a stopped engine did not end yet. h.mu is held.
func (h *Host) stoppingLocked() bool {
	if h.stopped == nil {
		return false
	}
	select {
	case <-h.stopped:
		h.stopped = nil
		return false
	default:
		return true
	}
}

// startLocked starts the active engine if no engine runs. While a stopped
// engine did not end, the start waits for it. h.mu is held.
func (h *Host) startLocked() {
	if h.cmd != nil || h.busy != "" {
		return
	}
	if h.stoppingLocked() {
		if !h.pending {
			h.pending = true
			go h.startAfter(h.stopped)
		}
		return
	}
	e := h.engines[h.active]
	if h.running != h.active {
		// The token ids of the labels are those of one model.
		h.ec = nil
	}
	h.running, h.pending = h.active, false
	c := e.Command
	cmd := exec.Command(c[0], c[1:]...)
	setGroup(cmd)
	if h.cfg.LogFile != "" {
		if f, err := os.OpenFile(h.cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close()
		}
	}
	if err := cmd.Start(); err != nil {
		h.reason, h.failed = "engine did not start: "+err.Error(), h.now()
		h.log.Error("engine start", "err", err)
		return
	}
	h.cmd, h.ready, h.readyCh, h.exited = cmd, false, make(chan struct{}), make(chan struct{})
	h.started, h.lastUsed = h.now(), h.now()
	h.log.Info("engine starting", "pid", cmd.Process.Pid, "model", e.Model)
	exited, readyCh := h.exited, h.readyCh
	go func() {
		err := cmd.Wait()
		close(exited)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.cmd == cmd {
			h.reason, h.failed = fmt.Sprintf("engine exited: %v", err), h.now()
			h.log.Warn("engine exited", "err", err)
			h.cmd, h.ready = nil, false
			closeOnce(readyCh)
		}
	}()
	go h.waitReady(cmd, e.Port, readyCh, exited)
}

func (h *Host) waitReady(cmd *exec.Cmd, port int, readyCh, exited chan struct{}) {
	deadline := time.Now().Add(h.cfg.StartLimit)
	hc := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return
		case <-time.After(500 * time.Millisecond):
		}
		resp, err := hc.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
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
		h.failed = h.now()
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
	h.cmd, h.ready, h.reason, h.stopped = nil, false, why, exited
	closeOnce(h.readyCh)
	h.log.Info("engine stopping", "reason", why)
	pid := cmd.Process.Pid
	stopGroup(pid, false)
	go func() {
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			stopGroup(pid, true)
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
	st := api.Status{Model: h.engines[h.active].Model, InFlight: uint64(h.inFlight)}
	switch {
	case h.cmd != nil && h.running != h.active, h.cmd == nil && h.pending && h.busy == "":
		// The engine before a load ends its requests, or its process ends.
		st.State = "starting"
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
func (h *Host) Status(ctx context.Context, req api.StatusRequest) (api.Status, error) {
	if err := h.allowed(ctx); err != nil {
		return api.Status{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.statusLocked()
	if req.Detail != nil && *req.Detail {
		h.detailLocked(&st)
	}
	return st, nil
}

// detailLocked adds the facts for a placement: the GPUs, the GPU memory of
// the engine, and the model names that the engine serves.
func (h *Host) detailLocked(st *api.Status) {
	known := func(v int) *uint64 {
		if v < 0 {
			return nil
		}
		return ptr(uint64(v))
	}
	st.Gpus = []api.Gpu{}
	for _, g := range h.sample.GPUs {
		st.Gpus = append(st.Gpus, api.Gpu{Name: g.Name, TotalMib: known(g.TotalMiB), UsedMib: known(g.UsedMiB), Util: known(g.Util)})
	}
	if h.cmd != nil && h.sample.EngineMiB > 0 {
		st.EngineMib = ptr(uint64(h.sample.EngineMiB))
	}
	st.Models = h.engines[h.active].names()
	if len(h.engines) > 1 {
		st.Loadable = []string{}
		for _, e := range h.engines {
			st.Loadable = append(st.Loadable, e.Model)
		}
	}
	if h.active != 0 {
		st.HoldUntilMs = ptr(uint64(h.holdUntil.UnixMilli()))
	}
}

// names gives the model names of the engine: the base model, then the
// names of Models in order.
func (e Engine) names() []string {
	names := make([]string, 0, len(e.Models))
	for n := range e.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	return append([]string{e.Model}, names...)
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

// OpenAI serves /v1/ by forwarding to the engine of the model that the
// request names.
func (h *Host) OpenAI() http.Handler {
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
		r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBody)
		model := ""
		if r.Method == http.MethodPost {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			r.Body.Close()
			setBody(r, b)
			model = modelOf(b)
		}
		i, err := h.acquire(r.Context(), model)
		if err != nil {
			var b *refusal
			if errors.As(err, &b) {
				refuse(w, b.kind, b.msg, b.retry)
				return
			}
			refuse(w, "engine_error", err.Error(), 0)
			return
		}
		defer h.release()
		e, proxy := h.engines[i], h.proxies[i]
		r.Header.Del("Authorization")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			e.writeModels(w)
			return
		}
		if r.Method == http.MethodPost && len(e.Models) > 0 {
			name, err := e.applyVariant(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			if name != "" {
				// The answer names the variant, as the caller asked.
				rw := &renameWriter{ResponseWriter: w, from: e.Model, to: name}
				proxy.ServeHTTP(rw, r)
				rw.finish()
				return
			}
		}
		proxy.ServeHTTP(w, r)
	})
}

// modelOf gives the model name of a JSON request body, or "".
func modelOf(b []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(b, &req)
	return req.Model
}

func setBody(r *http.Request, b []byte) {
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	r.Header.Set("Content-Length", strconv.Itoa(len(b)))
}

type refusal struct {
	kind, msg string
	retry     int
}

func (r *refusal) Error() string { return r.msg }

// ownUseGrace is how long after a request the GPU use still counts as the
// engine's own.
const ownUseGrace = 20 * time.Second

// acquire counts a request for model in flight once the engine of the
// model is ready, and gives the index of that engine. A model of an engine
// that is not the active one is refused: Magi loads it first (Load), or it
// uses a different model.
func (h *Host) acquire(ctx context.Context, model string) (int, error) {
	h.mu.Lock()
	i, ok := h.engineOf(model)
	if !ok {
		// A host with one engine sends each name to it, as before. A host
		// with more engines cannot tell which engine a wrong name means.
		if len(h.engines) > 1 {
			h.mu.Unlock()
			return 0, &refusal{"not_loaded", fmt.Sprintf("no model %q on this host", model), 0}
		}
		i = 0
	}
	if i != h.active {
		msg := fmt.Sprintf("model %s is not loaded: the host runs %s", h.engines[i].Model, h.engines[h.active].Model)
		h.mu.Unlock()
		return 0, &refusal{"not_loaded", msg, 30}
	}
	if h.busy != "" && h.cmd == nil {
		why := h.busy
		h.mu.Unlock()
		return 0, &refusal{"host_busy", "the host is busy: " + why, 60}
	}
	if h.cmd != nil && h.running != h.active {
		// The engine before a load ends its requests.
		h.mu.Unlock()
		return 0, &refusal{"warming", "the host changes its engine", 15}
	}
	h.startLocked()
	if h.cmd == nil && h.pending {
		// A stopped engine did not end yet; the engine starts after it.
		stopped := h.stopped
		h.mu.Unlock()
		if h.cfg.StartWait <= 0 || stopped == nil {
			return 0, &refusal{"warming", "the engine is starting", 15}
		}
		select {
		case <-stopped:
		case <-time.After(h.cfg.StartWait):
			return 0, &refusal{"warming", "the engine is starting", 15}
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		h.mu.Lock()
		if i != h.active {
			// A load came while the request waited.
			msg := fmt.Sprintf("model %s is not loaded: the host runs %s", h.engines[i].Model, h.engines[h.active].Model)
			h.mu.Unlock()
			return 0, &refusal{"not_loaded", msg, 30}
		}
		// startAfter can be behind this request: start the engine here.
		if !h.stoppingLocked() {
			h.pending = false
		}
		h.startLocked()
	}
	if h.cmd == nil {
		why := h.reason
		h.mu.Unlock()
		return 0, &refusal{"engine_error", why, 30}
	}
	h.touchLocked()
	if h.ready {
		h.inFlight++
		h.mu.Unlock()
		return i, nil
	}
	cmd, readyCh := h.cmd, h.readyCh
	h.mu.Unlock()
	if h.cfg.StartWait <= 0 {
		return 0, &refusal{"warming", "the engine is starting", 15}
	}
	select {
	case <-readyCh:
	case <-time.After(h.cfg.StartWait):
		return 0, &refusal{"warming", "the engine is starting", 15}
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ready || h.cmd != cmd {
		return 0, &refusal{"engine_error", "the engine did not start: " + h.reason, 30}
	}
	h.inFlight++
	return i, nil
}

// touchLocked records a use of the engine. It also moves the end of the
// hold of an engine that is not the first one. h.mu is held.
func (h *Host) touchLocked() {
	h.lastUsed = h.now()
	if h.active != 0 && h.running == h.active {
		h.holdUntil = h.lastUsed.Add(h.holdFor)
	}
}

func (h *Host) release() {
	h.mu.Lock()
	h.inFlight--
	h.touchLocked()
	// A load came while this request ran: the old engine can stop now.
	h.drainLocked("engine change to " + h.engines[h.active].Model)
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
