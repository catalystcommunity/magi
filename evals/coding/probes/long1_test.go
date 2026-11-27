// Probes of case long1. Target: primitives/inference/host
package host

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/inference/api"
)

// The test binary is also the nvidia-smi and the engine of the probes, so
// that a probe needs no GPU. A link with the name nvidia-smi makes it print
// the rows of the file nvidia-smi.json next to the link. The argument
// "probe-engine" makes it an engine that writes its process id to a file.
func init() {
	if filepath.Base(os.Args[0]) == "nvidia-smi" {
		os.Exit(probeSMI(os.Args[0]+".json", os.Args[1:]))
	}
	if len(os.Args) == 4 && os.Args[1] == "probe-engine" {
		os.WriteFile(os.Args[3], []byte(strconv.Itoa(os.Getpid())), 0o644)
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(http.ResponseWriter, *http.Request) {})
		for i := 0; i < 100; i++ {
			if l, err := net.Listen("tcp", "127.0.0.1:"+os.Args[2]); err == nil {
				http.Serve(l, mux)
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		os.Exit(0)
	}
}

// probeSMIData is the machine that the stand-in for nvidia-smi shows. A GPU
// has the values of the query fields "name", "utilization.gpu",
// "memory.used", and "memory.total". PIDFile has the process id of the
// engine, which uses EngineMiB of GPU memory.
type probeSMIData struct {
	GPUs      []map[string]string `json:"gpus"`
	PIDFile   string              `json:"pid_file"`
	EngineMiB string              `json:"engine_mib"`
}

// probeSMI answers --query-gpu and --query-compute-apps as nvidia-smi does:
// the fields of the query in the sequence of the query, a header line
// without "noheader", units without "nounits", and "[N/A]" for a value that
// it does not have. Thus a probe does not depend on the fields that the
// change asks for.
func probeSMI(dataFile string, args []string) int {
	var d probeSMIData
	b, err := os.ReadFile(dataFile)
	if err != nil || json.Unmarshal(b, &d) != nil {
		fmt.Fprintln(os.Stderr, "probe nvidia-smi: no data")
		return 9
	}
	var fields []string
	kind, header, units := "", true, true
	for _, a := range args {
		k, v, _ := strings.Cut(a, "=")
		switch k {
		case "--query-gpu", "--query-compute-apps":
			kind = k
			for _, f := range strings.Split(v, ",") {
				fields = append(fields, strings.TrimSpace(f))
			}
		case "--format":
			header = !strings.Contains(v, "noheader")
			units = !strings.Contains(v, "nounits")
		default:
			fmt.Fprintf(os.Stderr, "probe nvidia-smi: the argument %s is not supported\n", a)
			return 2
		}
	}
	unit := func(f string) string {
		switch {
		case strings.HasPrefix(f, "utilization."):
			return "%"
		case strings.HasPrefix(f, "memory.") || strings.HasSuffix(f, "used_memory"):
			return "MiB"
		}
		return ""
	}
	var rows []map[string]string
	switch kind {
	case "--query-gpu":
		for i, g := range d.GPUs {
			r := map[string]string{"index": strconv.Itoa(i), "count": strconv.Itoa(len(d.GPUs)), "uuid": fmt.Sprintf("GPU-probe-%d", i),
				"gpu_name": g["name"]}
			for k, v := range g {
				r[k] = v
			}
			used, e1 := strconv.Atoi(g["memory.used"])
			total, e2 := strconv.Atoi(g["memory.total"])
			if e1 == nil && e2 == nil {
				r["memory.free"] = strconv.Itoa(total - used)
			}
			rows = append(rows, r)
		}
	case "--query-compute-apps":
		// A process of a different program is on the GPU too.
		rows = append(rows, map[string]string{"pid": "1", "used_memory": "1234", "used_gpu_memory": "1234", "name": "other", "process_name": "other"})
		if pid, err := os.ReadFile(d.PIDFile); err == nil && len(pid) > 0 {
			rows = append(rows, map[string]string{"pid": string(pid), "used_memory": d.EngineMiB, "used_gpu_memory": d.EngineMiB,
				"name": "engine", "process_name": "engine"})
		}
	default:
		fmt.Fprintln(os.Stderr, "probe nvidia-smi: no query")
		return 2
	}
	if header {
		var hs []string
		for _, f := range fields {
			if u := unit(f); u != "" && units {
				f += " [" + u + "]"
			}
			hs = append(hs, f)
		}
		fmt.Println(strings.Join(hs, ", "))
	}
	for _, r := range rows {
		var vs []string
		for _, f := range fields {
			v, ok := r[f]
			if !ok {
				v = "[N/A]"
			}
			if u := unit(f); u != "" && units && v != "[N/A]" {
				v += " " + u
			}
			vs = append(vs, v)
		}
		fmt.Println(strings.Join(vs, ", "))
	}
	return 0
}

func probeGPU(name, util, used, total string) map[string]string {
	return map[string]string{"name": name, "utilization.gpu": util, "memory.used": used, "memory.total": total}
}

// probeHost is a host with the System sampler of the base, the stand-in for
// nvidia-smi, and the InferenceService on the real HTTP transport.
type probeHost struct {
	t    *testing.T
	c    *api.InferenceClient
	data string
	d    probeSMIData
}

func probeStart(t *testing.T, gpus []map[string]string, opts ...func(*Config)) *probeHost {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	smi := filepath.Join(dir, "nvidia-smi")
	if err := os.Symlink(exe, smi); err != nil {
		t.Fatal(err)
	}
	p := &probeHost{t: t, data: smi + ".json", d: probeSMIData{PIDFile: filepath.Join(dir, "engine.pid"), EngineMiB: "7000"}}
	p.machine(gpus)
	port := 0
	for i := 0; i < 200 && port == 0; i++ {
		n := 20000 + rand.IntN(12000)
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", n)); err == nil {
			l.Close()
			port = n
		}
	}
	// No busy rule: the load of the machine that runs the probe must not
	// change the result.
	cfg := Config{
		Engine: Engine{Command: []string{exe, "probe-engine", strconv.Itoa(port), p.d.PIDFile}, Port: port, Model: "local",
			Models: map[string]Variant{"local-notes": {}, "local-decide": {}}},
		IdleStop: 10 * time.Minute, Interval: 50 * time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	h := New(cfg, System{NvidiaSMI: smi}, ver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; time.Sleep(100 * time.Millisecond) })
	rs := &rpc.Server{Verifier: ver, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rs.Register("InferenceService", api.InferenceServiceOps(h))
	hs := httptest.NewServer(rpc.Mux(rs, nil))
	t.Cleanup(hs.Close)
	tok, err := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{Scope}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p.c = api.NewInferenceClient(rpc.NewClient(hs.URL, rpc.StaticToken(tok)))
	return p
}

// machine changes the GPUs that the stand-in for nvidia-smi shows.
func (p *probeHost) machine(gpus []map[string]string) {
	p.t.Helper()
	p.d.GPUs = gpus
	b, _ := json.Marshal(p.d)
	tmp := p.data + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Rename(tmp, p.data); err != nil {
		p.t.Fatal(err)
	}
}

// status gives the status as the JSON object of the generated type. The
// keys are the field names of the contract, so a probe does not depend on
// the Go names of the new type.
func (p *probeHost) status(detail bool) map[string]any {
	p.t.Helper()
	req := api.StatusRequest{}
	if detail {
		req.Detail = ptr(true)
	}
	st, err := p.c.Status(context.Background(), req)
	if err != nil {
		p.t.Fatalf("status: %v", err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		p.t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		p.t.Fatal(err)
	}
	return out
}

// wait reads the status with detail until ok. A host can take its sample
// on a timer or for each request; the probe permits the two.
func (p *probeHost) wait(what string, ok func(map[string]any) bool) map[string]any {
	p.t.Helper()
	var st map[string]any
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if st = p.status(true); ok(st) {
			return st
		}
	}
	p.t.Fatalf("%s: the status with detail is %v", what, st)
	return nil
}

func probeGPUs(st map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	list, _ := st["gpus"].([]any)
	for _, x := range list {
		if g, ok := x.(map[string]any); ok {
			out[fmt.Sprint(g["name"])] = g
		}
	}
	return out
}

func probeHasGPUs(n int) func(map[string]any) bool {
	return func(st map[string]any) bool { return len(probeGPUs(st)) >= n }
}

// probeValues compares the values of one GPU. A value of -1 must be absent.
func probeValues(t *testing.T, g map[string]any, total, used, util float64) {
	t.Helper()
	if g == nil {
		t.Fatal("the GPU is not in the status")
	}
	for k, want := range map[string]float64{"total_mib": total, "used_mib": used, "util": util} {
		got, has := g[k]
		if want < 0 && has {
			t.Errorf("%s of %v is %v, want no value", k, g["name"], got)
		}
		if want >= 0 && got != want {
			t.Errorf("%s of %v is %v, want %v", k, g["name"], got, want)
		}
	}
}

var probeTwo = []map[string]string{
	probeGPU("NVIDIA GeForce RTX 4080", "3", "9000", "16000"),
	probeGPU("NVIDIA GeForce RTX 3060", "55", "2000", "12000"),
}

// When the request of status has detail true, the status also has gpus: each GPU of the machine with its name, total_mib, used_mib, and util.
func TestProbeDetailHasEachGPU(t *testing.T) {
	p := probeStart(t, probeTwo)
	g := probeGPUs(p.wait("two GPUs of nvidia-smi", probeHasGPUs(2)))
	probeValues(t, g["NVIDIA GeForce RTX 4080"], 16000, 9000, 3)
	probeValues(t, g["NVIDIA GeForce RTX 3060"], 12000, 2000, 55)
}

// Without detail, the status has none of the three fields.
func TestProbeNoDetailNoFields(t *testing.T) {
	p := probeStart(t, probeTwo)
	p.wait("the model names", func(st map[string]any) bool { return st["models"] != nil })
	st := p.status(false)
	for _, k := range []string{"gpus", "engine_mib", "models"} {
		if v, has := st[k]; has {
			t.Errorf("the status without detail has %s: %v", k, v)
		}
	}
	if st["state"] != "off" || st["model"] != "local" {
		t.Errorf("the fields of the base changed: %v", st)
	}
}

// A value that the host cannot read is absent. nvidia-smi prints [N/A] for a value that it cannot read.
func TestProbeUnknownValueIsAbsent(t *testing.T) {
	p := probeStart(t, []map[string]string{probeGPU("NVIDIA Tesla K80", "[N/A]", "[N/A]", "11441")})
	g := probeGPUs(p.wait("one GPU of nvidia-smi", probeHasGPUs(1)))
	probeValues(t, g["NVIDIA Tesla K80"], 11441, -1, -1)
}

// models: the model names that the engine serves: the model of the engine first, then the names of engine.models in alphabetical order.
func TestProbeModelNames(t *testing.T) {
	p := probeStart(t, probeTwo)
	st := p.wait("the model names", func(st map[string]any) bool { return st["models"] != nil })
	if got := fmt.Sprint(st["models"]); got != "[local local-decide local-notes]" {
		t.Errorf("models = %s, want [local local-decide local-notes]", got)
	}
	one := probeStart(t, probeTwo, func(c *Config) { c.Engine.Models = nil })
	st = one.wait("the model names", func(st map[string]any) bool { return st["models"] != nil })
	if got := fmt.Sprint(st["models"]); got != "[local]" {
		t.Errorf("models of an engine with one name = %s, want [local]", got)
	}
}

// engine_mib: the GPU memory that the engine process uses.
func TestProbeEngineMemory(t *testing.T) {
	p := probeStart(t, probeTwo)
	if _, err := p.c.Warm(context.Background(), api.WarmRequest{}); err != nil {
		t.Fatal(err)
	}
	// 7000 is the memory of the engine process. 1234 is of a different
	// process, and 9000 is the used memory of the GPU.
	st := p.wait("the memory of the engine", func(st map[string]any) bool { return st["engine_mib"] != nil })
	if st["engine_mib"] != float64(7000) {
		t.Errorf("engine_mib = %v, want 7000", st["engine_mib"])
	}
}

// engine_mib is absent when the engine does not run.
func TestProbeNoEngineNoEngineMemory(t *testing.T) {
	off := probeStart(t, probeTwo)
	st := off.wait("two GPUs of nvidia-smi", probeHasGPUs(2))
	if v, has := st["engine_mib"]; has || st["state"] != "off" {
		t.Fatalf("the engine did not start: state %v, engine_mib %v", st["state"], v)
	}
	// An engine that ran and stopped for idle time.
	p := probeStart(t, probeTwo, func(c *Config) { c.IdleStop = 3 * time.Second })
	if _, err := p.c.Warm(context.Background(), api.WarmRequest{}); err != nil {
		t.Fatal(err)
	}
	p.wait("the memory of the engine", func(st map[string]any) bool { return st["engine_mib"] != nil })
	p.wait("no memory after the engine stopped", func(st map[string]any) bool {
		_, has := st["engine_mib"]
		return st["state"] == "off" && !has
	})
}

// The busy rules do not change: they use the first GPU of nvidia-smi only.
func TestProbeBusyRulesUseTheFirstGPU(t *testing.T) {
	p := probeStart(t, []map[string]string{
		probeGPU("NVIDIA GeForce RTX 4080", "3", "9000", "16000"),
		probeGPU("NVIDIA GeForce RTX 3060", "99", "11900", "12000"),
	}, func(c *Config) { c.Rules = Rules{GPUUtil: 40, NeedMiB: 5000} })
	time.Sleep(time.Second)
	if st := p.status(false); st["state"] != "off" {
		t.Fatalf("the second GPU made the host busy: %v", st)
	}
	p.machine([]map[string]string{
		probeGPU("NVIDIA GeForce RTX 4080", "99", "9000", "16000"),
		probeGPU("NVIDIA GeForce RTX 3060", "3", "2000", "12000"),
	})
	var st map[string]any
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if st = p.status(false); st["state"] == "busy" {
			break
		}
	}
	if st["state"] != "busy" || !strings.Contains(fmt.Sprint(st["reason"]), "GPU use is 99%") {
		t.Fatalf("the first GPU did not make the host busy: %v", st)
	}
}

