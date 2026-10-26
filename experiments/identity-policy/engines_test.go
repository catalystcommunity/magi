package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeFixture(t testing.TB, dir, name string, value any) string {
	t.Helper()
	var b []byte
	if text, ok := value.(string); ok {
		b = []byte(text)
	} else {
		var err error
		b, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func prefixed(expression, prefix string) string {
	keys := append(append([]string{}, stringFacts...), boolFacts...)
	re := regexp.MustCompile(`\b(` + strings.Join(keys, "|") + `)\b`)
	return re.ReplaceAllString(expression, prefix+"$1")
}

type cedarEngine struct{ binary, policy, entities, dir string }

func newCedar(t testing.TB) *cedarEngine {
	t.Helper()
	binary := os.Getenv("OPAL_W02_CEDAR")
	if binary == "" {
		t.Skip("set OPAL_W02_CEDAR to the verified Cedar 4.13.0 executable")
	}
	checkVersion(t, binary, "cedar-policy-cli 4.13.0")
	dir := t.TempDir()
	p := `permit(principal, action, resource) when { ` + prefixed(permitExpression, "context.") + ` };` + "\n" + `forbid(principal, action, resource) when { context.blocked };`
	return &cedarEngine{binary: binary, dir: dir, policy: writeFixture(t, dir, "policy.cedar", p), entities: writeFixture(t, dir, "entities.json", []any{})}
}

func (e *cedarEngine) evaluate(t testing.TB, facts map[string]any) (native, strict bool, output string, elapsed time.Duration) {
	t.Helper()
	file := writeFixture(t, e.dir, "context.json", facts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, e.binary, "authorize", "--entities", e.entities, "--policies", e.policy, "--context", file, "--principal", `Actor::"fixture"`, "--action", `Action::"access"`, "--resource", `Resource::"fixture"`, "--verbose", "--timing", "--error-format", "plain")
	out, err := cmd.CombinedOutput()
	if cmd.ProcessState != nil {
		if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			t.Logf("Cedar maxrss_kib=%d", usage.Maxrss)
		}
	}
	elapsed = time.Since(start)
	output = string(out)
	// The CLI uses a nonzero exit code for DENY. Every other failure fails the test.
	if err != nil && !strings.Contains(output, "DENY") {
		t.Fatalf("Cedar: %v %s", err, output)
	}
	native = strings.Contains(output, "\nALLOW\n") || strings.HasPrefix(output, "ALLOW\n")
	if !native && !strings.Contains(output, "DENY") {
		t.Fatalf("unrecognized Cedar output: %s", output)
	}
	strict = native && !strings.Contains(output, "error while evaluating policy")
	return
}

func TestCedar(t *testing.T) {
	e := newCedar(t)
	for _, reverse := range []bool{false, true} {
		for _, s := range scenarios() {
			t.Run(fmt.Sprintf("reverse_%t/%s", reverse, s.name), func(t *testing.T) {
				f := newFixture(t, reverse)
				s.edit(f)
				a, q, attr := f.messages()
				facts, err := f.r.facts(a, q, attr)
				if err != nil {
					t.Fatal(err)
				}
				native, strict, out, elapsed := e.evaluate(t, facts)
				if native != s.allow || strict != s.allow {
					t.Fatalf("native=%t strict=%t want=%t %s", native, strict, s.allow, out)
				}
				t.Logf("process_us=%d %s", elapsed.Microseconds(), strings.TrimSpace(out))
			})
		}
	}
	t.Run("missing_deny_fact_native_allows_wrapper_denies", func(t *testing.T) {
		f := newFixture(t, false)
		a, q, attr := f.messages()
		facts, err := f.r.facts(a, q, attr)
		if err != nil {
			t.Fatal(err)
		}
		delete(facts, "blocked")
		native, strict, out, _ := e.evaluate(t, facts)
		if !native || strict {
			t.Fatalf("unexpected Cedar error semantics: %s", out)
		}
		t.Log(out)
	})
	t.Run("schema_validation", func(t *testing.T) {
		fields := []string{}
		for _, k := range stringFacts {
			fields = append(fields, k+": String")
		}
		for _, k := range boolFacts {
			fields = append(fields, k+": Bool")
		}
		schema := `entity Actor; entity Resource; action "access" appliesTo { principal: [Actor], resource: [Resource], context: { ` + strings.Join(fields, ", ") + ` } };`
		path := writeFixture(t, e.dir, "schema.cedarschema", schema)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, e.binary, "validate", "--schema", path, "--policies", e.policy).CombinedOutput(); err != nil {
			t.Fatalf("valid policy rejected: %v %s", err, out)
		}
		bad := writeFixture(t, e.dir, "bad.cedar", `permit(principal, action, resource) when { context.unknown };`)
		if out, err := exec.CommandContext(ctx, e.binary, "validate", "--schema", path, "--policies", bad).CombinedOutput(); err == nil {
			t.Fatalf("invalid policy accepted: %s", out)
		}
	})
}

