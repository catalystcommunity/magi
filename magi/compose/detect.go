package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// The prompt injection detector (docs/prompt-injection.md, 3.4 and plan
// step 9). The injection-detector plugin of the egress proxy sends the text
// of the responses that tasks get (web pages, READMEs, API answers); Magi
// scores it with the detect engine (primitives/speech/engine, --engine
// detect). The detector is triage, not a gate. Measured on 2026-11-04 with
// ProtectAI deberta v3 v2 at 0.98: 11 of 12 attack samples found, 0 of 21
// documentation pages and 5 of 168 conversation messages flagged (the
// messages are about Magi itself, a known weakness). So the default mode
// "flag" passes the response, checks it in the background, and tells the
// person of the task; "review" waits for the score and blocks a finding
// for an admin review.

// Detector is a client of the detect engine.
type Detector struct {
	URL       string
	TokenFile string
	Threshold float64 // a score at or above it is a finding
	Mode      string  // "flag" or "review"
	HTTP      *http.Client

	mu    sync.Mutex
	seen  map[string]time.Time // environment and URL: when the last check began
	slots chan struct{}        // background checks at the same time
}

const (
	detectMaxChars = 32 << 10
	detectRepeat   = time.Hour
	detectSlots    = 2
)

// DetectorFromEnv reads MAGI_DETECTOR_URL, MAGI_DETECTOR_TOKEN_FILE,
// MAGI_DETECTOR_THRESHOLD (0.98), and MAGI_DETECTOR_MODE (flag). It returns
// nil when MAGI_DETECTOR_URL is not set.
func DetectorFromEnv() (*Detector, error) {
	url := os.Getenv("MAGI_DETECTOR_URL")
	if url == "" {
		return nil, nil
	}
	d := &Detector{URL: strings.TrimRight(url, "/"), TokenFile: os.Getenv("MAGI_DETECTOR_TOKEN_FILE"), Threshold: 0.98, Mode: env("MAGI_DETECTOR_MODE", "flag")}
	if t := os.Getenv("MAGI_DETECTOR_THRESHOLD"); t != "" {
		if _, err := fmt.Sscan(t, &d.Threshold); err != nil || d.Threshold <= 0 || d.Threshold > 1 {
			return nil, fmt.Errorf("MAGI_DETECTOR_THRESHOLD %q: must be a number in (0, 1]", t)
		}
	}
	if d.Mode != "flag" && d.Mode != "review" {
		return nil, fmt.Errorf(`MAGI_DETECTOR_MODE %q: must be "flag" or "review"`, d.Mode)
	}
	return d, nil
}

