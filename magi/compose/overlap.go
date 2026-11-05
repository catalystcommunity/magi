package compose

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
)

// check-overlap (docs/speech-interaction.md, 2.3.1, step 2): the person
// speaks while a persona speaks. Magi checks that the audio is speech
// (Silero VAD), transcribes it, and classifies the words with the last
// words that the persona said (a small cross-encoder: ack, turn, unclear).
// The classifier decides on the meaning, not on a list of words.

const (
	// minSpeechMs: less speech than this is not a person who speaks.
	minSpeechMs = 150
	// overlapContextWords is how many words before the spoken cursor the
	// classifier gets.
	overlapContextWords = 20
	// longOverlapWords: this many words are a turn, unless the classifier
	// is very sure that they are an acknowledgment.
	longOverlapWords = 8
	annOverlap       = "overlap"
)

// Thresholds of the classifier (from the held-out tests of the training,
// primitives/speech/engine/turn/). A wrong "continue" ignores a real
// interruption, so it needs high confidence; the model scores at most
// about 0.94 (label smoothing). A wrong "stop" only stops the persona.
var (
	ackThreshold  = 0.85
	turnThreshold = 0.50
)

func (m *Service) CheckOverlap(ctx context.Context, req magiapi.CheckOverlapRequest) (magiapi.CheckOverlapResponse, error) {
	start := time.Now()
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.CheckOverlapResponse{}, err
	}
	s, uctx := m.S, AsCaller(ctx)
	if len(req.Audio) == 0 {
		return magiapi.CheckOverlapResponse{}, rpc.Invalid("audio is required")
	}
	if len(req.Audio) > 4<<20 {
		return magiapi.CheckOverlapResponse{}, rpc.Invalid("audio is larger than 4 MiB; send at most the first seconds of the overlap")
	}
	// Reading the message as the caller is the access check.
	u, err := s.Conversation.GetUtterance(uctx, conversationapi.GetUtteranceRequest{Id: req.Message})
	if err != nil {
		return magiapi.CheckOverlapResponse{}, err
	}
	mt := deref(req.AudioType)
	if mt == "" {
		mt = "audio/wav"
	}
	var (
		wg   sync.WaitGroup
		vad  speechapi.DetectSpeechResponse
		tr   speechapi.Transcript
		verr error
		terr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		vad, verr = s.Speech.DetectSpeech(uctx, speechapi.DetectSpeechRequest{Audio: req.Audio, MediaType: &mt})
	}()
	go func() {
		defer wg.Done()
		tr, terr = s.Speech.Transcribe(uctx, speechapi.TranscribeRequest{Audio: req.Audio, MediaType: mt})
	}()
	wg.Wait()
	if verr != nil {
		return magiapi.CheckOverlapResponse{}, verr
	}
	final := deref(req.Final)
	out := magiapi.CheckOverlapResponse{SpeechMs: vad.SpeechMs}
	defer func() { out.ElapsedMs = uint64(time.Since(start).Milliseconds()) }()
	if vad.SpeechMs < minSpeechMs {
		// Noise, a cough, the television without voices: the persona goes on.
		out.Decision = "continue"
		if !final {
			out.Decision = "wait"
		}
		s.noteOverlap(ctx, caller.Subject, u.Id, out, final)
		return out, nil
	}
	if terr != nil {
		return magiapi.CheckOverlapResponse{}, terr
	}
	words := strings.TrimSpace(tr.Text)
	out.Words = words
	if words == "" {
		out.Decision = "wait"
		if final {
			out.Decision = "continue"
		}
		s.noteOverlap(ctx, caller.Subject, u.Id, out, final)
		return out, nil
	}
	if isEcho(words, u.Text, int(deref(req.HeardUntil))) {
		// The microphone heard the persona: the echo canceller missed it.
		out.Decision = "continue"
		s.noteOverlap(ctx, caller.Subject, u.Id, out, final)
		return out, nil
	}
	ctxText := lastWords(u.Text, int(deref(req.HeardUntil)), overlapContextWords)
	cls, err := s.Speech.ClassifyTurn(uctx, speechapi.ClassifyTurnRequest{Items: []speechapi.TurnText{{Context: ctxText, Text: strings.ToLower(words)}}})
	if err != nil || len(cls.Results) != 1 {
		if err == nil {
			err = rpc.Unavailable("classify-turn returned no result")
		}
		return magiapi.CheckOverlapResponse{}, err
	}
	r := cls.Results[0]
	out.Ack, out.Turn = r.Ack, r.Turn
	out.Decision = decideOverlap(r, len(strings.Fields(words)), final)
	s.noteOverlap(ctx, caller.Subject, u.Id, out, final)
	return out, nil
}

// decideOverlap is the decision from the classes of the words.
func decideOverlap(r speechapi.TurnClass, nWords int, final bool) string {
	switch {
	case r.Ack >= ackThreshold:
		return "continue"
	case r.Turn >= turnThreshold, nWords >= longOverlapWords:
		return "stop"
	case !final:
		return "wait"
	case r.Turn >= r.Ack:
		// The person stopped speaking and it is not clear: they take the
		// turn. A wrong stop costs less than an ignored interruption.
		return "stop"
	}
	return "continue"
}

// noteOverlap records a decided overlap on the persona message, for the
// conversation graph: backchannels and interruptions are evidence of how
// the person listened.
func (s *System) noteOverlap(ctx context.Context, person, message string, out magiapi.CheckOverlapResponse, final bool) {
	if out.Decision == "wait" {
		return
	}
	b, _ := json.Marshal(map[string]any{"person": person, "decision": out.Decision, "words": out.Words, "ack": out.Ack,
		"turn": out.Turn, "speech_ms": out.SpeechMs, "final": final})
	if _, err := s.Conversation.Annotate(AsCaller(ctx), conversationapi.AnnotateRequest{Utterance: message, Kind: annOverlap, Body: string(b)}); err != nil {
		s.Log.Warn("overlap note", "message", message, "err", err)
	}
}

// lastWords is the last n words of text before the code point offset at
// (all of text when at is 0).
func lastWords(text string, at, n int) string {
	rs := []rune(text)
	if at <= 0 || at > len(rs) {
		at = len(rs)
	}
	f := strings.FieldsFunc(string(rs[:at]), unicode.IsSpace)
	if len(f) > n {
		f = f[len(f)-n:]
	}
	return strings.Join(f, " ")
}

// isEcho reports whether the words are the persona's own words near the
// spoken cursor: at least two words, and most of them in the 30 words
// before the cursor or the 10 words after it.
func isEcho(words, text string, at int) bool {
	key := func(s string) []string {
		var out []string
		for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' }) {
			out = append(out, f)
		}
		return out
	}
	heard := key(words)
	if len(heard) < 2 {
		return false
	}
	rs := []rune(text)
	if at <= 0 || at > len(rs) {
		at = len(rs)
	}
	before, after := key(string(rs[:at])), key(string(rs[at:]))
	window := map[string]bool{}
	for _, w := range before[max(0, len(before)-30):] {
		window[w] = true
	}
	for _, w := range after[:min(len(after), 10)] {
		window[w] = true
	}
	in := 0
	for _, w := range heard {
		if window[w] {
			in++
		}
	}
	return float64(in) >= 0.8*float64(len(heard))
}