type cerbosEngine struct {
	base   string
	client *http.Client
	pid    int
	start  time.Duration
}

func freeAddress(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func newCerbos(t testing.TB, strict bool) *cerbosEngine {
	t.Helper()
	binary := os.Getenv("OPAL_W02_CERBOS")
	if binary == "" {
		t.Skip("set OPAL_W02_CERBOS to the verified Cerbos 0.55.0 executable")
	}
	checkVersion(t, binary, "0.55.0")
	dir := t.TempDir()
	rule := func(name, effect, expression string) map[string]any {
		return map[string]any{"name": name, "actions": []string{"access"}, "effect": effect, "roles": []string{"authenticated"}, "condition": map[string]any{"match": map[string]string{"expr": expression}}}
	}
	policy := map[string]any{"apiVersion": "api.cerbos.dev/v1", "resourcePolicy": map[string]any{"resource": "opal", "version": "default", "rules": []any{rule("permit", "EFFECT_ALLOW", prefixed(permitExpression, "P.attr.")), rule("deny", "EFFECT_DENY", "P.attr.blocked")}}}
	writeFixture(t, dir, "policy.yaml", policy)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	out, err := exec.CommandContext(ctx, binary, "compile", dir, "--skip-tests").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("Cerbos compile: %v %s", err, out)
	}
	addr := freeAddress(t)
	grpc := freeAddress(t)
	debug := freeAddress(t)
	logPath := filepath.Join(dir, "server.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// No inherited Hub credentials or proxy settings. Both listeners and the
	// debug endpoint bind loopback. Telemetry and policy watching are disabled.
	cmd := exec.Command(binary, "server", "--log-level=error", "--debug-listen-addr="+debug,
		"--set=server.httpListenAddr="+addr, "--set=server.grpcListenAddr="+grpc,
		"--set=storage.driver=disk", "--set=storage.disk.directory="+dir,
		"--set=storage.disk.watchForChanges=false", "--set=telemetry.disabled=true",
		"--set=engine.strictEvaluation="+strconv.FormatBool(strict))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Dir = dir
	cmd.Stdout = log
	cmd.Stderr = log
	start := time.Now()
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Cerbos process did not exit")
		}
		_ = log.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Logf("Cerbos logs: %s", b)
		}
	})
	e := &cerbosEngine{base: "http://" + addr, client: &http.Client{Timeout: 2 * time.Second}, pid: cmd.Process.Pid}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, err := e.client.Get(e.base + "/_cerbos/health")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == 200 {
				e.start = time.Since(start)
				t.Logf("Cerbos strict=%t startup_us=%d rss=%s", strict, e.start.Microseconds(), residentMemory(e.pid))
				return e
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Cerbos readiness timeout")
	return nil
}

func residentMemory(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
		}
	}
	return "unavailable"
}

func (e *cerbosEngine) evaluate(t testing.TB, facts map[string]any) (bool, time.Duration) {
	t.Helper()
	req := map[string]any{"requestId": "w02", "principal": map[string]any{"id": "fixture", "roles": []string{"authenticated"}, "attr": facts}, "resources": []any{map[string]any{"resource": map[string]any{"kind": "opal", "id": "fixture", "attr": map[string]any{}}, "actions": []string{"access"}}}}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := e.client.Post(e.base+"/api/check/resources", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, 65536))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("Cerbos HTTP %d: %s", res.StatusCode, out)
	}
	var reply struct {
		Results []struct{ Actions map[string]string }
	}
	if err := json.Unmarshal(out, &reply); err != nil || len(reply.Results) != 1 {
		t.Fatalf("Cerbos reply: %s %v", out, err)
	}
	effect := reply.Results[0].Actions["access"]
	if effect != "EFFECT_ALLOW" && effect != "EFFECT_DENY" {
		t.Fatalf("unknown effect: %s", out)
	}
	return effect == "EFFECT_ALLOW", time.Since(start)
}

