// Package server implements SpeechService on top of HTTP speech engines.
//
// Speech keeps one kind of record: the household lexicon. Any
// authenticated caller can use the service and read the lexicon. Changes
// to the lexicon need ScopeLexicon.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/speech/api"
)

const (
	// MaxAudio is the largest audio input.
	MaxAudio = 16 << 20
	// MaxText is the largest synthesis input, in characters.
	MaxText = 4000
	// MaxRequest is the rpc server body limit. It leaves room for encoding.
	MaxRequest = MaxAudio + 1<<20
)

// ScopeLexicon lets a caller change the lexicon.
const ScopeLexicon = "speech:lexicon"

// Lexicon stores the lexicon (store.DB).
type Lexicon interface {
	List(ctx context.Context) ([]api.LexiconEntry, error)
	Put(ctx context.Context, e api.LexiconEntry) error
	Delete(ctx context.Context, written string) (bool, error)
}

// Service implements api.SpeechService. STT serves transcribe; TTS serves
// synthesize and list-voices. Without Lexicon, the lexicon is empty and
// cannot change.
type Service struct {
	STT, TTS *Engine
	Lexicon  Lexicon
	Log      *slog.Logger
}

var _ api.SpeechService = (*Service)(nil)

// Register adds the service operations to s.
func Register(s *rpc.Server, svc *Service) {
	ops := map[string]rpc.Op{}
	for name, op := range api.SpeechServiceOps(svc) {
		ops[name] = op
	}
	s.Register("SpeechService", ops)
}

// Ready checks both engines.
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return errors.Join(s.STT.Health(ctx), s.TTS.Health(ctx))
}

func (s *Service) Transcribe(ctx context.Context, req api.TranscribeRequest) (api.Transcript, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.Transcript{}, err
	}
	switch {
	case len(req.Audio) == 0:
		return api.Transcript{}, rpc.Invalid("audio is required")
	case len(req.Audio) > MaxAudio:
		return api.Transcript{}, rpc.Invalid("audio is larger than %d bytes", MaxAudio)
	case !audioType(req.MediaType):
		return api.Transcript{}, rpc.Invalid("media_type %q is not audio", req.MediaType)
	}
	r, err := s.STT.do(ctx, http.MethodPost, "/transcribe", req.MediaType, req.Audio)
	if err != nil {
		return api.Transcript{}, err
	}
	var t struct {
		Text      string `json:"text"`
		Engine    string `json:"engine"`
		AudioMs   uint64 `json:"audio_ms"`
		ElapsedMs uint64 `json:"elapsed_ms"`
	}
	if err := json.Unmarshal(r.Body, &t); err != nil {
		return api.Transcript{}, rpc.Unavailable("speech engine /transcribe: bad reply")
	}
	out := api.Transcript{Text: Heard(t.Text, s.lexicon(ctx)), Engine: t.Engine, AudioMs: t.AudioMs, ElapsedMs: t.ElapsedMs}
	if out.Text != t.Text {
		out.Heard = &t.Text
	}
	return out, nil
}

func (s *Service) Synthesize(ctx context.Context, req api.SynthesizeRequest) (api.Audio, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.Audio{}, err
	}
	text := strings.TrimSpace(req.Text)
	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return api.Audio{}, rpc.Invalid("text is required")
	case n > MaxText:
		return api.Audio{}, rpc.Invalid("text is longer than %d characters", MaxText)
	}
	if req.Format != nil && *req.Format != "" && *req.Format != "wav" {
		return api.Audio{}, rpc.Invalid("format %q is not supported; use wav", *req.Format)
	}
	if req.Raw == nil || !*req.Raw {
		text = Spoken(text, s.lexicon(ctx))
		if text == "" {
			return api.Audio{}, rpc.Invalid("text has nothing to say")
		}
		if utf8.RuneCountInString(text) > 2*MaxText {
			return api.Audio{}, rpc.Invalid("spoken text is longer than %d characters", 2*MaxText)
		}
	}
	in := map[string]any{"text": text}
	if req.Voice != nil && *req.Voice != "" {
		in["voice"] = *req.Voice
	}
	if req.Speed != nil && *req.Speed != 0 {
		if *req.Speed < 0.5 || *req.Speed > 2 {
			return api.Audio{}, rpc.Invalid("speed must be from 0.5 to 2.0")
		}
		in["speed"] = *req.Speed
	}
	body, _ := json.Marshal(in)
	r, err := s.TTS.do(ctx, http.MethodPost, "/synthesize", "application/json", body)
	if err != nil {
		return api.Audio{}, err
	}
	return api.Audio{
		Body: r.Body, Spoken: text, MediaType: "audio/wav", Voice: r.Header.Get("X-Voice"),
		Engine: r.Header.Get("X-Engine"), AudioMs: r.AudioMs, ElapsedMs: r.ElapsedMs,
	}, nil
}

