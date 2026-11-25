package compose

import (
	"context"
	"encoding/json"
	"math"
	"net/http"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
)

// Voice prints (docs/speech-interaction.md, 3.2). A device compares the
// speech of a turn with the voice print of its person and sends only
// speech that it did not reject. Magi keeps the profile, so each device
// of the person has the same prints, and it checks again the turns that
// the device was not sure about. Code decides this from a fixed limit; a
// model does not. The voice selects speech. It never authorizes an
// action: the identity of a request is the signed-in device.

// speakerAccept is the least score (the cosine of the print of a turn
// and the mean print of the person) of a turn that Magi takes after its
// own check. Measured on 25 November 2026 with the 3D-Speaker ERes2Net
// model on quiet speech from a phone: 1.5 s of the person scored 0.36 or
// more, and 26 other speakers scored 0.34 or less (whole turns: 0.42 or
// less). A turn that comes here is one that the device was not sure
// about, so the limit is at the top of the range of other speakers.
const speakerAccept = 0.40

// annSpeaker is the annotation with the voice print check of a spoken
// message.
const annSpeaker = "speaker"

// speakerVerdict is what the voice print check found for one spoken turn.
// The annotation of the message has it (annSpeaker).
type speakerVerdict struct {
	// State, Score, Model, and Wake are the check of the device.
	State string  `json:"state"`
	Score float64 `json:"score"`
	Model string  `json:"model"`
	Wake  string  `json:"wake,omitempty"`
	// Checked: Magi checked the turn again. MagiScore is the score of the
	// audio as the device sent it. ExtractedScore is the score after the
	// extraction of the voice of the person, when that ran. Extracted:
	// the message has the extracted audio, and the text is from it.
	Checked        bool     `json:"checked,omitempty"`
	MagiScore      float64  `json:"magi_score,omitempty"`
	ExtractedScore *float64 `json:"extracted_score,omitempty"`
	Extracted      bool     `json:"extracted,omitempty"`
	Engine         string   `json:"engine,omitempty"`
	// Unchecked says why Magi did not check a turn that the device was
	// not sure about.
	Unchecked string `json:"unchecked,omitempty"`

	audio []byte // the extracted audio, when it is the better one
}

// speakerGate decides if Magi takes the spoken turn in audio. check is
// what the device found; nil is a client with no voice print, and Magi
// takes its audio as before. wake: the turn must start a conversation.
// take false: the caller keeps nothing of the audio.
func (s *System) speakerGate(ctx context.Context, audio []byte, mediaType string, check *magiapi.SpeakerCheck, wake bool) (v *speakerVerdict, take bool, err error) {
	if check == nil {
		return nil, true, nil
	}
	switch {
	case check.State != "verified" && check.State != "unsure":
		return nil, false, rpc.Invalid("speaker.state must be verified or unsure")
	case math.IsNaN(check.Score) || check.Score < -1.01 || check.Score > 1.01:
		return nil, false, rpc.Invalid("speaker.score must be from -1 to 1")
	case check.Model == "" || len(check.Model) > 100 || len(deref(check.Wake)) > 80:
		return nil, false, rpc.Invalid("speaker.model is required, and model and wake are short names")
	}
	v = &speakerVerdict{State: check.State, Score: check.Score, Model: check.Model, Wake: deref(check.Wake)}
	if check.State == "verified" {
		return v, true, nil
	}
	res, err := s.Speech.VerifySpeaker(ctx, speechapi.VerifySpeakerRequest{Audio: audio, MediaType: &mediaType, Model: check.Model, Extract: ptr(true)})
	if err != nil {
		// Magi cannot check (no speaker engine, or the engine does not have
		// the model of the device). In an open conversation the device
		// verified the turns before this one, so Magi takes the turn. A
		// turn that must start a conversation needs a check.
		if code := rpc.CodeOf(err); code != http.StatusServiceUnavailable && code != http.StatusBadRequest {
			return nil, false, err
		}
		v.Unchecked = "the speaker check is not available"
		s.Log.Warn("speaker check not available", "err", err, "wake", wake, "device_score", check.Score)
		return v, !wake, nil
	}
	if !res.Enrolled {
		s.Log.Info("spoken turn dropped: the person has no voice profile", "model", check.Model, "device_score", check.Score)
		return v, false, nil
	}
	v.Checked, v.MagiScore, v.ExtractedScore, v.Engine = true, res.Score, res.ExtractedScore, res.Engine
	best := res.Score
	if res.Extracted != nil && res.ExtractedScore != nil && *res.ExtractedScore > best {
		best = *res.ExtractedScore
		v.Extracted, v.audio = true, *res.Extracted
	}
	if best < speakerAccept {
		s.Log.Info("spoken turn dropped: the voice print does not match", "device_score", check.Score, "score", res.Score,
			"extracted_score", deref(res.ExtractedScore), "limit", speakerAccept)
		return v, false, nil
	}
	return v, true, nil
}

// noteSpeaker keeps the voice print check with the spoken message, so an
// admin can see why Magi took the turn.
func (s *System) noteSpeaker(ctx context.Context, utterance string, v *speakerVerdict) {
	if v == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if _, err := s.Conversation.Annotate(ctx, conversationapi.AnnotateRequest{Utterance: utterance, Kind: annSpeaker, Body: string(b)}); err != nil {
		s.Log.Warn("speaker annotation", "err", err)
	}
}

func voiceProfile(p speechapi.SpeakerProfile) magiapi.VoiceProfile {
	out := magiapi.VoiceProfile{Model: p.Model, Prints: []magiapi.VoicePrint{}, HasSample: p.HasSample, UpdatedMs: p.UpdatedMs}
	for _, pr := range p.Prints {
		out.Prints = append(out.Prints, magiapi.VoicePrint{Vector: pr.Vector, Source: pr.Source, CreatedMs: pr.CreatedMs})
	}
	return out
}

// The voice profile of the caller. The speech service keeps it; the
// owner there is the caller, because Magi forwards the token.

func (m *Service) GetVoiceProfile(ctx context.Context, req magiapi.GetVoiceProfileRequest) (magiapi.VoiceProfile, error) {
	p, err := m.S.Speech.GetSpeakerProfile(AsCaller(ctx), speechapi.GetSpeakerProfileRequest{Model: req.Model})
	if err != nil {
		return magiapi.VoiceProfile{}, err
	}
	return voiceProfile(p), nil
}

func (m *Service) PutVoiceProfile(ctx context.Context, req magiapi.PutVoiceProfileRequest) (magiapi.VoiceProfile, error) {
	in := speechapi.PutSpeakerProfileRequest{Model: req.Model, Prints: []speechapi.SpeakerPrint{}, Sample: req.Sample, SampleType: req.SampleType}
	for _, pr := range req.Prints {
		in.Prints = append(in.Prints, speechapi.SpeakerPrint{Vector: pr.Vector, Source: pr.Source, CreatedMs: pr.CreatedMs})
	}
	p, err := m.S.Speech.PutSpeakerProfile(AsCaller(ctx), in)
	if err != nil {
		return magiapi.VoiceProfile{}, err
	}
	return voiceProfile(p), nil
}

func (m *Service) DeleteVoiceProfile(ctx context.Context, req magiapi.DeleteVoiceProfileRequest) (magiapi.DeleteVoiceProfileResponse, error) {
	res, err := m.S.Speech.DeleteSpeakerProfile(AsCaller(ctx), speechapi.DeleteSpeakerProfileRequest{Model: req.Model})
	return magiapi.DeleteVoiceProfileResponse{Deleted: res.Deleted}, err
}
