package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
)

// fakeDecideEngine is the llama-server API of decide in the fake engine.
// Labels A, B, C, 0..4 are tokens 1, 2, 3, 10..14; "AB" is two tokens. The
// first token is B (logprob -0.1), then A (-2.5); a prompt with "lora" in
// the request makes A the first.
func fakeDecideEngine() {
	ids := map[string]int{"A": 1, "B": 2, "C": 3, "0": 10, "1": 11, "2": 12, "3": 13, "4": 14}
	http.HandleFunc("/tokenize", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Content string }
		json.NewDecoder(r.Body).Decode(&in)
		if id, ok := ids[in.Content]; ok {
			fmt.Fprintf(w, `{"tokens":[%d]}`, id)
			return
		}
		fmt.Fprint(w, `{"tokens":[7,8]}`)
	})
	http.HandleFunc("/apply-template", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Messages []struct{ Content string } }
		json.NewDecoder(r.Body).Decode(&in)
		json.NewEncoder(w).Encode(map[string]string{"prompt": in.Messages[0].Content + "|" + in.Messages[1].Content})
	})
	http.HandleFunc("/completion", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		first, second := 2, 1
		if strings.Contains(string(b), `"lora"`) {
			first, second = 1, 2
		}
		fmt.Fprintf(w, `{"completion_probabilities":[{"top_logprobs":[{"id":%d,"logprob":-0.1},{"id":%d,"logprob":-2.5},{"id":99,"logprob":-9}]}]}`, first, second)
	})
}

// fakeHeadEngine is the llama-server API of a decision model in the fake
// engine: the probability of "yes" is 0.96 when the state has "official",
// else 0.02. A question with the id "lost" gets no answer.
func fakeHeadEngine() {
	http.HandleFunc("/v1/systemone", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model     string
			State     string
			Questions map[string]struct{ Type, Instructions string }
		}
		json.NewDecoder(r.Body).Decode(&in)
		answers := map[string]any{}
		for id, q := range in.Questions {
			if id == "lost" || q.Type != "noul" || q.Instructions == "" || in.Model != "local" {
				continue
			}
			p := 0.02
			if strings.Contains(in.State, "official") {
				p = 0.96
			}
			answers[id] = map[string]any{"type": "noul", "noul": p}
		}
		json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	})
}

