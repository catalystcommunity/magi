package rpc

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// A stopping server finishes open long polls and accepts new requests
// during the drain delay, with connections that close.
func TestServeDrains(t *testing.T) {
	DrainDelay, DrainTimeout = 300*time.Millisecond, 5*time.Second
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	m := Mux(&Server{}, nil)
	m.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		w.Write([]byte("done"))
	})
	m.HandleFunc("/quick", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("quick")) })
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, l, m) }()

	get := func(path string) (int, string, error) {
		resp, err := http.Get(base + path)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), nil
	}
	if code, _, err := get("/quick"); err != nil || code != 200 {
		t.Fatalf("before stop: %d %v", code, err)
	}
	poll := make(chan string, 1)
	go func() {
		code, body, err := get("/poll")
		if err != nil {
			body = err.Error()
		} else if code != 200 {
			body = http.StatusText(code)
		}
		poll <- body
	}()
	time.Sleep(100 * time.Millisecond)
	stop()
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Get(base + "/quick")
	if err != nil || resp.StatusCode != 200 || !resp.Close {
		t.Fatalf("request while draining: %v %v (must be 200 with Connection: close)", resp, err)
	}
	resp.Body.Close()
	if got := <-poll; got != "done" {
		t.Fatalf("open long poll: %q", got)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if _, _, err := get("/quick"); err == nil {
		t.Fatal("the server accepts requests after the drain")
	}
}
