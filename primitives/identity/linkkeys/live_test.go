package linkkeys

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestLiveRP calls a real RP server. It needs MAGI_TEST_LINKKEYS=1 and
// MAGI_LINKKEYS_RUNTIME. It does not log in; see server/live_test.go.
func TestLiveRP(t *testing.T) {
	if os.Getenv("MAGI_TEST_LINKKEYS") != "1" {
		t.Skip("MAGI_TEST_LINKKEYS is not 1")
	}
	rt, err := LoadRuntime(os.Getenv("MAGI_LINKKEYS_RUNTIME"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{RP: NewRpClient(rt.Transport()), RPDomain: rt.RPDomain}
	ctx := context.Background()
	url, p, err := c.Begin(ctx, Identity{Username: "myuser", Domain: rt.RPDomain}, "https://magi."+rt.RPDomain+"/login/callback")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "/auth/authorize?") || !strings.Contains(url, "signed_request=") || p.State == "" {
		t.Fatalf("redirect = %s", url)
	}
	keys, err := c.DomainKeys(ctx, rt.RPDomain)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("browser base %s; %d pinned domain keys", p.APIBase, len(keys))
}
