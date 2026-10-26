//go:build integration

package speech

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	w "github.com/catalystcommunity/magi/generated/go"
	standalone "github.com/catalystcommunity/magi/generated/speech/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const postgresImage = "postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd"

func docker(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("docker %s: %v: %s", args[0], e, out)
	}
	return strings.TrimSpace(string(out))
}
func token() string { var b [8]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func fixture(t *testing.T) func(*testing.T) *Store {
	id := docker(t, "run", "--pull=never", "-d", "--name", "opal-speech-test-"+token(), "--label", "opal.fixture=speech", "--memory", "512m", "--cpus", "2", "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=synthetic-only", postgresImage)
	if len(id) != 64 {
		t.Fatal("invalid container ID")
	}
	t.Cleanup(func() {
		if docker(t, "inspect", "-f", `{{index .Config.Labels "opal.fixture"}}`, id) != "speech" {
			t.Error("fixture ownership changed")
			return
		}
		docker(t, "rm", "-f", "-v", id)
		t.Log("Removed only the disposable speech test database and its volume")
	})
	address := docker(t, "port", id, "5432/tcp")
	dsn := "postgres://postgres:synthetic-only@" + address + "/postgres?sslmode=disable"
	admin, e := pgxpool.New(context.Background(), dsn)
	must(t, e)
	t.Cleanup(admin.Close)
	end := time.Now().Add(30 * time.Second)
	for admin.Ping(context.Background()) != nil {
		if time.Now().After(end) {
			t.Fatal("database readiness")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return func(t *testing.T) *Store {
		schema := "fixture_" + token()
		_, e := admin.Exec(context.Background(), "CREATE SCHEMA "+schema)
		must(t, e)
		cfg, e := pgxpool.ParseConfig(dsn)
		must(t, e)
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		cfg.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
		pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
		must(t, e)
		t.Cleanup(pool.Close)
		s := &Store{Pool: pool, Application: receiver}
		must(t, s.Initialize(context.Background()))
		must(t, s.SetGrant(context.Background(), Grant{Actor: actor, Owner: owner, Allowed: true}))
		return s
	}
}
func submitted(t *testing.T, s *Store, id string) w.SpeechStatus {
	t.Helper()
	v, e := s.Submit(context.Background(), actor, request(id), w.EncodeSignedRecord(w.SignedRecord{Input: []byte("fixture"), Signature: []byte("fixture")}))
	must(t, e)
	return v
}
func claim(t *testing.T, s *Store, kind w.SpeechKind) Work {
	t.Helper()
	work, ok, e := s.Claim(context.Background(), kind)
	must(t, e)
	if !ok {
		t.Fatal("no claim")
	}
	return work
}
func TestDurableSpeech(t *testing.T) {
	fresh := fixture(t)
	ctx := context.Background()
	t.Run("concurrent-identical-submissions-and-changed-content", func(t *testing.T) {
		s := fresh(t)
		v := request("same")
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		for range 12 {
			wg.Go(func() { _, e := s.Submit(ctx, actor, v, []byte("proof")); errs <- e })
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			must(t, e)
		}
		var count int
		must(t, s.Pool.QueryRow(ctx, `SELECT count(*) FROM speech_requests`).Scan(&count))
		if count != 1 {
			t.Fatal(count)
		}
		v.Text = ptr("Changed")
		if _, e := s.Submit(ctx, actor, v, nil); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	})
	t.Run("two-replicas-one-claim-cancel-fences-late-output", func(t *testing.T) {
		s := fresh(t)
		status := submitted(t, s, "cancel")
		replica := &Store{Pool: s.Pool, Application: receiver}
		must(t, replica.Check(ctx))
		work := claim(t, s, "tts")
		_, ok, e := replica.Claim(ctx, "tts")
		must(t, e)
		if ok {
			t.Fatal("duplicate claim")
		}
		_, e = replica.Cancel(ctx, actor, status.Ref)
		must(t, e)
		if e = s.Finish(ctx, work, Result{PCM: []byte{0, 0}, SampleRate: 24000}, ""); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
		status, e = s.Read(ctx, actor, status.Ref)
		must(t, e)
		if status.State != "cancelled" {
			t.Fatal(status)
		}
	})
	t.Run("chunk-retries-integrity-and-separate-capacity", func(t *testing.T) {
		s := fresh(t)
		submitted(t, s, "tts")
		claim(t, s, "tts")
		input := make([]byte, MaxChunk+10)
		hash := sha256.Sum256(input)
		digest := hash[:]
		v := request("stt")
		v.Kind = "stt"
		v.Text = nil
		v.ProfileId = nil
		v.InputSamples = ptr(uint64(len(input) / 2))
		v.InputDigest = &digest
		status, e := s.Submit(ctx, actor, v, []byte("proof"))
		must(t, e)
		upload := w.SpeechUpload{Ref: status.Ref, Pcm: input[:MaxChunk]}
		status, e = s.Upload(ctx, actor, upload)
		must(t, e)
		if status.State != "uploading" {
			t.Fatal(status)
		}
		_, e = s.Upload(ctx, actor, upload)
		must(t, e)
		wrong := upload
		wrong.Pcm = []byte{0, 0}
		if _, e = s.Upload(ctx, actor, wrong); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		wrong = upload
		wrong.Pcm = bytes.Repeat([]byte{1}, MaxChunk)
		if _, e = s.Upload(ctx, actor, wrong); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
		status, e = s.Upload(ctx, actor, w.SpeechUpload{Ref: status.Ref, Offset: MaxChunk, Pcm: input[MaxChunk:]})
		must(t, e)
		work := claim(t, s, "stt")
		if !bytes.Equal(work.Input, input) {
			t.Fatal("PCM changed")
		}
		must(t, s.Finish(ctx, work, Result{Transcript: "Hello"}, ""))
		status, e = s.Read(ctx, actor, status.Ref)
		must(t, e)
		if status.Transcript == nil || *status.Transcript != "Hello" {
			t.Fatal(status)
		}
	})
	t.Run("completed-audio-history-and-cancellation", func(t *testing.T) {
		s := fresh(t)
		status := submitted(t, s, "audio")
		work := claim(t, s, "tts")
		audio := make([]byte, MaxChunk+4)
		must(t, s.Finish(ctx, work, Result{PCM: audio, SampleRate: 24000}, ""))
		first, e := s.Audio(ctx, actor, w.SpeechReadAudio{Ref: status.Ref})
		must(t, e)
		last, e := s.Audio(ctx, actor, w.SpeechReadAudio{Ref: status.Ref, Offset: MaxChunk})
		must(t, e)
		if len(first.Pcm) != MaxChunk || len(last.Pcm) != 4 {
			t.Fatal("chunk sizes")
		}
		evidence, e := s.Evidence(ctx, actor, status.Ref)
		must(t, e)
		if len(evidence.Observations) != 3 || evidence.Observations[1].Status.State != "running" || evidence.Observations[2].Status.State != "completed" {
			t.Fatal(evidence)
		}
		_, e = s.Cancel(ctx, actor, status.Ref)
		must(t, e)
		if _, e = s.Audio(ctx, actor, w.SpeechReadAudio{Ref: status.Ref}); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	})
	t.Run("worker-loss-never-replays", func(t *testing.T) {
		s := fresh(t)
		status := submitted(t, s, "lost")
		work := claim(t, s, "tts")
		_, e := s.Pool.Exec(ctx, `UPDATE speech_requests SET lease_until=now()-interval '1 second'`)
		must(t, e)
		must(t, s.Sweep(ctx))
		status, e = s.Read(ctx, actor, status.Ref)
		must(t, e)
		if status.State != "failed" || status.ErrorCode != "worker_lost" {
			t.Fatal(status)
		}
		_, ok, e := s.Claim(ctx, "tts")
		must(t, e)
		if ok {
			t.Fatal("replayed")
		}
		if e = s.Finish(ctx, work, Result{PCM: []byte{0, 0}, SampleRate: 24000}, ""); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	})
	t.Run("revocation-and-cross-actor-isolation", func(t *testing.T) {
		s := fresh(t)
		status := submitted(t, s, "private")
		other := actor
		other.Identity.AccountId = "other"
		must(t, s.SetGrant(ctx, Grant{Actor: other, Owner: owner, Allowed: true}))
		if _, e := s.Read(ctx, other, status.Ref); !errors.Is(e, ErrDenied) {
			t.Fatal(e)
		}
		work := claim(t, s, "tts")
		must(t, s.SetGrant(ctx, Grant{Actor: actor, Owner: owner, Allowed: false}))
		if e := s.Renew(ctx, work); !errors.Is(e, ErrDenied) {
			t.Fatal(e)
		}
		if e := s.Finish(ctx, work, Result{PCM: []byte{0, 0}, SampleRate: 24000}, ""); !errors.Is(e, ErrDenied) {
			t.Fatal(e)
		}
	})
	t.Run("admission-quota", func(t *testing.T) {
		s := fresh(t)
		for i := range 16 {
			submitted(t, s, fmt.Sprint("request-", i))
		}
		if _, e := s.Submit(ctx, actor, request("excess"), nil); !errors.Is(e, ErrCapacity) {
			t.Fatal(e)
		}
	})
	t.Run("retention-removes-audio-keeps-tombstone", func(t *testing.T) {
		s := fresh(t)
		status := submitted(t, s, "expiry")
		work := claim(t, s, "tts")
		must(t, s.Finish(ctx, work, Result{PCM: []byte{0, 0}, SampleRate: 24000}, ""))
		current, e := s.Read(ctx, actor, status.Ref)
		must(t, e)
		current.ExpiresAt = time.Now().UTC().Truncate(time.Millisecond).Add(-time.Second)
		_, e = s.Pool.Exec(ctx, `UPDATE speech_requests SET expires_at=$1,status=$2`, current.ExpiresAt, w.EncodeSpeechStatus(current))
		must(t, e)
		must(t, s.Sweep(ctx))
		var size int
		must(t, s.Pool.QueryRow(ctx, `SELECT octet_length(output) FROM speech_requests`).Scan(&size))
		if size != 0 {
			t.Fatal(size)
		}
		if _, e = s.Audio(ctx, actor, w.SpeechReadAudio{Ref: status.Ref}); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
		current, e = s.Read(ctx, actor, status.Ref)
		must(t, e)
		if current.State != "completed" || current.OutputBytes != 2 {
			t.Fatal(current)
		}
	})
	t.Run("standalone-client-over-authenticated-carrier", func(t *testing.T) {
		s := fresh(t)
		pub, key, e := ed25519.GenerateKey(rand.Reader)
		must(t, e)
		service := &Service{Store: s, Keys: keys{pub}}
		server := httptest.NewUnstartedServer(nil)
		handler, e := p.NewRPCHandler("http://"+server.Listener.Addr().String(), service.Dispatch, s.Check)
		must(t, e)
		server.Config.Handler = handler
		server.Start()
		defer server.Close()
		transport, e := NewHTTPTransport(server.URL + "/csil/v1/rpc")
		must(t, e)
		client := standalone.NewSpeechClient(transport)
		packet, e := Sign("submit", w.EncodeSpeechSubmit(request("wire")), actor, receiver, "test-key", key, time.Now().UTC().Truncate(time.Millisecond))
		must(t, e)
		independent, e := standalone.DecodeSignedRecord(w.EncodeSignedRecord(packet))
		must(t, e)
		status, e := client.Submit(ctx, independent)
		must(t, e)
		if status.State != "queued" {
			t.Fatal(status)
		}
		independent.Signature[0] ^= 1
		if _, e = client.Submit(ctx, independent); e == nil {
			t.Fatal("bad proof accepted")
		}
	})
}
