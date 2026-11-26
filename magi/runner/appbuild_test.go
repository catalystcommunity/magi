package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAppVersionCode(t *testing.T) {
	// The same count as tools/build-android.sh: the minute since 2020.
	at := time.Date(2026, 11, 23, 22, 35, 59, 0, time.UTC)
	if got := appVersionCode(at); got != 3627275 {
		t.Fatalf("version code = %d", got)
	}
	if appVersionCode(at.Add(time.Minute)) != 3627276 {
		t.Fatal("the next minute is not the next code")
	}
}

func TestAppBuildScript(t *testing.T) {
	s := appBuildScript("/opt/gradle 8", 41, "0.1.0-abc")
	for _, want := range []string{"tar -xf app-src.tar", "'/opt/gradle 8/bin/gradle'", "--offline", "-PmagiVersionCode=41", "-PmagiVersionName='0.1.0-abc'", "assembleDebug"} {
		if !strings.Contains(s, want) {
			t.Errorf("the script has no %q:\n%s", want, s)
		}
	}
}

// The build reads the voice files of the app from a directory of the host:
// the view has it, and the build gets its path.
func TestAppBuildVoice(t *testing.T) {
	c := AppBuild{Sdk: "/sdk", Gradle: "/gradle", JavaHome: "/jdk", DepCache: "/cache"}
	if slices.Contains(c.env(), "MAGI_ANDROID_VOICE=") || len(c.readOnly()) != 4 {
		t.Fatalf("with no voice directory: %v %v", c.env(), c.readOnly())
	}
	c.Voice = "/voice"
	if !slices.Contains(c.env(), "MAGI_ANDROID_VOICE=/voice") || !slices.Contains(c.readOnly(), "/voice") {
		t.Fatalf("with a voice directory: %v %v", c.env(), c.readOnly())
	}
}

// The view of a build has no network: code of a change cannot send what it
// reads. The view of the checker keeps the network for its coding agent.
func TestHostViewNetwork(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the view is for Linux")
	}
	dir := t.TempDir()
	closed, err := hostView("bwrap", t.TempDir(), dir, []string{"/bin/sh", "-c", "true"}, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(closed, "--share-net") || !slices.Contains(closed, "--unshare-all") {
		t.Fatalf("the build view: %v", closed)
	}
	open, err := checkerView("bwrap", t.TempDir(), dir, []string{"/bin/sh", "-c", "true"}, nil, nil)
	if err != nil || !slices.Contains(open, "--share-net") {
		t.Fatalf("the checker view: %v %v", open, err)
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed")
	}
	// A command in the build view has no real network interfaces.
	// Only loopback and virtual interfaces (tunnels, etc.) are acceptable.
	argv, err := hostView("bwrap", t.TempDir(), dir, []string{"/bin/sh", "-c", "tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' ' > " + dir + "/net"}, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Skipf("the view does not start here: %v %s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "net"))
	if err != nil {
		t.Fatalf("read interfaces: %v", err)
	}
	ifaces := strings.Split(strings.TrimSpace(string(b)), "\n")
	for _, iface := range ifaces {
		if iface == "" {
			continue
		}
		if iface != "lo" && !strings.HasPrefix(iface, "tun") && !strings.HasPrefix(iface, "sit") && !strings.HasPrefix(iface, "veth") && !strings.HasPrefix(iface, "docker") && !strings.HasPrefix(iface, "br-") {
			t.Fatalf("real network interface in the build view: %q", iface)
		}
	}
}

// A real build of the app of this repository, on a host with the Android
// toolchains. Set MAGI_TEST_APP_BUILD to a JSON object of AppBuild.
func TestAppBuildOnThisHost(t *testing.T) {
	spec := os.Getenv("MAGI_TEST_APP_BUILD")
	if spec == "" {
		t.Skip("MAGI_TEST_APP_BUILD is not set")
	}
	var c AppBuild
	if err := json.Unmarshal([]byte(spec), &c); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "archive", "-o", filepath.Join(dir, "app-src.tar"), "HEAD", "android", "clients/kotlin").CombinedOutput(); err != nil {
		t.Fatalf("git archive: %v %s", err, out)
	}
	apk, err := c.build(context.Background(), dir, 41, "0.1.0-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(apk) < 100_000 || string(apk[:2]) != "PK" {
		t.Fatalf("the package has %d bytes", len(apk))
	}
	// The signature is of the key of the configuration, not of a key that the build made.
	signer, err := apksigner(c.Sdk)
	if err != nil {
		t.Fatal(err)
	}
	signed := filepath.Join(dir, "check.apk")
	if err := os.WriteFile(signed, apk, 0o600); err != nil {
		t.Fatal(err)
	}
	verify := exec.Command(signer, "verify", "--print-certs", signed)
	verify.Env = []string{"PATH=/usr/bin:/bin", "JAVA_HOME=" + c.JavaHome}
	got, err := verify.CombinedOutput()
	if err != nil {
		t.Fatalf("verify: %v %s", err, got)
	}
	keytool := exec.Command(filepath.Join(c.JavaHome, "bin", "keytool"), "-list", "-v", "-keystore", c.Keystore, "-storepass", "android")
	want, err := keytool.CombinedOutput()
	if err != nil {
		t.Fatalf("keytool: %v %s", err, want)
	}
	digest := regexp.MustCompile(`certificate SHA-256 digest: ([0-9a-f]{64})`).FindStringSubmatch(string(got))
	if digest == nil || !strings.Contains(strings.ToLower(strings.ReplaceAll(string(want), ":", "")), digest[1]) {
		t.Fatalf("the package is not signed with the keystore: %s", got)
	}
}
