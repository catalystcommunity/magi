package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/speech/api"
)

const engineToken = "test-engine-token-0123456789abcdef"

// fakeEngine serves the engine protocol of engine/server.py.
func fakeEngine(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+engineToken {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"bearer token required"}`))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"engine":"fake"}`))
	})
	mux.HandleFunc("GET /voices", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"voices":[{"id":"af_heart","name":"Heart","language":"en-US","description":"female"},
			{"id":"bm_george","name":"George","language":"en-GB","description":"male"}]}`))
	}))
	mux.HandleFunc("POST /transcribe", auth(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch string(b) {
		case "bad":
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"audio could not be decoded"}`))
		case "crash":
			w.WriteHeader(500)
			w.Write([]byte(`{"error":"inference failed"}`))
		default:
			json.NewEncoder(w).Encode(map[string]any{"text": "heard " + r.Header.Get("Content-Type"),
				"engine": "fake-stt", "audio_ms": len(b), "elapsed_ms": 7})
		}
	}))
	mux.HandleFunc("POST /synthesize", auth(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text, Voice string
			Speed       float64
			Timings     bool
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Voice == "" {
			in.Voice = "af_heart"
		}
		if in.Timings { // each word 100 ms
			var words [][]any
			for i, f := range strings.Fields(in.Text) {
				words = append(words, []any{f, i * 100, i*100 + 90})
			}
			json.NewEncoder(w).Encode(map[string]any{"audio": []byte("RIFF" + in.Text), "media_type": "audio/wav", "words": words,
				"audio_ms": len(words) * 100, "elapsed_ms": 5, "voice": in.Voice, "engine": "fake-tts"})
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-Audio-Ms", "1500")
		w.Header().Set("X-Elapsed-Ms", "120")
		w.Header().Set("X-Voice", in.Voice)
		w.Header().Set("X-Engine", "fake-tts")
		w.Write([]byte("RIFF" + in.Text))
	}))
	mux.HandleFunc("POST /vad", auth(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) == "noise" {
			w.Write([]byte(`{"speech_ms":0,"audio_ms":900,"segments":[],"engine":"fake-turn","elapsed_ms":3}`))
			return
		}
		w.Write([]byte(`{"speech_ms":640,"audio_ms":900,"segments":[[96,736]],"engine":"fake-turn","elapsed_ms":3}`))
	}))
	mux.HandleFunc("POST /turn", auth(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Items []struct{ Context, Text string }
		}
		json.NewDecoder(r.Body).Decode(&in)
		var res []map[string]float64
		for _, it := range in.Items {
			if strings.HasPrefix(it.Text, "wait") {
				res = append(res, map[string]float64{"ack": 0.01, "turn": 0.98, "unclear": 0.01})
			} else {
				res = append(res, map[string]float64{"ack": 0.97, "turn": 0.02, "unclear": 0.01})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": res, "engine": "fake-turn", "elapsed_ms": 2})
	}))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestTurn(t *testing.T) {
	eng := fakeEngine(t)
	ctx := context.Background()
	c := harness(t, &Service{STT: NewEngine(eng.URL, engineToken), TTS: NewEngine(eng.URL, engineToken), Turn: NewEngine(eng.URL, engineToken)})
	d, err := c.DetectSpeech(ctx, api.DetectSpeechRequest{Audio: []byte("RIFF")})
	if err != nil || d.SpeechMs != 640 || len(d.Segments) != 1 || d.Segments[0].EndMs != 736 || d.Engine != "fake-turn" {
		t.Fatalf("detect speech = %+v, %v", d, err)
	}
	d, err = c.DetectSpeech(ctx, api.DetectSpeechRequest{Audio: []byte("noise"), MediaType: ptr("audio/webm")})
	if err != nil || d.SpeechMs != 0 || d.Segments == nil || len(d.Segments) != 0 {
		t.Fatalf("detect noise = %+v, %v", d, err)
	}
	r, err := c.ClassifyTurn(ctx, api.ClassifyTurnRequest{Items: []api.TurnText{
		{Context: "the oven should be at three fifty", Text: "mm hmm"}, {Context: "", Text: "wait what"}}})
	if err != nil || len(r.Results) != 2 || r.Results[0].Ack < 0.9 || r.Results[1].Turn < 0.9 {
		t.Fatalf("classify turn = %+v, %v", r, err)
	}
	for name, req := range map[string]api.ClassifyTurnRequest{
		"no items":   {},
		"empty text": {Items: []api.TurnText{{Context: "x", Text: " "}}},
		"long":       {Items: []api.TurnText{{Text: strings.Repeat("a", MaxTurnText+1)}}},
	} {
		if _, err := c.ClassifyTurn(ctx, req); rpc.CodeOf(err) != 400 {
			t.Errorf("%s: err = %v, want 400", name, err)
		}
	}
	if _, err := c.DetectSpeech(ctx, api.DetectSpeechRequest{Audio: []byte("x"), MediaType: ptr("text/plain")}); rpc.CodeOf(err) != 400 {
		t.Errorf("text media type: %v", err)
	}
	// Without the engine, the operations are unavailable.
	c = harness(t, &Service{STT: NewEngine(eng.URL, engineToken), TTS: NewEngine(eng.URL, engineToken)})
	if _, err := c.ClassifyTurn(ctx, api.ClassifyTurnRequest{Items: []api.TurnText{{Text: "okay"}}}); rpc.CodeOf(err) != 503 {
		t.Errorf("no turn engine: %v", err)
	}
}

