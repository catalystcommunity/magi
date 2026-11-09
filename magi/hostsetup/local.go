package hostsetup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/magi/magi/model"
)

// Local is the real host. It keeps downloads in Dir:
//
//	Dir/models/<file>.gguf
//	Dir/engines/<name>-<version>-<arch>/llama-server
type Local struct {
	Dir   string
	Pins  *Pins
	Arch  string
	Out   io.Writer     // the person sees each command, output, and question here
	In    *bufio.Reader // answers of the person
	HTTP  *http.Client
	Shell string // default /bin/sh
	// Confirm asks the person before each command.
	Confirm bool
	// RAMAvail returns the available RAM now (default: /proc/meminfo).
	RAMAvail func() int64
	// GPUOf returns the GPU of a GPU engine (Plan.GPUOf). The engine
	// command then names its llama.cpp device (devices.go).
	GPUOf   func(engine string) (GPU, bool)
	devices map[string]string
}

const maxOutput = 8000

func (l *Local) say(format string, a ...any) { fmt.Fprintf(l.Out, format, a...) }

func (l *Local) Exec(ctx context.Context, cmd string) string {
	l.say("\n$ %s\n", cmd)
	if l.Confirm && !l.yes("Run this command?") {
		l.say("(not run)\n")
		return "The person did not permit this command."
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	sh := l.Shell
	if sh == "" {
		sh = "/bin/sh"
	}
	c := exec.CommandContext(ctx, sh, "-c", cmd)
	c.Stdin = nil
	b, err := c.CombinedOutput()
	out := string(b)
	if err != nil {
		out += "\n(" + err.Error() + ")"
	}
	if len(out) > maxOutput {
		out = out[:maxOutput] + "\n(output cut)"
	}
	l.say("%s\n", strings.TrimRight(out, "\n"))
	return out
}

func (l *Local) yes(q string) bool {
	l.say("%s [y/N] ", q)
	s, _ := l.In.ReadString('\n')
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

func (l *Local) Ask(_ context.Context, q string) string {
	l.say("\n>>> %s\nYour answer (one line): ", q)
	s, err := l.In.ReadString('\n')
	if err != nil && s == "" {
		return "The person did not answer."
	}
	return strings.TrimSpace(s)
}

// Download gets a pinned item. A file that is there with the correct
// hash is not downloaded again.
func (l *Local) Download(ctx context.Context, item string) string {
	if m, ok := l.Pins.Model(item); ok {
		dst := filepath.Join(l.Dir, "models", m.FileName())
		if err := l.fetch(ctx, File{URL: m.URL, SHA256: m.SHA256, Size: m.Size}, dst); err != nil {
			return "error: " + err.Error()
		}
		return "ok: " + item + " is at " + dst + ", sha256 verified"
	}
	if e, ok := l.Pins.Engine(item, l.Arch); ok {
		dir := l.engineDir(e)
		for _, f := range e.Files {
			arc := filepath.Join(l.Dir, "downloads", filepath.Base(f.URL))
			if err := l.fetch(ctx, f, arc); err != nil {
				return "error: " + err.Error()
			}
			if err := extract(arc, dir); err != nil {
				return "error: extract " + filepath.Base(arc) + ": " + err.Error()
			}
		}
		return "ok: " + item + " is in " + dir + ", sha256 verified"
	}
	return "error: " + item + " has no download in this version"
}

func (l *Local) engineDir(e Engine) string {
	return filepath.Join(l.Dir, "engines", e.Name+"-"+e.Version+"-"+e.Arch)
}

// fetch downloads f to dst and checks its hash. A mismatch removes the file.
func (l *Local) fetch(ctx context.Context, f File, dst string) error {
	if sum, err := fileSHA256(dst); err == nil && sum == f.SHA256 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	l.say("\n(download %s, %s)\n", f.URL, GB(f.Size))
	req, err := http.NewRequestWithContext(ctx, "GET", f.URL, nil)
	if err != nil {
		return err
	}
	hc := l.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %d", f.URL, resp.StatusCode)
	}
	part := dst + ".part"
	w, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(w, h), resp.Body)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != f.SHA256 {
		os.Remove(part)
		return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s. The file was removed", filepath.Base(dst), f.SHA256[:12], got[:12])
	}
	return os.Rename(part, dst)
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extract unpacks a .tar.gz into dir. It removes the first path part when
// all entries share it, and refuses paths and links that leave dir.
func extract(arc, dir string) error {
	strip, nested := "", false
	err := walkTar(arc, func(hd *tar.Header, _ io.Reader) error {
		first, _, sub := strings.Cut(strings.TrimPrefix(filepath.Clean(hd.Name), "./"), "/")
		nested = nested || sub
		switch {
		case strip == "":
			strip = first
		case strip != first:
			strip = "\x00" // no common part
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !nested {
		strip = "\x00"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	inside := func(p string) bool {
		rel, err := filepath.Rel(root, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
	}
	return walkTar(arc, func(hd *tar.Header, r io.Reader) error {
		name := strings.TrimPrefix(filepath.Clean(hd.Name), "./")
		if filepath.IsAbs(name) {
			return fmt.Errorf("path %q is absolute", hd.Name)
		}
		if name == strip {
			return nil
		}
		name = strings.TrimPrefix(name, strip+"/")
		dst := filepath.Join(root, name)
		if !inside(dst) {
			return fmt.Errorf("path %q leaves the directory", hd.Name)
		}
		switch hd.Typeflag {
		case tar.TypeDir:
			return os.MkdirAll(dst, 0o755)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			os.Remove(dst)
			w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hd.Mode&0o755)|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, r)
			if cerr := w.Close(); err == nil {
				err = cerr
			}
			return err
		case tar.TypeSymlink:
			if filepath.IsAbs(hd.Linkname) || !inside(filepath.Join(filepath.Dir(dst), hd.Linkname)) {
				return fmt.Errorf("link %q leaves the directory", hd.Name)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			os.Remove(dst)
			return os.Symlink(hd.Linkname, dst)
		}
		// Other entry types are not necessary for an engine build.
		return nil
	})
}

// walkTar calls fn for each entry of a .tar.gz.
func walkTar(arc string, fn func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(arc)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(hd, tr); err != nil {
			return err
		}
	}
}

// EngineCommand is the command that serves m with e on port.
func (l *Local) EngineCommand(e Engine, m Model, port int) []string {
	ngl := "0"
	if e.GPU() {
		ngl = "99"
	}
	cmd := []string{filepath.Join(l.engineDir(e), "llama-server"),
		"-m", filepath.Join(l.Dir, "models", m.FileName()), "--alias", m.Name,
		"--jinja", "--reasoning-budget", "0", "-c", "16384", "-ngl", ngl,
		"--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	if d := l.device(e); d != "" {
		cmd = append(cmd, "--device", d)
	}
	return cmd
}

// Start checks that the engine program loads, then starts it with the
// model and waits for its health. When the setup model uses the RAM that
// the CPU engine needs, only the load check runs now; Finalize does the
// full check after the setup model stops.
func (l *Local) Start(ctx context.Context, engine, model string) string {
	e, ok := l.Pins.Engine(engine, l.Arch)
	if !ok {
		return "error: unknown engine " + engine
	}
	m, ok := l.Pins.Model(model)
	if !ok {
		return "error: unknown model " + model
	}
	bin := filepath.Join(l.engineDir(e), "llama-server")
	ver := exec.CommandContext(ctx, bin, "--version")
	ver.Env = l.engineEnv(e)
	if b, err := ver.CombinedOutput(); err != nil {
		return "error: " + engine + " does not load: " + strings.TrimSpace(string(b)) + " (" + err.Error() + ")"
	}
	if !e.GPU() && l.RAMAvail != nil && l.RAMAvail() < m.Size+RAMHeadroom {
		return "ok: the engine program loads. The full start check runs when the setup ends, because the setup model uses the RAM now."
	}
	port, err := freePort()
	if err != nil {
		return "error: " + err.Error()
	}
	d, out, err := l.startAndWait(ctx, e, m, port)
	if err != nil {
		return "error: " + err.Error() + "\nengine log (end):\n" + out
	}
	return fmt.Sprintf("ok: engine healthy (test start on 127.0.0.1:%d in %.0f s)", port, d.Seconds())
}

func (l *Local) engineEnv(e Engine) []string {
	return append(os.Environ(), "LD_LIBRARY_PATH="+l.engineDir(e)+":"+os.Getenv("LD_LIBRARY_PATH"))
}

// startAndWait starts the engine, waits for /health, and stops it. It
// returns the start time, or the end of the engine log on failure.
func (l *Local) startAndWait(ctx context.Context, e Engine, m Model, port int) (time.Duration, string, error) {
	p, err := l.spawn(e, m, port)
	if err != nil {
		return 0, "", err
	}
	defer p.stop()
	t0 := time.Now()
	if err := waitHealth(ctx, "http://127.0.0.1:"+strconv.Itoa(port), 10*time.Minute, p.done); err != nil {
		return 0, tail(p.log, 2000), err
	}
	return time.Since(t0), "", nil
}

// proc is a running engine.
type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
	log  string
}

// spawn starts the engine in its own process group, with its log in Dir.
func (l *Local) spawn(e Engine, m Model, port int) (*proc, error) {
	args := l.EngineCommand(e, m, port)
	logf := filepath.Join(l.Dir, "logs", e.Name+"-"+m.Name+".log")
	if err := os.MkdirAll(filepath.Dir(logf), 0o755); err != nil {
		return nil, err
	}
	w, err := os.Create(logf)
	if err != nil {
		return nil, err
	}
	c := exec.Command(args[0], args[1:]...)
	c.Env = l.engineEnv(e)
	c.Stdout, c.Stderr = w, w
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		w.Close()
		return nil, err
	}
	p := &proc{cmd: c, done: make(chan struct{}), log: logf}
	go func() { c.Wait(); w.Close(); close(p.done) }()
	return p, nil
}

// stop ends the process group: SIGTERM, then SIGKILL after 10 s.
func (p *proc) stop() {
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
}

// waitHealth waits until GET base/health answers 200, done closes (the
// process ended), or the time is over.
func waitHealth(ctx context.Context, base string, limit time.Duration, done <-chan struct{}) error {
	end := time.Now().Add(limit)
	hc := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(end) {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/health", nil)
		if resp, err := hc.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("the engine stopped before it was healthy")
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("the engine was not healthy after %s", limit)
}

// freePort picks a free port below the ephemeral range (Linux 32768 and
// up), so that a connection of another program does not take the port
// between this check and the start of the engine.
func freePort() (int, error) {
	for i := 0; i < 64; i++ {
		p := 20000 + rand.IntN(10000)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		ln.Close()
		return p, nil
	}
	return 0, errors.New("no free port in 20000-29999")
}

func tail(p string, n int) string {
	b, _ := os.ReadFile(p)
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

// EngineConfig is the "engine" part of the inference host configuration
// (primitives/inference).
type EngineConfig struct {
	Command []string `json:"command"`
	Port    int      `json:"port"`
	Model   string   `json:"model"`
}

// Finalize runs the full start check of the chosen engine and model, and
// writes Dir/inference-engine.json for the inference host. Call it after
// the setup model stops.
func (l *Local) Finalize(ctx context.Context, engine, model string, port int) (string, error) {
	e, ok := l.Pins.Engine(engine, l.Arch)
	if !ok {
		return "", fmt.Errorf("unknown engine %s", engine)
	}
	m, ok := l.Pins.Model(model)
	if !ok {
		return "", fmt.Errorf("unknown model %s", model)
	}
	if _, out, err := l.startAndWait(ctx, e, m, port); err != nil {
		return "", fmt.Errorf("%w\nengine log (end):\n%s", err, out)
	}
	return l.WriteEngineConfig(e, m, port)
}

// WriteEngineConfig writes Dir/inference-engine.json and returns its path.
func (l *Local) WriteEngineConfig(e Engine, m Model, port int) (string, error) {
	cfg := EngineConfig{Command: l.EngineCommand(e, m, port), Port: port, Model: m.Name}
	b, err := json.MarshalIndent(map[string]any{"engine": cfg}, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(l.Dir, "inference-engine.json")
	return p, os.WriteFile(p, append(b, '\n'), 0o644)
}

// ReadRAMAvail reads MemAvailable from /proc/meminfo.
func ReadRAMAvail() int64 {
	b, _ := os.ReadFile("/proc/meminfo")
	return meminfoAvail(string(b))
}

// Helper is the setup model that runs on the CPU of the host.
type Helper struct {
	Client *model.Client
	p      *proc
}

// Stop ends the helper engine.
func (h *Helper) Stop() {
	if h != nil && h.p != nil {
		h.p.stop()
	}
}

// StartHelper downloads the CPU engine and the first helper model that fits
// the available RAM, starts it on 127.0.0.1, and waits for its health.
func (l *Local) StartHelper(ctx context.Context, ramAvail int64) (*Helper, error) {
	e, ok := l.Pins.Engine("llama.cpp-cpu", l.Arch)
	if !ok {
		return nil, fmt.Errorf("no CPU engine build for %s", l.Arch)
	}
	var m Model
	for _, name := range l.Pins.Helper {
		if c, ok := l.Pins.Model(name); ok && c.Size+RAMHeadroom <= ramAvail {
			m = c
			break
		}
	}
	if m.Name == "" {
		return nil, fmt.Errorf("no setup model fits the available RAM (%s)", GB(ramAvail))
	}
	l.say("Setup model: %s on the CPU. The first start downloads %s.\n", m.Name, GB(m.Size+e.Size()))
	for _, item := range []string{e.Name, m.Name} {
		if out := l.Download(ctx, item); !strings.HasPrefix(out, "ok") {
			return nil, errors.New(strings.TrimPrefix(out, "error: "))
		}
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	p, err := l.spawn(e, m, port)
	if err != nil {
		return nil, err
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	if err := waitHealth(ctx, base, 10*time.Minute, p.done); err != nil {
		p.stop()
		return nil, fmt.Errorf("setup model: %w\n%s", err, tail(p.log, 2000))
	}
	return &Helper{p: p, Client: &model.Client{BaseURL: base + "/v1", Model: m.Name, HTTP: &http.Client{Timeout: 10 * time.Minute}}}, nil
}
