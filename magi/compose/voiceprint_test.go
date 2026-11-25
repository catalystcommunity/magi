package compose

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// voiceSpeech is the speech service for the speaker gate. The audio of a
// test is a word that selects the answer of verify-speaker. It keeps the
// profiles by owner, so a test sees whose profile a call reached.
type voiceSpeech struct {
	speechapi.SpeechService
	mu          sync.Mutex
	verified    []string // the audio of each verify-speaker call
	transcribed []string // the audio of each transcribe call
	profiles    map[string]speechapi.SpeakerProfile
	samples     map[string]int
}

func (f *voiceSpeech) Transcribe(_ context.Context, r speechapi.TranscribeRequest) (speechapi.Transcript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transcribed = append(f.transcribed, string(r.Audio))
	return speechapi.Transcript{Text: "Archimedes, what is running? " + string(r.Audio), Engine: "fake-stt"}, nil
}

func (f *voiceSpeech) VerifySpeaker(_ context.Context, r speechapi.VerifySpeakerRequest) (speechapi.VerifySpeakerResponse, error) {
	f.mu.Lock()
	f.verified = append(f.verified, string(r.Audio))
	f.mu.Unlock()
	if !deref(r.Extract) {
		return speechapi.VerifySpeakerResponse{}, rpc.Invalid("Magi must ask for the extraction")
	}
	clean := []byte("clean")
	out := speechapi.VerifySpeakerResponse{Enrolled: true, Engine: "fake-speaker"}
	switch string(r.Audio) {
	case "clear": // the person, with no extraction (no sample)
		out.Score = 0.55
	case "noisy": // the extraction brings the voice back
		out.Score, out.Extracted, out.ExtractedScore = 0.2, &clean, ptr(0.7)
	case "damaged": // the extraction makes the audio worse
		out.Score, out.Extracted, out.ExtractedScore = 0.5, &clean, ptr(0.1)
	case "stranger":
		out.Score, out.Extracted, out.ExtractedScore = 0.2, &clean, ptr(0.3)
	case "edge": // exactly the limit
		out.Score = speakerAccept
	case "nobody":
		return speechapi.VerifySpeakerResponse{}, nil
	case "down":
		return out, rpc.Unavailable("speech engine for /speaker is not configured")
	case "broken":
		return out, rpc.Denied("no")
	}
	return out, nil
}

func (f *voiceSpeech) owner(ctx context.Context) string {
	p, _ := rpc.Caller(ctx)
	return p.Subject
}

func (f *voiceSpeech) GetSpeakerProfile(ctx context.Context, r speechapi.GetSpeakerProfileRequest) (speechapi.SpeakerProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.profiles[f.owner(ctx)]; ok {
		return p, nil
	}
	return speechapi.SpeakerProfile{Owner: f.owner(ctx), Model: r.Model, Prints: []speechapi.SpeakerPrint{}}, nil
}

func (f *voiceSpeech) PutSpeakerProfile(ctx context.Context, r speechapi.PutSpeakerProfileRequest) (speechapi.SpeakerProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := speechapi.SpeakerProfile{Owner: f.owner(ctx), Model: r.Model, Prints: r.Prints, HasSample: r.Sample != nil, UpdatedMs: 7}
	f.profiles[p.Owner] = p
	if r.Sample != nil {
		f.samples[p.Owner] = len(*r.Sample)
	}
	return p, nil
}

func (f *voiceSpeech) DeleteSpeakerProfile(ctx context.Context, r speechapi.DeleteSpeakerProfileRequest) (speechapi.DeleteSpeakerProfileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.profiles[f.owner(ctx)]; !ok {
		return speechapi.DeleteSpeakerProfileResponse{}, nil
	}
	delete(f.profiles, f.owner(ctx))
	return speechapi.DeleteSpeakerProfileResponse{Deleted: 1}, nil
}

// voiceConversation is the conversation service for Say: one stream, the
// posts, and the annotations.
type voiceConversation struct {
	conversationapi.ConversationService
	mu    sync.Mutex
	posts []conversationapi.PostRequest
	anns  []conversationapi.AnnotateRequest
}