// harness starts SpeechService behind the real rpc server and returns a
// generated client with a valid user token.
func harness(t *testing.T, svc *Service) *api.SpeechClient {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok, err := iss.Issue(authn.Principal{Subject: "user:ada@example.local", Kind: authn.KindUser}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s := &rpc.Server{Verifier: ver, MaxBody: MaxRequest}
	Register(s, svc)
	hs := httptest.NewServer(rpc.Mux(s, func() error { return svc.Ready(context.Background()) }))
	t.Cleanup(hs.Close)
	return api.NewSpeechClient(rpc.NewClient(hs.URL, rpc.StaticToken(tok)))
}

func ptr[T any](v T) *T { return &v }

func TestOperations(t *testing.T) {
	eng := fakeEngine(t)
	svc := &Service{STT: NewEngine(eng.URL, engineToken), TTS: NewEngine(eng.URL, engineToken)}
	c := harness(t, svc)
	ctx := context.Background()

	tr, err := c.Transcribe(ctx, api.TranscribeRequest{Audio: []byte("abc"), MediaType: "audio/webm;codecs=opus"})
	if err != nil || tr.Text != "heard audio/webm;codecs=opus" || tr.Engine != "fake-stt" || tr.AudioMs != 3 || tr.ElapsedMs != 7 {
		t.Fatalf("transcribe = %+v, %v", tr, err)
	}
	a, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: " hi ", Voice: ptr("bm_george"), Speed: ptr(1.2)})
	if err != nil || string(a.Body) != "RIFFhi" || a.MediaType != "audio/wav" || a.Voice != "bm_george" ||
		a.Engine != "fake-tts" || a.AudioMs != 1500 || a.ElapsedMs != 120 {
		t.Fatalf("synthesize = %+v, %v", a, err)
	}
	// With timings, the word places are in the request text, not in the
	// spoken text ("4-6" is "4 to 6").
	a, err = c.Synthesize(ctx, api.SynthesizeRequest{Text: "Wait 4-6 **minutes**.", Timings: ptr(true)})
	if err != nil || a.Spoken != "Wait 4 to 6 minutes." || string(a.Body) != "RIFFWait 4 to 6 minutes." || len(a.Words) != 5 ||
		a.Words[4].Word != "minutes." || a.Words[4].Start != 11 || a.Words[4].End != 18 || a.Words[4].StartMs != 400 || a.Words[3].Start != 7 {
		t.Fatalf("synthesize with timings = %+v, %v", a, err)
	}
	v, err := c.ListVoices(ctx, api.ListVoicesRequest{})
	if err != nil || len(v.Voices) != 2 || v.Voices[0].Id != "af_heart" || *v.Voices[1].Description != "male" {
		t.Fatalf("list voices = %+v, %v", v, err)
	}
	v, err = c.ListVoices(ctx, api.ListVoicesRequest{Language: ptr("en-gb")})
	if err != nil || len(v.Voices) != 1 || v.Voices[0].Id != "bm_george" {
		t.Fatalf("list en-gb = %+v, %v", v, err)
	}
	v, _ = c.ListVoices(ctx, api.ListVoicesRequest{Language: ptr("en")})
	if len(v.Voices) != 2 {
		t.Fatalf("list en = %d voices", len(v.Voices))
	}
	if err := svc.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestErrors(t *testing.T) {
	eng := fakeEngine(t)
	ctx := context.Background()
	c := harness(t, &Service{STT: NewEngine(eng.URL, engineToken), TTS: NewEngine(eng.URL, engineToken)})
	cases := []struct {
		name string
		call func() error
		code int
	}{
		{"no audio", func() error { _, err := c.Transcribe(ctx, api.TranscribeRequest{MediaType: "audio/wav"}); return err }, 400},
		{"big audio", func() error {
			_, err := c.Transcribe(ctx, api.TranscribeRequest{Audio: make([]byte, MaxAudio+1), MediaType: "audio/wav"})
			return err
		}, 400},
		{"not audio", func() error {
			_, err := c.Transcribe(ctx, api.TranscribeRequest{Audio: []byte("x"), MediaType: "text/plain"})
			return err
		}, 400},
		{"undecodable", func() error {
			_, err := c.Transcribe(ctx, api.TranscribeRequest{Audio: []byte("bad"), MediaType: "audio/wav"})
			return err
		}, 400},
		{"engine crash", func() error {
			_, err := c.Transcribe(ctx, api.TranscribeRequest{Audio: []byte("crash"), MediaType: "audio/wav"})
			return err
		}, 503},
		{"empty text", func() error { _, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: "  "}); return err }, 400},
		{"long text", func() error {
			_, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: strings.Repeat("é", MaxText+1)})
			return err
		}, 400},
		{"format", func() error {
			_, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: "a", Format: ptr("mp3")})
			return err
		}, 400},
		{"speed", func() error {
			_, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: "a", Speed: ptr(5.0)})
			return err
		}, 400},
	}
	for _, tc := range cases {
		if code := rpc.CodeOf(tc.call()); code != tc.code {
			t.Errorf("%s: code %d, want %d", tc.name, code, tc.code)
		}
	}
	if _, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: strings.Repeat("é", MaxText)}); err != nil {
		t.Errorf("text at limit: %v", err)
	}
}

