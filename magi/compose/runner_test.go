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

func TestRunnerName(t *testing.T) {
	for _, c := range []struct {
		p    authn.Principal
		want string
	}{
		{authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, "ht0"},
		{authn.Principal{Subject: "runner:ht0", Kind: authn.KindService}, ""},                                  // no scope
		{authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, ""}, // not a runner
		{authn.Principal{Subject: "runner:ht0", Kind: authn.KindUser, Scopes: []string{ScopeRunner}}, ""},      // a user
	} {
		got, err := runnerName(authn.With(context.Background(), c.p))
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("%+v: %q %v", c.p, got, err)
		}
	}
}

// The proxy needs a runner token, uses Magi's provider key and model, and
// never passes the runner token to the provider.
func TestModelProxy(t *testing.T) {
	var seen struct{ auth, model string }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		seen.auth, seen.model = r.Header.Get("Authorization"), body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer provider.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: provider.URL, Model: "coder-model", APIKey: "provider-key"}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer proxy.Close()
	call := func(tok string) int {
		req, _ := http.NewRequest("POST", proxy.URL, strings.NewReader(`{"model":"anything","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	user, _ := iss.Issue(authn.Principal{Subject: "user:a@example.local", Kind: authn.KindUser, Scopes: []string{ScopeRunner}}, time.Hour)
	runner, _ := iss.Issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
	if code := call(""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code := call(user); code != 401 {
		t.Fatalf("user token: %d", code)
	}
	if code := call(runner); code != 200 || seen.auth != "Bearer provider-key" || seen.model != "coder-model" {
		t.Fatalf("runner: %d %+v", code, seen)
	}
}

// TestModelProxyStreams: the runner gets each chunk of a streamed answer
// when the provider sends it, not at the end. Else the idle limit of the
// apprentice cannot tell a slow answer from a provider that stopped.
func TestModelProxyStreams(t *testing.T) {
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	defer close(release)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	proxy := httptest.NewServer(ModelProxy(ver, &model.Client{BaseURL: provider.URL, Model: "m"}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer proxy.Close()
	runner, _ := iss.Issue(authn.Principal{Subject: "runner:ht0", Kind: authn.KindService, Scopes: []string{ScopeRunner}}, time.Hour)
	req, _ := http.NewRequest("POST", proxy.URL, strings.NewReader(`{"stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+runner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 100)
		n, _ := resp.Body.Read(b)
		got <- string(b[:n])
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, `"content":"a"`) {
			t.Fatalf("first chunk: %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk did not come before the end of the answer")
	}
}
