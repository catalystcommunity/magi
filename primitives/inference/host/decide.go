package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
)

// The prompts of evals/decide/decide.py: the temperatures of a model are
// fit with the same text.
const decideSystem = "You are a decision model. Read the state. Then answer the question with the label of the one option " +
	"that is correct. Answer with the label only."

const (
	maxQuestions = 64
	maxState     = 64 << 10
	nProbs       = 200
)

var letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// labels gives the one-token labels and the option texts of q.
func labels(q api.Question) ([]string, []string, error) {
	switch q.Type {
	case "noul":
		return []string{"A", "B"}, []string{"yes", "no"}, nil
	case "choice":
		n := len(q.Choices)
		if n < 2 || n > 26 {
			return nil, nil, fmt.Errorf("question %s: a choice needs 2 to 26 choices", q.Id)
		}
		out := make([]string, n)
		for i := range out {
			out[i] = letters[i : i+1]
		}
		return out, q.Choices, nil
	case "score":
		mx := uint64(4)
		if q.Max != nil {
			mx = *q.Max
		}
		if mx < 1 || mx > 9 {
			return nil, nil, fmt.Errorf("question %s: max must be 1 to 9", q.Id)
		}
		var ls, ts []string
		for i := uint64(0); i <= mx; i++ {
			l := strconv.FormatUint(i, 10)
			ls = append(ls, l)
			ts = append(ts, q.Scale[l])
		}
		return ls, ts, nil
	}
	return nil, nil, fmt.Errorf("question %s: type must be noul, choice, or score", q.Id)
}

func userPrompt(state string, q api.Question, ls, ts []string) string {
	var lines []string
	for i, l := range ls {
		if ts[i] != "" {
			lines = append(lines, l+": "+ts[i])
		} else {
			lines = append(lines, l)
		}
	}
	kind := "one letter"
	if q.Type == "score" {
		kind = "one number"
	}
	return fmt.Sprintf("State:\n<<<\n%s\n>>>\n\nQuestion: %s\nOptions:\n%s\n\nAnswer with %s.", state, q.Question, strings.Join(lines, "\n"), kind)
}

// engineClient calls the llama-server of the engine.
type engineClient struct {
	base string
	http *http.Client
	mu   sync.Mutex
	tok  map[string]int
}

func (c *engineClient) post(ctx context.Context, path string, body any, out any) error {
	b, _ := json.Marshal(body)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("engine %s: status %d: %.200s", path, resp.StatusCode, rb)
	}
	return json.Unmarshal(rb, out)
}

// token is the one token of a label.
func (c *engineClient) token(ctx context.Context, label string) (int, error) {
	c.mu.Lock()
	id, ok := c.tok[label]
	c.mu.Unlock()
	if ok {
		return id, nil
	}
	var out struct{ Tokens []int }
	if err := c.post(ctx, "/tokenize", map[string]any{"content": label, "add_special": false}, &out); err != nil {
		return 0, err
	}
	if len(out.Tokens) != 1 {
		return 0, fmt.Errorf("label %q is %d tokens for this model", label, len(out.Tokens))
	}
	c.mu.Lock()
	if c.tok == nil {
		c.tok = map[string]int{}
	}
	c.tok[label] = out.Tokens[0]
	c.mu.Unlock()
	return out.Tokens[0], nil
}

