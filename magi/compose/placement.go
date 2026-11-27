package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	inferenceapi "github.com/catalystcommunity/magi/primitives/inference/api"
)

// placer tells an inference host to load a model (decision record 0016).
// A host with more than one engine runs one of them, and refuses a request
// for a model of a different engine with the error type "not_loaded".
type placer struct {
	// Hold is how long the host keeps the model after its last request.
	// A code change has no model call while its checks run, so the hold is
	// longer than a check.
	Hold time.Duration
	// Wait is the limit of one load; Poll is the time between two looks at
	// the host.
	Wait, Poll time.Duration

	mu    sync.Mutex
	hosts map[string]*sync.Mutex
}

func newPlacer() *placer {
	return &placer{Hold: 20 * time.Minute, Wait: 5 * time.Minute, Poll: 2 * time.Second}
}

// notLoaded tells if the body of a 503 answer has the error type
// "not_loaded".
func notLoaded(body []byte) bool {
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &e) == nil && e.Error.Type == "not_loaded"
}

// load makes the host of m run the engine of m, and waits until the engine
// is ready. One load runs at a time for each host: a second call for the
// same model finds it ready.
func (p *placer) load(ctx context.Context, m *runnerModel) error {
	// The host root: the base URL without /v1.
	root := strings.TrimSuffix(strings.TrimRight(m.BaseURL, "/"), "/v1")
	p.mu.Lock()
	if p.hosts == nil {
		p.hosts = map[string]*sync.Mutex{}
	}
	l, ok := p.hosts[root]
	if !ok {
		l = &sync.Mutex{}
		p.hosts[root] = l
	}
	p.mu.Unlock()
	l.Lock()
	defer l.Unlock()

	c := rpc.NewClient(root, rpc.StaticToken(m.APIKey))
	c.HTTP = &http.Client{Timeout: 30 * time.Second}
	if m.HTTP != nil {
		c.HTTP.Transport = m.HTTP.Transport
	}
	ic := inferenceapi.NewInferenceClient(c)
	// The key of the model, never a token of the context.
	ctx = rpc.WithToken(ctx, m.APIKey)
	st, err := ic.Load(ctx, inferenceapi.LoadRequest{Model: m.Model, HoldSeconds: ptr(uint64(p.Hold.Seconds()))})
	if err != nil {
		return fmt.Errorf("load %s: %w", m.Model, err)
	}
	deadline := time.Now().Add(p.Wait)
	for {
		switch {
		case !slices.Contains(st.Models, m.Model):
			// A different load came after this one.
			return fmt.Errorf("load %s: the host runs %s", m.Model, st.Model)
		case st.State == "ready":
			return nil
		case st.State == "busy" || st.State == "off":
			// The machine is busy with the work of its owner, or the engine
			// did not start.
			return fmt.Errorf("load %s: the host is %s: %s", m.Model, st.State, deref(st.Reason))
		case time.Now().After(deadline):
			return fmt.Errorf("load %s: not ready after %s", m.Model, p.Wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.Poll):
		}
		if st, err = ic.Status(ctx, inferenceapi.StatusRequest{Detail: ptr(true)}); err != nil {
			return fmt.Errorf("load %s: %w", m.Model, err)
		}
	}
}
