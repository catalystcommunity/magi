package server

import (
	"cmp"
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/speech/api"
)

// Voice prints (docs/speech-interaction.md, 3.2). A person has a profile
// for each speaker model: the prints that a device made, and speech of
// the person for the extraction. The owner of a profile is the caller.
// A profile selects speech; it is not a credential.

// Profiles stores the speaker profiles (store.DB).
type Profiles interface {
	Profile(ctx context.Context, owner, model string) (api.SpeakerProfile, error)
	// PutProfile replaces the prints. sample nil keeps the sample.
	PutProfile(ctx context.Context, p api.SpeakerProfile, sample []byte, sampleType string) (api.SpeakerProfile, error)
	Sample(ctx context.Context, owner, model string) ([]byte, string, error)
	// DeleteProfiles removes the profile for model, or all with model "".
	DeleteProfiles(ctx context.Context, owner, model string) (uint64, error)
}

// SpeakerModel is the speaker model of the engine "speaker"
// (engine/server.py). A print of it has 192 numbers.
const SpeakerModel = "3dspeaker-eres2net-en-voxceleb-16k"

// speakerModels has the count of numbers in a print of each model that
// the service takes prints for.
var speakerModels = map[string]int{SpeakerModel: 192}

const (
	// MaxPrints is the most prints of one profile.
	MaxPrints = 40
	// MaxSample is the largest sample of a profile.
	MaxSample = 2 << 20
	// minSampleMs: the extraction model ignores a voice sample that is
	// shorter than 3 seconds, and then it keeps the loudest voice of the
	// audio. A profile does not take such a sample. The margin is for a
	// sample that the engine converts to its sample rate.
	minSampleMs = 3100
)

var printSources = []string{"enroll", "turn"}

func (s *Service) profiles(ctx context.Context, model string) (owner string, err error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return "", err
	}
	if _, ok := speakerModels[model]; !ok {
		return "", rpc.Invalid("speaker model %q is not known; use %s", model, SpeakerModel)
	}
	if s.Profiles == nil {
		return "", rpc.Unavailable("the speaker profiles have no database")
	}
	return p.Subject, nil
}

func (s *Service) GetSpeakerProfile(ctx context.Context, req api.GetSpeakerProfileRequest) (api.SpeakerProfile, error) {
	owner, err := s.profiles(ctx, req.Model)
	if err != nil {
		return api.SpeakerProfile{}, err
	}
	p, err := s.Profiles.Profile(ctx, owner, req.Model)
	if err != nil {
		return api.SpeakerProfile{}, rpc.Unavailable("speaker profile: %v", err)
	}
	return p, nil
}

func (s *Service) PutSpeakerProfile(ctx context.Context, req api.PutSpeakerProfileRequest) (api.SpeakerProfile, error) {
	owner, err := s.profiles(ctx, req.Model)
	if err != nil {
		return api.SpeakerProfile{}, err
	}
	if len(req.Prints) == 0 || len(req.Prints) > MaxPrints {
		return api.SpeakerProfile{}, rpc.Invalid("prints must have 1 to %d prints; delete-speaker-profile removes a profile", MaxPrints)
	}
	now := uint64(time.Now().UnixMilli())
	p := api.SpeakerProfile{Owner: owner, Model: req.Model, UpdatedMs: now}
	for i, pr := range req.Prints {
		v, ok := unit(pr.Vector, speakerModels[req.Model])
		if !ok {
			return api.SpeakerProfile{}, rpc.Invalid("print %d: vector must be %d float32 numbers (little-endian) that are finite and not all 0", i, speakerModels[req.Model])
		}
		src := trimmed(pr.Source)
		if src != nil && !slices.Contains(printSources, *src) {
			return api.SpeakerProfile{}, rpc.Invalid("print %d: source must be one of %v", i, printSources)
		}
		if pr.CreatedMs > now+60_000 {
			return api.SpeakerProfile{}, rpc.Invalid("print %d: created_ms is in the future", i)
		}
		p.Prints = append(p.Prints, api.SpeakerPrint{Vector: floatBytes(v), Source: src, CreatedMs: cmp.Or(pr.CreatedMs, now)})
	}
	var sample []byte
	var sampleType string
	if req.Sample != nil && len(*req.Sample) > 0 {
		sample, sampleType = *req.Sample, strings.ToLower(strings.TrimSpace(cmp.Or(deref(req.SampleType), "audio/wav")))
		ms, ok := wavMs(sample)
		switch {
		case len(sample) > MaxSample:
			return api.SpeakerProfile{}, rpc.Invalid("sample is larger than %d bytes", MaxSample)
		case !slices.Contains([]string{"audio/wav", "audio/x-wav", "audio/wave"}, sampleType) || !ok:
			return api.SpeakerProfile{}, rpc.Invalid("sample must be a WAV file with PCM audio")
		case ms < minSampleMs:
			return api.SpeakerProfile{}, rpc.Invalid("sample must have %d ms of audio or more, and it has %d", minSampleMs, ms)
		}
		sampleType = "audio/wav"
	}
	out, err := s.Profiles.PutProfile(ctx, p, sample, sampleType)
	if err != nil {
		return api.SpeakerProfile{}, rpc.Unavailable("speaker profile: %v", err)
	}
	return out, nil
}

func (s *Service) DeleteSpeakerProfile(ctx context.Context, req api.DeleteSpeakerProfileRequest) (api.DeleteSpeakerProfileResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.DeleteSpeakerProfileResponse{}, err
	}
	if s.Profiles == nil {
		return api.DeleteSpeakerProfileResponse{}, rpc.Unavailable("the speaker profiles have no database")
	}
	n, err := s.Profiles.DeleteProfiles(ctx, p.Subject, strings.TrimSpace(deref(req.Model)))
	if err != nil {
		return api.DeleteSpeakerProfileResponse{}, rpc.Unavailable("speaker profile: %v", err)
	}
	return api.DeleteSpeakerProfileResponse{Deleted: n}, nil
}

