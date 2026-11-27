package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/magi/model"
	inferenceapi "github.com/catalystcommunity/magi/primitives/inference/api"
	modelsapi "github.com/catalystcommunity/magi/primitives/models/api"
)

// fakeHost is an inference host with the engines "usual" and "careful".
// It runs "usual" until a load. A chat request for the other engine gets
// 503 with the error type "not_loaded".
type fakeHost struct {
	inferenceapi.InferenceService
	mu     sync.Mutex
	active string
	loads  []inferenceapi.LoadRequest
	// polls is how many status calls answer "starting" after a load.
	polls int
	// busy: the machine is busy, so a load starts no engine.
	busy bool
	// chats counts the chat requests that the host answered with 200.
	chats int
}

func (f *fakeHost) status() inferenceapi.Status {
	st := inferenceapi.Status{State: "ready", Model: f.active, Models: []string{f.active}, Loadable: []string{"usual", "careful"}}
	if f.busy {
		st.State, st.Reason = "busy", ptr("process vrserver")
	} else if f.polls > 0 {
		f.polls--
		st.State = "starting"
	}
	return st
}

func (f *fakeHost) Load(_ context.Context, r inferenceapi.LoadRequest) (inferenceapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Model != "usual" && r.Model != "careful" {
		return inferenceapi.Status{}, rpc.NotFound("no model %q on this host", r.Model)
	}
	f.loads = append(f.loads, r)
	f.active = r.Model
	return f.status(), nil
}

func (f *fakeHost) Status(context.Context, inferenceapi.StatusRequest) (inferenceapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status(), nil
}

func (f *fakeHost) chat(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if body["model"] != f.active || f.busy {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(503)
		kind := "not_loaded"
		if f.busy && body["model"] == f.active {
			kind = "host_busy"
		}
		fmt.Fprintf(w, `{"error":{"type":%q,"message":"model %v is not loaded: the host runs %s"}}`, kind, body["model"], f.active)
		return
	}
	f.chats++
	fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, body["model"])
}

// placementTest starts a model proxy with a catalog whose models "usual"
// and "careful" are on one fake host. def is the default of the coding
// task; chain are the models of a select with no name.
func placementTest(t *testing.T, def string, chain ...string) (string, string, *fakeHost, *proxyCatalog) {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	issue := func(p authn.Principal) string {
		tok, err := iss.Issue(p, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	hostKey := issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"inference:use"}})
	f := &fakeHost{active: "usual"}
	hs := &rpc.Server{Verifier: ver, Log: quiet}
	hs.Register("InferenceService", inferenceapi.InferenceServiceOps(f))
	mux := rpc.Mux(hs, nil)
	mux.HandleFunc("/v1/chat/completions", f.chat)
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)

	// A second host with one engine that is always busy.
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"host_busy","message":"busy"}}`, 503)
	}))
	t.Cleanup(busy.Close)
	cat := &proxyCatalog{chain: chain, bases: map[string]string{"usual": host.URL + "/v1", "careful": host.URL + "/v1", "busy": busy.URL},
		attrs: map[string]map[string]string{"usual": {"context": "65536"}, "careful": {"context": "131072", "work_seconds": "7200"}}}
	if def != "" {
		cat.tasks = map[string]modelsapi.Selector{"step." + CodeChange + ".work": {Name: ptr(def)}}
	}
	tok := issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService})
	cs := &rpc.Server{Verifier: ver, Log: quiet}
	cs.Register("ModelService", modelsapi.ModelServiceOps(cat))
	ch := httptest.NewServer(rpc.Mux(cs, nil))
	t.Cleanup(ch.Close)
	router := &ModelRouter{Catalog: modelsapi.NewModelClient(rpc.NewClient(ch.URL, rpc.StaticToken(tok))), Token: tok,
		Keys: map[string]string{"inference": hostKey}, Log: quiet}
	fixed := &model.Client{BaseURL: "http://fixed.invalid", Model: "fixed-model", APIKey: "provider-key"}
	pm := http.NewServeMux()
	pm.Handle("/runner/v1/chat/completions", modelProxy(ver, fixed, router, quiet, &placer{Hold: 20 * time.Minute, Wait: 5 * time.Second, Poll: 5 * time.Millisecond}))
	pm.Handle("/runner/v1/models/", RunnerModelLimits(ver, fixed, router))
	proxy := httptest.NewServer(pm)
	t.Cleanup(proxy.Close)
	runner := issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}})
	return proxy.URL + "/runner/v1", runner, f, cat
}

// A run names a model that its host did not load: the proxy tells the
// host to load it, waits until it is ready, and the call gets its answer.
func TestModelProxyLoadsANamedModel(t *testing.T) {
	url, runner, f, _ := placementTest(t, "usual", "usual")
	f.polls = 3
	code, body := callProxy(t, url+"/chat/completions", runner, `{"model":"careful","messages":[]}`)
	if code != 200 || !strings.Contains(body, `"model":"careful"`) {
		t.Fatalf("named model that was not loaded: %d %s", code, body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.loads) != 1 || f.loads[0].Model != "careful" || f.loads[0].HoldSeconds == nil || *f.loads[0].HoldSeconds != 1200 {
		t.Fatalf("loads = %+v", f.loads)
	}
	if f.polls != 0 || f.chats != 1 {
		t.Fatalf("the call did not wait for the engine: %d polls left, %d chats", f.polls, f.chats)
	}
}

// The model that the task prefers is not loaded (the host still holds a
// different model): the proxy loads it also for a call with no name.
func TestModelProxyLoadsTheModelOfTheTask(t *testing.T) {
	url, runner, f, _ := placementTest(t, "usual", "usual")
	f.active = "careful"
	code, body := callProxy(t, url+"/chat/completions", runner, `{"model":"coder","messages":[]}`)
	if code != 200 || !strings.Contains(body, `"model":"usual"`) {
		t.Fatalf("task model that was not loaded: %d %s", code, body)
	}
	if len(f.loads) != 1 || f.loads[0].Model != "usual" {
		t.Fatalf("loads = %+v", f.loads)
	}
}

// A model that is loaded gets no load call.
func TestModelProxyNoLoadForALoadedModel(t *testing.T) {
	url, runner, f, _ := placementTest(t, "usual", "usual")
	if code, body := callProxy(t, url+"/chat/completions", runner, `{"model":"coder","messages":[]}`); code != 200 {
		t.Fatalf("loaded model: %d %s", code, body)
	}
	if len(f.loads) != 0 {
		t.Fatalf("loads = %+v", f.loads)
	}
}

// The host is busy with the work of its owner: the load starts no engine,
// and the runner gets the refusal of the host with its Retry-After.
func TestModelProxyLoadOnABusyHost(t *testing.T) {
	url, runner, f, _ := placementTest(t, "usual", "usual")
	f.busy = true
	req, _ := http.NewRequest("POST", url+"/chat/completions", strings.NewReader(`{"model":"careful","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+runner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 503 || !strings.Contains(string(b), "not_loaded") || resp.Header.Get("Retry-After") != "30" {
		t.Fatalf("busy host: %d %q Retry-After %q", resp.StatusCode, b, resp.Header.Get("Retry-After"))
	}
	if len(f.loads) != 1 || f.chats != 0 {
		t.Fatalf("loads = %+v, chats = %d", f.loads, f.chats)
	}
}