// A host with a decision model answers decide with one call to the engine.
func TestDecideWithAHead(t *testing.T) {
	e := setup(t, 30*time.Second, func(c *Config) { c.Engine.Decide = "head" })
	s := &rpc.Server{Verifier: e.h.ver}
	s.Register("InferenceService", api.InferenceServiceOps(e.h))
	hs := httptest.NewServer(rpc.Mux(s, nil))
	t.Cleanup(hs.Close)
	c := api.NewInferenceClient(rpc.NewClient(hs.URL, rpc.StaticToken(e.tok(Scope))))
	ctx := context.Background()
	q := api.Question{Id: "source", Type: "noul", Question: "Is this host a known public source?"}
	var res api.DecideResponse
	var err error
	// The first call starts the engine.
	for i := 0; i < 100; i++ {
		if res, err = c.Decide(ctx, api.DecideRequest{State: "Host: the official site", Questions: []api.Question{q}}); err == nil || rpc.CodeOf(err) != 503 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || len(res.Answers) != 1 {
		t.Fatalf("decide: %+v, %v", res, err)
	}
	if a := res.Answers[0]; a.Answer != "yes" || a.Probs[0] != 0.96 || a.Confidence != 0.96 || res.Model != "local" {
		t.Fatalf("answer = %+v, model %s", a, res.Model)
	}
	res, err = c.Decide(ctx, api.DecideRequest{State: "Host: a paste site", Questions: []api.Question{q}})
	if err != nil || res.Answers[0].Answer != "no" || res.Answers[0].Probs[0] != 0.02 || res.Answers[0].Confidence != 0.98 {
		t.Fatalf("no: %+v, %v", res, err)
	}
	// A head answers only yes or no questions, and an answer that is not
	// there is an error, not a "no".
	if _, err := c.Decide(ctx, api.DecideRequest{State: "x", Questions: []api.Question{{Id: "k", Type: "choice", Question: "?", Choices: []string{"a", "b"}}}}); rpc.CodeOf(err) != 400 {
		t.Fatalf("choice: %v", err)
	}
	q.Id = "lost"
	if _, err := c.Decide(ctx, api.DecideRequest{State: "x", Questions: []api.Question{q}}); err == nil || rpc.CodeOf(err) == 400 {
		t.Fatalf("lost answer: %v", err)
	}
}

func withVariants(c *Config) {
	c.Engine.Description = "base"
	c.Engine.Models = map[string]Variant{"local-notetaker": {Lora: []LoraScale{{Id: 0, Scale: 1}}, Description: "Tuned for the note taker."}}
}

func TestVariants(t *testing.T) {
	e := setup(t, 30*time.Second, withVariants)
	tok := e.tok(Scope)
	post := func(body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", e.srv.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, out := post(`{"model":"local-notetaker","messages":[]}`)
	if code != 200 || out["model"] != "local-notetaker" {
		t.Fatalf("variant: %d %v", code, out)
	}
	echo, _ := out["echo"].(string)
	if !strings.Contains(echo, `"model":"local"`) || !strings.Contains(echo, `"lora":[{"id":0,"scale":1}]`) {
		t.Fatalf("variant request to the engine: %s", echo)
	}
	code, out = post(`{"model":"local","messages":[]}`)
	if echo, _ := out["echo"].(string); code != 200 || out["model"] != "local" || strings.Contains(echo, "lora") {
		t.Fatalf("base: %d %v", code, out)
	}
	if code, _ := post(`{"model":"local","lora":[{"id":0,"scale":1}]}`); code != 400 {
		t.Fatalf("a lora field: %d", code)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var ml struct {
		Data []struct{ Id, Description string }
	}
	json.NewDecoder(resp.Body).Decode(&ml)
	resp.Body.Close()
	if len(ml.Data) != 2 || ml.Data[1].Id != "local-notetaker" || ml.Data[1].Description == "" {
		t.Fatalf("models: %+v", ml)
	}
}

func TestDecide(t *testing.T) {
	e := setup(t, 30*time.Second, withVariants)
	s := &rpc.Server{Verifier: e.h.ver}
	s.Register("InferenceService", api.InferenceServiceOps(e.h))
	hs := httptest.NewServer(rpc.Mux(s, nil))
	t.Cleanup(hs.Close)
	c := api.NewInferenceClient(rpc.NewClient(hs.URL, rpc.StaticToken(e.tok(Scope))))
	ctx := context.Background()
	two := 2.0
	qs := []api.Question{
		{Id: "record", Type: "noul", Question: "Anything to keep?"},
		{Id: "about", Type: "choice", Question: "About whom?", Choices: []string{"me", "another person", "a pet"}, Temperature: &two},
		{Id: "count", Type: "score", Question: "How many?", Max: ptr(uint64(4))},
	}
	res, err := c.Decide(ctx, api.DecideRequest{State: "Uma lives in Louisville.", Questions: qs})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "local" || len(res.Answers) != 3 {
		t.Fatalf("decide: %+v", res)
	}
	r, a, n := res.Answers[0], res.Answers[1], res.Answers[2]
	if r.Answer != "no" || math.Abs(r.Probs[0]+r.Probs[1]-1) > 1e-9 || r.Probs[1] < 0.9 {
		t.Errorf("noul: %+v", r)
	}
	// T=2 makes the answer less sure; C is not in the top tokens: lowest-1.
	if a.Answer != "another person" || a.Confidence > 0.8 || a.Probs[2] >= a.Probs[0] {
		t.Errorf("choice: %+v", a)
	}
	// Labels 0..4 are not in the top tokens: all at the floor, equal.
	if n.Expected == nil || math.Abs(*n.Expected-2) > 1e-9 {
		t.Errorf("score: %+v", n)
	}
	res, err = c.Decide(ctx, api.DecideRequest{State: "x", Questions: qs[:1], Model: ptr("local-notetaker")})
	if err != nil || res.Model != "local-notetaker" || res.Answers[0].Answer != "yes" {
		t.Fatalf("variant: %+v %v", res, err)
	}
	for _, bad := range []api.DecideRequest{
		{State: "x"},
		{State: "x", Questions: []api.Question{{Id: "q", Type: "choice", Question: "?", Choices: []string{"one"}}}},
		{State: "x", Questions: []api.Question{{Id: "q", Type: "vote", Question: "?"}}},
		{State: "x", Questions: qs[:1], Model: ptr("nope")},
	} {
		if _, err := c.Decide(ctx, bad); rpc.CodeOf(err) != 400 {
			t.Errorf("bad %+v: %v", bad, err)
		}
	}
	nope := api.NewInferenceClient(rpc.NewClient(hs.URL, rpc.StaticToken(e.tok("other"))))
	if _, err := nope.Decide(ctx, api.DecideRequest{State: "x", Questions: qs[:1]}); rpc.CodeOf(err) != 403 {
		t.Errorf("no scope: %v", err)
	}
}
