package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	b := browser()
	if b == "" {
		t.Skip("no Chromium or Chrome (set MAGI_TEST_BROWSER)")
	}
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	m := regexp.MustCompile(`(?s)<script type="importmap">.*?</script>`).FindString(rec.Body.String())
	if m == "" {
		t.Fatal("no import map in index.html")
	}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	mux.HandleFunc("/runtime-test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><html><head>" + m + `<script type="module">` + runtimeCases + "</script></head><body>RUNNING</body></html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, b, "--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+t.TempDir(), "--virtual-time-budget=5000", "--dump-dom", srv.URL+"/runtime-test").Output()
	if err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	body := regexp.MustCompile(`(?s)<body>(.*)</body>`).FindStringSubmatch(string(out))
	if body == nil || !strings.HasPrefix(body[1], "PASS") {
		t.Fatalf("runtime cases: %s", out)
	}
	t.Log(body[1])
}