// probeSysfs runs the probe again in a mount namespace where
// /sys/class/drm is a directory of the probe: two cards of AMD and Intel,
// a card of NVIDIA, a card of a maker that the task does not name, and the
// other entries that Linux has there. The task does not name a setting for
// the directory, so the probe changes what the host reads at the fixed
// path. It needs unshare and mount of util-linux and user namespaces.
func probeSysfs(t *testing.T, probe func(g map[string]map[string]any)) {
	t.Helper()
	if os.Getenv("PROBE_LONG1_SYSFS") == "" {
		root := t.TempDir()
		card := func(name string, files map[string]string) {
			dir := filepath.Join(root, name, "device")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for f, v := range files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(v+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		card("card0", map[string]string{"vendor": "0x1002", "device": "0x7480", "mem_info_vram_total": "8573157376",
			"mem_info_vram_used": "7412670464", "gpu_busy_percent": "12"})
		card("card1", map[string]string{"vendor": "0x8086", "device": "0x46a6"})
		card("card2", map[string]string{"vendor": "0x10de", "device": "0x2704"})
		card("card3", map[string]string{"vendor": "0x1a03", "device": "0x2000"})
		// An output of a card: its device is the card.
		out := filepath.Join(root, "card0-DP-1")
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(out, "status"), []byte("connected\n"), 0o644)
		if err := os.Symlink("../card0", filepath.Join(out, "device")); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(root, "renderD128"), 0o755)
		os.WriteFile(filepath.Join(root, "version"), []byte("drm 1.1.0 20060810\n"), 0o644)
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("unshare", "--user", "--map-root-user", "--mount", "sh", "-c",
			`mount --bind "$1" /sys/class/drm && exec "$2" -test.count=1 -test.run "$3"`, "sh", root, exe, "^"+t.Name()+"$")
		cmd.Env = append(os.Environ(), "PROBE_LONG1_SYSFS=1")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("the probe in a mount namespace: %v\n%s", err, b)
		}
		return
	}
	p := probeStart(t, []map[string]string{probeGPU("NVIDIA GeForce RTX 4080", "3", "9000", "16000")})
	probe(probeGPUs(p.wait("the GPU of nvidia-smi and the cards of /sys/class/drm", probeHasGPUs(3))))
}

