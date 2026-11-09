package envd

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
)

func TestFrames(t *testing.T) {
	var b bytes.Buffer
	WriteFrame(&b, FrameData, []byte("hello"))
	WriteFrame(&b, FrameResize, ResizePayload(120, 40))
	WriteFrame(&b, FrameExit, ExitPayload(-3))
	WriteFrame(&b, FramePing, nil)
	if err := WriteFrame(&b, FrameData, make([]byte, MaxFrame+1)); err != ErrFrameTooLarge {
		t.Fatalf("large frame: %v", err)
	}
	typ, p, err := ReadFrame(&b)
	if err != nil || typ != FrameData || string(p) != "hello" {
		t.Fatalf("data: %c %q %v", typ, p, err)
	}
	typ, p, _ = ReadFrame(&b)
	if c, r, err := ParseResize(p); typ != FrameResize || c != 120 || r != 40 || err != nil {
		t.Fatalf("resize: %c %d %d %v", typ, c, r, err)
	}
	typ, p, _ = ReadFrame(&b)
	if code, err := ParseExit(p); typ != FrameExit || code != -3 || err != nil {
		t.Fatalf("exit: %c %d %v", typ, code, err)
	}
	if typ, p, err = ReadFrame(&b); typ != FramePing || len(p) != 0 || err != nil {
		t.Fatalf("ping: %c %q %v", typ, p, err)
	}
	if _, _, err := ReadFrame(&b); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
	// A cut frame and a length above the limit are errors.
	if _, _, err := ReadFrame(bytes.NewReader([]byte{'d', 0, 0, 0, 9, 'a'})); err != io.ErrUnexpectedEOF {
		t.Fatalf("cut frame: %v", err)
	}
	if _, _, err := ReadFrame(bytes.NewReader([]byte{'d', 0, 2, 0, 0})); err != ErrFrameTooLarge {
		t.Fatalf("large length: %v", err)
	}
	if _, _, err := ParseResize([]byte{1}); err == nil {
		t.Fatal("short resize payload")
	}
}

// ptyReader collects the output of a terminal session.
type ptyReader struct {
	out  bytes.Buffer
	exit *int32
	err  error
}

// until reads frames until the output has want, or the session ends.
func (p *ptyReader) until(t *testing.T, r io.Reader, want string) {
	t.Helper()
	for !strings.Contains(p.out.String(), want) {
		typ, b, err := ReadFrame(r)
		if err != nil {
			t.Fatalf("waiting for %q: %v; output %q", want, err, p.out.String())
		}
		switch typ {
		case FrameData:
			p.out.Write(b)
		case FrameExit:
			c, _ := ParseExit(b)
			p.exit = &c
			if !strings.Contains(p.out.String(), want) {
				t.Fatalf("exit %d before %q; output %q", c, want, p.out.String())
			}
		}
	}
}

func TestPTY(t *testing.T) {
	c, dir := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := c.PTY(ctx, PTYRequest{Cols: 80, Rows: 24, Env: map[string]string{"PS1": "$ "}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var p ptyReader
	send := func(s string) {
		if err := WriteFrame(conn, FrameData, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	// The input echo has "$((40+2))", the output has 42.
	send("echo hi-$((40+2)); pwd\n")
	p.until(t, conn, "hi-42")
	p.until(t, conn, dir)
	if err := WriteFrame(conn, FrameResize, ResizePayload(100, 30)); err != nil {
		t.Fatal(err)
	}
	send("echo size-$(stty size | tr ' ' x)\n")
	p.until(t, conn, "size-30x100")
	send("echo term-$TERM; exit 7\n")
	p.until(t, conn, "term-xterm-256color")
	for p.exit == nil {
		typ, b, err := ReadFrame(conn)
		if err != nil {
			t.Fatalf("no exit frame: %v; output %q", err, p.out.String())
		}
		if typ == FrameExit {
			c, _ := ParseExit(b)
			p.exit = &c
		}
	}
	if *p.exit != 7 {
		t.Fatalf("exit %d, want 7", *p.exit)
	}
	if _, _, err := ReadFrame(conn); err == nil {
		t.Fatal("a frame after the exit frame")
	}
}

// When the client goes away, envd ends the shell.
func TestPTYHangup(t *testing.T) {
	c, dir := setup(t)
	ctx := context.Background()
	conn, err := c.PTY(ctx, PTYRequest{Argv: []string{"sh", "-c", "echo ready; trap '' HUP; echo $$ > pid; sleep 60"}})
	if err != nil {
		t.Fatal(err)
	}
	var p ptyReader
	p.until(t, conn, "ready")
	conn.Close()
	// SIGHUP is ignored, so envd sends SIGKILL after the grace time.
	deadline := time.Now().Add(ptyGrace + 5*time.Second)
	for time.Now().Before(deadline) {
		r, _ := c.Exec(ctx, api.ExecRequest{Argv: []string{"sh", "-c", "kill -0 $(cat " + dir + "/pid) 2>/dev/null && echo alive || echo gone"}})
		if strings.TrimSpace(string(r.Stdout)) == "gone" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the shell runs after the client went away")
}

func TestPTYErrors(t *testing.T) {
	c, _ := setup(t)
	ctx := context.Background()
	if _, err := c.PTY(ctx, PTYRequest{Argv: []string{"/no/such/program"}}); code(err) != http.StatusBadRequest {
		t.Fatalf("unknown program: %v", err)
	}
	bad := &Client{Base: c.Base, Token: "wrong"}
	if _, err := bad.PTY(ctx, PTYRequest{}); code(err) != http.StatusUnauthorized {
		t.Fatalf("bad token: %v", err)
	}
	// Without the upgrade header, the route answers 400.
	req, _ := http.NewRequest("POST", c.Base+PathPTY, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("no upgrade: %d", res.StatusCode)
	}
}