func (f *voiceConversation) OpenStream(_ context.Context, r conversationapi.OpenStreamRequest) (conversationapi.Stream, error) {
	return conversationapi.Stream{Id: "str_1", Persona: r.Persona}, nil
}

func (f *voiceConversation) ListUtterances(context.Context, conversationapi.ListUtterancesRequest) (conversationapi.ListUtterancesResponse, error) {
	return conversationapi.ListUtterancesResponse{Utterances: []conversationapi.Utterance{}, Links: []conversationapi.Link{}}, nil
}

func (f *voiceConversation) ListPersonas(context.Context, conversationapi.ListPersonasRequest) (conversationapi.ListPersonasResponse, error) {
	return conversationapi.ListPersonasResponse{Personas: []conversationapi.Persona{{Id: Engineering, Name: "Archimedes", Sharing: "private", Members: []string{}}}}, nil
}

func (f *voiceConversation) Post(_ context.Context, r conversationapi.PostRequest) (conversationapi.Utterance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, r)
	return conversationapi.Utterance{Id: "utt_1", Stream: r.Stream, Seq: 1, Text: r.Text, Modality: deref(r.Modality), Audio: r.Audio,
		AuthorKind: "human", Attachments: []conversationapi.Ref{}}, nil
}

func (f *voiceConversation) Annotate(_ context.Context, r conversationapi.AnnotateRequest) (conversationapi.Annotation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anns = append(f.anns, r)
	return conversationapi.Annotation{Id: "ann_1", Utterance: r.Utterance, Kind: r.Kind, Body: r.Body}, nil
}

type voiceArtifacts struct {
	artifactsapi.ArtifactService
	mu   sync.Mutex
	puts []artifactsapi.PutRequest
}

func (f *voiceArtifacts) Put(_ context.Context, r artifactsapi.PutRequest) (artifactsapi.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, r)
	return artifactsapi.Artifact{Digest: "sha256:" + string(r.Body), MediaType: r.MediaType, Labels: map[string]string{}}, nil
}

type voiceWorkflow struct {
	workflowapi.WorkflowService
}

func (f *voiceWorkflow) Start(_ context.Context, r workflowapi.StartRequest) (workflowapi.Run, error) {
	return workflowapi.Run{Id: "run_1", Definition: r.Definition, Vars: r.Vars}, nil
}

type voiceWorld struct {
	speech *voiceSpeech
	conv   *voiceConversation
	arts   *voiceArtifacts
	client *magiapi.MagiClient
	as     func(subject string, scopes ...string) context.Context
}

func newVoiceWorld(t *testing.T) *voiceWorld {
	w := &voiceWorld{speech: &voiceSpeech{profiles: map[string]speechapi.SpeakerProfile{}, samples: map[string]int{}},
		conv: &voiceConversation{}, arts: &voiceArtifacts{}}
	_, w.client, w.as = adminEnv(t, Config{}, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"SpeechService":       speechapi.SpeechServiceOps(w.speech),
		"ConversationService": conversationapi.ConversationServiceOps(w.conv),
		"ArtifactService":     artifactsapi.ArtifactServiceOps(w.arts),
		"WorkflowService":     workflowapi.WorkflowServiceOps(&voiceWorkflow{}),
	})
	return w
}

