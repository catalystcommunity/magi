// Package testenv gives tests a disposable database, test tokens, and envd builds.
package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/jackc/pgx/v5"
)

// Database creates a uniquely named database and returns its URL. The
// database is dropped when the test ends. The test is skipped when
// MAGI_TEST_DATABASE_URL is not set.
func Database(t testing.TB) string {
	t.Helper()
	base := os.Getenv("MAGI_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("MAGI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	b := make([]byte, 6)
	rand.Read(b)
	name := "magi_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Keys is a test token issuer with its key file on disk.
type Keys struct {
	Issuer  *authn.Issuer
	KeyFile string
}

// NewKeys writes a new issuer key file in a temporary directory.
func NewKeys(t testing.TB) Keys {
	t.Helper()
	kf, err := authn.GenerateKeyFile("magi-test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "issuer.json")
	b, _ := json.Marshal(kf)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	iss, err := authn.LoadIssuer(path)
	if err != nil {
		t.Fatal(err)
	}
	return Keys{Issuer: iss, KeyFile: path}
}

// Verifier loads a verifier from the key file.
func (k Keys) Verifier(t testing.TB) *authn.Verifier {
	t.Helper()
	v, err := authn.LoadVerifier(k.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Token issues a token for subject with scopes. Subjects that start with
// "service:" get the service kind.
func (k Keys) Token(t testing.TB, subject string, scopes ...string) string {
	t.Helper()
	kind := authn.KindUser
	if len(subject) > 8 && subject[:8] == "service:" {
		kind = authn.KindService
	}
	tok, err := k.Issuer.Issue(authn.Principal{Subject: subject, Kind: kind, Scopes: scopes}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// BuildEnvd builds static envd binaries for the given platforms (such as
// "linux-amd64") into a new directory. The caller removes the directory.
func BuildEnvd(platforms ...string) (string, error) {
	dir, err := os.MkdirTemp("", "magi-envd-")
	if err != nil {
		return "", err
	}
	_, file, _, _ := runtime.Caller(0)
	module := filepath.Join(filepath.Dir(file), "..", "..")
	for _, p := range platforms {
		goos, goarch, _ := strings.Cut(p, "-")
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(dir, "envd-"+p), "./cmd/envd")
		cmd.Dir = module
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("build envd %s: %v: %s", p, err, out)
		}
	}
	return dir, nil
}
