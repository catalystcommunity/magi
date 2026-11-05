package web

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// runtimeCases tests the JSX runtime of the client (src/jsx-runtime.ts) in
// a real browser: the cases of the two bugs of Solid's h runtime that the
// client had (see magi/web/README.md).
const runtimeCases = `
import { createSignal, For, Show } from "solid-js";
import { render } from "solid-js/web";
import { jsx } from "solid-js/h/jsx-runtime";
const out = [];
const el = document.createElement("div");
const [list, setList] = createSignal([1]);
const [flag, setFlag] = createSignal(false);
const [obj, setObj] = createSignal(null);
const [items, setItems] = createSignal([]);
const Comp = () => jsx("section", { children: jsx(For, { each: list, children: (x) => jsx("span", { children: String(x) }) }) });
render(() => jsx("main", { children: [
  jsx(Show, { when: flag, children: jsx("b", { children: "F" }) }),
  jsx("div", { children: jsx(Comp, {}) }),
  jsx(Show, { when: obj, children: jsx("i", { children: [jsx("u", { children: () => obj().t })] }) }),
  jsx(Show, { when: items, children: jsx(Show, { when: () => items().length, fallback: jsx("em", { children: "none" }),
    children: jsx("ul", { children: jsx(For, { each: items, children: (x) => jsx("li", { children: x }) }) }) }) }),
] }), el);
const step = (want) => out.push(el.textContent === want ? "ok" : "got " + JSON.stringify(el.textContent) + ", want " + JSON.stringify(want));
step("1none");
setList([1, 2]); step("12none");
setFlag(true); step("F12none");
setList([1, 2, 3]); step("F123none");       // a sibling Show changed: the For still updates
setObj({ t: "T" }); step("F123Tnone");      // a hidden Show makes its children only when it shows them
setFlag(false); setList([4]); step("4Tnone");
setItems(null); step("4T");
setItems(["a", "b"]); step("4Tab");         // shown again: new nodes, not the disposed ones
setItems(null); setItems([]); step("4Tnone");
setItems(["c"]); step("4Tc");
const bad = out.filter((s) => s !== "ok");
document.body.textContent = bad.length ? "FAIL " + bad.join("; ") : "PASS " + out.length;
`

// vadCases tests the voice activity detector and the WAV encoder.
const vadCases = `
import { Vad } from "/s/@/js/magi/web/src/vad.js";
import { wav } from "/s/@/js/magi/web/src/listen.js";
const out = [];
const check = (ok, what) => out.push(ok ? "ok" : what);
const frame = (db, i) => {
  const f = new Float32Array(320), a = Math.pow(10, db / 20) * Math.SQRT2;
  for (let k = 0; k < f.length; k++) f[k] = a * Math.sin(2 * Math.PI * 220 * (i * 320 + k) / 16000);
  return f;
};
const run = (parts) => {
  const v = new Vad(), ev = [];
  let i = 0;
  for (const [ms, db] of parts) for (let t = 0; t < ms; t += 20, i++) { const e = v.push(frame(db, i)); if (e) ev.push(e + "@" + i * 20); }
  return ev.join(" ");
};
// Quiet room, then 1.5 s of speech, then quiet: onset, turn, end.
let ev = run([[1000, -70], [1500, -20], [1200, -70]]);
check(ev === "onset@1100 turn@1680 end@3280", "speech: " + ev);
// A short sound: onset, then cancel.
ev = run([[1000, -70], [200, -20], [1200, -70]]);
check(ev === "onset@1100 cancel@1980", "short: " + ev);
// Loud but steady noise is the floor, not speech.
ev = run([[3000, -40]]);
check(ev === "", "steady noise: " + ev);
// A short pause in a sentence does not end it.
ev = run([[1000, -70], [600, -20], [400, -70], [600, -20], [1200, -70]]);
check(ev === "onset@1100 turn@2080 end@3380", "pause: " + ev);
// WAV: 0.5 s at 48 kHz becomes 8,000 samples at 16 kHz.
const w = wav([new Float32Array(24000).fill(0.5)], 48000);
const dv = new DataView(w.buffer);
check(w.length === 44 + 16000 && dv.getUint32(24, true) === 16000 && dv.getInt16(44, true) === 16383, "wav: " + w.length);
const bad = out.filter((s) => s !== "ok");
document.body.textContent = bad.length ? "FAIL " + bad.join("; ") : "PASS " + out.length;
`