// Score returns the highest score of texts and the engine name.
func (d *Detector) Score(ctx context.Context, texts []string) (float64, string, error) {
	body, _ := json.Marshal(map[string]any{"texts": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL+"/detect", bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.TokenFile != "" {
		tok, err := os.ReadFile(d.TokenFile)
		if err != nil {
			return 0, "", fmt.Errorf("detector token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	}
	hc := d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("detector: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Scores []float64 `json:"scores"`
		Engine string    `json:"engine"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Scores) != len(texts) {
		return 0, "", fmt.Errorf("detector: bad answer")
	}
	best := 0.0
	for _, s := range out.Scores {
		best = max(best, s)
	}
	return best, out.Engine, nil
}

// limitTexts keeps the first detectMaxChars characters of texts.
func limitTexts(texts []string) []string {
	var out []string
	left := detectMaxChars
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" || left <= 0 {
			continue
		}
		if rs := []rune(t); len(rs) > left {
			t = string(rs[:left])
		}
		left -= len([]rune(t))
		out = append(out, t)
	}
	return out
}

// Detect answers the injection-detector plugin.
func (e *Egress) Detect(ctx context.Context, req magiapi.EgressDetectRequest) (magiapi.EgressVerdict, error) {
	if err := egressCaller(ctx); err != nil {
		return magiapi.EgressVerdict{}, err
	}
	d := e.S.Detector
	texts := limitTexts(req.Texts)
	if d == nil || len(texts) == 0 {
		return magiapi.EgressVerdict{Verdict: "pass", Reason: "no detector or no text", By: "detector"}, nil
	}
	// The model calls of Magi services (ServiceUser) are not task input.
	if strings.HasPrefix(req.Flow.Environment, "svc_") {
		return magiapi.EgressVerdict{Verdict: "pass", Reason: "a Magi service", By: "detector"}, nil
	}
	if d.Mode == "review" {
		score, engine, err := d.Score(ctx, texts)
		if err != nil {
			return magiapi.EgressVerdict{Verdict: "review", Reason: "the detector failed: " + err.Error(), By: "detector"}, nil
		}
		if score >= d.Threshold {
			return magiapi.EgressVerdict{Verdict: "review", Reason: fmt.Sprintf("possible prompt injection (score %.3f)", score), By: engine}, nil
		}
		return magiapi.EgressVerdict{Verdict: "pass", Reason: fmt.Sprintf("score %.3f", score), By: engine}, nil
	}
	// flag: pass now, check in the background, once an hour for each
	// environment and URL, with few checks at the same time.
	key := req.Flow.Environment + " " + req.Flow.Url
	d.mu.Lock()
	if d.seen == nil {
		d.seen, d.slots = map[string]time.Time{}, make(chan struct{}, detectSlots)
	}
	last, dup := d.seen[key]
	dup = dup && time.Since(last) < detectRepeat
	if !dup {
		d.seen[key] = time.Now()
		for k, t := range d.seen {
			if time.Since(t) > detectRepeat {
				delete(d.seen, k)
			}
		}
	}
	slots := d.slots
	d.mu.Unlock()
	if dup {
		return magiapi.EgressVerdict{Verdict: "pass", Reason: "checked already", By: "detector"}, nil
	}
	select {
	case slots <- struct{}{}:
	default:
		e.S.Log.Warn("injection detector busy; not checked", "environment", req.Flow.Environment, "url", req.Flow.Url)
		return magiapi.EgressVerdict{Verdict: "pass", Reason: "the detector is busy", By: "detector"}, nil
	}
	flow := req.Flow
	go func() {
		defer func() { <-slots }()
		bctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		score, engine, err := d.Score(bctx, texts)
		if err != nil {
			e.S.Log.Warn("injection detector", "url", flow.Url, "err", err)
			return
		}
		if score < d.Threshold {
			return
		}
		e.S.Log.Warn("possible prompt injection", "environment", flow.Environment, "owner", flow.Owner, "url", flow.Url, "score", score, "engine", engine)
		// One message for each environment and host in an hour: a task
		// that reads many pages of one host gets one warning.
		hk := "host " + flow.Environment + " " + safePlace(flow.Url)
		d.mu.Lock()
		last, told := d.seen[hk]
		told = told && time.Since(last) < detectRepeat
		if !told {
			d.seen[hk] = time.Now()
		}
		d.mu.Unlock()
		if !told {
			e.flagInjection(bctx, flow, score)
		}
	}()
	return magiapi.EgressVerdict{Verdict: "pass", Reason: "checked in the background", By: "detector"}, nil
}

// flagInjection tells the person of the task (in the stream of the task)
// that a response that the task got can carry a prompt injection.
func (e *Egress) flagInjection(ctx context.Context, f magiapi.EgressFlow, score float64) {
	sctx := e.S.AsService(ctx)
	env, err := e.environment(ctx, magiapi.EgressEnvironmentRequest{Environment: f.Environment})
	run := deref(env.Task)
	if err != nil || run == "" {
		return // not a task (for example a Magi service)
	}
	r, err := e.S.Workflow.GetRun(sctx, workflowapi.GetRunRequest{Id: run})
	if err != nil {
		e.S.Log.Warn("injection flag: run", "run", run, "err", err)
		return
	}
	vars := maps.Clone(r.Vars)
	if vars == nil {
		vars = map[string]string{}
	}
	if vars["stream"] == "" {
		// A task from the task page has no stream: the stream of the owner
		// with the default persona.
		st, err := e.S.Conversation.OpenStream(sctx, conversationapi.OpenStreamRequest{Persona: Persona, Person: ptr(r.Owner)})
		if err != nil {
			e.S.Log.Warn("injection flag: stream", "run", run, "err", err)
			return
		}
		vars["stream"], vars["persona"] = st.Id, Persona
	}
	e.S.post(sctx, workflowapi.Assignment{Run: r.Id, Vars: vars}, fmt.Sprintf(
		"The task got text that can be a prompt injection (detector score %.3f), from %s. Check the change with care before you approve it.",
		score, safePlace(f.Url)))
}

// safePlace is the host of a URL only: the persona reads the stream
// later, and the path or the query can carry the injection itself. The full
// URL is in the log of Magi and in the flow records of the proxy.
func safePlace(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "a response"
	}
	return u.Host
}
