package compose

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
)

func TestSpeechSpans(t *testing.T) {
	text := "Sure, I can help with that. The meeting is at 3.5 pm tomorrow, in room 12. Bring the notes!\n\nAnd one more thing: the build passed."
	var got []string
	for _, sp := range speechSpans(text) {
		got = append(got, string([]rune(text)[sp.start:sp.end]))
	}
	want := []string{"Sure, I can help with that.", "The meeting is at 3.5 pm tomorrow, in room 12.", "Bring the notes!",
		"And one more thing: the build passed."}
	if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// The first part ends at the first clause after a few words.
	got = nil
	text = "Yes, it is done, and the change is merged into main now."
	for _, sp := range speechSpans(text) {
		got = append(got, string([]rune(text)[sp.start:sp.end]))
	}
	if len(got) != 2 || got[0] != "Yes, it is done," {
		t.Fatalf("first clause: %q", got)
	}
	// No sentence end: the part ends at a space.
	long := strings.Repeat("word ", 100)
	for _, sp := range speechSpans(long) {
		if p := string([]rune(long)[sp.start:sp.end]); strings.HasPrefix(p, " ") || strings.HasSuffix(p, " ") || strings.Contains(p, "wo rd") || sp.end-sp.start > partMax {
			t.Fatalf("bad part %q", p)
		}
	}
	if len(speechSpans("  \n ")) != 0 {
		t.Fatal("white space has no parts")
	}
}

func wavOf(samples ...int16) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+2*len(samples)))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(24000), uint32(48000), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(2*len(samples)))
	binary.Write(&b, binary.LittleEndian, samples)
	return b.Bytes()
}

func TestJoinWAV(t *testing.T) {
	j, err := joinWAV([][]byte{wavOf(1, 2), wavOf(3)})
	if err != nil || !bytes.Equal(j, wavOf(1, 2, 3)) {
		t.Fatalf("join: %v\n%x\n%x", err, j, wavOf(1, 2, 3))
	}
	if _, err := joinWAV([][]byte{[]byte("nope")}); err == nil {
		t.Fatal("not a WAV")
	}
}

// fakeConversation keeps annotations and the audio of one utterance.
type fakeConversation struct {
	conversationapi.ConversationService
	mu    sync.Mutex
	anns  []conversationapi.Annotation
	audio map[string]string
}

