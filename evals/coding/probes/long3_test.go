// Probes of case long3. Target: primitives/inference/cmd/magi-inference
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
)

// The test binary is also the engine of the probes. The arguments
// "probe3-engine PORT NAME" make it an engine that answers each chat
// request with its name. A request with "slow" in its body takes 1.5 s.
func init() {
	if len(os.Args) != 4 || os.Args[1] != "probe3-engine" {
		return
	}
	name := os.Args[3]
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if bytes.Contains(b, []byte("slow")) {
			time.Sleep(1500 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"engine":%q}`, name)
	})
	for i := 0; i < 100; i++ {
		if l, err := net.Listen("tcp", "127.0.0.1:"+os.Args[2]); err == nil {
			http.Serve(l, mux)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	os.Exit(0)
}

func probe3Port(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// probe3Host is a host program that runs from a configuration file, as an
// operator starts it.
type probe3Host struct {
	t     *testing.T
	base  string
	tok   string
	ports map[string]int // the port of each engine, by its model
}

type probe3Engine struct {
	Model  string
	Models []string // more model names of the engine
}

// probe3Config writes the configuration and gives its path. The first
// engine is `engine`, the others are `engines`.
func probe3Config(t *testing.T, more map[string]any, engines ...probe3Engine) (path, base, tok string, ports map[string]int) {
	t.Helper()
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := json.Marshal(authn.KeyFile{Issuer: "probe", KeyID: "k1", PublicKey: base64.StdEncoding.EncodeToString(pub)})
	keyPath := filepath.Join(dir, "issuer-public.json")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	iss := &authn.Issuer{Name: "probe", KeyID: "k1", Key: priv}
	tok, err := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"inference:use"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ports = map[string]int{}
	var list []map[string]any
	for _, e := range engines {
		port := probe3Port(t)
		ports[e.Model] = port
		m := map[string]any{"command": []string{os.Args[0], "probe3-engine", fmt.Sprint(port), e.Model}, "port": port}
		if e.Model != "" {
			m["model"] = e.Model
		}
		if len(e.Models) > 0 {
			vs := map[string]any{}
			for _, n := range e.Models {
				vs[n] = map[string]any{"lora": []any{}}
			}
			m["models"] = vs
		}
		list = append(list, m)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", probe3Port(t))
	cfg := map[string]any{"listen": listen, "auth_keys": []string{keyPath}, "engine": list[0], "start_wait": "20s",
		// The load of the test machine must not make the host busy.
		"rules": map[string]any{"cpu_load": 1000, "gpu_util": 101, "need_mib": 0, "quiet_for": "1s", "processes": []string{}}}
	if len(list) > 1 {
		cfg["engines"] = list[1:]
	}
	for k, v := range more {
		cfg[k] = v
	}
	b, _ := json.Marshal(cfg)
	path = filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, "http://" + listen, tok, ports
}

// probe3Start runs the host program with the engines until the test ends.
func probe3Start(t *testing.T, more map[string]any, engines ...probe3Engine) *probe3Host {
	t.Helper()
	path, base, tok, ports := probe3Config(t, more, engines...)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, path, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
		time.Sleep(100 * time.Millisecond)
	})
	p := &probe3Host{t: t, base: base, tok: tok, ports: ports}
	for i := 0; i < 200; i++ {
		select {
		case err := <-done:
			t.Fatalf("the host program ended at the start: %v", err)
		default:
		}
		if c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://")); err == nil {
			c.Close()
			return p
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the host program does not listen")
	return nil
}

var probe3Two = []probe3Engine{{Model: "usual"}, {Model: "careful", Models: []string{"careful-notes"}}}

// chat sends a chat request for model. It gives the status, the error
// type, the name of the engine that answered, and the Retry-After header.
func (p *probe3Host) chat(model, note string) (int, string, string, string) {
	p.t.Helper()
	body := fmt.Sprintf(`{"model":%q,"note":%q}`, model, note)
	req, _ := http.NewRequest("POST", p.base+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Engine string
		Error  struct{ Type string }
	}
	json.Unmarshal(b, &out)
	return resp.StatusCode, out.Error.Type, out.Engine, resp.Header.Get("Retry-After")
}

func (p *probe3Host) client() *rpc.Client { return rpc.NewClient(p.base, rpc.StaticToken(p.tok)) }

func probe3JSON(t *testing.T, st api.Status) map[string]any {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// status gives the status with detail in its JSON form, so that a probe
// does not use the Go names of new fields.
func (p *probe3Host) status() map[string]any {
	p.t.Helper()
	detail := true
	st, err := api.NewInferenceClient(p.client()).Status(context.Background(), api.StatusRequest{Detail: &detail})
	if err != nil {
		p.t.Fatalf("status: %v", err)
	}
	return probe3JSON(p.t, st)
}

func probe3Text(s string) []byte {
	if len(s) < 24 {
		return append([]byte{0x60 | byte(len(s))}, s...)
	}
	return append([]byte{0x78, byte(len(s))}, s...)
}

// load calls InferenceService/load through the real HTTP transport. The
// request is CBOR written by hand: {model, ? hold_seconds}. hold 0 leaves
// hold_seconds out.
func (p *probe3Host) load(model string, hold uint32) (map[string]any, error) {
	p.t.Helper()
	req := []byte{0xa1}
	if hold > 0 {
		req = []byte{0xa2}
	}
	req = append(req, probe3Text("model")...)
	req = append(req, probe3Text(model)...)
	if hold > 0 {
		req = append(req, probe3Text("hold_seconds")...)
		req = append(req, 0x1a, byte(hold>>24), byte(hold>>16), byte(hold>>8), byte(hold))
	}
	resp, err := p.client().Call(context.Background(), "InferenceService", "load", req)
	if err != nil {
		return nil, err
	}
	st, err := api.DecodeStatus(resp)
	if err != nil {
		p.t.Fatalf("the answer of load is not a status: %v", err)
	}
	return probe3JSON(p.t, st), nil
}

func (p *probe3Host) mustLoad(model string, hold uint32) map[string]any {
	p.t.Helper()
	st, err := p.load(model, hold)
	if err != nil {
		p.t.Fatalf("load %s: %v", model, err)
	}
	return st
}

// wait polls the status until ok.
func (p *probe3Host) wait(what string, limit time.Duration, ok func(map[string]any) bool) map[string]any {
	p.t.Helper()
	var st map[string]any
	for end := time.Now().Add(limit); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if st = p.status(); ok(st) {
			return st
		}
	}
	p.t.Fatalf("no status with %s in %s; the last status: %v", what, limit, st)
	return nil
}

func probe3Ready(model string) func(map[string]any) bool {
	return func(st map[string]any) bool { return st["state"] == "ready" && st["model"] == model }
}

// runs tells if the engine of model has a process that listens.
func (p *probe3Host) runs(model string) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p.ports[model]), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func probe3Names(v any) string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		out = append(out, fmt.Sprint(x))
	}
	return strings.Join(out, ",")
}

// The host runs its first engine until a load.
func TestProbeFirstEngineUntilALoad(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	if code, _, engine, _ := p.chat("usual", ""); code != 200 || engine != "usual" {
		t.Fatalf("a request for the model of the first engine: %d from %q", code, engine)
	}
	if st := p.status(); st["model"] != "usual" {
		t.Fatalf("model of the status = %v", st["model"])
	}
	if p.runs("careful") {
		t.Fatal("the second engine runs with no load")
	}
}

// A request for a model of a different engine is refused: status 503, the JSON error type not_loaded, and a Retry-After header.
func TestProbeOtherEngineIsRefused(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	code, kind, _, retry := p.chat("careful", "")
	if code != 503 || kind != "not_loaded" || retry == "" {
		t.Fatalf("request for the model of the second engine: %d %q Retry-After %q", code, kind, retry)
	}
	if code, kind, _, _ := p.chat("careful-notes", ""); code != 503 || kind != "not_loaded" {
		t.Fatalf("request for a name of models of the second engine: %d %q", code, kind)
	}
}

// A chat request never changes the engine, and a refused request does not start an engine.
func TestProbeChatDoesNotChangeTheEngine(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	p.chat("careful", "")
	time.Sleep(300 * time.Millisecond)
	if p.runs("careful") || p.runs("usual") {
		t.Fatalf("a refused request started an engine: usual %v, careful %v", p.runs("usual"), p.runs("careful"))
	}
	if st := p.status(); st["model"] != "usual" || st["state"] != "off" {
		t.Fatalf("status after a refused request: model %v, state %v", st["model"], st["state"])
	}
}

// On a host with more than one engine, a model name that no engine has gets the same refusal (503, not_loaded).
func TestProbeUnknownNameWithTwoEngines(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	if code, kind, _, _ := p.chat("nope", ""); code != 503 || kind != "not_loaded" {
		t.Fatalf("unknown model name: %d %q", code, kind)
	}
}

// A host with one engine sends each model name to that engine, as now.
func TestProbeOneEngineTakesEachName(t *testing.T) {
	p := probe3Start(t, nil, probe3Engine{Model: "usual"})
	if code, _, engine, _ := p.chat("some-other-name", ""); code != 200 || engine != "usual" {
		t.Fatalf("a host with one engine: %d from %q", code, engine)
	}
}

// load makes the engine that serves model the engine of the host.
func TestProbeLoadChangesTheEngine(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	if code, _, _, _ := p.chat("usual", ""); code != 200 {
		t.Fatalf("first engine: %d", code)
	}
	if st := p.mustLoad("careful", 0); st["model"] != "careful" {
		t.Fatalf("model of the status after the load = %v", st["model"])
	}
	p.wait("the second engine ready", 20*time.Second, probe3Ready("careful"))
	if code, _, engine, _ := p.chat("careful", ""); code != 200 || engine != "careful" {
		t.Fatalf("request for the loaded model: %d from %q", code, engine)
	}
}

// From the load on, a new request for a model of the old engine is refused (not_loaded).
func TestProbeOldEngineRefusedAfterALoad(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	p.chat("usual", "")
	p.mustLoad("careful", 0)
	if code, kind, _, _ := p.chat("usual", ""); code != 503 || kind != "not_loaded" {
		t.Fatalf("request for the old engine after the load: %d %q", code, kind)
	}
}

// It never runs two engines at the same time: an engine starts only after the process of the engine before it ended.
func TestProbeNeverTwoEngines(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	p.chat("usual", "")
	if !p.runs("usual") {
		t.Fatal("the first engine does not run after a request")
	}
	p.mustLoad("careful", 0)
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		usual, careful := p.runs("usual"), p.runs("careful")
		if usual && careful {
			t.Fatal("the two engines run at the same time")
		}
		if careful && !usual {
			return
		}
	}
	t.Fatal("the second engine did not start")
}

// A model that no engine has is "not found" (status 404).
func TestProbeLoadOfAnUnknownModel(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	// A host with no load operation also answers 404: the operation must
	// be there for a model that an engine has.
	p.mustLoad("careful", 0)
	if _, err := p.load("nope", 0); rpc.CodeOf(err) != 404 {
		t.Fatalf("load of an unknown model: %v", err)
	}
	if st := p.status(); st["model"] != "careful" {
		t.Fatalf("a refused load changed the engine to %v", st["model"])
	}
}

// model is the model of an engine or a name of its models.
func TestProbeLoadByANameOfModels(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	if st := p.mustLoad("careful-notes", 0); st["model"] != "careful" {
		t.Fatalf("model of the status after a load by an adapter name = %v", st["model"])
	}
	p.wait("the second engine ready", 20*time.Second, probe3Ready("careful"))
	if !p.runs("careful") {
		t.Fatal("the second engine does not run")
	}
}

// Requests in flight get their answers from the old engine. The state of the status is starting from the load until the new engine is ready, also while the old engine still answers a request.
func TestProbeLoadWithARequestInFlight(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	p.chat("usual", "")
	type answer struct {
		code   int
		engine string
	}
	done := make(chan answer, 1)
	go func() {
		code, _, engine, _ := p.chat("usual", "slow")
		done <- answer{code, engine}
	}()
	p.wait("a request in flight", 5*time.Second, func(st map[string]any) bool { return st["in_flight"] == float64(1) })
	if st := p.mustLoad("careful", 0); st["state"] != "starting" || st["model"] != "careful" {
		t.Fatalf("status of a load with a request in flight: state %v, model %v", st["state"], st["model"])
	}
	if a := <-done; a.code != 200 || a.engine != "usual" {
		t.Fatalf("the request in flight: %+v", a)
	}
	p.wait("the second engine ready", 20*time.Second, probe3Ready("careful"))
}

// On a host with more than one engine, loadable has the model of each engine, in the sequence of the configuration; on a host with one engine it is absent.
func TestProbeLoadable(t *testing.T) {
	p := probe3Start(t, nil, probe3Engine{Model: "usual"}, probe3Engine{Model: "careful"}, probe3Engine{Model: "another"})
	if got := probe3Names(p.status()["loadable"]); got != "usual,careful,another" {
		t.Fatalf("loadable = %q", got)
	}
	one := probe3Start(t, nil, probe3Engine{Model: "usual"})
	if v, ok := one.status()["loadable"]; ok {
		t.Fatalf("loadable on a host with one engine = %v", v)
	}
}

// models has the model names of the engine that the host runs or starts next.
func TestProbeModelsOfTheLoadedEngine(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	if got := probe3Names(p.status()["models"]); got != "usual" {
		t.Fatalf("models before a load = %q", got)
	}
	if got := probe3Names(p.mustLoad("careful", 0)["models"]); got != "careful,careful-notes" {
		t.Fatalf("models in the answer of load = %q", got)
	}
}

// GET /v1/models lists the model names of the engine that the host runs.
func TestProbeModelListOfTheLoadedEngine(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	p.mustLoad("careful", 0)
	p.wait("the second engine ready", 20*time.Second, probe3Ready("careful"))
	req, _ := http.NewRequest("GET", p.base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+p.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Data []struct{ Id string } }
	json.NewDecoder(resp.Body).Decode(&out)
	var ids []string
	for _, d := range out.Data {
		ids = append(ids, d.Id)
	}
	if got := strings.Join(ids, ","); got != "careful,careful-notes" {
		t.Fatalf("GET /v1/models after a load = %q", got)
	}
}

// hold_until_ms is the time when the hold ends; it is absent while the host runs its first engine. The default is 900 seconds.
func TestProbeDefaultHold(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	if v, ok := p.status()["hold_until_ms"]; ok {
		t.Fatalf("hold_until_ms with the first engine = %v", v)
	}
	before := time.Now()
	ms, _ := p.mustLoad("careful", 0)["hold_until_ms"].(float64)
	hold := time.UnixMilli(int64(ms)).Sub(before)
	if hold < 890*time.Second || hold > 910*time.Second {
		t.Fatalf("the hold of a load with no hold_seconds is %s, want 900 s", hold.Round(time.Second))
	}
}

// The maximum is 14400: a larger value counts as 14400.
func TestProbeMaximumHold(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	before := time.Now()
	ms, _ := p.mustLoad("careful", 100000)["hold_until_ms"].(float64)
	hold := time.UnixMilli(int64(ms)).Sub(before)
	if hold < 14390*time.Second || hold > 14410*time.Second {
		t.Fatalf("the hold of a load with hold_seconds 100000 is %s, want 14400 s", hold.Round(time.Second))
	}
}

// When the hold ended and no request is in flight, the host goes back to its first engine: else it starts with the next request.
func TestProbeHoldEnds(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	p.mustLoad("careful", 1)
	p.wait("the second engine ready", 20*time.Second, probe3Ready("careful"))
	st := p.wait("the first engine again", 20*time.Second, func(st map[string]any) bool { return st["model"] == "usual" })
	if v, ok := st["hold_until_ms"]; ok {
		t.Fatalf("hold_until_ms after the hold = %v", v)
	}
	p.wait("no engine", 10*time.Second, func(st map[string]any) bool { return st["state"] == "off" })
	if p.runs("careful") || p.runs("usual") {
		t.Fatalf("after the hold with no keep_warm: usual runs %v, careful runs %v", p.runs("usual"), p.runs("careful"))
	}
	if code, _, engine, _ := p.chat("usual", ""); code != 200 || engine != "usual" {
		t.Fatalf("the next request for the first engine: %d from %q", code, engine)
	}
}

// With keep_warm the first engine starts at once.
func TestProbeHoldEndsWithKeepWarm(t *testing.T) {
	p := probe3Start(t, map[string]any{"keep_warm": true}, probe3Two...)
	p.wait("the first engine ready", 20*time.Second, probe3Ready("usual"))
	p.mustLoad("careful", 1)
	p.wait("the second engine ready", 20*time.Second, probe3Ready("careful"))
	p.wait("the first engine ready again, with no request", 30*time.Second, probe3Ready("usual"))
	if !p.runs("usual") || p.runs("careful") {
		t.Fatalf("after the hold: usual runs %v, careful runs %v", p.runs("usual"), p.runs("careful"))
	}
}

// A load of a model of the first engine ends the hold at once.
func TestProbeLoadOfTheFirstEngineEndsTheHold(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	p.mustLoad("careful", 0)
	p.wait("the second engine ready", 20*time.Second, probe3Ready("careful"))
	st := p.mustLoad("usual", 0)
	if _, ok := st["hold_until_ms"]; ok || st["model"] != "usual" {
		t.Fatalf("status after a load of the first engine: model %v, hold_until_ms %v", st["model"], st["hold_until_ms"])
	}
	p.wait("the first engine ready", 20*time.Second, probe3Ready("usual"))
	if code, _, engine, _ := p.chat("usual", ""); code != 200 || engine != "usual" {
		t.Fatalf("request for the first engine: %d from %q", code, engine)
	}
}

// InferenceService/decide: a model of a different engine is refused in the same way as a chat request for it (the error has not_loaded).
func TestProbeDecideForADifferentEngine(t *testing.T) {
	p := probe3Start(t, nil, probe3Two...)
	model := "careful"
	_, err := api.NewInferenceClient(p.client()).Decide(context.Background(), api.DecideRequest{State: "s", Model: &model,
		Questions: []api.Question{{Id: "q", Type: "noul", Question: "Is it so?"}}})
	if err == nil || !strings.Contains(err.Error(), "not_loaded") {
		t.Fatalf("decide for a model of an engine that is not loaded: %v", err)
	}
	if p.runs("careful") {
		t.Fatal("a decide request started the second engine")
	}
}

// Each model name (the model of an engine and each name of its models) must be in one engine only. The host program does not start with a configuration that breaks this rule.
func TestProbeConfigurationOneNameOneEngine(t *testing.T) {
	for _, engines := range [][]probe3Engine{
		{{Model: "usual"}, {Model: "usual"}},
		{{Model: "usual", Models: []string{"notes"}}, {Model: "careful", Models: []string{"notes"}}},
		{{Model: "usual"}, {Model: "careful", Models: []string{"usual"}}},
	} {
		path, _, _, _ := probe3Config(t, nil, engines...)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- run(ctx, path, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%+v: the host program ended with no error", engines)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%+v: the host program started", engines)
		}
		cancel()
	}
}

// With more than one engine, each engine needs command, port, and model.
func TestProbeConfigurationEngineNeedsAModel(t *testing.T) {
	path, _, _, _ := probe3Config(t, nil, probe3Engine{Model: "usual"}, probe3Engine{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, path, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the host program ended with no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the host program started with a second engine that has no model")
	}
}

// A configuration with no engines works as before.
func TestProbeNoEnginesAsBefore(t *testing.T) {
	p := probe3Start(t, nil, probe3Engine{})
	if code, _, engine, _ := p.chat("anything", ""); code != 200 || engine != "" {
		t.Fatalf("one engine with no model name: %d from %q", code, engine)
	}
}