func (s *Service) ListVoices(ctx context.Context, req api.ListVoicesRequest) (api.ListVoicesResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.ListVoicesResponse{}, err
	}
	var out struct {
		Voices []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Language    string `json:"language"`
			Description string `json:"description"`
		} `json:"voices"`
	}
	if err := s.TTS.json(ctx, http.MethodGet, "/voices", nil, &out); err != nil {
		return api.ListVoicesResponse{}, err
	}
	resp := api.ListVoicesResponse{Voices: []api.Voice{}}
	for _, v := range out.Voices {
		if req.Language != nil && *req.Language != "" && !langMatch(v.Language, *req.Language) {
			continue
		}
		voice := api.Voice{Id: v.ID, Name: v.Name, Language: v.Language}
		if v.Description != "" {
			d := v.Description
			voice.Description = &d
		}
		resp.Voices = append(resp.Voices, voice)
	}
	return resp, nil
}

// lexicon returns the lexicon. On a store failure, speech goes on without
// it.
func (s *Service) lexicon(ctx context.Context) []api.LexiconEntry {
	if s.Lexicon == nil {
		return nil
	}
	lex, err := s.Lexicon.List(ctx)
	if err != nil && s.Log != nil {
		s.Log.Warn("lexicon", "err", err)
	}
	return lex
}

func (s *Service) ListLexicon(ctx context.Context, _ api.ListLexiconRequest) (api.ListLexiconResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.ListLexiconResponse{}, err
	}
	if s.Lexicon == nil {
		return api.ListLexiconResponse{Entries: []api.LexiconEntry{}}, nil
	}
	lex, err := s.Lexicon.List(ctx)
	if err != nil {
		return api.ListLexiconResponse{}, rpc.Unavailable("lexicon: %v", err)
	}
	if lex == nil {
		lex = []api.LexiconEntry{}
	}
	return api.ListLexiconResponse{Entries: lex}, nil
}

// Lexicon limits.
const (
	maxWord     = 100
	maxSpoken   = 200
	maxHeard    = 20
	maxNote     = 500
	maxLexicon  = 5000
	ipaReserved = "[]()/\\"
)

