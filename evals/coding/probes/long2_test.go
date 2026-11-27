// Probes of case long2. Target: primitives/inference/host
package host

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
)

// The test binary is also the engine of the probes: a llama-server with a
// decision model. The argument "probe-head-engine" starts it with a port
// and a file, where it writes one line for each request.
func init() {
	if len(os.Args) == 4 && os.Args[1] == "probe-head-engine" {
		probeHeadEngine(os.Args[2], os.Args[3])
		os.Exit(0)
	}
}

// probeHeadRequest is one request to the engine.
type probeHeadRequest struct {
	Path string         `json:"path"`
	Body map[string]any `json:"body"`
}

// probeHeadEngine answers /v1/systemone as the task tells: the probability
// of "yes" is 0.96 when the state has "official", else 0.02. The question
// with the id "lost" gets no answer, and the question with the id "wide"
// gets a number that is not a probability. A request that does not have
// the form of the task gets no answers.
func probeHeadEngine(port, logFile string) {
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		req := probeHeadRequest{Path: r.URL.Path}
		json.NewDecoder(r.Body).Decode(&req.Body)
		line, _ := json.Marshal(req)
		mu.Lock()
		if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			f.Write(append(line, '\n'))
			f.Close()
		}
		mu.Unlock()
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		answers := map[string]any{}
		state, _ := req.Body["state"].(string)
		questions, _ := req.Body["questions"].(map[string]any)
		for id, v := range questions {
			q, _ := v.(map[string]any)
			text, _ := q["instructions"].(string)
			if id == "lost" || q["type"] != "noul" || text == "" || req.Body["model"] != "local" {
				continue
			}
			p := 0.02
			if strings.Contains(state, "official") {
				p = 0.96
			}
			if id == "wide" {
				p = 1.7
			}
			answers[id] = map[string]any{"type": "noul", "noul": p}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	})
	for i := 0; i < 100; i++ {
		if l, err := net.Listen("tcp", "127.0.0.1:"+port); err == nil {
			http.Serve(l, mux)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// probeQuiet is a machine with no other work.
type probeQuiet struct{}

func (probeQuiet) Sample(context.Context, int) Sample {
	return Sample{GPUUtil: 0, GPUUsedMiB: 1000, GPUTotalMiB: 16000}
}

type probeHead struct {
	t   *testing.T
	c   *api.InferenceClient
	log string
}

// probeHeadHost starts a host with the engine of the probes. The engine
// settings come from JSON, as in the configuration file of a host, so the
// probe does not depend on the Go name of the new setting.
func probeHeadHost(t *testing.T, decide string) *probeHead {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	for i := 0; i < 200 && port == 0; i++ {
		n := 20000 + rand.IntN(12000)
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", n)); err == nil {
			l.Close()
			port = n
		}
	}
	p := &probeHead{t: t, log: t.TempDir() + "/engine.jsonl"}
	settings := map[string]any{"command": []string{exe, "probe-head-engine", strconv.Itoa(port), p.log}, "port": port, "model": "local"}
	if decide != "" {
		settings["decide"] = decide
	}
	b, _ := json.Marshal(settings)
	cfg := Config{IdleStop: 10 * time.Minute, StartWait: 30 * time.Second, Interval: time.Hour}
	if err := json.Unmarshal(b, &cfg.Engine); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	h := New(cfg, probeQuiet{}, ver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; time.Sleep(100 * time.Millisecond) })
	rs := &rpc.Server{Verifier: ver, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rs.Register("InferenceService", api.InferenceServiceOps(h))
	hs := httptest.NewServer(rpc.Mux(rs, nil))
	t.Cleanup(hs.Close)
	tok, err := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{Scope}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p.c = api.NewInferenceClient(rpc.NewClient(hs.URL, rpc.StaticToken(tok)))
	return p
}

// decide asks the host. The first call starts the engine, and a host can
// refuse while the engine starts.
func (p *probeHead) decide(state string, qs ...api.Question) (api.DecideResponse, error) {
	p.t.Helper()
	var res api.DecideResponse
	var err error
	for i := 0; i < 200; i++ {
		if res, err = p.c.Decide(context.Background(), api.DecideRequest{State: state, Questions: qs}); err == nil || rpc.CodeOf(err) != 503 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return res, err
}

// requests gives the requests that the engine got, without the health checks.
func (p *probeHead) requests() []probeHeadRequest {
	p.t.Helper()
	b, _ := os.ReadFile(p.log)
	var out []probeHeadRequest
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r probeHeadRequest
		if json.Unmarshal([]byte(line), &r) == nil && r.Path != "" {
			out = append(out, r)
		}
	}
	return out
}

func probeNoul(id string) api.Question {
	return api.Question{Id: id, Type: "noul", Question: "Is this host a known public source? (" + id + ")"}
}

func probeNear(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The host answers each question with probs [p, 1 - p]. The answer is "yes" when p is 0.5 or more, and the confidence is the larger probability.
func TestProbeHeadAnswersYes(t *testing.T) {
	p := probeHeadHost(t, "head")
	res, err := p.decide("Host: the official site", probeNoul("source"))
	if err != nil || len(res.Answers) != 1 {
		t.Fatalf("decide: %+v, %v", res, err)
	}
	a := res.Answers[0]
	if a.Id != "source" || a.Answer != "yes" || len(a.Probs) != 2 || !probeNear(a.Probs[0], 0.96) || !probeNear(a.Probs[1], 0.04) || !probeNear(a.Confidence, 0.96) {
		t.Errorf("answer = %+v", a)
	}
	if res.Model != "local" {
		t.Errorf("the model of the answer is %q, want local", res.Model)
	}
}

// The answer is "no" when p is less than 0.5, and the confidence is the larger probability.
func TestProbeHeadAnswersNo(t *testing.T) {
	p := probeHeadHost(t, "head")
	res, err := p.decide("Host: a paste site", probeNoul("source"))
	if err != nil || len(res.Answers) != 1 {
		t.Fatalf("decide: %+v, %v", res, err)
	}
	a := res.Answers[0]
	if a.Answer != "no" || len(a.Probs) != 2 || !probeNear(a.Probs[0], 0.02) || !probeNear(a.Probs[1], 0.98) || !probeNear(a.Confidence, 0.98) {
		t.Errorf("answer = %+v", a)
	}
}

// The host sends the state and all questions in one call: POST /v1/systemone with the model of the engine, the state, and the questions by id, each with the type "noul" and the question text as "instructions".
func TestProbeHeadOneCallForAllQuestions(t *testing.T) {
	p := probeHeadHost(t, "head")
	res, err := p.decide("Host: the official site", probeNoul("b"), probeNoul("a"), probeNoul("c"))
	if err != nil || len(res.Answers) != 3 {
		t.Fatalf("decide: %+v, %v", res, err)
	}
	if res.Answers[0].Id != "b" || res.Answers[1].Id != "a" || res.Answers[2].Id != "c" {
		t.Errorf("the answers are not in the sequence of the questions: %+v", res.Answers)
	}
	reqs := p.requests()
	if len(reqs) != 1 || reqs[0].Path != "/v1/systemone" {
		t.Fatalf("the engine got %d requests, want one to /v1/systemone: %+v", len(reqs), reqs)
	}
	body := reqs[0].Body
	qs, _ := body["questions"].(map[string]any)
	if body["model"] != "local" || body["state"] != "Host: the official site" || len(qs) != 3 {
		t.Fatalf("the request: %v", body)
	}
	for _, id := range []string{"a", "b", "c"} {
		q, _ := qs[id].(map[string]any)
		if q["type"] != "noul" || q["instructions"] != probeNoul(id).Question {
			t.Errorf("question %s of the request: %v", id, q)
		}
	}
}

// A decision model answers only "noul" questions: a question of a different type is not valid (status 400).
func TestProbeHeadOnlyYesOrNoQuestions(t *testing.T) {
	p := probeHeadHost(t, "head")
	if _, err := p.decide("Host: the official site", probeNoul("source")); err != nil {
		t.Fatalf("decide: %v", err)
	}
	_, err := p.decide("x", api.Question{Id: "k", Type: "choice", Question: "Which?", Choices: []string{"a", "b"}})
	if rpc.CodeOf(err) != 400 {
		t.Errorf("a choice question: %v, want status 400", err)
	}
	_, err = p.decide("x", probeNoul("source"), api.Question{Id: "n", Type: "score", Question: "How much?"})
	if rpc.CodeOf(err) != 400 {
		t.Errorf("a score question after a noul question: %v, want status 400", err)
	}
}

// Each question needs its own id, and the id is not empty: else the request is not valid (status 400).
func TestProbeHeadEachQuestionNeedsItsOwnID(t *testing.T) {
	p := probeHeadHost(t, "head")
	if _, err := p.decide("Host: the official site", probeNoul("source")); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if _, err := p.decide("x", probeNoul("same"), probeNoul("same")); rpc.CodeOf(err) != 400 {
		t.Errorf("two questions with one id: %v, want status 400", err)
	}
	if _, err := p.decide("x", probeNoul("")); rpc.CodeOf(err) != 400 {
		t.Errorf("a question with no id: %v, want status 400", err)
	}
}

// When the engine gives no answer for a question, decide fails. It is an error of the host (not status 400), and it is not the answer "no".
func TestProbeHeadLostAnswerIsAnError(t *testing.T) {
	p := probeHeadHost(t, "head")
	if _, err := p.decide("Host: the official site", probeNoul("source")); err != nil {
		t.Fatalf("decide: %v", err)
	}
	res, err := p.decide("Host: the official site", probeNoul("source"), probeNoul("lost"))
	if err == nil || rpc.CodeOf(err) == 400 {
		t.Errorf("no answer of the engine: %+v, %v", res, err)
	}
}

// A number that is not from 0 to 1 is not a probability: decide fails in the same way.
func TestProbeHeadBadProbabilityIsAnError(t *testing.T) {
	p := probeHeadHost(t, "head")
	if _, err := p.decide("Host: the official site", probeNoul("source")); err != nil {
		t.Fatalf("decide: %v", err)
	}
	res, err := p.decide("Host: the official site", probeNoul("wide"))
	if err == nil || rpc.CodeOf(err) == 400 {
		t.Errorf("the engine gave 1.7: %+v, %v", res, err)
	}
}

// With no "decide" key, decide works as before.
func TestProbeNoDecideKeyNoHead(t *testing.T) {
	p := probeHeadHost(t, "")
	// The engine of the probes has no language model, so decide fails. The
	// probe looks at the requests that the host made.
	p.decide("Host: the official site", probeNoul("source"))
	reqs := p.requests()
	if len(reqs) == 0 {
		t.Fatal("the host made no request to the engine")
	}
	for _, r := range reqs {
		if r.Path == "/v1/systemone" {
			t.Errorf("a host with no decision model called /v1/systemone")
		}
	}
}
