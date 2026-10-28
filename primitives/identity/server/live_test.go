package server_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/identity/api"
	"github.com/catalystcommunity/magi/primitives/identity/internal/testenv"
	lk "github.com/catalystcommunity/magi/primitives/identity/linkkeys"
	"github.com/catalystcommunity/magi/primitives/identity/server"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

// TestLiveLogin logs in through a real LinkKeys RP. It needs:
//
//	MAGI_TEST_LINKKEYS=1, MAGI_LINKKEYS_RUNTIME, MAGI_TEST_DATABASE_URL
//	MAGI_LIVE_IDENTITY   (default myuser@<rp domain>)
//	MAGI_LIVE_PUBLIC_URL (default https://magi.<rp domain>)
//	MAGI_LIVE_LISTEN     (default 127.0.0.1:18471)
//	MAGI_LIVE_START_FILE (the test writes the start URL here)
//
// LinkKeys accepts only an https callback on the RP domain. A browser must
// open the start URL and send requests for the public origin to the listen
// address (for example, a Playwright route). See README.md.
func TestLiveLogin(t *testing.T) {
	if os.Getenv("MAGI_TEST_LINKKEYS") != "1" {
		t.Skip("MAGI_TEST_LINKKEYS is not 1")
	}
	rt, err := lk.LoadRuntime(os.Getenv("MAGI_LINKKEYS_RUNTIME"))
	if err != nil {
		t.Fatal(err)
	}
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	identity := get("MAGI_LIVE_IDENTITY", "myuser@"+rt.RPDomain)
	public := get("MAGI_LIVE_PUBLIC_URL", "https://magi."+rt.RPDomain)
	listen := get("MAGI_LIVE_LISTEN", "127.0.0.1:18471")
	ctx := context.Background()
	db, err := store.Open(ctx, testenv.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keys := testenv.NewKeys(t)
	id, _ := lk.ParseIdentity(identity)
	svc := &server.Service{DB: db, Issuer: keys.Issuer, PublicURL: public, Domains: map[string]bool{id.Domain: true},
		LinkKeys: &lk.Client{RP: lk.NewRpClient(rt.Transport()), RPDomain: rt.RPDomain}}
	s := &rpc.Server{Verifier: keys.Verifier(t)}
	server.Register(s, svc)
	mux := rpc.Mux(s, nil)
	svc.Routes(mux)
	l, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, mux)
	defer l.Close()

	c := api.NewIdentityClient(rpc.NewClient("http://"+listen, nil))
	cl := "live-test"
	r, err := c.BeginLogin(ctx, api.BeginLoginRequest{Identity: identity, Client: &cl})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("open %s", r.Url)
	if f := os.Getenv("MAGI_LIVE_START_FILE"); f != "" {
		if err := os.WriteFile(f, []byte(r.Url+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(9 * time.Minute)
	var p api.PollLoginResponse
	for time.Now().Before(deadline) {
		w := uint64(30000)
		if p, err = c.PollLogin(ctx, api.PollLoginRequest{LoginId: r.LoginId, WaitMs: &w}); err != nil {
			t.Fatal(err)
		}
		if p.Status != "pending" {
			break
		}
	}
	if p.Status != "complete" || p.Token == nil {
		t.Fatalf("login = %s %s", p.Status, str(p.Message))
	}
	who, err := api.NewIdentityClient(rpc.NewClient("http://"+listen, rpc.StaticToken(*p.Token))).WhoAmI(ctx, api.WhoAmIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("signed in as %s (%s), token expires %s", who.Subject, who.Kind, time.UnixMilli(int64(who.ExpiresMs)).UTC().Format(time.RFC3339))
	if again, _ := c.PollLogin(ctx, api.PollLoginRequest{LoginId: r.LoginId}); again.Token != nil {
		t.Fatal("token returned twice")
	}
}
