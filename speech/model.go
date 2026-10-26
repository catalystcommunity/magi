// Package speech implements bounded independent speech requests.
package speech

import (
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxChunk = 48000
const MaxInputSamples = 960000
const MaxOutputBytes = 5760000
const MaxText = 2000
const MaxRetention = 86400
const KokoroRevision = "f3ff3571791e39611d31c381e3a41a3af07b4987"
const ParakeetRevision = "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8"

var ErrDenied = errors.New("speech operation denied")
var ErrInvalid = errors.New("invalid speech request")
var ErrConflict = errors.New("speech request conflict")
var ErrCapacity = errors.New("speech capacity reached")
var ErrUnavailable = errors.New("speech unavailable")
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var Voices = []string{"af_heart", "af_bella", "af_nicole", "af_sarah", "af_sky", "am_fenrir", "am_michael", "am_puck", "bf_emma", "bf_isabella", "bm_george", "bm_fable"}

func Capabilities() w.SpeechCapabilities {
	profiles := []w.SpeechProfile{}
	for _, voice := range Voices {
		profiles = append(profiles, w.SpeechProfile{ProfileId: "kokoro-v1/" + voice, Engine: "kokoro", ModelRevision: KokoroRevision, Voice: voice, Language: "en", SampleRate: 24000, Format: "pcm_s16le_mono"})
	}
	return w.SpeechCapabilities{Kinds: []w.SpeechKind{"tts", "stt"}, Profiles: profiles, MaxInputSamples: MaxInputSamples, MaxTextBytes: MaxText, MaxChunkBytes: MaxChunk, OutputDelivery: "complete utterance; bounded chunk reads", InputSampleRate: 16000, MaxRetentionSeconds: MaxRetention}
}
func validIdentity(v w.IdentityRef) bool {
	return len(v.Domain) > 0 && len(v.Domain) <= 253 && safeName.MatchString(v.AccountId)
}
func validScope(v w.ScopeRef) bool {
	return validIdentity(v.Authority) && safeName.MatchString(v.ScopeId)
}
func validActor(v w.ApplicationRef) bool {
	return validIdentity(v.Identity) && safeName.MatchString(v.ApplicationId) && safeName.MatchString(v.InstanceId)
}
func validRef(v w.SpeechRef) bool {
	return validScope(v.Owner) && safeName.MatchString(v.RequestId) && v.Generation <= 9007199254740991
}
func requestRef(v w.SpeechSubmit) w.SpeechRef {
	return w.SpeechRef{RequestId: v.RequestId, Owner: v.Owner, Generation: v.Generation}
}
func profile(id string) (w.SpeechProfile, bool) {
	for _, p := range Capabilities().Profiles {
		if p.ProfileId == id {
			return p, true
		}
	}
	return w.SpeechProfile{}, false
}
func validate(v w.SpeechSubmit, now time.Time) error {
	if !validRef(requestRef(v)) || !v.Deadline.After(now) || v.Deadline.After(now.Add(2*time.Minute)) || v.RetentionSeconds < 120 || v.RetentionSeconds > MaxRetention || len(v.Sources) > 16 {
		return ErrInvalid
	}
	for _, r := range v.Sources {
		if !validIdentity(r.Authority) || !safeName.MatchString(r.Kind) || !safeName.MatchString(r.RecordId) {
			return ErrInvalid
		}
	}
	switch v.Kind {
	case "tts":
		if v.Text == nil || !utf8.ValidString(*v.Text) || len(*v.Text) > MaxText || strings.TrimSpace(*v.Text) == "" || v.ProfileId == nil || v.InputSamples != nil || v.InputDigest != nil {
			return ErrInvalid
		}
		if _, ok := profile(*v.ProfileId); !ok {
			return ErrInvalid
		}
	case "stt":
		if v.Text != nil || v.ProfileId != nil || v.InputSamples == nil || *v.InputSamples == 0 || *v.InputSamples > MaxInputSamples || v.InputDigest == nil || len(*v.InputDigest) != 32 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
