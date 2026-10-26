//go:build integration

package dialogue

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	cd "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/catalystcommunity/magi/conversation"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/speech"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func must(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func docker(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	raw, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("fixture docker %s: %v: %s", args[0], e, raw)
	}
	return strings.TrimSpace(string(raw))
}
func loopback(t testing.TB) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	must(t, e)
	address := l.Addr().String()
	l.Close()
	return address
}

type fixtureKeys struct {
	actor w.ApplicationRef
	key   ed25519.PublicKey
}

func (k fixtureKeys) Resolve(_ context.Context, a w.ApplicationRef, id string) (identity.VerifiedKey, error) {
	if a != k.actor || id != "local-test-key" {
		return identity.VerifiedKey{}, identity.ErrDenied
	}
	return identity.VerifiedKey{Public: k.key, CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type fixtureSpeech struct{ fail atomic.Bool }

func (b *fixtureSpeech) Process(context.Context, speech.Work) (speech.Result, error) {
	if b.fail.Load() {
		return speech.Result{}, errors.New("fixture failure")
	}
	return speech.Result{PCM: make([]byte, 480), SampleRate: 24000}, nil
}

func TestConnectedWorkflowRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	var random [8]byte
	_, e := rand.Read(random[:])
	must(t, e)
	prefix := "opal-dialogue-test-" + hex.EncodeToString(random[:])
	start := func(suffix, image, port string, env []string) string {
		args := []string{"run", "--pull=never", "-d", "--name", prefix + suffix, "--label", "opal.fixture=" + prefix, "--memory", "512m", "--cpus", "2", "--pids-limit", "256", "-p", "127.0.0.1::" + port}
		for _, v := range env {
			args = append(args, "-e", v)
		}
		args = append(args, image)
		id := docker(t, args...)
		if len(id) != 64 {
			t.Fatal("container ID")
		}
		t.Cleanup(func() {
			if docker(t, "inspect", "-f", `{{index .Config.Labels "opal.fixture"}}`, id) != prefix {
				t.Error("cleanup ownership")
				return
			}
			docker(t, "rm", "-f", "-v", id)
		})
		return docker(t, "port", id, port+"/tcp")
	}
	pg := start("-pg", "postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd", "5432", []string{"POSTGRES_PASSWORD=synthetic-only"})
	queue := start("-cd", configuredCorndogsImage(), "5080", []string{"STORAGE_BACKEND=file", "CORNDOGS_FILESTORE_DIR=/tmp/data", "CORNDOGS_FILESTORE_SYNC=always", "CORNDOGS_SUBMISSION_KEY_POLICY=required", "CORNDOGS_TASK_GUARD_POLICY=required", "LOGLEVEL=error"})
	dsn := "postgres://postgres:synthetic-only@" + pg + "/postgres?sslmode=disable"
	pool, e := pgxpool.New(ctx, dsn)
	must(t, e)
	defer pool.Close()
	until := time.Now().Add(25 * time.Second)
	for {
		e = pool.Ping(ctx)
		if e == nil {
			_, e = cd.NewCorndogsClient(&workflow.Transport{Seeds: []string{queue}}).GetServerInfo(ctx, cd.GetServerInfoRequest{})
		}
		if e == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("storage readiness", e)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var v struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&v) != nil || len(v.Messages) != 2 {
			wr.WriteHeader(400)
			return
		}
		answer := "Four."
		if strings.Contains(v.Messages[1].Content, "week") {
			answer = "Seven."
		}
		json.NewEncoder(wr).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": answer}}}})
	}))
	defer model.Close()
	python, e := exec.LookPath("python3")
	must(t, e)
	python, e = filepath.Abs(python)
	must(t, e)
	dir := filepath.Join(t.TempDir(), "config")
	must(t, question.InitDev(dir, loopback(t), dsn, queue, question.ModelConfig{Endpoint: model.URL + "/v1/chat/completions", Model: "fixture", Python: python, MaxTokens: 128}))
	config, e := question.LoadDevConfig(filepath.Join(dir, "server.json"))
	must(t, e)
	profile, e := question.LoadProfile(filepath.Join(dir, "client.json"))
	must(t, e)
	keys := fixtureKeys{profile.Actor, ed25519.PrivateKey(profile.PrivateKey).Public().(ed25519.PublicKey)}
	graphApplication := profile.Recipient
	graphApplication.ApplicationId = "graph"
	graphStore := &conversation.Store{Pool: pool, Application: graphApplication}
	must(t, graphStore.Initialize(ctx))
	for _, action := range []string{"read", "write"} {
		must(t, graphStore.SetGrant(ctx, conversation.Grant{Actor: profile.Actor, Owner: profile.Owner, Action: action, Allowed: true}))
	}
	gs := httptest.NewUnstartedServer(nil)
	gh, e := protocol.NewRPCHandler("http://"+gs.Listener.Addr().String(), (&conversation.Service{Store: graphStore, Keys: keys}).Dispatch, graphStore.Check)
	must(t, e)
	gs.Config.Handler = gh
	gs.Start()
	defer gs.Close()
	gp := conversation.Profile(profile)
	gp.Endpoint = gs.URL + "/csil/v1/rpc"
	gp.Recipient = graphApplication
	graph, e := conversation.NewClient(gp)
	must(t, e)
	speechApplication := profile.Recipient
	speechApplication.ApplicationId = "speech"
	speechStore := &speech.Store{Pool: pool, Application: speechApplication}
	must(t, speechStore.Initialize(ctx))
	must(t, speechStore.SetGrant(ctx, speech.Grant{Actor: profile.Actor, Owner: profile.Owner, Allowed: true}))
	ss := httptest.NewUnstartedServer(nil)
	sh, e := protocol.NewRPCHandler("http://"+ss.Listener.Addr().String(), (&speech.Service{Store: speechStore, Keys: keys}).Dispatch, speechStore.Check)
	must(t, e)
	ss.Config.Handler = sh
	ss.Start()
	defer ss.Close()
	sp := speech.Profile(profile)
	sp.Endpoint = ss.URL + "/csil/v1/rpc"
	sp.Recipient = speechApplication
	voice, e := speech.NewClient(sp)
	must(t, e)
	backend := &fixtureSpeech{}
	sw := &speech.Worker{Store: speechStore, Kind: "tts", Backend: backend}
	speechCtx, stopSpeech := context.WithCancel(ctx)
	speechDone := make(chan struct{})
	go func() {
		defer close(speechDone)
		for {
			if speechCtx.Err() != nil {
				return
			}
			sw.Tick(speechCtx)
			select {
			case <-speechCtx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	defer func() { stopSpeech(); <-speechDone }()
	serve := func() func() {
		runtime, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		ready := make(chan struct{}, 1)
		go func() {
			done <- question.ServeDevWithWorker(runtime, config, func(s *workflow.Store, c question.WorkerConfig) (*workflow.Worker, error) {
				return NewWorker(s, c, graph, voice)
			}, func(message string) {
				if strings.Contains(message, "API ready") {
					ready <- struct{}{}
				}
			})
		}()
		select {
		case <-ready:
		case e := <-done:
			t.Fatal(e)
		case <-ctx.Done():
			t.Fatal("runtime readiness")
		}
		return func() {
			stop()
			select {
			case e := <-done:
				must(t, e)
			case <-time.After(10 * time.Second):
				t.Fatal("runtime shutdown")
			}
		}
	}
	stop := serve()
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	client, e := NewClient(profile)
	must(t, e)
	wait := func(id string) map[string]string {
		for {
			state, e := client.Read(ctx, id)
			must(t, e)
			if state.Result != nil {
				out := map[string]string{}
				for _, v := range state.Result.Values {
					out[v.Name] = v.Value.(string)
				}
				return out
			}
			for _, step := range state.Steps {
				if step.Phase == "reconcile_required" || step.Phase == "rejected" {
					t.Fatalf("run %s requires investigation: %+v", id, state)
				}
			}
			if ctx.Err() != nil {
				t.Fatal("run timeout")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	v := request()
	v.Text = "What is two plus two?"
	v.Destinations = []Destination{{Session: "arithmetic", Text: v.Text}}
	v.Speak = true
	v.Voice = "af_heart"
	_, e = client.Submit(ctx, "first", v)
	must(t, e)
	first := wait("first")
	if first["answer"] != "Four." || first["speech_id"] != "first-speech" || first["speech_error"] != "" {
		t.Fatal(first)
	}
	for _, step := range []string{"prepare", "answer", "present"} {
		ev, e := client.CallEvidence(ctx, "first", step)
		must(t, e)
		if ev.Outcome != "returned" || ev.Response == nil {
			t.Fatal("missing evidence", step)
		}
	}
	_, e = client.Submit(ctx, "first", v)
	must(t, e)
	wait("first")
	if modelCalls.Load() != 1 {
		t.Fatal("duplicate model invocation")
	}
	v.InteractionID = "shared"
	v.Text = "What was the answer? How many days are in a week?"
	v.Destinations = []Destination{{Session: "arithmetic", Text: "What was the answer?"}, {Session: "calendar", Text: "How many days are in a week?"}}
	v.Speak = false
	for i, id := range []string{"shared-a", "shared-b"} {
		v.Destination = i
		_, e = client.Submit(ctx, id, v)
		must(t, e)
		wait(id)
	}
	ev, e := client.CallEvidence(ctx, "shared-b", "answer")
	must(t, e)
	if bytesContains(ev.Request, "two plus two") {
		t.Fatal("cross-session context leak")
	}
	a, e := graph.History(ctx, "arithmetic", 0)
	must(t, e)
	if len(a.Records) == 0 {
		t.Fatal("empty graph")
	}
	backend.fail.Store(true)
	v.InteractionID = "failed-voice"
	v.Destinations = v.Destinations[:1]
	v.Destination = 0
	v.Speak = true
	_, e = client.Submit(ctx, "fallback", v)
	must(t, e)
	fallback := wait("fallback")
	if fallback["answer"] != "Four." || fallback["speech_error"] == "" {
		t.Fatal("missing text fallback", fallback)
	}
	calls := modelCalls.Load()
	stop()
	stop = nil
	stop = serve()
	if wait("first")["answer"] != "Four." {
		t.Fatal("lost result after restart")
	}
	if modelCalls.Load() != calls {
		t.Fatal("restart repeated model work")
	}
}
func bytesContains(raw []byte, s string) bool { return strings.Contains(string(raw), s) }

func configuredCorndogsImage() string {
	if image := os.Getenv("MAGI_CORNDOGS_IMAGE"); image != "" {
		return image
	}
	return "registry.catalystsquad.local/corndogs@sha256:e614bf3f88dd37d40ec7c5488bc66c06be3efbd73576eef5adfe1b9da1c64a70"
}
