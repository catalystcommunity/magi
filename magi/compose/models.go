package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/magi/model"
	modelsapi "github.com/catalystcommunity/magi/primitives/models/api"
)

// Model calls by task (docs/models.md). Each call of Magi names its task in
// the context, from the most specific to the most general, for example
// "persona:persona:archimedes" then "persona-turn". The first task with a
// default in the model catalog gives the selector. A selector in the
// context (a request that names a model or a class of models) refines it.
// The catalog orders the models; Magi tries them in that order.
//
// With no catalog, no default, and no selector, the call uses the static
// chain of the environment (MAGI_PERSONA_MODELS, MAGI_LIBRARIAN_MODELS).

type modelTasksKey struct{}
type modelSelectorKey struct{}

type modelOverride struct {
	sel    modelsapi.Selector
	strict bool
}

// WithModelTask names the task of the model calls in ctx, from the most
// specific to the most general. The names go before the names that ctx
// has already.
func WithModelTask(ctx context.Context, tasks ...string) context.Context {
	old, _ := ctx.Value(modelTasksKey{}).([]string)
	return context.WithValue(ctx, modelTasksKey{}, append(append([]string{}, tasks...), old...))
}

// backgroundModel names the task of background work in a turn (the note
// taker, a search judge). The tasks and the model of the turn do not apply:
// only task, then the task of the model (for example "librarian").
func backgroundModel(ctx context.Context, task string) context.Context {
	ctx = context.WithValue(ctx, modelSelectorKey{}, nil)
	return context.WithValue(ctx, modelTasksKey{}, []string{task})
}

// WithModelSelector refines the model selection of the calls in ctx.
// strict: a named model must also agree with the default (a request of a
// person).
func WithModelSelector(ctx context.Context, s *modelsapi.Selector, strict bool) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, modelSelectorKey{}, modelOverride{sel: *s, strict: strict})
}

// modelVars puts the model of a run (the variables "model" and
// "model_strict" of a persona turn) in ctx.
func modelVars(ctx context.Context, vars map[string]string) context.Context {
	v := vars["model"]
	if v == "" {
		return ctx
	}
	var sel modelsapi.Selector
	if err := json.Unmarshal([]byte(v), &sel); err != nil {
		return ctx
	}
	return WithModelSelector(ctx, &sel, vars["model_strict"] == "true")
}

// personaTask is the task name of the turns of one persona:
// "persona:engineering" -> "persona.engineering".
func personaTask(persona string) string {
	return "persona." + strings.TrimPrefix(persona, "persona:")
}

// ModelRouter builds model chains from the catalog.
type ModelRouter struct {
	Catalog *modelsapi.ModelClient
	// Token authenticates the catalog calls (Magi's service token).
	Token string
	// Keys maps the key_ref of an endpoint to the key ("provider",
	// "inference"). A model whose key_ref is not here is skipped.
	Keys      map[string]string
	Transport http.RoundTripper
	Log       *slog.Logger
	// TTL is how long a selection stays in the cache (default 30 s).
	TTL time.Duration

	mu     sync.Mutex
	chains map[string]routedEntry
	tasks  map[string]taskEntry
}

type routedEntry struct {
	at    time.Time
	chain model.Chain
}

type taskEntry struct {
	at  time.Time
	sel *modelsapi.Selector // nil: no default
}

// For returns a model for task that falls back to static. A nil static
// stays nil: the part that uses it is off.
func (r *ModelRouter) For(task string, static model.Chatter) model.Chatter {
	if static == nil {
		return nil
	}
	return &Routed{r: r, task: task, static: static}
}

func (r *ModelRouter) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return 30 * time.Second
}

func (r *ModelRouter) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(rpc.WithToken(context.WithoutCancel(ctx), r.Token), 5*time.Second)
}

// taskDefault returns the default selector of task, or nil.
func (r *ModelRouter) taskDefault(ctx context.Context, task string) (*modelsapi.Selector, error) {
	r.mu.Lock()
	e, ok := r.tasks[task]
	r.mu.Unlock()
	if ok && time.Since(e.at) < r.ttl() {
		return e.sel, nil
	}
	cctx, cancel := r.ctx(ctx)
	defer cancel()
	t, err := r.Catalog.GetTask(cctx, modelsapi.GetTaskRequest{Task: task})
	var sel *modelsapi.Selector
	switch {
	case err == nil:
		sel = &t.Selector
	case rpc.CodeOf(err) == 404:
	default:
		return nil, err
	}
	r.mu.Lock()
	if r.tasks == nil {
		r.tasks = map[string]taskEntry{}
	}
	r.tasks[task] = taskEntry{at: time.Now(), sel: sel}
	r.mu.Unlock()
	return sel, nil
}

