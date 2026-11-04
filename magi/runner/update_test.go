package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPackUnpack(t *testing.T) {
	if testing.Short() {
		t.Skip("builds magi-runner")
	}
	src, _ := filepath.Abs("../..")
	a, err := PackBytes(src, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	b, err := PackBytes(src, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("two packs of the same source differ")
	}
	state := t.TempDir()
	if err := Install(state, "v1", a); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"magi-runner", "VERSION", "apprentice/magi_apprentice/__main__.py", "apprentice/magi_rpc.py"} {
		if _, err := os.Stat(filepath.Join(state, "current", f)); err != nil {
			t.Errorf("installed release: %v", err)
		}
	}
	if currentVersion(state) != "v1" {
		t.Fatalf("current = %q", currentVersion(state))
	}
}

func TestUnpackRefusesBadEntries(t *testing.T) {
	for _, name := range []string{"../evil", "/etc/evil"} {
		pkg := tarGz(t, map[string]string{name: "x", "magi-runner": "#!/bin/sh\n"})
		if err := Unpack(pkg, filepath.Join(t.TempDir(), "r")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := Unpack(tarGz(t, map[string]string{"README": "x"}), filepath.Join(t.TempDir(), "r")); err == nil {
		t.Error("a package without magi-runner was accepted")
	}
}

// A trial release that exits before its first hello is rolled back, and
// the error is recorded for the next hello.
func TestSuperviseRollsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	restartDelay = 10 * time.Millisecond
	state := t.TempDir()
	cfg := Config{StateDir: state}
	// v1 is healthy: it marks itself and then runs until stopped.
	release(t, state, "v1", "echo -n v1 > "+state+"/healthy; echo ran >> "+state+"/v1.log; exec sleep 60")
	// v2 is broken: it exits at once.
	release(t, state, "v2", "echo ran >> "+state+"/v2.log; exit 3")
	if err := switchCurrent(state, "v2"); err != nil {
		t.Fatal(err)
	}
	writeJSON(filepath.Join(state, "trial"), trial{Version: "v2", Previous: "v1"})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Supervise(ctx, "cfg.json", cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(filepath.Join(state, "v1.log")); len(b) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if v := currentVersion(state); v != "v1" {
		t.Fatalf("current = %s, want the rollback to v1", v)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "v2.log")); strings.Count(string(b), "ran") != 2 {
		t.Fatalf("v2 ran %q times, want 2 before the rollback", b)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "update-error")); !strings.Contains(string(b), "update to v2 failed") {
		t.Fatalf("update-error = %q", b)
	}
	if _, err := os.Stat(filepath.Join(state, "trial")); err == nil {
		t.Fatal("the trial record stays after the rollback")
	}
}

// A trial release that becomes healthy stays.
func TestSuperviseKeepsHealthyTrial(t *testing.T) {
	restartDelay = 10 * time.Millisecond
	state := t.TempDir()
	release(t, state, "v1", "exec sleep 60")
	release(t, state, "v2", "echo -n v2 > "+state+"/healthy; exec sleep 60")
	switchCurrent(state, "v2")
	writeJSON(filepath.Join(state, "trial"), trial{Version: "v2", Previous: "v1"})
	TrialTimeout = 300 * time.Millisecond
	defer func() { TrialTimeout = 3 * time.Minute }()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := Supervise(ctx, "cfg.json", Config{StateDir: state}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if v := currentVersion(state); v != "v2" {
		t.Fatalf("current = %s, want v2", v)
	}
	if _, err := os.Stat(filepath.Join(state, "update-error")); err == nil {
		t.Fatal("a healthy trial recorded an error")
	}
}

func release(t *testing.T, state, v, script string) {
	t.Helper()
	d := filepath.Join(state, "releases", v)
	os.MkdirAll(d, 0o755)
	if err := os.WriteFile(filepath.Join(d, "magi-runner"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	for n, body := range files {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return b.Bytes()
}

// A trial release that runs but never says hello is rolled back after
// TrialTimeout.
func TestSuperviseRollsBackHungTrial(t *testing.T) {
	restartDelay = 10 * time.Millisecond
	state := t.TempDir()
	release(t, state, "v1", "echo -n v1 > "+state+"/healthy; exec sleep 60")
	release(t, state, "v2", "exec sleep 60")
	switchCurrent(state, "v2")
	writeJSON(filepath.Join(state, "trial"), trial{Version: "v2", Previous: "v1"})
	TrialTimeout = 200 * time.Millisecond
	defer func() { TrialTimeout = 3 * time.Minute }()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := Supervise(ctx, "cfg.json", Config{StateDir: state}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if v := currentVersion(state); v != "v1" {
		t.Fatalf("current = %s, want the rollback to v1", v)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "update-error")); !strings.Contains(string(b), "not healthy") {
		t.Fatalf("update-error = %q", b)
	}
}

// A healthy release that installs the next update: the new trial must
// survive the restart, and a broken next release must roll back to the
// healthy one. (A bug once removed the new trial record here.)
func TestSuperviseTwoUpdatesInARow(t *testing.T) {
	restartDelay = 10 * time.Millisecond
	state := t.TempDir()
	// v2 becomes healthy, then installs v3 as a trial and asks for a restart.
	release(t, state, "v2", `echo -n v2 > `+state+`/healthy
sleep 1.5
printf '{"version":"v3","previous":"v2"}' > `+state+`/trial
ln -sfn releases/v3 `+state+`/current
exit 75`)
	release(t, state, "v3", "echo ran >> "+state+"/v3.log; exit 3")
	release(t, state, "v1", "exec sleep 60")
	switchCurrent(state, "v2")
	writeJSON(filepath.Join(state, "trial"), trial{Version: "v2", Previous: "v1"})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Supervise(ctx, "cfg.json", Config{StateDir: state}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && currentVersion(state) != "v2" || !fileHas(filepath.Join(state, "update-error"), "v3") {
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	<-done
	if v := currentVersion(state); v != "v2" {
		t.Fatalf("current = %s, want the rollback from v3 to v2", v)
	}
	if !fileHas(filepath.Join(state, "update-error"), "update to v3 failed") {
		b, _ := os.ReadFile(filepath.Join(state, "update-error"))
		t.Fatalf("update-error = %q", b)
	}
}

func fileHas(p, s string) bool {
	b, _ := os.ReadFile(p)
	return strings.Contains(string(b), s)
}
