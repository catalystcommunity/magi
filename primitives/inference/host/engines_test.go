package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
)

// twoEngines is a host with the engine "local" and a second engine
// "careful" with the adapter name "careful-notes".
func twoEngines(t *testing.T, opts ...func(*Config)) (*env, *api.InferenceClient) {
	t.Helper()
	second := freePort(t)
	e := setup(t, 20*time.Second, append([]func(*Config){func(c *Config) {
		c.Engines = []Engine{{Command: []string{os.Args[0], "fake-engine", fmt.Sprint(second), "careful"}, Port: second,
			Model: "careful", Models: map[string]Variant{"careful-notes": {}}}}
	}}, opts...)...)
	rs := &rpc.Server{Verifier: e.h.ver}
	rs.Register("InferenceService", api.InferenceServiceOps(e.h))
	hs := httptest.NewServer(rpc.Mux(rs, nil))
	t.Cleanup(hs.Close)
	return e, api.NewInferenceClient(rpc.NewClient(hs.URL, rpc.StaticToken(e.tok(Scope))))
}

// ask sends a chat request for model and gives the status, the error type,
// and the model name of the answer.
func (e *env) ask(t *testing.T, model, extra string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"note":%q}`, model, extra)))
	req.Header.Set("Authorization", "Bearer "+e.tok(Scope))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Model string
		Error struct{ Type string }
	}
	json.Unmarshal(b, &out)
	return resp.StatusCode, out.Error.Type, out.Model
}

func (e *env) activeModel() string {
	e.h.mu.Lock()
	defer e.h.mu.Unlock()
	return e.h.engines[e.h.active].Model
}

func TestLoadChangesTheEngine(t *testing.T) {
	e, c := twoEngines(t)
	ctx := context.Background()
	if code, _, model := e.ask(t, "local", ""); code != 200 || model != "local" {
		t.Fatalf("first engine: %d %s", code, model)
	}
	// The second engine is not loaded: the host refuses, and it does not
	// change its engine for a chat request.
	if code, kind, _ := e.ask(t, "careful", ""); code != 503 || kind != "not_loaded" {
		t.Fatalf("model of the second engine before a load: %d %s", code, kind)
	}
	if code, kind, _ := e.ask(t, "careful-notes", ""); code != 503 || kind != "not_loaded" {
		t.Fatalf("adapter name of the second engine before a load: %d %s", code, kind)
	}
	if e.activeModel() != "local" {
		t.Fatalf("a chat request changed the engine to %s", e.activeModel())
	}
	st, err := c.Load(ctx, api.LoadRequest{Model: "careful"})
	if err != nil || st.Model != "careful" || strings.Join(st.Loadable, ",") != "local,careful" || st.HoldUntilMs == nil {
		t.Fatalf("load = %+v, %v", st, err)
	}
	if st.State == "ready" && strings.Join(st.Models, ",") != "careful,careful-notes" {
		t.Fatalf("models after the load = %v", st.Models)
	}
	waitState(t, e, "ready")
	if code, _, model := e.ask(t, "careful", ""); code != 200 || model != "careful" {
		t.Fatalf("second engine after the load: %d %s", code, model)
	}
	if code, kind, _ := e.ask(t, "local", ""); code != 503 || kind != "not_loaded" {
		t.Fatalf("model of the first engine while the second is loaded: %d %s", code, kind)
	}
	// A load of the first engine ends the hold at once.
	st, err = c.Load(ctx, api.LoadRequest{Model: "local"})
	if err != nil || st.Model != "local" || st.HoldUntilMs != nil {
		t.Fatalf("load of the first engine = %+v, %v", st, err)
	}
	waitState(t, e, "ready")
	if code, _, model := e.ask(t, "local", ""); code != 200 || model != "local" {
		t.Fatalf("first engine again: %d %s", code, model)
	}
}

func TestLoadOfAnUnknownModel(t *testing.T) {
	e, c := twoEngines(t)
	if _, err := c.Load(context.Background(), api.LoadRequest{Model: "nope"}); rpc.CodeOf(err) != 404 {
		t.Fatalf("load of an unknown model: %v", err)
	}
	// With more than one engine, a wrong name has no engine.
	if code, kind, _ := e.ask(t, "nope", ""); code != 503 || kind != "not_loaded" {
		t.Fatalf("unknown model on a host with two engines: %d %s", code, kind)
	}
	if e.state() != "off" {
		t.Fatalf("a refused request started the engine: %s", e.state())
	}
}

func TestOneEngineTakesEachName(t *testing.T) {
	e := setup(t, 20*time.Second)
	if code, _, model := e.ask(t, "some-other-name", ""); code != 200 || model != "local" {
		t.Fatalf("a host with one engine: %d %s", code, model)
	}
}

func TestHoldKeepsTheEngineUntilItEnds(t *testing.T) {
	e, c := twoEngines(t)
	ctx := context.Background()
	if _, err := c.Load(ctx, api.LoadRequest{Model: "careful", HoldSeconds: ptr(uint64(60))}); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "ready")
	e.advance(50 * time.Second)
	// A request moves the end of the hold to 60 s after it.
	if code, _, _ := e.ask(t, "careful", ""); code != 200 {
		t.Fatalf("request in the hold: %d", code)
	}
	e.advance(50 * time.Second)
	e.h.check(ctx)
	if e.activeModel() != "careful" {
		t.Fatal("the hold ended 50 s after a request, with a hold of 60 s")
	}
	e.advance(11 * time.Second)
	e.h.check(ctx)
	if e.activeModel() != "local" {
		t.Fatal("the hold did not end 61 s after the last request")
	}
	// No keep_warm: the first engine starts with the next request.
	waitState(t, e, "off")
	if code, _, model := e.ask(t, "local", ""); code != 200 || model != "local" {
		t.Fatalf("first engine after the hold: %d %s", code, model)
	}
}

func TestKeepWarmStartsTheFirstEngineAfterAHold(t *testing.T) {
	e, c := twoEngines(t, func(c *Config) { c.KeepWarm = true })
	ctx := context.Background()
	waitState(t, e, "ready")
	if _, err := c.Load(ctx, api.LoadRequest{Model: "careful", HoldSeconds: ptr(uint64(60))}); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "ready")
	if code, _, model := e.ask(t, "careful", ""); code != 200 || model != "careful" {
		t.Fatalf("second engine: %d %s", code, model)
	}
	e.advance(61 * time.Second)
	e.h.check(ctx)
	// The host starts the first engine again with no request.
	for i := 0; i < 600 && (e.activeModel() != "local" || e.state() != "ready"); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if e.activeModel() != "local" || e.state() != "ready" {
		t.Fatalf("after the hold: %s %s", e.activeModel(), e.state())
	}
}

func TestLoadWaitsForARequestInFlight(t *testing.T) {
	e, c := twoEngines(t)
	if code, _, _ := e.ask(t, "local", ""); code != 200 {
		t.Fatal("first engine did not answer")
	}
	type answer struct {
		code  int
		model string
	}
	done := make(chan answer, 1)
	go func() {
		code, _, model := e.ask(t, "local", "slow")
		done <- answer{code, model}
	}()
	for i := 0; i < 200 && e.inFlight() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	st, err := c.Load(context.Background(), api.LoadRequest{Model: "careful"})
	if err != nil || st.State != "starting" || st.Model != "careful" {
		t.Fatalf("load with a request in flight = %+v, %v", st, err)
	}
	// A new request for the old engine is refused; the request in flight
	// gets its answer from the old engine.
	if code, kind, _ := e.ask(t, "local", ""); code != 503 || kind != "not_loaded" {
		t.Fatalf("new request for the old engine: %d %s", code, kind)
	}
	if a := <-done; a.code != 200 || a.model != "local" {
		t.Fatalf("request in flight: %+v", a)
	}
	waitState(t, e, "ready")
	if code, _, model := e.ask(t, "careful", ""); code != 200 || model != "careful" {
		t.Fatalf("second engine after the request ended: %d %s", code, model)
	}
}

func TestCheckEngines(t *testing.T) {
	first := Engine{Command: []string{"x"}, Port: 1, Model: "a", Models: map[string]Variant{"a-notes": {}}}
	if err := CheckEngines(Engine{Command: []string{"x"}, Port: 1}, nil); err != nil {
		t.Fatalf("one engine with no model name: %v", err)
	}
	if err := CheckEngines(first, []Engine{{Command: []string{"y"}, Port: 2, Model: "b"}}); err != nil {
		t.Fatalf("two engines: %v", err)
	}
	if err := CheckEngines(first, []Engine{{Command: []string{"y"}, Port: 2, Model: "a-notes"}}); err == nil {
		t.Fatal("a model name in two engines must be an error")
	}
	if err := CheckEngines(first, []Engine{{Command: []string{"y"}, Port: 2}}); err == nil {
		t.Fatal("a second engine with no model name must be an error")
	}
}
