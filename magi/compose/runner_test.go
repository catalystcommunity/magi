package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
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
	modelsapi "github.com/catalystcommunity/magi/primitives/models/api"
)

func TestRunnerName(t *testing.T) {
	for _, c := range []struct {
		p    authn.Principal
		want string
	}{
		{authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, "ht0"},
		{authn.Principal{Subject: "runner:ht0", Kind: authn.KindService}, ""},                                  // no scope
		{authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, ""}, // not a runner
		{authn.Principal{Subject: "runner:ht0", Kind: authn.KindUser, Scopes: []string{ScopeRunner}}, ""},      // a user
	} {
		got, err := runnerName(authn.With(context.Background(), c.p))
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("%+v: %q %v", c.p, got, err)
		}
	}
}

// The proxy needs a runner token, uses Magi's provider key and model, and
// never passes the runner token to the provider.
func TestModelProxy(t *testing.T) {
	var seen struct{ auth, model string }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		seen.auth, seen.model = r.Header.Get("Authorization"), body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer provider.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: provider.URL, Model: "coder-model", APIKey: "provider-key"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer proxy.Close()
	call := func(tok string) int {
		req, _ := http.NewRequest("POST", proxy.URL, strings.NewReader(`{"model":"anything","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	user, _ := iss.Issue(authn.Principal{Subject: "user:a@example.local", Kind: authn.KindUser, Scopes: []string{ScopeRunner}}, time.Hour)
	runner, _ := iss.Issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
	if code := call(""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code := call(user); code != 401 {
		t.Fatalf("user token: %d", code)
	}
	if code := call(runner); code != 200 || seen.auth != "Bearer provider-key" || seen.model != "coder-model" {
		t.Fatalf("runner: %d %+v", code, seen)
	}
}

// A provider 5xx body (account, host, key detail) does not reach the runner.
func TestModelProxyHidesProviderServerError(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":"org-secret account over quota at 10.9.8.7"}`)
	}))
	defer provider.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: provider.URL, Model: "m"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer proxy.Close()
	runner, _ := iss.Issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
	req, _ := http.NewRequest("POST", proxy.URL, strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+runner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 502 || strings.Contains(string(b), "secret") || strings.Contains(string(b), "10.9.8.7") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

// TestModelProxyStreams: the runner gets each chunk of a streamed answer
// when the provider sends it, not at the end. Else the idle limit of the
// apprentice cannot tell a slow answer from a provider that stopped.
func TestModelProxyStreams(t *testing.T) {
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	defer close(release)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: provider.URL, Model: "m"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer proxy.Close()
	runner, _ := iss.Issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
	req, _ := http.NewRequest("POST", proxy.URL, strings.NewReader(`{"stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+runner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 100)
		n, _ := resp.Body.Read(b)
		got <- string(b[:n])
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, `"content":"a"`) {
			t.Fatalf("first chunk: %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk did not come before the end of the answer")
	}
}

// proxyCatalog is a model catalog for the proxy tests. tasks has the
// default of the coding task (nil: no default). chain are the models that
// a select returns, in order; each name answers on the server of that
// name. A select that names a model returns only that model. down makes
// every call fail.
type proxyCatalog struct {
	modelsapi.ModelService
	mu    sync.Mutex
	tasks map[string]modelsapi.Selector
	chain []string
	bases map[string]string
	sel   []modelsapi.SelectRequest
	down  bool
}

func (f *proxyCatalog) GetTask(_ context.Context, r modelsapi.GetTaskRequest) (modelsapi.TaskDefault, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return modelsapi.TaskDefault{}, rpc.Unavailable("down")
	}
	s, ok := f.tasks[r.Task]
	if !ok {
		return modelsapi.TaskDefault{}, rpc.NotFound("task %s", r.Task)
	}
	return modelsapi.TaskDefault{Task: r.Task, Selector: s}, nil
}

func (f *proxyCatalog) Select(_ context.Context, r modelsapi.SelectRequest) (modelsapi.SelectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return modelsapi.SelectResponse{}, rpc.Unavailable("down")
	}
	f.sel = append(f.sel, r)
	names := f.chain
	if r.Selector != nil && r.Selector.Name != nil {
		names = []string{*r.Selector.Name}
	}
	out := modelsapi.SelectResponse{}
	for _, n := range names {
		if _, ok := f.bases[n]; !ok {
			continue
		}
		out.Candidates = append(out.Candidates, modelsapi.Candidate{Model: modelsapi.Model{Name: n,
			Endpoint: modelsapi.Endpoint{BaseUrl: f.bases[n], Model: n, KeyRef: ptr("inference")}, Enabled: true}, Reason: "test"})
	}
	return out, nil
}

// proxyTest starts a model proxy with a catalog. def is the task default
// (nil: no default). chain are the models of a select, in order; each name
// gets a model endpoint that answers with the model name of the request,
// or 503 when the name is "busy".
func proxyTest(t *testing.T, def *modelsapi.Selector, chain ...string) (string, string, *proxyCatalog) {
	t.Helper()
	bases := map[string]string{}
	for _, n := range chain {
		n := n
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer inference-key" {
				http.Error(w, `{"error":{"message":"no key"}}`, 401)
				return
			}
			if n == "busy" {
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":{"message":"busy"}}`, 503)
				return
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, body["model"])
		}))
		t.Cleanup(srv.Close)
		bases[n] = srv.URL
	}
	// The fixed model: a local endpoint, so that the fallback of the proxy
	// is reachable from a test.
	fixed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			http.Error(w, `{"error":{"message":"no key"}}`, 401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, body["model"])
	}))
	t.Cleanup(fixed.Close)
	cat := &proxyCatalog{bases: bases, chain: chain}
	if def != nil {
		cat.tasks = map[string]modelsapi.Selector{"step." + CodeChange + ".work": *def}
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService}, time.Hour)
	srv := &rpc.Server{Verifier: ver, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("ModelService", modelsapi.ModelServiceOps(cat))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	router := &ModelRouter{Catalog: modelsapi.NewModelClient(rpc.NewClient(h.URL, rpc.StaticToken(tok))), Token: tok,
		Keys: map[string]string{"inference": "inference-key"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: fixed.URL, Model: "fixed-model", APIKey: "provider-key"},
		router, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	t.Cleanup(proxy.Close)
	runner, _ := iss.Issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
	return proxy.URL, runner, cat
}

// callProxy calls the proxy and gives the answer body.
func callProxy(t *testing.T, url, tok, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// With no catalog default for the task, the proxy uses the fixed model.
func TestModelProxyNoCatalogDefault(t *testing.T) {
	url, runner, _ := proxyTest(t, nil, "local-coder")
	code, body := callProxy(t, url, runner, `{"model":"coder","messages":[]}`)
	if code != 200 || !strings.Contains(body, `"model":"fixed-model"`) {
		t.Fatalf("no default: %d %s", code, body)
	}
}

// With a task default, the proxy uses the catalog model and its key.
func TestModelProxyCatalogDefault(t *testing.T) {
	url, runner, cat := proxyTest(t, &modelsapi.Selector{Name: ptr("local-coder")}, "local-coder")
	code, body := callProxy(t, url, runner, `{"model":"coder","messages":[]}`)
	if code != 200 || !strings.Contains(body, `"model":"local-coder"`) {
		t.Fatalf("task default: %d %s", code, body)
	}
	cat.mu.Lock()
	last := cat.sel[len(cat.sel)-1]
	cat.mu.Unlock()
	if deref(last.Task) != "step."+CodeChange+".work" {
		t.Fatalf("select task: %s", deref(last.Task))
	}
}

// A named model: only that model, and only by name.
func TestModelProxyNamedModel(t *testing.T) {
	url, runner, cat := proxyTest(t, &modelsapi.Selector{Name: ptr("local-coder")}, "local-coder", "glm-5.3-coder")
	code, body := callProxy(t, url, runner, `{"model":"glm-5.3-coder","messages":[]}`)
	if code != 200 || !strings.Contains(body, `"model":"glm-5.3-coder"`) {
		t.Fatalf("named model: %d %s", code, body)
	}
	cat.mu.Lock()
	last := cat.sel[len(cat.sel)-1]
	cat.mu.Unlock()
	if last.Selector == nil || deref(last.Selector.Name) != "glm-5.3-coder" ||
		last.Selector.OnlyName == nil || !*last.Selector.OnlyName {
		t.Fatalf("select request: %+v", last)
	}
}

// A busy inference host answers 503; the next model of the chain answers.
func TestModelProxyBusyHost(t *testing.T) {
	url, runner, _ := proxyTest(t, &modelsapi.Selector{Name: ptr("busy")}, "busy", "local-coder")
	code, body := callProxy(t, url, runner, `{"model":"coder","messages":[]}`)
	if code != 200 || !strings.Contains(body, `"model":"local-coder"`) {
		t.Fatalf("busy host: %d %s", code, body)
	}
}

// A name that the catalog does not have: 400.
func TestModelProxyUnknownName(t *testing.T) {
	url, runner, _ := proxyTest(t, &modelsapi.Selector{Name: ptr("local-coder")}, "local-coder")
	code, body := callProxy(t, url, runner, `{"model":"no-such-model","messages":[]}`)
	if code != 400 || !strings.Contains(body, "no-such-model") {
		t.Fatalf("unknown name: %d %s", code, body)
	}
}

// A catalog that does not answer and a named model: 502, not the fixed
// model. The name is the choice of an operator, so the proxy must not
// send the call to an other model.
func TestModelProxyCatalogDownNamedModel(t *testing.T) {
	url, runner, cat := proxyTest(t, &modelsapi.Selector{Name: ptr("local-coder")}, "local-coder")
	cat.mu.Lock()
	cat.down = true
	cat.mu.Unlock()
	code, body := callProxy(t, url, runner, `{"model":"local-coder","messages":[]}`)
	if code != 502 || !strings.Contains(body, "local-coder") {
		t.Fatalf("catalog down: %d %s", code, body)
	}
}

// The proxy sends a catalog model through the transport of the router:
// that transport reaches inference hosts that connect out
// (http://<host>.revconn/v1), which the client of the proxy cannot.
func TestModelProxyRouterTransport(t *testing.T) {
	var seen []string
	hosts := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.Host)
		if r.URL.Host != "ht0.revconn" {
			return nil, errors.New("no such host: " + r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))}, nil
	})
	// The endpoint of the catalog is a revconn host. Only the transport of
	// the router reaches it.
	cat := &proxyCatalog{bases: map[string]string{"local-coder": "http://ht0.revconn/v1"}, chain: []string{"local-coder"},
		tasks: map[string]modelsapi.Selector{"step." + CodeChange + ".work": {Name: ptr("local-coder")}}}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService}, time.Hour)
	srv := &rpc.Server{Verifier: ver, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("ModelService", modelsapi.ModelServiceOps(cat))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	router := &ModelRouter{Catalog: modelsapi.NewModelClient(rpc.NewClient(h.URL, rpc.StaticToken(tok))), Token: tok,
		Keys: map[string]string{"inference": "inference-key"}, Transport: hosts, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: "http://fixed.invalid", Model: "fixed-model", APIKey: "provider-key"},
		router, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	t.Cleanup(proxy.Close)
	runner, _ := iss.Issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
	code, body := callProxy(t, proxy.URL, runner, `{"model":"coder","messages":[]}`)
	if code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("router transport: %d %s", code, body)
	}
	if len(seen) != 1 || seen[0] != "ht0.revconn" {
		t.Fatalf("the call did not go through the transport of the router: %v", seen)
	}
}

