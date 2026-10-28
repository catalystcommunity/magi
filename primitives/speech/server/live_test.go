package server

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/speech/api"
)

// Clips in testdata/ simulate a user who speaks to Magi.
var clips = map[string]string{
	"hello.opus":    "Hello Magi, can you hear me?",
	"calendar.opus": "What is on my calendar today?",
	"remember.opus": "Remember that my work calendar is in the company Google account and my family calendar is in the household Nextcloud.",
	"readme.opus":   "Please add a health endpoint summary to the Magi README.",
}

// spoken replaces text for synthesis where the voice misreads the written form.
var spoken = map[string]string{"readme.opus": "Please add a health endpoint summary to the Magi readme."}

const clipVoice = "am_michael"

func words(s string) []string {
	return regexp.MustCompile(`[a-z0-9']+`).FindAllString(strings.ToLower(s), -1)
}

// overlap is the fraction of the words of want that occur in got.
func overlap(want, got string) float64 {
	seen := map[string]bool{}
	for _, w := range words(got) {
		seen[w] = true
	}
	w, n := words(want), 0
	for _, x := range w {
		if seen[x] {
			n++
		}
	}
	return float64(n) / float64(len(w))
}

func liveService(t *testing.T) *Service {
	if os.Getenv("MAGI_TEST_SPEECH") != "1" {
		t.Skip("set MAGI_TEST_SPEECH=1 and MAGI_SPEECH_{STT_URL,TTS_URL,ENGINE_TOKEN_FILE}")
	}
	b, err := os.ReadFile(os.Getenv("MAGI_SPEECH_ENGINE_TOKEN_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(string(b))
	return &Service{STT: NewEngine(os.Getenv("MAGI_SPEECH_STT_URL"), tok), TTS: NewEngine(os.Getenv("MAGI_SPEECH_TTS_URL"), tok)}
}

// TestLiveRoundTrip synthesizes speech with the real engines, then
// transcribes it, through the rpc transport.
func TestLiveRoundTrip(t *testing.T) {
	c := harness(t, liveService(t))
	ctx := context.Background()
	v, err := c.ListVoices(ctx, api.ListVoicesRequest{})
	if err != nil || len(v.Voices) < 8 {
		t.Fatalf("voices = %d, %v", len(v.Voices), err)
	}
	const text = "The quick brown fox jumps over the lazy dog near the river bank."
	start := time.Now()
	a, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: text, Voice: ptr(clipVoice)})
	if err != nil {
		t.Fatal(err)
	}
	synth := time.Since(start)
	start = time.Now()
	tr, err := c.Transcribe(ctx, api.TranscribeRequest{Audio: a.Body, MediaType: a.MediaType})
	if err != nil {
		t.Fatal(err)
	}
	stt := time.Since(start)
	t.Logf("synthesize: audio %d ms, engine %d ms, call %d ms, %d bytes", a.AudioMs, a.ElapsedMs, synth.Milliseconds(), len(a.Body))
	t.Logf("transcribe: audio %d ms, engine %d ms, call %d ms: %q", tr.AudioMs, tr.ElapsedMs, stt.Milliseconds(), tr.Text)
	if o := overlap(text, tr.Text); o < 0.8 {
		t.Fatalf("word overlap %.2f: %q", o, tr.Text)
	}
}

// TestLiveClips transcribes the testdata clips. With
// MAGI_SPEECH_WRITE_TESTDATA=1 it first makes them again (Ogg Opus with ffmpeg).
func TestLiveClips(t *testing.T) {
	c := harness(t, liveService(t))
	ctx := context.Background()
	for name, text := range clips {
		path := filepath.Join("..", "testdata", name)
		if os.Getenv("MAGI_SPEECH_WRITE_TESTDATA") == "1" {
			say := text
			if s, ok := spoken[name]; ok {
				say = s
			}
			a, err := c.Synthesize(ctx, api.SynthesizeRequest{Text: say, Voice: ptr(clipVoice)})
			if err != nil {
				t.Fatal(err)
			}
			body := a.Body
			cmd := exec.Command("ffmpeg", "-loglevel", "error", "-i", "pipe:0", "-ar", "16000", "-ac", "1", "-c:a", "libopus", "-b:a", "24k",
				"-application", "voip", "-map_metadata", "-1", "-fflags", "+bitexact", "-f", "ogg", "pipe:1")
			cmd.Stdin = bytes.NewReader(body)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("ffmpeg: %v", err)
			}
			body = out
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		audio, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		tr, err := c.Transcribe(ctx, api.TranscribeRequest{Audio: audio, MediaType: "audio/ogg"})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes, audio %d ms, engine %d ms: %q", name, len(audio), tr.AudioMs, tr.ElapsedMs, tr.Text)
		if o := overlap(text, tr.Text); o < 0.8 {
			t.Errorf("%s: word overlap %.2f", name, o)
		}
	}
}