// Only the first model of the chain gets a load: a later model is a
// fallback, and a fallback must not take the GPU from the model that the
// host runs.
func TestModelProxyNoLoadForAFallback(t *testing.T) {
	url, runner, f, _ := placementTest(t, "busy", "busy", "careful")
	code, body := callProxy(t, url+"/chat/completions", runner, `{"model":"coder","messages":[]}`)
	if code != 503 || !strings.Contains(body, "not_loaded") {
		t.Fatalf("fallback that is not loaded: %d %s", code, body)
	}
	if len(f.loads) != 0 {
		t.Fatalf("loads = %+v", f.loads)
	}
}

func getLimits(t *testing.T, url, tok string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// The limits of a run come from the attributes of the catalog model.
func TestRunnerModelLimits(t *testing.T) {
	url, runner, _, _ := placementTest(t, "usual", "usual", "careful")
	code, out := getLimits(t, url+"/models/coder", runner)
	if code != 200 || out["context"] != float64(65536) || out["work_seconds"] != nil || fmt.Sprint(out["models"]) != "[usual careful]" {
		t.Fatalf("limits of the task model: %d %v", code, out)
	}
	code, out = getLimits(t, url+"/models/careful", runner)
	if code != 200 || out["context"] != float64(131072) || out["work_seconds"] != float64(7200) || fmt.Sprint(out["models"]) != "[careful]" {
		t.Fatalf("limits of a named model: %d %v", code, out)
	}
	if code, _ := getLimits(t, url+"/models/nope", runner); code != 400 {
		t.Fatalf("unknown model: %d", code)
	}
	if code, _ := getLimits(t, url+"/models/coder", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
}

// With no catalog default for the task, the fixed model has no limits.
func TestRunnerModelLimitsOfTheFixedModel(t *testing.T) {
	url, runner, _, _ := placementTest(t, "")
	code, out := getLimits(t, url+"/models/coder", runner)
	if code != 200 || out["id"] != "coder" || len(out) != 1 {
		t.Fatalf("fixed model: %d %v", code, out)
	}
}
