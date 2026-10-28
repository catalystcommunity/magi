package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestChainFallsBack(t *testing.T) {
	var localCalls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(503)
		w.Write([]byte(`{"error":{"type":"host_busy","message":"the host is busy: process rustc is running"}}`))
	}))
	defer local.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer remote-key" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer remote.Close()
	c := Chain{
		{BaseURL: local.URL, Model: "local", Attempts: 1, HTTP: local.Client()},
		{BaseURL: remote.URL, Model: "remote", APIKey: "remote-key", HTTP: remote.Client()},
	}
	m, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, 10)
	if err != nil || m.Content != "hi" {
		t.Fatalf("chat = %+v, %v", m, err)
	}
	if n := localCalls.Load(); n != 1 {
		t.Fatalf("local tried %d times; one try is enough to fall back", n)
	}
}
