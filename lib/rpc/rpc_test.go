package rpc

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
)

func TestRoundTrip(t *testing.T) {
	kf, _ := authn.GenerateKeyFile("test")
	iss, ver := keys(t, kf)
	s := &Server{Verifier: ver}
	s.Register("EchoService", map[string]func(context.Context, []byte) ([]byte, error){
		"echo": func(ctx context.Context, b []byte) ([]byte, error) {
			p, err := Caller(ctx)
			if err != nil {
				return nil, err
			}
			return append([]byte(p.Subject+":"), b...), nil
		},
		"fail": func(ctx context.Context, b []byte) ([]byte, error) { return nil, NotFound("no %s", "thing") },
	})
	srv := httptest.NewServer(Mux(s, nil))
	defer srv.Close()
	tok, _ := iss.Issue(authn.Principal{Subject: "user:a@x", Kind: authn.KindUser}, time.Minute)
	c := NewClient(srv.URL, StaticToken(tok))
	out, err := c.Call(context.Background(), "EchoService", "echo", []byte("hi"))
	if err != nil || string(out) != "user:a@x:hi" {
		t.Fatalf("echo: %q %v", out, err)
	}
	if _, err := c.Call(context.Background(), "EchoService", "fail", nil); CodeOf(err) != 404 {
		t.Fatalf("fail: %v", err)
	}
	if _, err := NewClient(srv.URL, nil).Call(context.Background(), "EchoService", "echo", nil); CodeOf(err) != 401 {
		t.Fatalf("anon: %v", err)
	}
	if _, err := c.Call(context.Background(), "EchoService", "nope", nil); CodeOf(err) != 404 {
		t.Fatalf("unknown: %v", err)
	}
}

func keys(t *testing.T, kf authn.KeyFile) (*authn.Issuer, *authn.Verifier) {
	dir := t.TempDir()
	path := dir + "/k.json"
	writeJSON(t, path, kf)
	iss, err := authn.LoadIssuer(path)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := authn.LoadVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	return iss, ver
}
