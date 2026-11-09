package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/websocket"
	"golang.org/x/term"
)

// The terminal messages (primitives/execution/envd/frame.go): the type
// byte and the payload. The CLI does not import envd: envd builds only for
// Linux and macOS.
const (
	frameData   = 'd'
	frameResize = 'r'
	frameExit   = 'x'
	frameError  = 'e'
	maxFrame    = 64 << 10
)

// terminal opens a shell in the sandbox of a task through
// /api/terminal on the Magi server (see magi/compose/terminal.go). The
// local terminal is in raw mode: keys such as Ctrl-C go to the shell. The
// session ends when the shell exits; terminal returns the exit status of
// the shell.
func (c *cli) terminal(task string, stdin *os.File, stdout io.Writer) (int, error) {
	u, err := url.Parse(c.p.Server)
	if err != nil {
		return 0, err
	}
	origin := u.Scheme + "://" + u.Host
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return 0, fmt.Errorf("server %q: expected http or https", c.p.Server)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/terminal"
	u.RawQuery = url.Values{"task": {task}}.Encode()
	cfg, err := websocket.NewConfig(u.String(), origin)
	if err != nil {
		return 0, err
	}
	cfg.Header.Set("Authorization", "Bearer "+c.p.Token)
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return 0, fmt.Errorf("open the terminal: %w", err)
	}
	defer ws.Close()
	ws.MaxPayloadBytes = maxFrame + 1
	in, out := int(stdin.Fd()), int(os.Stdout.Fd())
	cols, rows := 80, 24
	if w, h, err := term.GetSize(out); err == nil {
		cols, rows = w, h
	}
	hello, _ := json.Marshal(map[string]int{"cols": cols, "rows": rows})
	if err := websocket.Message.Send(ws, string(hello)); err != nil {
		return 0, err
	}
	restore := func() {}
	if term.IsTerminal(in) {
		if st, err := term.MakeRaw(in); err == nil {
			restore = func() { term.Restore(in, st) }
		}
	}
	defer restore()
	send := func(typ byte, p []byte) error { return websocket.Message.Send(ws, append([]byte{typ}, p...)) }
	stopResize := watchResize(out, func(w, h int) {
		send(frameResize, binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, uint16(w)), uint16(h)))
	})
	defer stopResize()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := stdin.Read(buf)
			if n > 0 && send(frameData, buf[:n]) != nil {
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		var b []byte
		if err := websocket.Message.Receive(ws, &b); err != nil {
			restore()
			return 0, errors.New("the connection to Magi closed")
		}
		if len(b) == 0 {
			continue
		}
		switch b[0] {
		case frameData:
			stdout.Write(b[1:])
		case frameExit:
			code := 0
			if len(b) == 5 {
				code = int(int32(binary.BigEndian.Uint32(b[1:])))
			}
			restore()
			return code & 0xff, nil
		case frameError:
			restore()
			return 0, errors.New(string(b[1:]))
		}
	}
}