func TestEngineDown(t *testing.T) {
	eng := fakeEngine(t)
	url := eng.URL
	eng.Close()
	svc := &Service{STT: NewEngine(url, engineToken), TTS: NewEngine(url, engineToken)}
	c := harness(t, svc)
	ctx := context.Background()
	_, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: "hello"})
	if rpc.CodeOf(err) != 503 || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("synthesize with engine down: %v", err)
	}
	if _, err := c.ListVoices(ctx, api.ListVoicesRequest{}); rpc.CodeOf(err) != 503 {
		t.Fatalf("list voices with engine down: %v", err)
	}
	if svc.Ready(ctx) == nil {
		t.Fatal("ready with engine down")
	}
	// A wrong engine token is an engine failure, not a caller error.
	eng2 := fakeEngine(t)
	c = harness(t, &Service{STT: NewEngine(eng2.URL, "wrong"), TTS: NewEngine(eng2.URL, "wrong")})
	if _, err := c.ListVoices(ctx, api.ListVoicesRequest{}); rpc.CodeOf(err) != 503 {
		t.Fatalf("wrong engine token: %v", err)
	}
	// Unconfigured engines are unavailable.
	c = harness(t, &Service{})
	if _, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: "x"}); rpc.CodeOf(err) != 503 {
		t.Fatalf("unconfigured: %v", err)
	}
}

func TestNeedsToken(t *testing.T) {
	eng := fakeEngine(t)
	svc := &Service{STT: NewEngine(eng.URL, engineToken), TTS: NewEngine(eng.URL, engineToken)}
	s := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test"}}
	Register(s, svc)
	hs := httptest.NewServer(rpc.Mux(s, nil))
	defer hs.Close()
	_, err := api.NewSpeechClient(rpc.NewClient(hs.URL, nil)).ListVoices(context.Background(), api.ListVoicesRequest{})
	if rpc.CodeOf(err) != 401 {
		t.Fatalf("anonymous call: %v", err)
	}
}