// logprobs gives the log-probabilities of the labels as the first token
// of the answer. A label that is not in the top nProbs gets the lowest
// value minus 1.
func (c *engineClient) logprobs(ctx context.Context, prompt string, ids []int, lora []LoraScale) ([]float64, error) {
	body := map[string]any{"prompt": prompt, "n_predict": 1, "n_probs": nProbs, "temperature": 0, "cache_prompt": true}
	if lora != nil {
		body["lora"] = lora
	}
	var out struct {
		Probs []struct {
			Top []struct {
				Id      int     `json:"id"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
		} `json:"completion_probabilities"`
	}
	if err := c.post(ctx, "/completion", body, &out); err != nil {
		return nil, err
	}
	if len(out.Probs) == 0 || len(out.Probs[0].Top) == 0 {
		return nil, errors.New("the engine gave no token probabilities")
	}
	got := map[int]float64{}
	floor := math.Inf(1)
	for _, t := range out.Probs[0].Top {
		got[t.Id] = t.Logprob
		floor = math.Min(floor, t.Logprob)
	}
	lp := make([]float64, len(ids))
	for i, id := range ids {
		v, ok := got[id]
		if !ok {
			v = floor - 1
		}
		lp[i] = v
	}
	return lp, nil
}

func softmax(xs []float64, t float64) []float64 {
	m := math.Inf(-1)
	for _, x := range xs {
		m = math.Max(m, x/t)
	}
	out := make([]float64, len(xs))
	var sum float64
	for i, x := range xs {
		out[i] = math.Exp(x/t - m)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}

func (h *Host) engine() *engineClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ec == nil {
		h.ec = &engineClient{base: fmt.Sprintf("http://127.0.0.1:%d", h.cfg.Engine.Port), http: &http.Client{Timeout: 2 * time.Minute}}
	}
	return h.ec
}

// Decide answers the questions about one state (contracts/inference.csil).
// The questions go one after the other, so the engine reuses the prompt of
// the state (llama-server prompt cache; Gemma 4 needs --swa-full).
func (h *Host) Decide(ctx context.Context, req api.DecideRequest) (api.DecideResponse, error) {
	if err := h.allowed(ctx); err != nil {
		return api.DecideResponse{}, err
	}
	if len(req.Questions) == 0 || len(req.Questions) > maxQuestions {
		return api.DecideResponse{}, rpc.Invalid("1 to %d questions", maxQuestions)
	}
	if len(req.State) > maxState {
		return api.DecideResponse{}, rpc.Invalid("the state is more than %d bytes", maxState)
	}
	name := h.cfg.Engine.Model
	var lora []LoraScale
	if req.Model != nil && *req.Model != "" && *req.Model != name {
		v, ok := h.cfg.Engine.Models[*req.Model]
		if !ok {
			return api.DecideResponse{}, rpc.Invalid("no model %s on this host", *req.Model)
		}
		name, lora = *req.Model, v.Lora
	}
	if h.cfg.Engine.Decide == "head" {
		return h.decideHead(ctx, req, name)
	}
	type prepared struct {
		q   api.Question
		ls  []string
		ts  []string
		ids []int
	}
	if err := h.acquire(ctx); err != nil {
		var b *refusal
		if errors.As(err, &b) {
			return api.DecideResponse{}, rpc.Unavailable("%s: %s", b.kind, b.msg)
		}
		return api.DecideResponse{}, err
	}
	defer h.release()
	ec := h.engine()
	var ps []prepared
	for _, q := range req.Questions {
		ls, ts, err := labels(q)
		if err != nil {
			return api.DecideResponse{}, rpc.Invalid("%s", err.Error())
		}
		if q.Temperature != nil && *q.Temperature <= 0 {
			return api.DecideResponse{}, rpc.Invalid("question %s: temperature must be more than 0", q.Id)
		}
		ids := make([]int, len(ls))
		for i, l := range ls {
			id, err := ec.token(ctx, l)
			if err != nil {
				return api.DecideResponse{}, err
			}
			ids[i] = id
		}
		ps = append(ps, prepared{q, ls, ts, ids})
	}
	start := time.Now()
	var out []api.Answer
	for _, p := range ps {
		qs := time.Now()
		msgs := []map[string]string{{"role": "system", "content": decideSystem}, {"role": "user", "content": userPrompt(req.State, p.q, p.ls, p.ts)}}
		var tmpl struct{ Prompt string }
		if err := ec.post(ctx, "/apply-template", map[string]any{"messages": msgs}, &tmpl); err != nil {
			return api.DecideResponse{}, err
		}
		lp, err := ec.logprobs(ctx, tmpl.Prompt, p.ids, lora)
		if err != nil {
			return api.DecideResponse{}, err
		}
		t := 1.0
		if p.q.Temperature != nil {
			t = *p.q.Temperature
		}
		pr := softmax(lp, t)
		best := 0
		for i := range pr {
			if pr[i] > pr[best] {
				best = i
			}
		}
		a := api.Answer{Id: p.q.Id, Probs: pr, Confidence: pr[best], Ms: uint64(time.Since(qs).Milliseconds())}
		switch p.q.Type {
		case "noul":
			a.Answer = p.ts[best]
		case "choice":
			a.Answer = p.q.Choices[best]
		default:
			a.Answer = p.ls[best]
			var e float64
			for i, x := range pr {
				e += float64(i) * x
			}
			a.Expected = &e
		}
		out = append(out, a)
	}
	return api.DecideResponse{Answers: out, Model: name, Ms: uint64(time.Since(start).Milliseconds())}, nil
}

// decideHead answers the questions with a decision model: the engine has a
// scoring head and gives the probability of "yes" for each question in one
// call (llama-server /v1/systemone). The question text is the instruction
// that the head was trained with. A head has its calibration from its
// training, so the temperature of a question is not used.
func (h *Host) decideHead(ctx context.Context, req api.DecideRequest, name string) (api.DecideResponse, error) {
	qs := map[string]any{}
	for _, q := range req.Questions {
		if q.Type != "noul" {
			return api.DecideResponse{}, rpc.Invalid("question %s: the decision model of this host answers only yes or no questions", q.Id)
		}
		if _, dup := qs[q.Id]; dup || q.Id == "" {
			return api.DecideResponse{}, rpc.Invalid("each question needs its own id")
		}
		qs[q.Id] = map[string]string{"type": "noul", "instructions": q.Question}
	}
	if err := h.acquire(ctx); err != nil {
		var b *refusal
		if errors.As(err, &b) {
			return api.DecideResponse{}, rpc.Unavailable("%s: %s", b.kind, b.msg)
		}
		return api.DecideResponse{}, err
	}
	defer h.release()
	start := time.Now()
	var res struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := h.engine().post(ctx, "/v1/systemone", map[string]any{"model": h.cfg.Engine.Model, "state": req.State, "questions": qs}, &res); err != nil {
		return api.DecideResponse{}, err
	}
	ms := uint64(time.Since(start).Milliseconds())
	out := make([]api.Answer, 0, len(req.Questions))
	for _, q := range req.Questions {
		a, ok := res.Answers[q.Id]
		if !ok || a.Noul == nil || *a.Noul < 0 || *a.Noul > 1 {
			return api.DecideResponse{}, fmt.Errorf("the decision model gave no probability for question %s", q.Id)
		}
		yes := *a.Noul
		ans := api.Answer{Id: q.Id, Probs: []float64{yes, 1 - yes}, Answer: "yes", Confidence: yes, Ms: ms}
		if yes < 0.5 {
			ans.Answer, ans.Confidence = "no", 1-yes
		}
		out = append(out, ans)
	}
	return api.DecideResponse{Answers: out, Model: name, Ms: ms}, nil
}
