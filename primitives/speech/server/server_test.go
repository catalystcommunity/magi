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
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Voice == "" {
			in.Voice = "af_heart"
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-Audio-Ms", "1500")
		w.Header().Set("X-Elapsed-Ms", "120")
		w.Header().Set("X-Voice", in.Voice)
		w.Header().Set("X-Engine", "fake-tts")
		w.Write([]byte("RIFF" + in.Text))
	}))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
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
