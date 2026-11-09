package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/backend"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
)

// The owner opens a terminal in an environment; another person cannot.
func TestPTYRoute(t *testing.T) {
	h := newHarness(t, map[string]backend.Backend{"local": localBackend(t)}, "local")
	ctx := context.Background()
	env, err := h.client(alice).Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{}, WaitReadyMs: u64(10000)})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/pty?cols=90&rows=20&environment=" + env.Id
	if c, err := envd.DialUpgrade(ctx, h.url, path, h.keys.Token(t, bob)); err == nil || !strings.Contains(err.Error(), "403") {
		if c != nil {
			c.Close()
		}
		t.Fatalf("another person: %v", err)
	}
	if _, err := envd.DialUpgrade(ctx, h.url, "/v1/pty?environment=env_none", h.keys.Token(t, alice)); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("unknown environment: %v", err)
	}
	c, err := envd.DialUpgrade(ctx, h.url, path, h.keys.Token(t, alice))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	envd.WriteFrame(c, envd.FrameData, []byte("echo size-$(stty size | tr ' ' x); exit 5\n"))
	var out strings.Builder
	c.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		typ, p, err := envd.ReadFrame(c)
		if err != nil {
			t.Fatalf("read: %v; output %q", err, out.String())
		}
		if typ == envd.FrameData {
			out.Write(p)
		}
		if typ == envd.FrameExit {
			if code, _ := envd.ParseExit(p); code != 5 || !strings.Contains(out.String(), "size-20x90") {
				t.Fatalf("exit %d; output %q", code, out.String())
			}
			return
		}
	}
}