func (f *fakeConversation) Annotate(ctx context.Context, r conversationapi.AnnotateRequest) (conversationapi.Annotation, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return conversationapi.Annotation{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a := conversationapi.Annotation{Id: fmt.Sprint("ann_", len(f.anns)), Utterance: r.Utterance, Kind: r.Kind, Author: p.Subject, Body: r.Body}
	f.anns = append(f.anns, a)
	return a, nil
}

func (f *fakeConversation) ListAnnotations(_ context.Context, r conversationapi.ListAnnotationsRequest) (conversationapi.ListAnnotationsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := conversationapi.ListAnnotationsResponse{Annotations: []conversationapi.Annotation{}}
	for _, a := range f.anns {
		if a.Utterance == r.Utterance {
			out.Annotations = append(out.Annotations, a)
		}
	}
	return out, nil
}

func (f *fakeConversation) SetAudio(_ context.Context, r conversationapi.SetAudioRequest) (conversationapi.Utterance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audio[r.Utterance] = r.Audio
	return conversationapi.Utterance{Id: r.Utterance, Audio: &r.Audio}, nil
}

// fakeSpeech makes one sample for each word, 100 ms each.
type fakeSpeech struct {
	speechapi.SpeechService
	mu    sync.Mutex
	texts []string
	slow  time.Duration
}

func (f *fakeSpeech) Synthesize(_ context.Context, r speechapi.SynthesizeRequest) (speechapi.Audio, error) {
	f.mu.Lock()
	f.texts = append(f.texts, r.Text)
	f.mu.Unlock()
	time.Sleep(f.slow)
	a := speechapi.Audio{MediaType: "audio/wav", Spoken: r.Text, Voice: "af_heart", Engine: "fake", Words: []speechapi.WordTime{}}
	var samples []int16
	at := 0
	for i, w := range strings.Fields(r.Text) {
		start := strings.Index(r.Text[at:], w) + at
		at = start + len(w)
		samples = append(samples, int16(i))
		a.Words = append(a.Words, speechapi.WordTime{Word: w, StartMs: uint64(i * 100), EndMs: uint64(i*100 + 90),
			Start: uint64(len([]rune(r.Text[:start]))), End: uint64(len([]rune(r.Text[:at])))})
	}
	a.Body, a.AudioMs = wavOf(samples...), uint64(100*len(samples))
	return a, nil
}

func TestSpeakParts(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok := func(sub, kind string) string {
		s, err := iss.Issue(authn.Principal{Subject: sub, Kind: kind}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	serve := func(name string, ops map[string]func(context.Context, []byte) ([]byte, error)) string {
		srv := &rpc.Server{Verifier: ver, Log: log}
		srv.Register(name, ops)
		h := httptest.NewServer(rpc.Mux(srv, nil))
		t.Cleanup(h.Close)
		return h.URL
	}
	fc := &fakeConversation{audio: map[string]string{}}
	fs := &fakeSpeech{}
	fa := &fakeArtifacts{body: map[string][]byte{}}
	svc := tok("service:magi", authn.KindService)
	sys := New(Config{Conversation: serve("ConversationService", conversationapi.ConversationServiceOps(fc)),
		Speech: serve("SpeechService", speechapi.SpeechServiceOps(fs)), Artifacts: serve("ArtifactService", artifactsapi.ArtifactServiceOps(fa)),
		ServiceToken: svc}, nil, log)
	srv := &rpc.Server{Verifier: ver, Log: log}
	srv.Register("MagiService", magiapi.MagiServiceOps(&Service{S: sys}))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	defer h.Close()
	user := rpc.WithToken(context.Background(), tok("user:a@example.local", authn.KindUser))
	mc := magiapi.NewMagiClient(rpc.NewClient(h.URL, nil))

	text := "Sure, I can help with that. The meeting is at 3 pm tomorrow, in room twelve."
	u := conversationapi.Utterance{Id: "utt_1", Text: text}
	// A person cannot add a part: listen counts only parts of services.
	if _, err := conversationapi.NewConversationClient(rpc.NewClient(sys.Cfg.Conversation, nil)).Annotate(user,
		conversationapi.AnnotateRequest{Utterance: "utt_1", Kind: annSpeechPart, Body: `{"index":0,"audio":"sha256:evil"}`}); err != nil {
		t.Fatal(err)
	}
	// listen waits for the first part.
	fs.slow = 300 * time.Millisecond
	go sys.speakParts(sys.AsService(context.Background()), "user:a@example.local", u, "")
	began := time.Now()
	r, err := mc.Listen(user, magiapi.ListenRequest{Message: "utt_1", WaitMs: ptr(uint64(5000))})
	if err != nil || len(r.Parts) != 1 || r.Parts[0].Audio == "sha256:evil" || r.Done || time.Since(began) < 200*time.Millisecond {
		t.Fatalf("first part: %+v %v", r, err)
	}
	p := r.Parts[0]
	if string([]rune(text)[p.Start:p.End]) != "Sure, I can help with that." || p.AudioMs != 600 || len(p.Words) != 6 {
		t.Fatalf("part 0: %+v", p)
	}
	for r.Done == false {
		next, err := mc.Listen(user, magiapi.ListenRequest{Message: "utt_1", After: ptr(uint64(len(r.Parts))), WaitMs: ptr(uint64(5000))})
		if err != nil {
			t.Fatal(err)
		}
		r.Parts, r.Done = append(r.Parts, next.Parts...), next.Done
	}
	if len(r.Parts) != 2 || r.Parts[1].Index != 1 {
		t.Fatalf("parts: %+v", r.Parts)
	}
	// Word places are in the message text, not in the part.
	w := r.Parts[1].Words[0]
	if w.Word != "The" || string([]rune(text)[w.Start:w.End]) != "The" {
		t.Fatalf("word of part 1: %+v", w)
	}
	// The message gets the whole audio.
	whole := fa.body[fc.audio["utt_1"]]
	if len(whole) != len(wavOf(make([]int16, 6+10)...)) {
		t.Fatalf("whole audio: %d bytes", len(whole))
	}
	// A stop: no more parts, and no whole audio.
	fs.slow = 0
	if _, err := mc.StopSpeech(user, magiapi.StopSpeechRequest{Message: "utt_2", Reason: ptr("interrupted")}); err != nil {
		t.Fatal(err)
	}
	if err := sys.speakParts(sys.AsService(context.Background()), "user:a@example.local", conversationapi.Utterance{Id: "utt_2", Text: text}, ""); err != nil {
		t.Fatal(err)
	}
	if r, _ := mc.Listen(user, magiapi.ListenRequest{Message: "utt_2"}); !r.Done || !r.Stopped || len(r.Parts) != 0 || fc.audio["utt_2"] != "" {
		t.Fatalf("stopped: %+v %q", r, fc.audio["utt_2"])
	}
	// Delivery reports.
	if _, err := mc.ReportDelivery(user, magiapi.ReportDeliveryRequest{Message: "utt_1", SpokenUntil: ptr(uint64(20)), Reason: ptr("interrupted")}); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.ReportDelivery(user, magiapi.ReportDeliveryRequest{Message: "utt_1", Reason: ptr("bored")}); err == nil {
		t.Fatal("unknown reason")
	}
	last := fc.anns[len(fc.anns)-1]
	if last.Kind != annDelivery || last.Author != "user:a@example.local" || !strings.Contains(last.Body, `"spoken_until":20`) {
		t.Fatalf("delivery: %+v", last)
	}
}
