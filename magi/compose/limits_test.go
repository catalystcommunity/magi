package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/magi/model"
)

func testLimits(l Limits) (*System, *time.Time) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	s := &System{Cfg: Config{Limits: l}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.limits = newLimiter(s.Log)
	s.limits.now = func() time.Time { return now }
	return s, &now
}

func TestModelLimitReachedAndWindowReset(t *testing.T) {
	s, now := testLimits(Limits{ModelPerMinute: 3})
	for i := 0; i < 3; i++ {
		if err := s.allowModel("user:a"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	err := s.allowModel("user:a")
	if !isLimit(err) || !strings.Contains(limitMessage(err), "at most 3 model calls per minute") || !strings.Contains(limitMessage(err), "Try again in") {
		t.Fatalf("want a clear 429, got %v", err)
	}
	if isLimit(s.allowModel("user:a")) != true {
		t.Fatal("still limited")
	}
	*now = now.Add(61 * time.Second)
	if err := s.allowModel("user:a"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestLimitsPerUserAndDaily(t *testing.T) {
	s, now := testLimits(Limits{ModelPerMinute: 1, ModelPerDay: 2, TasksPerHour: 1})
	if s.allowModel("user:a") != nil || s.allowModel("user:b") != nil {
		t.Fatal("each user has their own count")
	}
	if !isLimit(s.allowModel("user:a")) {
		t.Fatal("a is over the minute limit")
	}
	*now = now.Add(2 * time.Minute)
	if s.allowModel("user:a") != nil {
		t.Fatal("second call of the day")
	}
	*now = now.Add(2 * time.Minute)
	if !isLimit(s.allowModel("user:a")) {
		t.Fatal("daily limit")
	}
	if s.allowTask("user:a") != nil || !isLimit(s.allowTask("user:a")) || s.allowTask("user:b") != nil {
		t.Fatal("task limit is per user")
	}
	// Model and task counts are separate.
	if s.allowModel("user:b") == nil && s.allowTask("user:c") != nil {
		t.Fatal("separate kinds")
	}
}

func TestLimitsOffAndNil(t *testing.T) {
	s, _ := testLimits(Limits{})
	for i := 0; i < 1000; i++ {
		if s.allowModel("user:a") != nil || s.allowTask("user:a") != nil {
			t.Fatal("0 turns limits off")
		}
	}
	if (&System{}).allowModel("user:a") != nil || (&System{}).allowTask("user:a") != nil {
		t.Fatal("no limiter, no limit")
	}
}

func TestLimitsFromEnv(t *testing.T) {
	l, err := limitsFromEnv()
	if err != nil || l != DefaultLimits() {
		t.Fatalf("defaults: %+v %v", l, err)
	}
	t.Setenv("MAGI_LIMIT_MODEL_PER_MINUTE", "7")
	t.Setenv("MAGI_LIMIT_TASKS_PER_DAY", "0")
	l, err = limitsFromEnv()
	if err != nil || l.ModelPerMinute != 7 || l.TasksPerDay != 0 || l.ModelPerDay != 5000 {
		t.Fatalf("%+v %v", l, err)
	}
	t.Setenv("MAGI_LIMIT_MODEL_PER_DAY", "lots")
	if _, err = limitsFromEnv(); err == nil {
		t.Fatal("a bad value is an error")
	}
}

// A background call is charged to the person it is for, and a person over
// the limit gets no model call.
type countChat struct{ n int }

func (c *countChat) Chat(context.Context, []model.Message, []model.Tool, int) (model.Message, error) {
	c.n++
	return model.Message{}, nil
}
func (c *countChat) ChatSchema(context.Context, []model.Message, map[string]any, int) (model.Message, error) {
	c.n++
	return model.Message{Content: `{"sentence":0}`}, nil
}

func TestBackgroundCallAttributedToPerson(t *testing.T) {
	s, _ := testLimits(Limits{ModelPerMinute: 1})
	m := &countChat{}
	s.LibrarianModel = m
	d := &delivery{}
	sentences := []span{{0, 5}}
	s.answeredSentence(context.Background(), "user:a", "Hello", sentences, d, "stop")
	s.answeredSentence(context.Background(), "user:a", "Hello", sentences, d, "stop")
	if m.n != 1 {
		t.Fatalf("second call of user:a should be refused, model calls: %d", m.n)
	}
	s.answeredSentence(context.Background(), "user:b", "Hello", sentences, d, "stop")
	if m.n != 2 {
		t.Fatalf("user:b has their own limit, model calls: %d", m.n)
	}
}

func TestRunnerProxyChargesRunOwner(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer provider.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	s, _ := testLimits(Limits{ModelPerMinute: 2})
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: provider.URL, Model: "m"}, s))
	defer proxy.Close()
	call := func(runner string) (int, string) {
		tok, _ := iss.Issue(authn.Principal{Subject: "runner:" + runner, Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
		req, _ := http.NewRequest("POST", proxy.URL, strings.NewReader(`{"messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var e struct{ Error struct{ Message string } }
		json.NewDecoder(resp.Body).Decode(&e)
		return resp.StatusCode, e.Error.Message
	}
	// Two runners work for the same person: they share one count.
	s.runnerOwner.set("r1", "user:a")
	s.runnerOwner.set("r2", "user:a")
	s.runnerOwner.set("r3", "user:b")
	if c, _ := call("r1"); c != 200 {
		t.Fatal(c)
	}
	if c, _ := call("r2"); c != 200 {
		t.Fatal(c)
	}
	c, msg := call("r1")
	if c != http.StatusTooManyRequests || !strings.Contains(msg, "limit reached") {
		t.Fatalf("owner a over the limit: %d %q", c, msg)
	}
	if c, _ := call("r3"); c != 200 {
		t.Fatalf("user b is separate: %d", c)
	}
	// A runner without a run is charged as itself; clearing releases the owner.
	if c, _ := call("r9"); c != 200 {
		t.Fatal(c)
	}
	s.runnerOwner.clear("r1")
	if got := s.runnerOwner.of("r1"); got != "runner:r1" {
		t.Fatal(got)
	}
}
