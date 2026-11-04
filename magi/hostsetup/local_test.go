package hostsetup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name, body, link string
	dir              bool
}

func tarGz(t *testing.T, es []entry) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		switch {
		case e.dir:
			h.Typeflag, h.Size = tar.TypeDir, 0
		case e.link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.link, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, e.body)
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

func TestExtract(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "a.tar.gz")
	os.WriteFile(arc, tarGz(t, []entry{{name: "llama-b1/", dir: true}, {name: "llama-b1/llama-server", body: "bin"},
		{name: "llama-b1/libx.so.0", link: "libx.so.0.1"}, {name: "llama-b1/libx.so.0.1", body: "lib"}}), 0o644)
	out := filepath.Join(dir, "out")
	if err := extract(arc, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "llama-server")); string(b) != "bin" {
		t.Errorf("llama-server: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "libx.so.0")); string(b) != "lib" {
		t.Errorf("link: %q", b)
	}
	// A flat archive keeps its names.
	os.WriteFile(arc, tarGz(t, []entry{{name: "libcudart.so.12", body: "rt"}}), 0o644)
	if err := extract(arc, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "libcudart.so.12")); err != nil {
		t.Error(err)
	}
	for _, bad := range [][]entry{
		{{name: "x/../../evil", body: "x"}, {name: "y/z", body: "x"}},
		{{name: "x/a", link: "../../../etc/passwd"}},
		{{name: "x/a", link: "/etc/passwd"}},
	} {
		os.WriteFile(arc, tarGz(t, bad), 0o644)
		if err := extract(arc, filepath.Join(dir, "bad")); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
		t.Error("a file left the directory")
	}
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestDownload(t *testing.T) {
	body := []byte("model bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	dir := t.TempDir()
	pins := &Pins{Models: []Model{
		{Name: "good", URL: srv.URL + "/good.gguf", SHA256: sum(body), Size: int64(len(body))},
		{Name: "bad", URL: srv.URL + "/bad.gguf", SHA256: strings.Repeat("0", 64), Size: int64(len(body))},
	}}
	l := &Local{Dir: dir, Pins: pins, Arch: "x86_64", Out: io.Discard}
	if out := l.Download(context.Background(), "good"); !strings.HasPrefix(out, "ok") {
		t.Fatal(out)
	}
	if out := l.Download(context.Background(), "bad"); !strings.Contains(out, "sha256 mismatch") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(dir, "models", "bad.gguf")); err == nil {
		t.Error("a file with a bad hash stays")
	}
	if out := l.Download(context.Background(), "other"); !strings.HasPrefix(out, "error") {
		t.Error(out)
	}
}

func TestLocalExecAndAsk(t *testing.T) {
	var log bytes.Buffer
	l := &Local{Out: &log, In: bufio.NewReader(strings.NewReader("n\nyes I did\n")), Confirm: true}
	if out := l.Exec(context.Background(), "echo hi"); !strings.Contains(out, "did not permit") {
		t.Errorf("confirm: %q", out)
	}
	if got := l.Ask(context.Background(), "Install ffmpeg?"); got != "yes I did" {
		t.Errorf("ask: %q", got)
	}
	l.Confirm = false
	if out := l.Exec(context.Background(), "echo hi"); strings.TrimSpace(out) != "hi" {
		t.Errorf("exec: %q", out)
	}
	if !strings.Contains(log.String(), "$ echo hi") || !strings.Contains(log.String(), "Install ffmpeg?") {
		t.Errorf("the person does not see the commands: %q", log.String())
	}
}

// TestFakeEngine is the fake llama-server of TestStartAndFinalize. It runs
// only as a child process.
func TestFakeEngine(t *testing.T) {
	if os.Getenv("HOSTSETUP_FAKE_ENGINE") == "" {
		t.Skip("child process only")
	}
	args := os.Args
	for i, a := range args {
		if a == "--version" {
			fmt.Println("version: 12017 (fake)")
			os.Exit(0)
		}
		if a == "--port" {
			http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"ok"}`) })
			fmt.Println(http.ListenAndServe("127.0.0.1:"+args[i+1], nil))
			os.Exit(1)
		}
	}
	os.Exit(2)
}

func TestStartAndFinalize(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOSTSETUP_FAKE_ENGINE", "1")
	dir := t.TempDir()
	pins := &Pins{
		Engines: []Engine{{Name: "llama.cpp-cpu", Kind: "cpu", Arch: "x86_64", Version: "b1"}},
		Models:  []Model{{Name: "m", URL: "https://example.local/m.gguf", Size: 1 << 30}},
	}
	l := &Local{Dir: dir, Pins: pins, Arch: "x86_64", Out: io.Discard}
	bin := filepath.Join(l.engineDir(pins.Engines[0]), "llama-server")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, []byte("#!/bin/sh\nexec "+exe+" -test.run=^TestFakeEngine$ -- \"$@\"\n"), 0o755)

	if out := l.Start(context.Background(), "llama.cpp-cpu", "m"); !strings.HasPrefix(out, "ok: engine healthy") {
		t.Fatal(out)
	}
	l.RAMAvail = func() int64 { return 1 << 30 }
	if out := l.Start(context.Background(), "llama.cpp-cpu", "m"); !strings.Contains(out, "full start check runs when the setup ends") {
		t.Fatal(out)
	}
	p, err := l.Finalize(context.Background(), "llama.cpp-cpu", "m", mustPort(t))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct{ Engine EngineConfig }
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.Engine.Model != "m" || cfg.Engine.Command[0] != bin {
		t.Fatalf("config %s: %v", b, err)
	}

	os.WriteFile(bin, []byte("#!/bin/sh\necho 'libcuda.so.1: cannot open shared object file' >&2\nexit 127\n"), 0o755)
	if out := l.Start(context.Background(), "llama.cpp-cpu", "m"); !strings.Contains(out, "does not load") || !strings.Contains(out, "libcuda") {
		t.Fatal(out)
	}
}

func mustPort(t *testing.T) int {
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
