package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	identityapi "github.com/catalystcommunity/magi/primitives/identity/api"
)

// The refresh call goes to IdentityService/refresh with the current token.
func TestRefreshAt(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		b, err := identityapi.EncodeTokenResponse(identityapi.TokenResponse{Token: "magi1.new.sig", Subject: "inference:box", ExpiresMs: uint64(exp.UnixMilli())})
		if err != nil {
			t.Error(err)
		}
		w.Write(b)
	}))
	defer srv.Close()
	got, err := refreshAt(srv.URL)(context.Background(), "magi1.old.sig")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/rpc/IdentityService/refresh" || auth != "Bearer magi1.old.sig" {
		t.Fatalf("request %q %q", path, auth)
	}
	if got.Token != "magi1.new.sig" || got.Subject != "inference:box" || !got.Expires.Equal(exp) {
		t.Fatalf("result %+v", got)
	}
}