// The speaker gate of Say, through the Magi API: which turns Magi takes,
// which audio it reads and keeps, and what it keeps of a dropped turn
// (nothing).
func TestSpeakerGate(t *testing.T) {
	const model = "3dspeaker-eres2net-en-voxceleb-16k"
	for _, c := range []struct {
		name        string
		audio       string
		check       *magiapi.SpeakerCheck
		wake        bool
		take        bool
		verify      bool   // Magi checks the turn again
		kept        string // the audio that Magi transcribes and keeps
		extracted   bool
		unavailable bool
	}{
		{name: "a client with no voice print", audio: "plain", take: true, kept: "plain"},
		{name: "the device is sure", audio: "stranger", check: &magiapi.SpeakerCheck{State: "verified", Score: 0.71, Model: model, Wake: ptr("archimedes")}, wake: true, take: true, kept: "stranger"},
		{name: "unsure, and the print matches", audio: "clear", check: &magiapi.SpeakerCheck{State: "unsure", Score: 0.31, Model: model}, wake: true, take: true, verify: true, kept: "clear"},
		{name: "unsure, and the limit is enough", audio: "edge", check: &magiapi.SpeakerCheck{State: "unsure", Score: 0.3, Model: model}, take: true, verify: true, kept: "edge"},
		{name: "unsure, and the extraction finds the person", audio: "noisy", check: &magiapi.SpeakerCheck{State: "unsure", Score: 0.28, Model: model}, wake: true, take: true, verify: true, kept: "clean", extracted: true},
		{name: "unsure, and the extraction is worse", audio: "damaged", check: &magiapi.SpeakerCheck{State: "unsure", Score: 0.33, Model: model}, take: true, verify: true, kept: "damaged"},
		{name: "unsure, and not the person", audio: "stranger", check: &magiapi.SpeakerCheck{State: "unsure", Score: 0.27, Model: model}, verify: true},
		{name: "unsure, and the person has no profile", audio: "nobody", check: &magiapi.SpeakerCheck{State: "unsure", Score: 0.3, Model: model}, verify: true},
		{name: "no speaker engine in a conversation", audio: "down", check: &magiapi.SpeakerCheck{State: "unsure", Score: 0.3, Model: model}, take: true, verify: true, kept: "down", unavailable: true},
		{name: "no speaker engine for a wake name", audio: "down", check: &magiapi.SpeakerCheck{State: "unsure", Score: 0.3, Model: model}, wake: true, verify: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newVoiceWorld(t)
			audio := []byte(c.audio)
			req := magiapi.SayRequest{Persona: ptr(Engineering), Audio: &audio, Speaker: c.check}
			if c.wake {
				req.WakeWord = ptr(true)
			}
			res, err := w.client.Say(w.as("user:kim@example.local"), req)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(w.speech.verified) == 1; got != c.verify {
				t.Errorf("verify-speaker calls = %v, want one: %v", w.speech.verified, c.verify)
			}
			if !c.take {
				// Nothing of a dropped turn is read or kept.
				if !deref(res.Ignored) || res.Message.Id != "" {
					t.Errorf("response = %+v, want ignored", res)
				}
				if len(w.speech.transcribed)+len(w.arts.puts)+len(w.conv.posts)+len(w.conv.anns) != 0 {
					t.Errorf("Magi kept something of a dropped turn: transcribed %v, artifacts %d, posts %d, annotations %d",
						w.speech.transcribed, len(w.arts.puts), len(w.conv.posts), len(w.conv.anns))
				}
				return
			}
			if deref(res.Ignored) || res.Message.Id != "utt_1" {
				t.Fatalf("response = %+v, want the message", res)
			}
			if len(w.speech.transcribed) != 1 || w.speech.transcribed[0] != c.kept {
				t.Errorf("transcribed = %v, want %q", w.speech.transcribed, c.kept)
			}
			if len(w.arts.puts) != 1 || string(w.arts.puts[0].Body) != c.kept {
				t.Errorf("kept audio = %+v, want %q", w.arts.puts, c.kept)
			}
			if c.extracted && w.arts.puts[0].MediaType != "audio/wav" {
				t.Errorf("the extracted audio has the type %q", w.arts.puts[0].MediaType)
			}
			if c.check == nil {
				if len(w.conv.anns) != 0 {
					t.Errorf("annotations with no check: %+v", w.conv.anns)
				}
				return
			}
			if len(w.conv.anns) != 1 || w.conv.anns[0].Kind != annSpeaker || w.conv.anns[0].Utterance != "utt_1" {
				t.Fatalf("annotations = %+v", w.conv.anns)
			}
			var v speakerVerdict
			if err := json.Unmarshal([]byte(w.conv.anns[0].Body), &v); err != nil {
				t.Fatal(err)
			}
			if v.State != c.check.State || v.Score != c.check.Score || v.Model != model || v.Extracted != c.extracted ||
				v.Checked != (c.verify && !c.unavailable) || (v.Unchecked != "") != c.unavailable || v.Wake != deref(c.check.Wake) {
				t.Errorf("annotation = %s", w.conv.anns[0].Body)
			}
		})
	}

	t.Run("a check that is not valid", func(t *testing.T) {
		w := newVoiceWorld(t)
		audio := []byte("clear")
		for _, check := range []magiapi.SpeakerCheck{
			{State: "rejected", Score: 0.1, Model: model},
			{State: "unsure", Score: 3, Model: model},
			{State: "unsure", Score: 0.3},
		} {
			_, err := w.client.Say(w.as("user:kim@example.local"), magiapi.SayRequest{Audio: &audio, Speaker: &check})
			if rpc.CodeOf(err) != http.StatusBadRequest {
				t.Errorf("check %+v: err = %v, want an invalid request", check, err)
			}
		}
		// An error of the speech service that is not "unavailable" is an
		// error of the call, not a turn that Magi takes or drops.
		audio = []byte("broken")
		if _, err := w.client.Say(w.as("user:kim@example.local"), magiapi.SayRequest{Audio: &audio, Speaker: &magiapi.SpeakerCheck{State: "unsure", Score: 0.3, Model: model}}); err == nil {
			t.Error("Say gave no error for a failed speaker check")
		}
		if len(w.speech.transcribed)+len(w.conv.posts) != 0 {
			t.Errorf("Magi read or kept a turn with a check that is not valid")
		}
	})
}