// A streamed answer of a catalog model still goes to the runner chunk by
// chunk, and a busy host does not stop the stream of the next model.
func TestModelProxyStreamsCatalog(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	bases := map[string]string{}
	for _, n := range []string{"busy", "local-coder"} {
		n := n
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if n == "busy" {
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":{"message":"busy"}}`, 503)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
			w.(http.Flusher).Flush()
			<-release
			io.WriteString(w, "data: [DONE]\n\n")
		}))
		t.Cleanup(srv.Close)
		mu.Lock()
		bases[n] = srv.URL
		mu.Unlock()
	}
	cat := &proxyCatalog{bases: bases, chain: []string{"busy", "local-coder"},
		tasks: map[string]modelsapi.Selector{"step." + CodeChange + ".work": {Name: ptr("busy")}}}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService}, time.Hour)
	srv := &rpc.Server{Verifier: ver, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("ModelService", modelsapi.ModelServiceOps(cat))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	router := &ModelRouter{Catalog: modelsapi.NewModelClient(rpc.NewClient(h.URL, rpc.StaticToken(tok))), Token: tok,
		Keys: map[string]string{"inference": "inference-key"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: "http://fixed.invalid", Model: "fixed-model", APIKey: "provider-key"},
		router, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	t.Cleanup(proxy.Close)
	runner, _ := iss.Issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
	req, _ := http.NewRequest("POST", proxy.URL, strings.NewReader(`{"model":"coder","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+runner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 100)
		n, _ := resp.Body.Read(b)
		got <- string(b[:n])
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, `"content":"a"`) {
			t.Fatalf("first chunk: %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk did not come before the end of the answer")
	}
	close(release)
}

// roundTripperFunc is a RoundTripper that one function makes.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