// On Linux, AMD and Intel GPUs come from /sys/class/drm. The name of such a GPU is the maker and the device id without 0x, for example "AMD 7480". The memory files have bytes.
func TestProbeSysfsAMDCard(t *testing.T) {
	probeSysfs(t, func(g map[string]map[string]any) {
		probeValues(t, g["AMD 7480"], 8176, 7069, 12)
		probeValues(t, g["NVIDIA GeForce RTX 4080"], 16000, 9000, 3)
	})
}

// A card with no file for a value has no value (most Intel cards have no memory files).
func TestProbeSysfsCardWithNoMemoryFiles(t *testing.T) {
	probeSysfs(t, func(g map[string]map[string]any) {
		probeValues(t, g["Intel 46a6"], -1, -1, -1)
	})
}

// Do not report cards of NVIDIA from this directory, and do not report cards of other makers. A directory such as card0-DP-1 is an output of a card, not a card.
func TestProbeSysfsOnlyAMDAndIntelCards(t *testing.T) {
	probeSysfs(t, func(g map[string]map[string]any) {
		var names []string
		for n := range g {
			names = append(names, n)
		}
		if len(g) != 3 || g["AMD 7480"] == nil || g["Intel 46a6"] == nil || g["NVIDIA GeForce RTX 4080"] == nil {
			t.Errorf("the GPUs are %q, want the GPU of nvidia-smi, AMD 7480, and Intel 46a6", names)
		}
	})
}
