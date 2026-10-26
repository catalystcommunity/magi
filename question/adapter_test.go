package question

import (
	"bytes"
	"context"
	"encoding/json"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/workflow"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testOwner() w.ScopeRef {
	return w.ScopeRef{Authority: w.IdentityRef{Domain: "local.opal.test", AccountId: "test-human"}, ScopeId: "test"}
}

func TestModelConfig(t *testing.T) {
	c := ModelConfig{Endpoint: "http://127.0.0.1:8088/v1/chat/completions", Model: "tiny", Python: "/usr/bin/python3", MaxTokens: 128}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, endpoint := range []string{"http://external.example/v1/chat/completions", "http://localhost:8088/v1/chat/completions", "https://user:secret@example.test/v1/chat/completions", "https://example.test/v1/chat/completions?token=secret", "https://example.test/v1/chat/completions#secret", "file:///etc/passwd"} {
		t.Run(endpoint, func(t *testing.T) {
			bad := c
			bad.Endpoint = endpoint
			if bad.Validate() == nil {
				t.Fatal("unsafe configuration")
			}
		})
	}
	for name, change := range map[string]func(*ModelConfig){"relative-python": func(c *ModelConfig) { c.Python = "python3" }, "token-limit": func(c *ModelConfig) { c.MaxTokens = 513 }, "no-model": func(c *ModelConfig) { c.Model = "" }} {
		t.Run(name, func(t *testing.T) {
			bad := c
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("unsafe configuration")
			}
		})
	}
}
func TestModelResponse(t *testing.T) {
	good := `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"4"}}]}`
	if answer, e := parseAnswer([]byte(good)); e != nil || answer != "4" {
		t.Fatal(answer, e)
	}
	for name, raw := range map[string]string{"malformed": "{", "empty": "{}", "trailing": good + "{}", "truncated": strings.Replace(good, "stop", "length", 1), "tool": strings.Replace(good, `"content":"4"`, `"content":"4","tool_calls":[{"name":"shell"}]`, 1), "role": strings.Replace(good, "assistant", "user", 1), "null": strings.Replace(good, `"content":"4"`, `"content":null`, 1), "huge": strings.Replace(good, `"4"`, `"`+strings.Repeat("x", 3501)+`"`, 1)} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseAnswer([]byte(raw)); e == nil {
				t.Fatal("unsafe answer")
			}
		})
	}
}
func TestQuestionDefinition(t *testing.T) {
	// An ordinary question containing template notation remains literal data.
	d := Definition(testOwner())
	if !workflow.ValidateDefinition(d).Valid {
		t.Fatal(workflow.ValidateDefinition(d))
	}
	if !bytes.Equal(Reference(d.Owner).Digest, Reference(d.Owner).Digest) {
		t.Fatal("unstable reference")
	}
}

func TestModelTextCannotControlTerminal(t *testing.T) {
	if got := TerminalText("hello\x1b]52;clipboard\a\rworld\n\t"); got != "hello\\u001b]52;clipboard\\u0007\\u000dworld\n\t" {
		t.Fatal(got)
	}
}

func TestPythonModelBoundary(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Fatal(e)
	}
	var redirected atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true); w.WriteHeader(200) }))
	defer destination.Close()
	for _, mode := range []string{"ok", "http-error", "redirect", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Authorization") != "" {
					t.Error("unexpected credential or method")
				}
				switch mode {
				case "redirect":
					http.Redirect(w, r, destination.URL, 307)
				case "http-error":
					w.WriteHeader(429)
					w.Write([]byte(`{"error":"busy"}`))
				case "oversize":
					w.Write([]byte(strings.Repeat("x", 65537)))
				default:
					w.Write([]byte(`{"ok":true}`))
				}
			}))
			defer srv.Close()
			input, _ := json.Marshal(workerInput{Version: 1, Endpoint: srv.URL, Request: []byte(`{"model":"test"}`), Timeout: 2})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, python, "-I", "-B", "-c", workerSource)
			cmd.Env = []string{"LANG=C.UTF-8"}
			cmd.Stdin = bytes.NewReader(input)
			raw, e := cmd.Output()
			if e != nil {
				t.Fatal(e)
			}
			var v workerOutput
			if json.Unmarshal(raw, &v) != nil {
				t.Fatal("invalid output")
			}
			if mode == "ok" && (v.Status != 200 || v.Error != "" || string(v.Response) != `{"ok":true}`) {
				t.Fatal(v)
			}
			if mode != "ok" && v.Error == "" {
				t.Fatal("failure hidden")
			}
			if mode == "oversize" && (v.Error != "response_too_large" || len(v.Response) != 65536) {
				t.Fatal("missing bounded partial evidence")
			}
		})
	}
	if redirected.Load() {
		t.Fatal("followed provider redirect")
	}
}
func TestPrivateLocalConfiguration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "qa")
	model := ModelConfig{Endpoint: "fixture", Model: "test", Python: "/usr/bin/python3", MaxTokens: 128}
	if e := InitDev(dir, "127.0.0.1:8093", "postgres://test@127.0.0.1:25432/test", "127.0.0.1:25080", model); e != nil {
		t.Fatal(e)
	}
	c, e := LoadDevConfig(filepath.Join(dir, "server.json"))
	if e != nil {
		t.Fatal(e)
	}
	p, e := LoadProfile(filepath.Join(dir, "client.json"))
	if e != nil {
		t.Fatal(e)
	}
	if p.Actor != c.Actor || p.Recipient != c.Application || len(p.PrivateKey) != 64 {
		t.Fatal("profile mismatch")
	}
	if e := InitDev(dir, c.Listen, c.DatabaseURL, c.Corndogs, model); e == nil {
		t.Fatal("overwrote configuration")
	}
	for name, change := range map[string]func(*DevConfig){"public-listen": func(c *DevConfig) { c.Listen = "0.0.0.0:8093" }, "public-domain": func(c *DevConfig) { c.Actor.Identity.Domain = "catalystsquad.local" }, "foreign-database": func(c *DevConfig) { c.DatabaseURL = "postgres://test@remote.example/db" }, "wrong-mode": func(c *DevConfig) { c.Mode = "production" }} {
		t.Run(name, func(t *testing.T) {
			bad := c
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("local boundary escaped")
			}
		})
	}
	if e := os.Chmod(filepath.Join(dir, "client.json"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadProfile(filepath.Join(dir, "client.json")); e == nil {
		t.Fatal("read public private key")
	}
}
