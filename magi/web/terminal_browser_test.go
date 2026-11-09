package web

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/websocket"
)

// terminalCase opens the terminal of the client (xterm.js from the vendor
// directory) against a fake /api/terminal: the hello, input, a resize,
// output in the terminal buffer, the extra keys, and the exit status.
const terminalCase = `
import { openTerminal, ctrl, extraKeys } from "/s/@/js/magi/web/src/terminal.js";
const out = [];
const check = (ok, what) => out.push(ok ? "ok" : what);
check(ctrl("c") === "\x03" && ctrl("[") === "\x1b" && ctrl("1") === null, "ctrl keys");
const up = extraKeys.find((k) => k.name === "Up");
check(up.seq(false) === "\x1b[A" && up.seq(true) === "\x1bOA", "arrow keys");
document.body.textContent = "WAITING";
// Outside the body: the test reads the text of the body.
const box = document.createElement("div");
box.style.cssText = "width: 700px; height: 320px";
document.documentElement.append(box);
const states = [];
let t;
const text = () => { const b = t.term.buffer.active, l = []; for (let i = 0; i < b.length; i++) l.push(b.getLine(i).translateToString(true)); return l.join("\n"); };
const wait = async (f, what) => { for (let i = 0; i < 100 && !f(); i++) await new Promise((r) => setTimeout(r, 50)); if (!f()) throw new Error("timeout: " + what + "; screen " + JSON.stringify(text()) + "; states " + JSON.stringify(states)); };
try {
  t = await openTerminal(box, "ws://" + location.host + "/api/terminal?task=run_x", () => "tok-1", (s, st) => states.push(st + ": " + s));
  check(t.term.cols > 40 && t.term.rows > 5, "fit: " + t.term.cols + "x" + t.term.rows);
  await wait(() => text().includes("ready$"), "the first output");
  check(states.some((s) => s.startsWith("open")), "open state");
  t.session.input("ls\r");
  await wait(() => text().includes("got:ls"), "the echo");
  t.session.ctrlNext = true;
  t.session.input("c");
  await wait(() => text().includes("got:^C"), "ctrl c");
  t.term.resize(100, 30);
  await wait(() => text().includes("size:100x30"), "the resize");
  t.session.input("exit\r");
  await wait(() => states.some((s) => s.startsWith("closed")), "the exit");
  check(states[states.length - 1] === "closed: The shell exited with code 3.", "exit: " + states[states.length - 1]);
  check(getComputedStyle(box.querySelector(".xterm")).position === "relative", "xterm.css loaded");
} catch (e) { out.push(String(e)); }
const bad = out.filter((x) => x !== "ok");
document.body.textContent = bad.length ? "FAIL " + bad.join("; ") : "PASS " + out.length;
`

func TestTerminalInBrowser(t *testing.T) {
	var hello struct {
		Token      string `json:"token"`
		Cols, Rows int
	}
	fake := websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(ws *websocket.Conn) {
		var b []byte
		if websocket.Message.Receive(ws, &b) != nil || json.Unmarshal(b, &hello) != nil || hello.Token != "tok-1" || hello.Cols == 0 {
			websocket.Message.Send(ws, []byte("ebad hello "+string(b)))
			return
		}
		websocket.Message.Send(ws, []byte("dready$ "))
		for {
			if err := websocket.Message.Receive(ws, &b); err != nil || len(b) == 0 {
				return
			}
			switch b[0] {
			case 'd':
				in := strings.TrimRight(string(b[1:]), "\r")
				if in == "exit" {
					websocket.Message.Send(ws, []byte{'x', 0, 0, 0, 3})
					return
				}
				if in == "\x03" {
					in = "^C"
				}
				websocket.Message.Send(ws, []byte("d\r\ngot:"+in+"\r\n"))
			case 'r':
				if len(b) == 5 {
					websocket.Message.Send(ws, []byte(fmt.Sprintf("dsize:%dx%d\r\n", binary.BigEndian.Uint16(b[1:3]), binary.BigEndian.Uint16(b[3:5]))))
				}
			}
		}
	}}
	got := runInBrowserWith(t, terminalCase, map[string]http.Handler{"/api/terminal": fake})
	if !strings.HasPrefix(got, "PASS") {
		t.Fatalf("terminal: %s", got)
	}
	t.Log(got)
}
