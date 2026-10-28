// Package server implements SpeechService on top of HTTP speech engines.
//
// Speech keeps no records. Any authenticated caller can use it.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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

// Service implements api.SpeechService. STT serves transcribe; TTS serves
// synthesize and list-voices.
type Service struct {
	STT, TTS *Engine
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
	return api.Transcript{Text: t.Text, Engine: t.Engine, AudioMs: t.AudioMs, ElapsedMs: t.ElapsedMs}, nil
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
		Body: r.Body, MediaType: "audio/wav", Voice: r.Header.Get("X-Voice"),
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
