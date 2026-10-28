// Package backend runs environments. Each backend starts the same envd
// agent inside the environment and reports where to reach it. A container,
// a VM, or a host backend differs only in how it starts envd and how the
// control plane connects to it.
package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
)

// States that Status reports.
const (
	Creating = "creating"
	Ready    = "ready"
	Failed   = "failed"
	Gone     = "gone"
)

// EnvdPort is the port envd listens on inside an environment.
const EnvdPort = 7070

// Status is the state of one environment in its backend.
type Status struct {
	State   string
	Message string
	// Endpoint is the envd base URL when State is Ready. It is valid only
	// with the HTTP client from Backend.HTTP.
	Endpoint string
}

// Backend starts and stops environments.
type Backend interface {
	// Create starts the environment. It does not wait until envd is ready.
	// token is the per-environment secret that envd must require.
	Create(ctx context.Context, id string, spec api.EnvironmentSpec, token string) error
	Status(ctx context.Context, id string) (Status, error)
	// Destroy removes the environment. It succeeds if it is already gone.
	Destroy(ctx context.Context, id string) error
	// HTTP returns the client that reaches Status endpoints.
	HTTP() *http.Client
}

// Binaries holds envd builds, one file per platform: envd-linux-amd64,
// envd-linux-arm64.
type Binaries struct {
	Dir string
	// Self, if set, is used for the control plane's own platform when Dir
	// has no build for it. The execution command also runs as envd when
	// its file name is "envd", so it can serve itself.
	Self string
	mu   sync.Mutex
	sums map[string]string
}

// Platforms that environments can use.
var Platforms = []string{"linux-amd64", "linux-arm64"}

// Path returns the file of platform, such as "linux-amd64".
func (b *Binaries) Path(platform string) (string, error) {
	ok := false
	for _, p := range Platforms {
		ok = ok || p == platform
	}
	if !ok {
		return "", fmt.Errorf("unknown envd platform %q", platform)
	}
	p := filepath.Join(b.Dir, "envd-"+platform)
	_, err := os.Stat(p)
	if err != nil && b.Self != "" && platform == runtime.GOOS+"-"+runtime.GOARCH {
		return b.Self, nil
	}
	if err != nil {
		return "", fmt.Errorf("envd for %s: %w", platform, err)
	}
	return p, nil
}

// Sum returns the hex SHA-256 of the build for platform.
func (b *Binaries) Sum(platform string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.sums[platform]; ok {
		return s, nil
	}
	p, err := b.Path(platform)
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if b.sums == nil {
		b.sums = map[string]string{}
	}
	b.sums[platform] = hex.EncodeToString(h.Sum(nil))
	return b.sums[platform], nil
}

// ServeHTTP serves GET /envd/{platform}. The build is not secret, so the
// handler needs no token. Environments verify the SHA-256 that the control
// plane gives them.
func (b *Binaries) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := b.Path(strings.TrimPrefix(r.URL.Path, "/envd/"))
	if err != nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

// Arch maps "uname -m" output to a Go architecture name.
func Arch(machine string) string {
	switch strings.TrimSpace(machine) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return strings.TrimSpace(machine)
}
