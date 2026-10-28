package linkkeys_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/primitives/identity/internal/fakelk"
	lk "github.com/catalystcommunity/magi/primitives/identity/linkkeys"
)

func TestParseIdentity(t *testing.T) {
	good := map[string]lk.Identity{
		"alice@Example.Local":   {Username: "alice", Domain: "example.local"},
		" example.local ":       {Domain: "example.local"},
		"a.b@localhost:8443":    {Username: "a.b", Domain: "localhost:8443"},
		"Bob+x@sub.example.org": {Username: "Bob+x", Domain: "sub.example.org"},
	}
	for in, want := range good {
		got, err := lk.ParseIdentity(in)
		if err != nil || got != want {
			t.Errorf("ParseIdentity(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "a@b@c.d", "@example.local", "alice@", "alice@localhost", "https://x.y", "a b@x.y",
		"alice@x.y/path", "alice@-x.y", "alice@x.y:0", "alice@x.y:70000", ".a@x.y", "ä@x.y"} {
		if _, err := lk.ParseIdentity(in); err == nil {
			t.Errorf("ParseIdentity(%q) accepted", in)
		}
	}
}

type env struct {
	d *fakelk.Domain
	c *lk.Client
}

func setup(t *testing.T) env {
	d := fakelk.New(t, "example.local")
	rt := d.Runtime()
	return env{d, &lk.Client{RP: lk.NewRpClient(rt.Transport()), RPDomain: rt.RPDomain, DNS: d}}
}

func (e env) begin(t *testing.T) (string, lk.Pending) {
	t.Helper()
	u, p, err := e.c.Begin(context.Background(), lk.Identity{Username: "alice", Domain: "example.local"}, "https://magi.example.local/login/callback")
	if err != nil {
		t.Fatal(err)
	}
	return u, p
}

func token(t *testing.T, callback string) string {
	u, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("encrypted_token")
}

func code(err error) string {
	var le *lk.LoginError
	if errors.As(err, &le) {
		return le.Code
	}
	return ""
}

func TestBeginAndComplete(t *testing.T) {
	e := setup(t)
	idp, p := e.begin(t)
	u, _ := url.Parse(idp)
	q := u.Query()
	if u.Host != "idp.example.local" || u.Path != "/lk/auth/authorize" || q.Get("username") != "alice" ||
		q.Get("relying_party") != "example.local" || q.Get("nonce") != p.Nonce || !strings.Contains(q.Get("callback_url"), "lk_state="+p.State) {
		t.Fatalf("redirect = %s", idp)
	}
	if p.APIBase != "https://idp.example.local/lk" {
		t.Fatalf("api base = %s", p.APIBase)
	}
	cb, err := e.d.Authorize(idp, "u-1", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.c.Complete(context.Background(), p, token(t, cb))
	if err != nil {
		t.Fatal(err)
	}
	if v.UserID != "u-1" || v.Domain != "example.local" || v.Handle != "Alice" {
		t.Fatalf("verified = %+v", v)
	}
}

func TestCompleteChecks(t *testing.T) {
	cases := map[string]struct {
		setup func(*env, *lk.Pending)
		want  string
	}{
		"unverified": {func(e *env, _ *lk.Pending) { e.d.Unverified = true }, "assertion-unverified"},
		"audience": {func(e *env, _ *lk.Pending) {
			e.d.Tamper = func(a *lk.IdentityAssertion) { a.Audience = "evil.example" }
		}, "audience-mismatch"},
		"nonce":     {func(_ *env, p *lk.Pending) { p.Nonce = "other" }, "nonce-mismatch"},
		"domain":    {func(e *env, _ *lk.Pending) { e.d.Tamper = func(a *lk.IdentityAssertion) { a.Domain = "other.example" } }, "domain-mismatch"},
		"expired":   {func(e *env, _ *lk.Pending) { e.c.Now = func() time.Time { return time.Now().Add(time.Hour) } }, "invalid-assertion-time"},
		"future":    {func(e *env, _ *lk.Pending) { e.c.Now = func() time.Time { return time.Now().Add(-time.Hour) } }, "invalid-assertion-time"},
		"no handle": {func(e *env, _ *lk.Pending) { e.d.Tamper = func(a *lk.IdentityAssertion) { a.AuthorizedClaims = nil } }, "unauthorized-claim"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			idp, p := e.begin(t)
			cb, err := e.d.Authorize(idp, "u-1", "alice")
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(&e, &p)
			_, err = e.c.Complete(context.Background(), p, token(t, cb))
			if code(err) != tc.want {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMissingHandle(t *testing.T) {
	e := setup(t)
	idp, p := e.begin(t)
	cb, _ := e.d.Authorize(idp, "u-1", "")
	v, err := e.c.Complete(context.Background(), p, token(t, cb))
	if err != nil || v.UserID != "u-1" || v.Handle != "" {
		t.Fatalf("v = %+v, err = %v", v, err)
	}
}

func TestCallbackURL(t *testing.T) {
	e := setup(t)
	for _, cb := range []string{"http://magi.example.local/cb", "https://evil.example/cb", "https://magi.example.local/cb?lk_state=x",
		"https://u:p@magi.example.local/cb", "https://notexample.local/cb", "https://magi.example.local/cb#f"} {
		if _, _, err := e.c.Begin(context.Background(), lk.Identity{Domain: "example.local"}, cb); code(err) != "invalid-callback" {
			t.Errorf("Begin(%s) err = %v", cb, err)
		}
	}
}

func TestTransportPin(t *testing.T) {
	e := setup(t)
	rt := e.d.Runtime()
	ctx := context.Background()
	bad := rt.Transport()
	bad.Fingerprints = []string{strings.Repeat("0", 64)}
	if _, err := lk.NewRpClient(bad).SignRequest(ctx, lk.RpSignRequest{CallbackUrl: "https://x.example.local", Nonce: "n"}); err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("wrong pin err = %v", err)
	}
	key := rt.Transport()
	key.APIKey = "wrong"
	var se *lk.ServerError
	if _, err := lk.NewRpClient(key).SignRequest(ctx, lk.RpSignRequest{CallbackUrl: "https://x.example.local", Nonce: "n"}); !errors.As(err, &se) || se.Status != 16 {
		t.Fatalf("wrong key err = %v", err)
	}
}

type pinless struct{ d *fakelk.Domain }

func (p pinless) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if strings.HasPrefix(name, "_linkkeys.") {
		return []string{"v=lk1 fp=" + strings.Repeat("ab", 32)}, nil
	}
	return p.d.LookupTXT(ctx, name)
}

type forgedRP struct {
	lk.Rp
	claim lk.Claim
}

func (f forgedRP) UserinfoFetch(ctx context.Context, r lk.RpUserInfoRequest) (lk.UserInfo, error) {
	info, err := f.Rp.UserinfoFetch(ctx, r)
	info.Claims = []lk.Claim{f.claim}
	return info, err
}
