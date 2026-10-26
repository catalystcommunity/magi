package dialogue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/catalystcommunity/magi/conversation"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/workflow"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

var testActor = w.ApplicationRef{Identity: w.IdentityRef{Domain: "local.opal.test", AccountId: "persona"}, ApplicationId: "demo", InstanceId: "test"}
var testOwner = w.ScopeRef{Authority: testActor.Identity, ScopeId: "public"}
var testTime = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

type memoryGraph struct {
	rows   []w.ConversationStored
	before []uint64
}

func (g *memoryGraph) Append(_ context.Context, v w.ConversationAppend) (w.ConversationReceipt, error) {
	h, e := conversation.Header(v.Node)
	if e != nil {
		return w.ConversationReceipt{}, e
	}
	for _, r := range g.rows {
		if r.Receipt.Ref == h.Ref {
			if !bytes.Equal(w.EncodeConversationAppend(r.Entry), w.EncodeConversationAppend(v)) {
				return r.Receipt, conversation.ErrConflict
			}
			return r.Receipt, nil
		}
	}
	receipt := w.ConversationReceipt{Ref: h.Ref, Owner: testOwner, Sequence: uint64(len(g.rows) + 1), AcceptedAt: testTime}
	g.rows = append(g.rows, w.ConversationStored{Entry: v, Receipt: receipt})
	return receipt, nil
}
func (g *memoryGraph) Before(_ context.Context, session string, before uint64) (w.ConversationPage, error) {
	g.before = append(g.before, before)
	out := w.ConversationPage{Records: []w.ConversationStored{}, NextSequence: before}
	for i := len(g.rows) - 1; i >= 0; i-- {
		r := g.rows[i]
		if r.Receipt.Sequence >= before || !slices.Contains(r.Entry.Sessions, session) {
			continue
		}
		if len(out.Records) == 4 {
			out.More = true
			break
		}
		out.Records = append(out.Records, r)
		out.NextSequence = r.Receipt.Sequence
	}
	return out, nil
}
func testAdapter(g *memoryGraph) *Adapter {
	return &Adapter{Graph: g, GraphActor: testActor, GraphOwner: testOwner, Owner: testOwner, Actor: testActor, Store: &workflow.Store{Application: testActor}}
}
func request() Request {
	return Request{InteractionID: "original", ObservedAt: testTime, Text: "Tomorrow works. What is the mass limit?", Destinations: []Destination{{Session: "calendar", Text: "Tomorrow works."}, {Session: "stars", Text: "What is the mass limit?"}}}
}
func TestDefinitionAndRequestBounds(t *testing.T) {
	if v := workflow.ValidateDefinition(Definition(testOwner)); !v.Valid {
		t.Fatalf("definition: %+v", v)
	}
	v := request()
	raw, _ := json.Marshal(v)
	if _, e := Parse(string(raw)); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Request){func(v *Request) { v.Destination = 2 }, func(v *Request) { v.Destinations[1].Session = "calendar" }, func(v *Request) { v.Speak = true; v.Voice = "not-installed" }, func(v *Request) { v.Text = strings.Repeat("a", 1001) }, func(v *Request) { v.SpeechInput = "../other" }, func(v *Request) { v.ObservedAt = v.ObservedAt.Add(time.Nanosecond) }} {
		v = request()
		change(&v)
		raw, _ = json.Marshal(v)
		if _, e := Parse(string(raw)); e == nil {
			t.Fatal("accepted invalid request")
		}
	}
}
func TestOneSourceTwoSessionInputs(t *testing.T) {
	ctx := context.Background()
	g := &memoryGraph{}
	a := testAdapter(g)
	v := request()
	for i, session := range []string{"calendar", "stars"} {
		v.Destination = i
		run := "run-" + session
		result, _, e := a.prepare(ctx, workflow.CallRequest{RunID: run}, v)
		if e != nil {
			t.Fatal(e)
		}
		prompt := result.Values[0].Value.(string)
		if !strings.Contains(prompt, v.Destinations[i].Text) || strings.Contains(prompt, v.Destinations[1-i].Text) {
			t.Fatalf("mixed session prompt: %s", prompt)
		}
	}
	counts := map[string]int{}
	for _, r := range g.rows {
		h, _ := conversation.Header(r.Entry.Node)
		counts[h.Ref.Kind]++
		if input, ok := r.Entry.Node.(w.SessionInput); ok {
			if len(input.Sources) != 1 || input.Sources[0].RecordId != "original" || len(input.Interpretations) != 1 {
				t.Fatal("lost common provenance")
			}
		}
	}
	if counts["interaction"] != 1 || counts["interpretation"] != 1 || counts["session-input"] != 2 || counts["context-snapshot"] != 2 {
		t.Fatalf("record kinds: %+v", counts)
	}
}
func TestRecentContextAndStableBoundary(t *testing.T) {
	ctx := context.Background()
	g := &memoryGraph{}
	a := testAdapter(g)
	for i, text := range []string{"old", strings.Repeat("earlier ", 380), "recent-answer"} {
		n := w.InteractionRecord{Header: a.header("old-"+string(rune('a'+i)), "assistant-output", testTime, []w.RecordRef{}), PointId: "fixture", Channel: "text", Content: literal(text)}
		g.Append(ctx, w.ConversationAppend{Node: n, Sessions: []string{"stars"}})
	}
	v := request()
	v.Destination = 1
	result, _, e := a.prepare(ctx, workflow.CallRequest{RunID: "recent"}, v)
	if e != nil {
		t.Fatal(e)
	}
	prompt := result.Values[0].Value.(string)
	if !strings.Contains(prompt, "recent-answer") || strings.Contains(prompt, "Assistant: old") || !strings.Contains(prompt, "omitted") {
		t.Fatal(prompt)
	}
	if len(g.before) == 0 {
		t.Fatal("no boundary query")
	}
	var inputSeq uint64
	for _, r := range g.rows {
		if r.Receipt.Ref.RecordId == "recent-input" {
			inputSeq = r.Receipt.Sequence
		}
	}
	if g.before[0] != inputSeq {
		t.Fatal("query not bounded by the admitted input")
	}
	for _, r := range g.rows {
		if n, ok := r.Entry.Node.(w.SourceContext); ok && n.Header.Ref.Kind == "model-input" {
			c, e := w.DecodeCapturedContent(n.Observation.Data)
			if e != nil {
				t.Fatal(e)
			}
			raw, e := protocol.DisplayContent(c)
			if e != nil || string(raw) != prompt {
				t.Fatal("projection does not retain exact prompt")
			}
		}
	}
}

