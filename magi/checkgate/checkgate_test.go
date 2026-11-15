package checkgate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptPath(t *testing.T) {
	for in, want := range map[string]string{
		"./tools/check.sh":      "tools/check.sh",
		"tools/check.sh --fast": "tools/check.sh",
		"go test ./...":         "",
		"/usr/bin/true":         "",
		"../x/check.sh":         "",
		"$HOME/check.sh":        "",
		"make":                  "",
		"":                      "",
	} {
		if got := ScriptPath(in); got != want {
			t.Errorf("ScriptPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuardedWithoutScriptIsUnchanged(t *testing.T) {
	if got := Guarded("go test ./...", "abc", false); got != "go test ./..." {
		t.Fatal(got)
	}
	if got := Guarded("./tools/check.sh", "abc", true); got != "./tools/check.sh" {
		t.Fatal("an approved script must run as it is:", got)
	}
}

// run executes the guarded check in a real git repository.
func TestGuardedRefusesChangedScript(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	sh := func(script string) (string, int) {
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}
	if err := os.MkdirAll(filepath.Join(dir, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools/check.sh"), []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := sh(`git init -q . && git add -A && git -c user.name=t -c user.email=t@t commit -qm base && git rev-parse HEAD`)
	if code != 0 {
		t.Fatal(out)
	}
	base := strings.TrimSpace(out)
	guard := Guarded("./tools/check.sh", base, false)

	if out, code := sh("{\n" + guard + "\n} 2>&1"); code != 0 || !strings.Contains(out, "ran") {
		t.Fatalf("unchanged script must run: %d %q", code, out)
	}
	// An edit that is not committed.
	os.WriteFile(filepath.Join(dir, "tools/check.sh"), []byte("#!/bin/sh\necho evil\n"), 0o755)
	if out, code := sh("{\n" + guard + "\n} 2>&1"); code != ExitCode || strings.Contains(out, "evil") {
		t.Fatalf("edited script must not run: %d %q", code, out)
	}
	// The same edit, approved by a person.
	if out, code := sh(Guarded("./tools/check.sh", base, true)); code != 0 || !strings.Contains(out, "evil") {
		t.Fatalf("approved script must run: %d %q", code, out)
	}
	// A committed edit still differs from the base commit.
	if out, code := sh(`git add -A && git -c user.name=t -c user.email=t@t commit -qm edit`); code != 0 {
		t.Fatal(out)
	}
	if _, code := sh(guard); code != ExitCode {
		t.Fatalf("committed edit must not run: %d", code)
	}
	// A deleted script.
	sh(`git checkout -q ` + base + ` -- . && rm tools/check.sh`)
	if _, code := sh(guard); code != ExitCode {
		t.Fatalf("deleted script must not run: %d", code)
	}
}

func TestExports(t *testing.T) {
	got := Exports(map[string]string{"GH_TOKEN": "ph'1", "A": "b"})
	if got != "export A='b'\nexport GH_TOKEN='ph'\\''1'\n" {
		t.Fatalf("%q", got)
	}
}
