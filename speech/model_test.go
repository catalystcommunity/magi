package speech

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
	"os/exec"
	"strings"
	"testing"
	"time"
)

var actor = w.ApplicationRef{Identity: w.IdentityRef{Domain: "primary.opal.test", AccountId: "human"}, ApplicationId: "client", InstanceId: "one"}
var receiver = w.ApplicationRef{Identity: w.IdentityRef{Domain: "secondary.opal.test", AccountId: "speech"}, ApplicationId: "speech", InstanceId: "one"}
var owner = w.ScopeRef{Authority: actor.Identity, ScopeId: "public-fixtures"}

func ptr[T any](v T) *T { return &v }
func request(id string) w.SpeechSubmit {
	return w.SpeechSubmit{RequestId: id, Owner: owner, Kind: "tts", Generation: 1, Deadline: time.Now().UTC().Truncate(time.Millisecond).Add(time.Minute), RetentionSeconds: 120, Sources: []w.RecordRef{}, Text: ptr("Hello there."), ProfileId: ptr("kokoro-v1/af_heart")}
}
func must(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func TestRequestBounds(t *testing.T) {
	caps := Capabilities()
	if len(caps.Profiles) != 12 {
		t.Fatal(caps)
	}
	for _, profile := range caps.Profiles {
		v := request("valid")
		v.ProfileId = &profile.ProfileId
		must(t, validate(v, time.Now()))
	}
	for name, mutate := range map[string]func(*w.SpeechSubmit){
		"empty": func(v *w.SpeechSubmit) { v.Text = ptr(" ") }, "large": func(v *w.SpeechSubmit) { v.Text = ptr(strings.Repeat("x", 2001)) },
		"voice": func(v *w.SpeechSubmit) { v.ProfileId = ptr("unknown") }, "expired": func(v *w.SpeechSubmit) { v.Deadline = time.Now().Add(-time.Second) },
		"long-deadline": func(v *w.SpeechSubmit) { v.Deadline = time.Now().Add(time.Hour) }, "retention": func(v *w.SpeechSubmit) { v.RetentionSeconds = 86401 },
		"mixed": func(v *w.SpeechSubmit) { v.InputSamples = ptr(uint64(1)) }, "missing-audio": func(v *w.SpeechSubmit) { v.Kind = "stt"; v.Text = nil; v.ProfileId = nil },
		"path": func(v *w.SpeechSubmit) { v.RequestId = "../../outside" }, "unknown-kind": func(v *w.SpeechSubmit) { v.Kind = "speaker-auth" },
	} {
		t.Run(name, func(t *testing.T) {
			v := request("bad")
			mutate(&v)
			if validate(v, time.Now()) == nil {
				t.Fatal("accepted")
			}
		})
	}
}

type keys struct{ public ed25519.PublicKey }

func (k keys) Resolve(_ context.Context, a w.ApplicationRef, key string) (identity.VerifiedKey, error) {
	if a != actor || key != "test-key" {
		return identity.VerifiedKey{}, identity.ErrDenied
	}
	return identity.VerifiedKey{Public: k.public, CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func TestAuthenticationBindings(t *testing.T) {
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	must(t, e)
	packet, e := Sign("submit", w.EncodeSpeechSubmit(request("signed")), actor, receiver, "test-key", key, time.Now().UTC().Truncate(time.Millisecond))
	must(t, e)
	_, e = identity.Authenticate(context.Background(), keys{pub}, receiver, "catalyst.speech/submit", Schema(), packet)
	must(t, e)
	for _, change := range []func(*w.SignedRecord){func(p *w.SignedRecord) { p.Signature[0] ^= 1 }, func(p *w.SignedRecord) {
		v, _ := w.DecodeSignatureInput(p.Input)
		v.Context = "catalyst.speech/cancel"
		p.Input = w.EncodeSignatureInput(v)
		p.Signature = ed25519.Sign(key, p.Input)
	}, func(p *w.SignedRecord) {
		v, _ := w.DecodeSignatureInput(p.Input)
		v.SignedAt = time.Now().Add(-2 * time.Minute)
		p.Input = w.EncodeSignatureInput(v)
		p.Signature = ed25519.Sign(key, p.Input)
	}} {
		copy := w.SignedRecord{Input: append([]byte{}, packet.Input...), Signature: append([]byte{}, packet.Signature...)}
		change(&copy)
		if _, e = identity.Authenticate(context.Background(), keys{pub}, receiver, "catalyst.speech/submit", Schema(), copy); !errors.Is(e, identity.ErrDenied) {
			t.Fatal(e)
		}
	}
}
func TestPythonProtocol(t *testing.T) {
	t.Setenv("OPAL_SENTINEL_SECRET", "must-not-enter-worker")
	source := `import sys,json,os
json.loads(sys.stdin.readline())
assert 'OPAL_SENTINEL_SECRET' not in os.environ
print('{"ready":true}',flush=True)
for line in sys.stdin:
 json.loads(line)
 print('{"pcm":"AAA=","sample_rate":24000,"transcript":"","error_code":""}',flush=True)
`
	backend := &PythonBackend{Config: PythonConfig{Python: "/usr/bin/python3", ModelDir: t.TempDir(), Kind: "tts"}, source: source}
	defer backend.Close()
	work := Work{Request: request("python")}
	for range 2 {
		result, e := backend.Process(context.Background(), work)
		must(t, e)
		if len(result.PCM) != 2 {
			t.Fatal(result)
		}
	}
	pid := backend.cmd.Process.Pid
	_, e := backend.Process(context.Background(), work)
	must(t, e)
	if backend.cmd.Process.Pid != pid {
		t.Fatal("model reloaded")
	}
}
func TestRemotePythonCommandClearsHostEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := `import os
assert 'OPAL_HOST_SENTINEL' not in os.environ
assert os.environ['HF_HUB_OFFLINE']=='1'
assert os.environ['PYTHONDONTWRITEBYTECODE']=='1'
print("quoted ' text $() stays code")
`
	command := exec.CommandContext(ctx, "/bin/sh", "-c", remotePythonCommand("/usr/bin/python3", source))
	command.Env = []string{"PATH=/usr/bin:/bin", "OPAL_HOST_SENTINEL=synthetic-test-only"}
	out, e := command.CombinedOutput()
	if e != nil || string(out) != "quoted ' text $() stays code\n" {
		t.Fatal("remote environment or quoting failed", e)
	}
}

func TestPythonCancellationDuringLoadAndCall(t *testing.T) {
	for _, source := range []string{"import time;time.sleep(60)", "import sys,time;sys.stdin.readline();print('{\"ready\":true}',flush=True);sys.stdin.readline();time.sleep(60)"} {
		backend := &PythonBackend{Config: PythonConfig{Python: "/usr/bin/python3", ModelDir: t.TempDir(), Kind: "tts"}, source: source}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		start := time.Now()
		_, e := backend.Process(ctx, Work{Request: request("cancel")})
		cancel()
		backend.Close()
		if !errors.Is(e, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
			t.Fatal(e, time.Since(start))
		}
	}
}