type unavailableSpeech struct{}

func (unavailableSpeech) Submit(context.Context, w.SpeechSubmit) (w.SpeechStatus, error) {
	return w.SpeechStatus{}, errors.New("offline")
}
func (unavailableSpeech) Wait(context.Context, w.SpeechRef) (w.SpeechStatus, error) {
	return w.SpeechStatus{}, errors.New("offline")
}
func (unavailableSpeech) Evidence(context.Context, w.SpeechRef) (w.SpeechEvidence, error) {
	return w.SpeechEvidence{}, errors.New("offline")
}
func TestSpeechFailurePreservesText(t *testing.T) {
	g := &memoryGraph{}
	a := testAdapter(g)
	a.Speech = unavailableSpeech{}
	v := request()
	v.Speak = true
	v.Voice = "af_heart"
	result, ref, e := a.present(context.Background(), workflow.CallRequest{RunID: "failure"}, v, "The answer is four.")
	if e != nil {
		t.Fatal(e)
	}
	values := map[string]string{}
	for _, d := range result.Values {
		values[d.Name] = d.Value.(string)
	}
	if values["answer"] != "The answer is four." || values["speech_error"] != "speech_submission_unavailable" || ref.Kind != "assistant-output" || len(g.rows) != 1 {
		t.Fatal("lost text fallback", values)
	}
}
func TestBrowserBoundary(t *testing.T) {
	s := &Web{Host: "127.0.0.1:8097", Token: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		name, host, origin, token, path string
		status                          int
	}{{"page", s.Host, "", "", "/", 200}, {"missing token", s.Host, "", "", "/api/voices", 403}, {"cross origin", s.Host, "https://attacker.invalid", s.Token, "/api/voices", 403}, {"rebound host", "attacker.invalid:8097", "", s.Token, "/", 403}, {"no query token", s.Host, "", s.Token, "/?token=x", 403}, {"unknown", s.Host, "", s.Token, "/api/unknown", 404}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://"+tc.host+tc.path, nil)
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Opal-Demo-Token", tc.token)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d", w.Code)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("cache enabled")
			}
		})
	}
}