// micCase starts hands-free listening with the fake microphone of
// Chromium (a WAV file with a tone) and waits for one turn.
const micCase = `
import { startListening } from "/s/@/js/magi/web/src/listen.js";
document.body.textContent = "WAITING";
const timer = setTimeout(() => { document.body.textContent = "FAIL no turn"; }, 15000);
startListening((w) => {
  clearTimeout(timer);
  const s = new DataView(w.buffer).getUint32(40, true) / 32000;
  document.body.textContent = s > 1 && s < 3 ? "PASS turn of " + s.toFixed(2) + " s" : "FAIL turn of " + s + " s";
}).catch((e) => { document.body.textContent = "FAIL " + e; });
`

// toneWAV is 1 s of quiet, 1.5 s of a 220 Hz tone, and 2 s of quiet, at 48 kHz.
func toneWAV() []byte {
	const rate = 48000
	n := rate * 9 / 2
	b := make([]byte, 44+2*n)
	copy(b, "RIFF")
	le := func(at int, v uint32, size int) {
		for i := 0; i < size; i++ {
			b[at+i] = byte(v >> (8 * i))
		}
	}
	le(4, uint32(36+2*n), 4)
	copy(b[8:], "WAVEfmt ")
	le(16, 16, 4)
	le(20, 1, 2)
	le(22, 1, 2)
	le(24, rate, 4)
	le(28, rate*2, 4)
	le(32, 2, 2)
	le(34, 16, 2)
	copy(b[36:], "data")
	le(40, uint32(2*n), 4)
	for i := 0; i < n; i++ {
		v := 0.0
		if i >= rate && i < rate*5/2 {
			v = 0.3 * math.Sin(2*math.Pi*220*float64(i)/rate)
		}
		le(44+2*i, uint32(uint16(int16(v*32767))), 2)
	}
	return b
}

func browser() string {
	if b := os.Getenv("MAGI_TEST_BROWSER"); b != "" {
		return b
	}
	for _, b := range []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return ""
}

func TestRuntimeInBrowser(t *testing.T) {
	if got := runInBrowser(t, runtimeCases); !strings.HasPrefix(got, "PASS") {
		t.Fatalf("runtime cases: %s", got)
	} else {
		t.Log(got)
	}
}

func TestVoiceInBrowser(t *testing.T) {
	if got := runInBrowser(t, vadCases); !strings.HasPrefix(got, "PASS") {
		t.Fatalf("vad cases: %s", got)
	}
	tone := filepath.Join(t.TempDir(), "tone.wav")
	os.WriteFile(tone, toneWAV(), 0o644)
	if got := runInBrowser(t, micCase, "--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream",
		"--use-file-for-fake-audio-capture="+tone+"%noloop", "--autoplay-policy=no-user-gesture-required"); !strings.HasPrefix(got, "PASS") {
		t.Fatalf("microphone: %s", got)
	} else {
		t.Log(got)
	}
}

// runInBrowser runs script (a module, with the import map of the client)
// in headless Chromium or Chrome and returns the text that the script puts
// in the body: the page sends the body to the test server when it changes
// from RUNNING or WAITING. The script can name files of the client as
// /s/@/PATH.
func runInBrowser(t *testing.T, script string, flags ...string) string {
	t.Helper()
	b := browser()
	if b == "" {
		t.Skip("no Chromium or Chrome (set MAGI_TEST_BROWSER)")
	}
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	m := regexp.MustCompile(`(?s)<script type="importmap">.*?</script>`).FindString(rec.Body.String())
	prefix := regexp.MustCompile(`/s/[0-9a-f]{16}/`).FindString(m)
	if m == "" || prefix == "" {
		t.Fatal("no import map in index.html")
	}
	result := make(chan string, 1)
	report := `new MutationObserver(() => { const t = document.body.textContent;
  if (t !== "RUNNING" && t !== "WAITING") fetch("/result", { method: "POST", body: t }); }).observe(document.body, { childList: true, characterData: true, subtree: true });`
	mux := http.NewServeMux()
	mux.Handle("/", h)
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><html><head>" + m + `<script type="module">` + strings.ReplaceAll(script, "/s/@/", prefix) +
			"</script></head><body>RUNNING<script>" + report + "</script></body></html>"))
	})
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case result <- string(b):
		default:
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	// Not t.TempDir: helper processes of the browser can still write to
	// the profile when the test ends.
	profile, err := os.MkdirTemp("", "magi-browser-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { time.Sleep(200 * time.Millisecond); os.RemoveAll(profile) }()
	args := append([]string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir=" + profile}, flags...)
	cmd := exec.CommandContext(ctx, b, append(args, srv.URL+"/test")...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	defer func() { cancel(); cmd.Wait() }()
	select {
	case got := <-result:
		return got
	case <-time.After(30 * time.Second):
		return "no result in 30 s"
	}
}
