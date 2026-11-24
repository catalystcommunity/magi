package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// AppBuild lets the merge step build the Android app of a merged change
// (android/ of the repository). Sdk empty: this runner builds no app.
//
// The build runs code of the change (Gradle scripts). It runs in a view of
// the host with no network and an empty home directory (checkerview.go).
// The view has the toolchains and a cache of the dependencies, read-only.
// The signing key is not in the view: the runner signs the package after
// the build, so code of a change cannot read the key.
type AppBuild struct {
	// Sdk is the Android SDK (ANDROID_HOME).
	Sdk string `json:"sdk"`
	// Gradle is the directory of a Gradle distribution (it has bin/gradle).
	Gradle string `json:"gradle"`
	// JavaHome is a JDK 17.
	JavaHome string `json:"java_home"`
	// DepCache is a Gradle cache directory of this host that has the
	// dependencies of the app (the directory with modules-2). The build is
	// offline and reads them from there (GRADLE_RO_DEP_CACHE).
	DepCache string `json:"dep_cache"`
	// Keystore signs the package. Updates of an installed app need the
	// same key each time. KeyAlias defaults to androiddebugkey.
	Keystore string `json:"keystore"`
	KeyAlias string `json:"key_alias"`
	// KeystorePassFile is a file with the password of the keystore. Empty:
	// the password of an Android debug keystore.
	KeystorePassFile string `json:"keystore_pass_file"`
}

// appPaths are the parts of the repository that the app is built from.
var appPaths = []string{"android", "clients/kotlin"}

// appEpoch is the start of the count of app version codes
// (tools/build-android.sh): 2020-01-01 UTC.
const appEpoch = 1577836800

// appVersionCode is the version code of a build at t: the minute since
// appEpoch, so that each build is newer than the one before it.
func appVersionCode(t time.Time) uint64 { return uint64((t.Unix() - appEpoch) / 60) }

// appBuildScript unpacks the sources of the app and builds the debug
// package with no network.
func appBuildScript(gradle string, code uint64, name string) string {
	return fmt.Sprintf("set -e\ntar -xf app-src.tar\nrm app-src.tar\n%s -q -p android --offline --no-daemon -PmagiVersionCode=%d -PmagiVersionName=%s assembleDebug\n",
		shellQuote(filepath.Join(gradle, "bin", "gradle")), code, shellQuote(name))
}

// apksigner returns the apksigner of the newest build tools of the SDK.
func apksigner(sdk string) (string, error) {
	found, _ := filepath.Glob(filepath.Join(sdk, "build-tools", "*", "apksigner"))
	if len(found) == 0 {
		return "", fmt.Errorf("no apksigner in %s/build-tools", sdk)
	}
	sort.Strings(found)
	return found[len(found)-1], nil
}

