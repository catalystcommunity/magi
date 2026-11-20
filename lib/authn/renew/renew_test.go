package renew

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
)

func issue(t *testing.T, iss *authn.Issuer, at time.Time, ttl time.Duration) string {
	t.Helper()
	i := *iss
	i.Now = func() time.Time { return at }
	tok, err := i.Issue(authn.Principal{Subject: "inference:box", Kind: authn.KindService, Scopes: []string{"inference:host"}}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestRenew(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	iss := &authn.Issuer{Name: "magi", KeyID: "k", Key: key}
	now := time.Now()
	var calls atomic.Int32
	var got atomic.Value
	refresh := func(_ context.Context, current string) (Token, error) {
		calls.Add(1)
		got.Store(current)
		return Token{Token: issue(t, iss, now, 30*24*time.Hour), Subject: "inference:box", Expires: now.Add(30 * 24 * time.Hour)}, nil
	}
	file := filepath.Join(t.TempDir(), "host-token")
	r := &Renewer{File: file, Refresh: refresh, Now: func() time.Time { return now }}

	// A fresh 30-day token is due at 20 days.
	os.WriteFile(file, []byte(issue(t, iss, now, 30*24*time.Hour)+"\n"), 0o600)
	if due, err := r.Due(); err != nil || due.Sub(now) < 19*24*time.Hour || due.Sub(now) > 21*24*time.Hour {
		t.Fatalf("due %v, %v", due, err)
	}
	// A minted one-year token is due now.
	old := issue(t, iss, now.Add(-time.Hour), 365*24*time.Hour)
	os.WriteFile(file, []byte(old+"\n"), 0o600)
	if due, err := r.Due(); err != nil || due.After(now) {
		t.Fatalf("due %v, %v", due, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	for i := 0; i < 200 && calls.Load() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	if got.Load() != old {
		t.Fatal("refresh did not use the current token")
	}
	b, _ := os.ReadFile(file)
	if tok := strings.TrimSpace(string(b)); tok == old || !strings.HasPrefix(tok, "magi1.") {
		t.Fatal("file not replaced")
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

// A result that is not a token does not replace the file.
func TestRenewKeepsFileOnBadResult(t *testing.T) {
	file := filepath.Join(t.TempDir(), "host-token")
	os.WriteFile(file, []byte("magi1.old.sig\n"), 0o600)
	for _, r := range []*Renewer{
		{File: file},
		{File: file, Refresh: func(context.Context, string) (Token, error) { return Token{Token: "<html>"}, nil }},
	} {
		if err := r.Renew(context.Background()); err == nil {
			t.Fatal("no error")
		}
		if b, _ := os.ReadFile(file); string(b) != "magi1.old.sig\n" {
			t.Fatalf("file changed: %q", b)
		}
	}
}
