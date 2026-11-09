package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"
)

// syncBuf is a buffer that two goroutines can use.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }

// The CLI sends its token in the header and its size in the hello,
// copies input and output, and returns the exit status of the shell.
func TestTerminalCommand(t *testing.T) {
	var got struct {
		auth, task string
		hello      map[string]int
		input      string
	}
	srv := httptest.NewServer(websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			got.auth, got.task = ws.Request().Header.Get("Authorization"), ws.Request().URL.Query().Get("task")
			var b []byte
			websocket.Message.Receive(ws, &b)
			json.Unmarshal(b, &got.hello)
			websocket.Message.Send(ws, []byte("dwelcome\r\n"))
			websocket.Message.Receive(ws, &b)
			got.input = string(b)
			websocket.Message.Send(ws, []byte{'x', 0, 0, 0, 9})
		}})
	defer srv.Close()
	r, w, _ := os.Pipe()
	defer r.Close()
	w.WriteString("ls\n")
	var out syncBuf
	c := &cli{p: profile{Server: srv.URL, Token: "magi1.tok"}}
	code, err := c.terminal("run_1", r, &out)
	if err != nil || code != 9 {
		t.Fatalf("code %d, err %v", code, err)
	}
	if got.auth != "Bearer magi1.tok" || got.task != "run_1" || got.hello["cols"] == 0 || got.input != "dls\n" {
		t.Fatalf("server saw %+v", got)
	}
	if !strings.Contains(out.b.String(), "welcome") {
		t.Fatalf("output %q", out.b.String())
	}
	// An error frame becomes the error of the command.
	srv2 := httptest.NewServer(websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			var b []byte
			websocket.Message.Receive(ws, &b)
			websocket.Message.Send(ws, []byte("eonly the owner can open it"))
		}})
	defer srv2.Close()
	c.p.Server = srv2.URL
	if _, err := c.terminal("run_1", r, &out); err == nil || err.Error() != "only the owner can open it" {
		t.Fatalf("error frame: %v", err)
	}
}