func (s *Service) PutLexiconEntry(ctx context.Context, req api.PutLexiconEntryRequest) (api.LexiconEntry, error) {
	p, err := s.lexiconEditor(ctx)
	if err != nil {
		return api.LexiconEntry{}, err
	}
	e := api.LexiconEntry{Written: strings.TrimSpace(req.Written), Spoken: trimmed(req.Spoken), Ipa: trimmed(req.Ipa),
		Note: trimmed(req.Note), HeardAs: []string{}, UpdatedBy: p.Subject, UpdatedMs: uint64(time.Now().UnixMilli())}
	if by := trimmed(req.By); by != nil {
		e.UpdatedBy = *by
	}
	for _, h := range req.HeardAs {
		if h = strings.TrimSpace(h); h != "" && !strings.EqualFold(h, e.Written) {
			e.HeardAs = append(e.HeardAs, h)
		}
	}
	switch {
	case e.Written == "" || utf8.RuneCountInString(e.Written) > maxWord || !line(e.Written):
		return api.LexiconEntry{}, rpc.Invalid("written must be one line of 1 to %d characters", maxWord)
	case !strings.ContainsFunc(e.Written, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }):
		return api.LexiconEntry{}, rpc.Invalid("written must have a letter or a digit")
	case e.Spoken == nil && e.Ipa == nil && len(e.HeardAs) == 0:
		return api.LexiconEntry{}, rpc.Invalid("give spoken, ipa, or heard_as")
	case e.Spoken != nil && (utf8.RuneCountInString(*e.Spoken) > maxSpoken || !line(*e.Spoken) || strings.ContainsAny(*e.Spoken, "[]()")):
		return api.LexiconEntry{}, rpc.Invalid("spoken must be one line of at most %d characters, with no brackets", maxSpoken)
	case e.Ipa != nil && (utf8.RuneCountInString(*e.Ipa) > maxSpoken || !line(*e.Ipa) || strings.ContainsAny(*e.Ipa, ipaReserved)):
		return api.LexiconEntry{}, rpc.Invalid("ipa must be one line of at most %d characters, with no brackets or slashes", maxSpoken)
	case len(e.HeardAs) > maxHeard:
		return api.LexiconEntry{}, rpc.Invalid("at most %d heard_as forms", maxHeard)
	case e.Note != nil && utf8.RuneCountInString(*e.Note) > maxNote:
		return api.LexiconEntry{}, rpc.Invalid("note is longer than %d characters", maxNote)
	}
	for _, h := range e.HeardAs {
		if utf8.RuneCountInString(h) > maxWord || !line(h) {
			return api.LexiconEntry{}, rpc.Invalid("each heard_as form must be one line of at most %d characters", maxWord)
		}
	}
	lex, err := s.Lexicon.List(ctx)
	if err != nil {
		return api.LexiconEntry{}, rpc.Unavailable("lexicon: %v", err)
	}
	if len(lex) >= maxLexicon {
		return api.LexiconEntry{}, rpc.Invalid("the lexicon has the maximum of %d entries", maxLexicon)
	}
	if err := s.Lexicon.Put(ctx, e); err != nil {
		return api.LexiconEntry{}, rpc.Unavailable("lexicon: %v", err)
	}
	return e, nil
}

func (s *Service) DeleteLexiconEntry(ctx context.Context, req api.DeleteLexiconEntryRequest) (api.DeleteLexiconEntryResponse, error) {
	if _, err := s.lexiconEditor(ctx); err != nil {
		return api.DeleteLexiconEntryResponse{}, err
	}
	ok, err := s.Lexicon.Delete(ctx, strings.TrimSpace(req.Written))
	if err != nil {
		return api.DeleteLexiconEntryResponse{}, rpc.Unavailable("lexicon: %v", err)
	}
	return api.DeleteLexiconEntryResponse{Deleted: ok}, nil
}

func (s *Service) PrepareText(ctx context.Context, req api.PrepareTextRequest) (api.PrepareTextResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.PrepareTextResponse{}, err
	}
	if utf8.RuneCountInString(req.Text) > MaxText {
		return api.PrepareTextResponse{}, rpc.Invalid("text is longer than %d characters", MaxText)
	}
	return api.PrepareTextResponse{Spoken: Spoken(strings.TrimSpace(req.Text), s.lexicon(ctx))}, nil
}

func (s *Service) lexiconEditor(ctx context.Context) (authn.Principal, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return p, err
	}
	if !p.Has(ScopeLexicon) {
		return p, rpc.Denied("a lexicon change needs scope %s", ScopeLexicon)
	}
	if s.Lexicon == nil {
		return p, rpc.Unavailable("the lexicon has no database")
	}
	return p, nil
}

func trimmed(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func line(s string) bool { return !strings.ContainsAny(s, "\r\n") }

// langMatch reports whether language tag have matches want, such as
// "en-US" for "en" or "en-us".
func langMatch(have, want string) bool {
	have, want = strings.ToLower(have), strings.ToLower(want)
	return have == want || strings.HasPrefix(have, want+"-")
}

func audioType(t string) bool {
	t = strings.ToLower(strings.TrimSpace(strings.SplitN(t, ";", 2)[0]))
	return strings.HasPrefix(t, "audio/") || t == "video/webm" || t == "video/ogg" ||
		t == "application/ogg" || t == "application/octet-stream"
}