// buildApp builds the Android app of the merged commit when the change
// touched it, signs the package, and uploads it. The rollout step of Magi
// makes it the app release. A failure does not stop the merge: the app is
// not a part of the release of the server. The person gets a notice.
func (r *Runner) buildApp(ctx context.Context, env, workdir, base, commit string, st magiapi.RunnerStep, out map[string]string) {
	c := r.Cfg.App
	res, err := r.exec(ctx, env, workdir, fmt.Sprintf("git diff --name-only %s %s -- %s | head -n 1", shellQuote(base), shellQuote(commit), strings.Join(appPaths, " ")), 60000)
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(string(res.Stdout)) == "" {
		return
	}
	if c.Sdk == "" {
		r.note(ctx, st, "The change touched the Android app. This runner does not build the app, so there is no new app build.")
		return
	}
	r.note(ctx, st, "The change touched the Android app. Building the app.")
	fail := func(why string) {
		out["app.error"] = tail(why, 2000)
		r.report(ctx, st, "normal", "App not built", "The change is merged, but the Android app did not build, so there is no new app build: "+tail(why, 600))
	}
	w := r.Boxes.Work(env)
	res, err = r.exec(ctx, env, workdir, fmt.Sprintf("git archive -o %s/app-src.tar %s %s", shellQuote(w), shellQuote(commit), strings.Join(appPaths, " ")), 120000)
	if err != nil || res.ExitCode != 0 {
		fail(fmt.Sprintf("git archive: %v %s", err, res.Stderr))
		return
	}
	src, err := r.readFile(ctx, env, w+"/app-src.tar", 256<<20)
	if err != nil {
		fail(err.Error())
		return
	}
	dir, err := os.MkdirTemp(r.Cfg.StateDir, "app-build-")
	if err != nil {
		fail(err.Error())
		return
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "app-src.tar"), src, 0o600); err != nil {
		fail(err.Error())
		return
	}
	code := appVersionCode(time.Now())
	name := "0.1.0-" + commit
	if len(commit) > 12 {
		name = "0.1.0-" + commit[:12]
	}
	apk, err := c.build(ctx, dir, code, name)
	if err != nil {
		fail(err.Error())
		return
	}
	d, err := r.Magi.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Step: st, MediaType: "application/vnd.android.package-archive", Body: apk,
		Labels: map[string]string{"kind": "app-build", "commit": commit, "version_code": strconv.FormatUint(code, 10)}})
	if err != nil {
		fail("upload: " + err.Error())
		return
	}
	out["app.package"], out["app.version_code"], out["app.version_name"] = d.Digest, strconv.FormatUint(code, 10), name
	r.note(ctx, st, fmt.Sprintf("Built app build %d (%s).", code, name))
}

// build makes the signed package from dir/app-src.tar.
func (c AppBuild) build(ctx context.Context, dir string, code uint64, name string) ([]byte, error) {
	home, _ := os.UserHomeDir()
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("the app build needs bubblewrap (bwrap) on this host: %w", err)
	}
	gradleHome := filepath.Join(dir, ".gradle-home")
	if err := os.Mkdir(gradleHome, 0o700); err != nil {
		return nil, err
	}
	argv, err := hostView(bwrap, home, dir, []string{"/bin/sh", "-c", appBuildScript(c.Gradle, code, name)}, nil,
		[]string{c.Sdk, c.Gradle, c.JavaHome, filepath.Join(c.DepCache, "modules-2")}, false)
	if err != nil {
		return nil, err
	}
	bctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(bctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TERM=dumb", "ANDROID_HOME=" + c.Sdk,
		"JAVA_HOME=" + c.JavaHome, "GRADLE_USER_HOME=" + gradleHome, "GRADLE_RO_DEP_CACHE=" + c.DepCache}
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gradle: %v\n%s", err, tail(string(b), 3000))
	}
	built := filepath.Join(dir, "android", "app", "build", "outputs", "apk", "debug", "app-debug.apk")
	if fi, err := os.Lstat(built); err != nil || !fi.Mode().IsRegular() {
		// A link here could name a file of the host for the signer.
		return nil, fmt.Errorf("the build made no package file")
	}
	// The build signed the package with a key that it made. The key of
	// this host replaces that signature, outside the view.
	signer, err := apksigner(c.Sdk)
	if err != nil {
		return nil, err
	}
	pass := "android"
	if c.KeystorePassFile != "" {
		b, err := os.ReadFile(c.KeystorePassFile)
		if err != nil {
			return nil, fmt.Errorf("keystore password: %w", err)
		}
		pass = strings.TrimSpace(string(b))
	}
	alias := c.KeyAlias
	if alias == "" {
		alias = "androiddebugkey"
	}
	signed := filepath.Join(dir, "signed.apk")
	// The password goes in a variable: an argument is public on the host.
	sign := exec.CommandContext(bctx, signer, "sign", "--ks", c.Keystore, "--ks-key-alias", alias, "--ks-pass", "env:MAGI_KEYSTORE_PASS",
		"--key-pass", "env:MAGI_KEYSTORE_PASS", "--out", signed, built)
	sign.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "JAVA_HOME=" + c.JavaHome, "MAGI_KEYSTORE_PASS=" + pass}
	if b, err := sign.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("apksigner: %v\n%s", err, tail(string(b), 1000))
	}
	return os.ReadFile(signed)
}