func TestCerbos(t *testing.T) {
	e := newCerbos(t, true)
	for _, reverse := range []bool{false, true} {
		for _, s := range scenarios() {
			t.Run(fmt.Sprintf("reverse_%t/%s", reverse, s.name), func(t *testing.T) {
				f := newFixture(t, reverse)
				s.edit(f)
				a, q, attr := f.messages()
				facts, err := f.r.facts(a, q, attr)
				if err != nil {
					t.Fatal(err)
				}
				allow, elapsed := e.evaluate(t, facts)
				if allow != s.allow {
					t.Fatalf("allow=%t want=%t", allow, s.allow)
				}
				t.Logf("http_us=%d", elapsed.Microseconds())
			})
		}
	}
	t.Run("missing_deny_fact", func(t *testing.T) {
		f := newFixture(t, false)
		a, q, attr := f.messages()
		facts, err := f.r.facts(a, q, attr)
		if err != nil {
			t.Fatal(err)
		}
		delete(facts, "blocked")
		if allow, _ := e.evaluate(t, facts); allow {
			t.Fatal("strict evaluation allowed missing deny attribute")
		}
	})
	t.Logf("Cerbos post-fixture rss=%s", residentMemory(e.pid))
}

func TestCerbosDefaultErrorSemantics(t *testing.T) {
	e := newCerbos(t, false)
	f := newFixture(t, false)
	a, q, attr := f.messages()
	facts, err := f.r.facts(a, q, attr)
	if err != nil {
		t.Fatal(err)
	}
	delete(facts, "blocked")
	allow, _ := e.evaluate(t, facts)
	t.Logf("Cerbos default missing-deny-fact allow=%t", allow)
	if !allow {
		t.Fatal("default semantics changed; review the version and report")
	}
	// This passing test records an unsafe default, not an approved configuration.
}

func checkVersion(t testing.TB, binary, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil || strings.Split(strings.TrimSpace(string(b)), "\n")[0] != want {
		t.Fatalf("tool version: want %s, got %s, error=%v", want, b, err)
	}
}

func TestCedarNativePrecision(t *testing.T) {
	e := newCedar(t)
	for _, p := range []struct {
		name, expression string
		valid            bool
	}{
		{"decimal_four_places", `decimal("1.0001").lessThan(decimal("2.0"))`, true},
		{"decimal_five_places", `decimal("1.00001").lessThan(decimal("2.0"))`, false},
		{"timestamp_milliseconds", `datetime("2026-09-30T12:00:00.123Z") == datetime("2026-09-30T12:00:00.123Z")`, true},
		{"timestamp_nanoseconds", `datetime("2026-09-30T12:00:00.123456789Z") == datetime("2026-09-30T12:00:00.123456789Z")`, false},
	} {
		t.Run(p.name, func(t *testing.T) {
			e.policy = writeFixture(t, e.dir, "precision.cedar", `permit(principal, action, resource) when { `+p.expression+` };`)
			native, _, out, _ := e.evaluate(t, map[string]any{})
			if native != p.valid {
				t.Fatalf("precision result: %s", out)
			}
			t.Log(out)
		})
	}
}

func TestCerbosInvalidPolicy(t *testing.T) {
	binary := os.Getenv("OPAL_W02_CERBOS")
	if binary == "" {
		t.Skip("set OPAL_W02_CERBOS")
	}
	dir := t.TempDir()
	writeFixture(t, dir, "bad.yaml", `apiVersion: api.cerbos.dev/v1
resourcePolicy:
  resource: opal
  version: default
  rules:
    - actions: [access]
      roles: [authenticated]
      effect: EFFECT_ALLOW
      condition:
        match:
          expr: "true &&"
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "compile", dir, "--skip-tests").CombinedOutput()
	if err == nil {
		t.Fatalf("invalid Cerbos policy accepted: %s", out)
	}
}
