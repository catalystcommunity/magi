package rpc

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
)

func TestServerErrorsAreGeneric(t *testing.T) {
	kf, _ := authn.GenerateKeyFile("test")
	iss, ver := keys(t, kf)
	s := &Server{Verifier: ver}
	secret := "postgres://user:pw@db.internal:5432/x"
	s.Register("S", map[string]func(context.Context, []byte) ([]byte, error){
		"plain": func(context.Context, []byte) ([]byte, error) { return nil, errors.New(secret) },
		"unavail": func(context.Context, []byte) ([]byte, error) {
			return nil, Unavailable("engine %s unreachable: %s", "http://10.0.0.7:9000", secret)
		},
		"bad":  func(context.Context, []byte) ([]byte, error) { return nil, Invalid("name is empty") },
		"rate": func(context.Context, []byte) ([]byte, error) { return nil, Errorf(429, "try again in 7 s") },
	})
	srv := httptest.NewServer(Mux(s, nil))
	defer srv.Close()
	tok, _ := iss.Issue(authn.Principal{Subject: "user:a@x", Kind: authn.KindUser}, time.Minute)
	c := NewClient(srv.URL, StaticToken(tok))
	for _, op := range []string{"plain", "unavail"} {
		_, err := c.Call(context.Background(), "S", op, nil)
		if CodeOf(err) < 500 || strings.Contains(err.Error(), "db.internal") || strings.Contains(err.Error(), "10.0.0.7") || !strings.Contains(err.Error(), "ref ") {
			t.Fatalf("%s: %v", op, err)
		}
	}
	// Errors about the caller's own request keep their text.
	if _, err := c.Call(context.Background(), "S", "bad", nil); !strings.Contains(err.Error(), "name is empty") {
		t.Fatalf("bad: %v", err)
	}
	if _, err := c.Call(context.Background(), "S", "rate", nil); !strings.Contains(err.Error(), "7 s") {
		t.Fatalf("rate: %v", err)
	}
}

func TestHealthzNotReadyHidesDetail(t *testing.T) {
	m := Mux(&Server{}, func() error { return errors.New("dial tcp 10.1.2.3:5432: refused") })
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	b, _ := io.ReadAll(rec.Body)
	if rec.Code != 503 || strings.Contains(string(b), "10.1.2.3") {
		t.Fatalf("%d %q", rec.Code, b)
	}
}