// The voice profile operations of Magi reach the profile of the caller.
func TestVoiceProfile(t *testing.T) {
	const model = "3dspeaker-eres2net-en-voxceleb-16k"
	w := newVoiceWorld(t)
	kim, lee := w.as("user:kim@example.local"), w.as("user:lee@example.local")
	p, err := w.client.GetVoiceProfile(kim, magiapi.GetVoiceProfileRequest{Model: model})
	if err != nil || len(p.Prints) != 0 || p.HasSample || p.Model != model {
		t.Fatalf("empty profile = %+v, %v", p, err)
	}
	sample := make([]byte, 1000)
	src := "enroll"
	p, err = w.client.PutVoiceProfile(kim, magiapi.PutVoiceProfileRequest{Model: model, Sample: &sample, SampleType: ptr("audio/wav"),
		Prints: []magiapi.VoicePrint{{Vector: []byte{1, 2, 3, 4}, Source: &src, CreatedMs: 5}, {Vector: []byte{5, 6, 7, 8}}}})
	if err != nil || len(p.Prints) != 2 || !p.HasSample || p.UpdatedMs != 7 || string(p.Prints[0].Vector) != "\x01\x02\x03\x04" ||
		deref(p.Prints[0].Source) != "enroll" || p.Prints[0].CreatedMs != 5 {
		t.Fatalf("put = %+v, %v", p, err)
	}
	// The owner in the speech service is the caller, and the sample came.
	if got := w.speech.profiles["user:kim@example.local"]; len(got.Prints) != 2 || w.speech.samples["user:kim@example.local"] != 1000 {
		t.Fatalf("the speech service has %+v", w.speech.profiles)
	}
	if p, err := w.client.GetVoiceProfile(lee, magiapi.GetVoiceProfileRequest{Model: model}); err != nil || len(p.Prints) != 0 {
		t.Fatalf("the profile of a different person = %+v, %v", p, err)
	}
	if d, err := w.client.DeleteVoiceProfile(lee, magiapi.DeleteVoiceProfileRequest{}); err != nil || d.Deleted != 0 {
		t.Fatalf("delete for a person with no profile = %+v, %v", d, err)
	}
	if d, err := w.client.DeleteVoiceProfile(kim, magiapi.DeleteVoiceProfileRequest{Model: ptr(model)}); err != nil || d.Deleted != 1 {
		t.Fatalf("delete = %+v, %v", d, err)
	}
	if p, _ := w.client.GetVoiceProfile(kim, magiapi.GetVoiceProfileRequest{Model: model}); len(p.Prints) != 0 {
		t.Fatalf("the profile after the delete = %+v", p)
	}
}