func (s *Service) VerifySpeaker(ctx context.Context, req api.VerifySpeakerRequest) (api.VerifySpeakerResponse, error) {
	owner, err := s.profiles(ctx, req.Model)
	if err != nil {
		return api.VerifySpeakerResponse{}, err
	}
	mt := cmp.Or(deref(req.MediaType), "audio/wav")
	switch {
	case len(req.Audio) == 0:
		return api.VerifySpeakerResponse{}, rpc.Invalid("audio is required")
	case len(req.Audio) > MaxAudio:
		return api.VerifySpeakerResponse{}, rpc.Invalid("audio is larger than %d bytes", MaxAudio)
	case !audioType(mt):
		return api.VerifySpeakerResponse{}, rpc.Invalid("media_type %q is not audio", mt)
	}
	p, err := s.Profiles.Profile(ctx, owner, req.Model)
	if err != nil {
		return api.VerifySpeakerResponse{}, rpc.Unavailable("speaker profile: %v", err)
	}
	mean, ok := meanPrint(p.Prints, speakerModels[req.Model])
	if !ok {
		// Nobody to compare with: no engine runs.
		return api.VerifySpeakerResponse{}, nil
	}
	in := map[string]any{"audio": req.Audio}
	if req.Extract != nil && *req.Extract && p.HasSample {
		sample, _, err := s.Profiles.Sample(ctx, owner, req.Model)
		if err != nil {
			return api.VerifySpeakerResponse{}, rpc.Unavailable("speaker profile: %v", err)
		}
		if len(sample) > 0 {
			in["sample"] = sample
		}
	}
	var out struct {
		Vector          []byte `json:"vector"`
		Model           string `json:"model"`
		Engine          string `json:"engine"`
		Extracted       []byte `json:"extracted"`
		ExtractedVector []byte `json:"extracted_vector"`
		ElapsedMs       uint64 `json:"elapsed_ms"`
	}
	if err := s.Speaker.json(ctx, http.MethodPost, "/speaker", in, &out); err != nil {
		return api.VerifySpeakerResponse{}, err
	}
	if out.Model != req.Model {
		return api.VerifySpeakerResponse{}, rpc.Unavailable("the speaker engine has the model %q, not %q", out.Model, req.Model)
	}
	res := api.VerifySpeakerResponse{Enrolled: true, Score: score(mean, out.Vector), Engine: out.Engine, ElapsedMs: out.ElapsedMs}
	if len(out.Extracted) > 0 {
		es := score(mean, out.ExtractedVector)
		res.Extracted, res.ExtractedScore = &out.Extracted, &es
	}
	return res, nil
}

// floats reads n little-endian float32 numbers. ok is false when b has a
// different length or a number that is not finite.
func floats(b []byte, n int) ([]float32, bool) {
	if n == 0 || len(b) != 4*n {
		return nil, false
	}
	v := make([]float32, n)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		if f := float64(v[i]); math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
	}
	return v, true
}

func floatBytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// unit reads a print and gives it unit length. ok is false for a vector
// of the wrong size, with a number that is not finite, or of length 0.
func unit(b []byte, n int) ([]float32, bool) {
	v, ok := floats(b, n)
	if !ok {
		return nil, false
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm < 1e-6 {
		return nil, false
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v, true
}

// meanPrint is the mean of the prints of a profile, with unit length. ok
// is false when the profile has no usable print.
func meanPrint(prints []api.SpeakerPrint, n int) ([]float32, bool) {
	sum := make([]float64, n)
	count := 0
	for _, pr := range prints {
		v, ok := unit(pr.Vector, n)
		if !ok {
			continue
		}
		for i, x := range v {
			sum[i] += float64(x)
		}
		count++
	}
	if count == 0 {
		return nil, false
	}
	b := make([]float32, n)
	for i := range sum {
		b[i] = float32(sum[i] / float64(count))
	}
	return unit(floatBytes(b), n)
}

// score is the cosine of the mean print and the print of some audio. A
// print that the engine did not give (too little sound) has the score 0.
func score(mean []float32, vector []byte) float64 {
	v, ok := unit(vector, len(mean))
	if !ok {
		return 0
	}
	var dot float64
	for i := range mean {
		dot += float64(mean[i]) * float64(v[i])
	}
	return math.Round(dot*10000) / 10000
}

// wavMs returns the length of the PCM audio of a WAV file in ms. ok is
// false when b is not a WAV file with PCM audio.
func wavMs(b []byte) (ms uint64, ok bool) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, false
	}
	var byteRate uint64
	for at := 12; at+8 <= len(b); {
		id, size := string(b[at:at+4]), int(binary.LittleEndian.Uint32(b[at+4:]))
		body := at + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(b) {
				return 0, false
			}
			format := binary.LittleEndian.Uint16(b[body:])
			// 1 is PCM; 0xFFFE is the extensible form of it.
			if format != 1 && format != 0xFFFE {
				return 0, false
			}
			byteRate = uint64(binary.LittleEndian.Uint32(b[body+8:]))
		case "data":
			if byteRate == 0 {
				return 0, false
			}
			// A writer that streams can leave a size that is too large.
			n := min(size, len(b)-body)
			return uint64(n) * 1000 / byteRate, true
		}
		at = body + size + size%2
	}
	return 0, false
}