// chain returns the chain for the tasks and the selector of ctx, or nil
// when the static chain applies.
func (r *ModelRouter) chain(ctx context.Context, task string) model.Chain {
	if r == nil || r.Catalog == nil {
		return nil
	}
	tasks, _ := ctx.Value(modelTasksKey{}).([]string)
	tasks = append(append([]string{}, tasks...), task)
	ov, hasOver := ctx.Value(modelSelectorKey{}).(modelOverride)
	over := ov.sel
	var use string
	var def *modelsapi.Selector
	for _, t := range tasks {
		s, err := r.taskDefault(ctx, t)
		if err != nil {
			r.log().Warn("model catalog", "task", t, "err", err)
			return nil
		}
		if s != nil {
			use, def = t, s
			break
		}
	}
	if def == nil && !hasOver {
		return nil
	}
	ob, _ := json.Marshal(over)
	key := use + "\x00" + string(ob) + fmt.Sprint(ov.strict)
	if !hasOver {
		key = use
	}
	r.mu.Lock()
	e, ok := r.chains[key]
	r.mu.Unlock()
	if ok && time.Since(e.at) < r.ttl() {
		return e.chain
	}
	req := modelsapi.SelectRequest{}
	if use != "" {
		req.Task = &use
	}
	if hasOver {
		req.Selector = &over
		req.Strict = &ov.strict
	}
	cctx, cancel := r.ctx(ctx)
	defer cancel()
	res, err := r.Catalog.Select(cctx, req)
	if err != nil {
		r.log().Warn("model catalog", "task", use, "err", err)
		return nil
	}
	var ch model.Chain
	var names []string
	for _, c := range res.Candidates {
		cl, ok := r.client(c.Model)
		if !ok {
			continue
		}
		ch = append(ch, cl)
		names = append(names, c.Model.Name+" ("+c.Reason+")")
	}
	r.log().Info("model selection", "task", use, "override", hasOver, "models", strings.Join(names, "; "))
	r.mu.Lock()
	if r.chains == nil {
		r.chains = map[string]routedEntry{}
	}
	r.chains[key] = routedEntry{at: time.Now(), chain: ch}
	r.mu.Unlock()
	return ch
}

func (r *ModelRouter) client(m modelsapi.Model) (*model.Client, bool) {
	key := ""
	if m.Endpoint.KeyRef != nil && *m.Endpoint.KeyRef != "" {
		k, ok := r.Keys[*m.Endpoint.KeyRef]
		if !ok {
			r.log().Warn("model catalog: no key", "model", m.Name, "key_ref", *m.Endpoint.KeyRef)
			return nil, false
		}
		key = k
	}
	t := 10 * time.Minute
	if m.Endpoint.TimeoutS != nil && *m.Endpoint.TimeoutS > 0 {
		t = time.Duration(*m.Endpoint.TimeoutS) * time.Second
	}
	att := 0
	if m.Endpoint.Attempts != nil {
		att = int(*m.Endpoint.Attempts)
	}
	return &model.Client{BaseURL: strings.TrimRight(m.Endpoint.BaseUrl, "/"), Model: m.Endpoint.Model, APIKey: key,
		Attempts: att, HTTP: &http.Client{Timeout: t, Transport: r.Transport}}, true
}

func (r *ModelRouter) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Routed is the model of one task: the catalog chain, or the static chain.
type Routed struct {
	r      *ModelRouter
	task   string
	static model.Chatter
}

func (m *Routed) pick(ctx context.Context) model.Chatter {
	if ch := m.r.chain(ctx, m.task); len(ch) > 0 {
		return ch
	}
	return m.static
}

func (m *Routed) Chat(ctx context.Context, msgs []model.Message, tools []model.Tool, maxTokens int) (model.Message, error) {
	return m.pick(ctx).Chat(ctx, msgs, tools, maxTokens)
}

func (m *Routed) ChatSchema(ctx context.Context, msgs []model.Message, schema map[string]any, maxTokens int) (model.Message, error) {
	c := m.pick(ctx)
	if sc, ok := c.(model.SchemaChatter); ok {
		return sc.ChatSchema(ctx, msgs, schema, maxTokens)
	}
	return c.Chat(ctx, msgs, nil, maxTokens)
}

// Models returns the catalog models for task (and the tasks in ctx),
// refined by sel, in order. strict: a named model must agree with the
// default. With no default and no sel, there are none.
func (r *ModelRouter) Models(ctx context.Context, task string, sel *modelsapi.Selector, strict bool) ([]modelsapi.Model, error) {
	if r == nil || r.Catalog == nil {
		return nil, nil
	}
	tasks, _ := ctx.Value(modelTasksKey{}).([]string)
	tasks = append(append([]string{}, tasks...), task)
	var use string
	var def *modelsapi.Selector
	for _, t := range tasks {
		s, err := r.taskDefault(ctx, t)
		if err != nil {
			return nil, err
		}
		if s != nil {
			use, def = t, s
			break
		}
	}
	if def == nil && sel == nil {
		return nil, nil
	}
	req := modelsapi.SelectRequest{Selector: sel, Strict: &strict}
	if use != "" {
		req.Task = &use
	}
	cctx, cancel := r.ctx(ctx)
	defer cancel()
	res, err := r.Catalog.Select(cctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]modelsapi.Model, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		out = append(out, c.Model)
	}
	return out, nil
}
