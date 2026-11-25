package server

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/speech/api"
	"github.com/catalystcommunity/magi/primitives/speech/internal/testenv"
	"github.com/catalystcommunity/magi/primitives/speech/store"
)

// axis is a print of 192 numbers with the weights of the first two.
func axis(a, b float32) []byte {
	v := make([]float32, 192)
	v[0], v[1] = a, b
	return floatBytes(v)
}

// testWav is a 16 kHz mono PCM WAV file of ms milliseconds of silence.
func testWav(ms int) []byte {
	n := 16 * ms * 2
	b := make([]byte, 44+n)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n))
	return b
}

// speakerEngine serves /speaker of engine/server.py. The audio is a word:
// "owner" has the print of the first axis, "other" of the second, "mix"
// of both, and "silent" has no print. With a sample, the engine gives
// the extracted audio with the print of the first axis.
type speakerEngine struct {
	*httptest.Server
	mu      sync.Mutex
	calls   int
	samples [][]byte
	model   string
}

func newSpeakerEngine(t *testing.T) *speakerEngine {
	e := &speakerEngine{model: SpeakerModel}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /speaker", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+engineToken {
			w.WriteHeader(401)
			return
		}
		var in struct{ Audio, Sample []byte }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid JSON request"}`))
			return
		}
		e.mu.Lock()
		e.calls++
		e.samples = append(e.samples, in.Sample)
		model := e.model
		e.mu.Unlock()
		out := map[string]any{"model": model, "engine": model + "+voxlock", "elapsed_ms": 9}
		switch string(in.Audio) {
		case "owner":
			out["vector"] = axis(1, 0)
		case "other":
			out["vector"] = axis(0, 1)
		case "mix":
			out["vector"] = axis(0.6, 0.8)
		case "silent":
			out["vector"] = nil
		case "bad":
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"audio could not be decoded"}`))
			return
		}
		if in.Sample != nil {
			out["extracted"], out["extracted_vector"] = []byte("RIFFclean"), axis(1, 0)
		}
		json.NewEncoder(w).Encode(out)
	})
	e.Server = httptest.NewServer(mux)
	t.Cleanup(e.Close)
	return e
}

func (e *speakerEngine) seen() (int, []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.samples) == 0 {
		return e.calls, nil
	}
	return e.calls, e.samples[len(e.samples)-1]
}

func TestSpeakerProfile(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, testenv.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	eng, spk := fakeEngine(t), newSpeakerEngine(t)
	svc := &Service{STT: NewEngine(eng.URL, engineToken), TTS: NewEngine(eng.URL, engineToken),
		Speaker: NewEngine(spk.URL, engineToken), Lexicon: db, Profiles: db}

	pub, priv, _ := ed25519.GenerateKey(nil)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	s := &rpc.Server{Verifier: ver, MaxBody: MaxRequest}
	Register(s, svc)
	hs := httptest.NewServer(rpc.Mux(s, nil))
	t.Cleanup(hs.Close)
	client := func(subject string) *api.SpeechClient {
		tok, err := iss.Issue(authn.Principal{Subject: subject, Kind: authn.KindUser}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return api.NewSpeechClient(rpc.NewClient(hs.URL, rpc.StaticToken(tok)))
	}
	ana, ben := client("user:ana@example.local"), client("user:ben@example.local")
	invalid := func(what string, err error) {
		t.Helper()
		if rpc.CodeOf(err) != http.StatusBadRequest {
			t.Errorf("%s: err = %v, want an invalid request", what, err)
		}
	}

	// A person with no profile: an empty profile, and no engine call.
	p, err := ana.GetSpeakerProfile(ctx, api.GetSpeakerProfileRequest{Model: SpeakerModel})
	if err != nil || len(p.Prints) != 0 || p.HasSample || p.Owner != "user:ana@example.local" {
		t.Fatalf("empty profile = %+v, %v", p, err)
	}
	v, err := ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Audio: []byte("owner"), Model: SpeakerModel})
	if err != nil || v.Enrolled || v.Score != 0 {
		t.Fatalf("verify with no profile = %+v, %v", v, err)
	}
	if n, _ := spk.seen(); n != 0 {
		t.Fatalf("the engine ran %d times for a person with no profile", n)
	}

	// Put: the service gives each print unit length.
	enroll := "enroll"
	p, err = ana.PutSpeakerProfile(ctx, api.PutSpeakerProfileRequest{Model: SpeakerModel, Prints: []api.SpeakerPrint{
		{Vector: axis(2, 0), Source: &enroll, CreatedMs: 1000}, {Vector: axis(0.9, 0.1)}}})
	if err != nil || len(p.Prints) != 2 || p.HasSample || p.UpdatedMs == 0 {
		t.Fatalf("put = %+v, %v", p, err)
	}
	if got, _ := floats(p.Prints[0].Vector, 192); got[0] != 1 || *p.Prints[0].Source != "enroll" || p.Prints[0].CreatedMs != 1000 {
		t.Errorf("print 0 = %v %v %d", got[:2], p.Prints[0].Source, p.Prints[0].CreatedMs)
	}
	if p.Prints[1].Source != nil || p.Prints[1].CreatedMs == 0 {
		t.Errorf("print 1 = %+v", p.Prints[1])
	}
	// A profile belongs to one person.
	if q, err := ben.GetSpeakerProfile(ctx, api.GetSpeakerProfileRequest{Model: SpeakerModel}); err != nil || len(q.Prints) != 0 {
		t.Fatalf("the profile of a different person = %+v, %v", q, err)
	}

	// Verify against the mean print.
	for _, c := range []struct {
		audio string
		want  float64
	}{{"owner", 0.998}, {"other", 0.055}, {"mix", 0.643}, {"silent", 0}} {
		v, err := ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Audio: []byte(c.audio), Model: SpeakerModel, Extract: ptr(true)})
		if err != nil || !v.Enrolled || math.Abs(v.Score-c.want) > 0.01 || v.Extracted != nil || v.ExtractedScore != nil {
			t.Errorf("verify %s = %+v, %v; want score %.3f and no extraction (the profile has no sample)", c.audio, v, err, c.want)
		}
	}
	if _, sample := spk.seen(); sample != nil {
		t.Errorf("the engine got a sample, and the profile has none")
	}
	_, err = ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Audio: []byte("bad"), Model: SpeakerModel})
	invalid("audio that the engine cannot read", err)

	// A sample: the extraction runs only when the caller asks for it.
	sample := testWav(3500)
	p, err = ana.PutSpeakerProfile(ctx, api.PutSpeakerProfileRequest{Model: SpeakerModel, Prints: []api.SpeakerPrint{{Vector: axis(1, 0)}}, Sample: &sample})
	if err != nil || !p.HasSample || len(p.Prints) != 1 {
		t.Fatalf("put with a sample = %+v, %v", p, err)
	}
	v, err = ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Audio: []byte("mix"), Model: SpeakerModel})
	if _, got := spk.seen(); err != nil || v.Extracted != nil || got != nil {
		t.Errorf("verify with no extract = %+v, %v (engine sample %d bytes)", v, err, len(got))
	}
	v, err = ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Audio: []byte("mix"), Model: SpeakerModel, Extract: ptr(true)})
	if err != nil || math.Abs(v.Score-0.6) > 0.01 || v.Extracted == nil || string(*v.Extracted) != "RIFFclean" || v.ExtractedScore == nil || *v.ExtractedScore < 0.99 {
		t.Errorf("verify with extract = %+v, %v", v, err)
	}
	if _, got := spk.seen(); len(got) != len(sample) {
		t.Errorf("the engine got a sample of %d bytes, want %d", len(got), len(sample))
	}
	if v.Engine != SpeakerModel+"+voxlock" || v.ElapsedMs != 9 {
		t.Errorf("engine = %q, elapsed = %d", v.Engine, v.ElapsedMs)
	}
	// A put with no sample keeps the sample.
	p, err = ana.PutSpeakerProfile(ctx, api.PutSpeakerProfileRequest{Model: SpeakerModel, Prints: []api.SpeakerPrint{{Vector: axis(1, 0)}, {Vector: axis(1, 0.1)}}})
	if err != nil || !p.HasSample || len(p.Prints) != 2 {
		t.Fatalf("put with no sample = %+v, %v", p, err)
	}

	// Limits.
	one := []api.SpeakerPrint{{Vector: axis(1, 0)}}
	many := make([]api.SpeakerPrint, MaxPrints+1)
	for i := range many {
		many[i] = api.SpeakerPrint{Vector: axis(1, 0)}
	}
	nan := axis(1, 0)
	binary.LittleEndian.PutUint32(nan[8:], math.Float32bits(float32(math.NaN())))
	bad, short, text, big := "phone", testWav(2000), []byte("not a wav file"), make([]byte, MaxSample+1)
	copy(big, testWav(4000))
	for what, req := range map[string]api.PutSpeakerProfileRequest{
		"no prints":        {Model: SpeakerModel, Prints: []api.SpeakerPrint{}},
		"too many prints":  {Model: SpeakerModel, Prints: many},
		"a short vector":   {Model: SpeakerModel, Prints: []api.SpeakerPrint{{Vector: axis(1, 0)[:100]}}},
		"a zero vector":    {Model: SpeakerModel, Prints: []api.SpeakerPrint{{Vector: axis(0, 0)}}},
		"a NaN":            {Model: SpeakerModel, Prints: []api.SpeakerPrint{{Vector: nan}}},
		"a bad source":     {Model: SpeakerModel, Prints: []api.SpeakerPrint{{Vector: axis(1, 0), Source: &bad}}},
		"a future time":    {Model: SpeakerModel, Prints: []api.SpeakerPrint{{Vector: axis(1, 0), CreatedMs: uint64(time.Now().Add(time.Hour).UnixMilli())}}},
		"an unknown model": {Model: "other-model", Prints: one},
		"a short sample":   {Model: SpeakerModel, Prints: one, Sample: &short},
		"a sample no WAV":  {Model: SpeakerModel, Prints: one, Sample: &text},
		"a large sample":   {Model: SpeakerModel, Prints: one, Sample: &big},
		"a sample type":    {Model: SpeakerModel, Prints: one, Sample: &sample, SampleType: ptr("audio/ogg")},
	} {
		_, err := ana.PutSpeakerProfile(ctx, req)
		invalid(what, err)
	}
	if p, _ := ana.GetSpeakerProfile(ctx, api.GetSpeakerProfileRequest{Model: SpeakerModel}); len(p.Prints) != 2 {
		t.Errorf("an invalid put changed the profile: %d prints", len(p.Prints))
	}
	_, err = ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Model: SpeakerModel})
	invalid("verify with no audio", err)
	_, err = ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Audio: []byte("owner"), Model: SpeakerModel, MediaType: ptr("text/plain")})
	invalid("verify with a text type", err)

	// An engine with a different model, and no engine: unavailable, so
	// the caller can tell it from "not the person".
	spk.mu.Lock()
	spk.model = "some-other-model"
	spk.mu.Unlock()
	if _, err := ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Audio: []byte("owner"), Model: SpeakerModel}); rpc.CodeOf(err) != http.StatusServiceUnavailable {
		t.Errorf("engine with a different model: err = %v, want unavailable", err)
	}
	svc.Speaker = nil
	if _, err := ana.VerifySpeaker(ctx, api.VerifySpeakerRequest{Audio: []byte("owner"), Model: SpeakerModel}); rpc.CodeOf(err) != http.StatusServiceUnavailable {
		t.Errorf("no engine: err = %v, want unavailable", err)
	}

	// Delete removes the prints and the sample of the caller only.
	if _, err := ben.PutSpeakerProfile(ctx, api.PutSpeakerProfileRequest{Model: SpeakerModel, Prints: one}); err != nil {
		t.Fatal(err)
	}
	if d, err := ana.DeleteSpeakerProfile(ctx, api.DeleteSpeakerProfileRequest{Model: ptr("other-model")}); err != nil || d.Deleted != 0 {
		t.Errorf("delete of a model with no profile = %+v, %v", d, err)
	}
	if d, err := ana.DeleteSpeakerProfile(ctx, api.DeleteSpeakerProfileRequest{}); err != nil || d.Deleted != 1 {
		t.Errorf("delete = %+v, %v", d, err)
	}
	if p, _ := ana.GetSpeakerProfile(ctx, api.GetSpeakerProfileRequest{Model: SpeakerModel}); len(p.Prints) != 0 || p.HasSample {
		t.Errorf("after delete: %+v", p)
	}
	if p, _ := ben.GetSpeakerProfile(ctx, api.GetSpeakerProfileRequest{Model: SpeakerModel}); len(p.Prints) != 1 {
		t.Errorf("the delete of one person removed the profile of a different person")
	}

	// Without a database the profile operations are unavailable.
	svc.Profiles = nil
	if _, err := ana.GetSpeakerProfile(ctx, api.GetSpeakerProfileRequest{Model: SpeakerModel}); rpc.CodeOf(err) != http.StatusServiceUnavailable {
		t.Errorf("no database: err = %v, want unavailable", err)
	}
}

func TestWavMs(t *testing.T) {
	if ms, ok := wavMs(testWav(3100)); !ok || ms != 3100 {
		t.Errorf("wavMs = %d, %v", ms, ok)
	}
	// A data size that is larger than the file (a writer that streamed).
	b := testWav(1000)
	binary.LittleEndian.PutUint32(b[40:], 0xFFFFFFFF)
	if ms, ok := wavMs(b); !ok || ms != 1000 {
		t.Errorf("wavMs with a large data size = %d, %v", ms, ok)
	}
	// Not PCM.
	b = testWav(1000)
	binary.LittleEndian.PutUint16(b[20:], 85)
	if _, ok := wavMs(b); ok {
		t.Error("wavMs took an MP3 in a WAV file")
	}
	for _, b := range [][]byte{nil, []byte("RIFF"), []byte("RIFFxxxxWAVE"), testWav(100)[:30]} {
		if _, ok := wavMs(b); ok {
			t.Errorf("wavMs took %q", b)
		}
	}
}
