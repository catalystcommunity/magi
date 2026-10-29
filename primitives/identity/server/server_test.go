package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/identity/api"
	"github.com/catalystcommunity/magi/primitives/identity/internal/fakelk"
	"github.com/catalystcommunity/magi/primitives/identity/internal/testenv"
	lk "github.com/catalystcommunity/magi/primitives/identity/linkkeys"
	"github.com/catalystcommunity/magi/primitives/identity/server"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

const public = "https://magi.example.local"

type env struct {
	t      *testing.T
	hs     *httptest.Server
	svc    *server.Service
	keys   testenv.Keys
	d      *fakelk.Domain
	offset *atomic.Int64 // service clock offset in ms
}

func setup(t *testing.T) env {
	ctx := context.Background()
	db, err := store.Open(ctx, testenv.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	keys := testenv.NewKeys(t)
	d := fakelk.New(t, "example.local")
	rt := d.Runtime()
	var off atomic.Int64
	svc := &server.Service{
		DB: db, Issuer: keys.Issuer, PublicURL: public, Domains: map[string]bool{"example.local": true},
		LinkKeys:     &lk.Client{RP: lk.NewRpClient(rt.Transport()), RPDomain: rt.RPDomain, DNS: d},
		Now:          func() time.Time { return time.Now().Add(time.Duration(off.Load()) * time.Millisecond) },
		PollInterval: 20 * time.Millisecond,
	}
	s := &rpc.Server{Verifier: keys.Verifier(t)}
	server.Register(s, svc)
	mux := rpc.Mux(s, nil)
	svc.Routes(mux)
	hs := httptest.NewTLSServer(mux)
	t.Cleanup(hs.Close)
	return env{t: t, hs: hs, svc: svc, keys: keys, d: d, offset: &off}
}

func (e env) client(token string) *api.IdentityClient {
	c := rpc.NewClient(e.hs.URL, rpc.StaticToken(token))
	c.HTTP = e.hs.Client()
	return api.NewIdentityClient(c)
}

// browser is a cookie-keeping HTTP client that does not follow redirects.
type browser struct {
	e  env
	hc *http.Client
}

func (e env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Transport: e.hs.Client().Transport, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &browser{e, hc}
}

// do sends a request for a public Magi URL to the test server.
func (b *browser) do(method, publicURL string, form url.Values, origin string) (*http.Response, string) {
	b.e.t.Helper()
	u := strings.Replace(publicURL, public, b.e.hs.URL, 1)
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, u, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	return resp, string(text)
}

// start opens the start page and continues to the IDP. It returns the IDP URL.
func (b *browser) start(startURL string) string {
	b.e.t.Helper()
	resp, page := b.do("GET", startURL, nil, "")
	if resp.StatusCode != 200 || !strings.Contains(page, "Continue") {
		b.e.t.Fatalf("start page %d: %s", resp.StatusCode, page)
	}
	t := strings.TrimPrefix(startURL, public+"/login/start?t=")
	resp, page = b.do("POST", public+"/login/start", url.Values{"t": {t}}, public)
	if resp.StatusCode != http.StatusSeeOther {
		b.e.t.Fatalf("start post %d: %s", resp.StatusCode, page)
	}
	return resp.Header.Get("Location")
}

func (e env) begin(identity string) api.BeginLoginResponse {
	e.t.Helper()
	cl := "test-cli"
	r, err := e.client("").BeginLogin(context.Background(), api.BeginLoginRequest{Identity: identity, Client: &cl})
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e env) poll(id string, waitMs uint64) api.PollLoginResponse {
	return e.pollCode(id, waitMs, "")
}

func (e env) pollCode(id string, waitMs uint64, code string) api.PollLoginResponse {
	e.t.Helper()
	req := api.PollLoginRequest{LoginId: id, WaitMs: &waitMs}
	if code != "" {
		req.Code = &code
	}
	r, err := e.client("").PollLogin(context.Background(), req)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

var pageCodeRE = regexp.MustCompile(`lcd_[A-Za-z0-9_-]{32}`)

// redirectCode checks that location is back plus a code, and returns the code.
func redirectCode(t *testing.T, location, back string) string {
	t.Helper()
	u, err := url.Parse(location)
	code := ""
	if err == nil {
		code = u.Query().Get("code")
		q := u.Query()
		q.Del("code")
		u.RawQuery = q.Encode()
	}
	b, _ := url.Parse(back)
	b.RawQuery = b.Query().Encode()
	if err != nil || u.String() != b.String() || !pageCodeRE.MatchString(code) {
		t.Fatalf("redirect %q, want %q with a code", location, back)
	}
	return code
}

// login runs a complete browser login and returns the callback URL used.
func (e env) login(b *browser, r api.BeginLoginResponse, handle string) (string, *http.Response, string) {
	e.t.Helper()
	cb, err := e.d.Authorize(b.start(r.Url), "uid-"+handle, handle)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, page := b.do("GET", cb, nil, "")
	return cb, resp, page
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestLoginSuccessAndTokenOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	r := e.begin("Alice@Example.Local")
	if !strings.HasPrefix(r.LoginId, "lgn_") || !strings.HasPrefix(r.Url, public+"/login/start?t=lst_") ||
		time.Until(time.UnixMilli(int64(r.ExpiresMs))) > 10*time.Minute || strings.Contains(r.Url, r.LoginId) {
		t.Fatalf("begin = %+v", r)
	}
	if p := e.poll(r.LoginId, 0); p.Status != "pending" || p.Token != nil {
		t.Fatalf("poll before login = %+v", p)
	}
	b := e.browser()
	resp, page := b.do("GET", r.Url, nil, "")
	if !strings.Contains(page, "test-cli") || !strings.Contains(page, "alice@example.local") {
		t.Fatalf("start page: %s", page)
	}
	_ = resp

	// A long poll returns when the callback completes.
	got := make(chan api.PollLoginResponse, 1)
	go func() {
		w := uint64(10000)
		p, _ := e.client("").PollLogin(ctx, api.PollLoginRequest{LoginId: r.LoginId, WaitMs: &w})
		got <- p
	}()
	cb, resp, page := e.login(b, r, "Alice")
	if resp.StatusCode != 200 || !strings.Contains(page, "Signed in as Alice") {
		t.Fatalf("callback %d: %s", resp.StatusCode, page)
	}
	if p := <-got; p.Status != "needs_code" || p.Token != nil {
		t.Fatalf("poll without code = %+v", p)
	}
	code := pageCodeRE.FindString(page)
	if code == "" {
		t.Fatalf("no code on the page: %s", page)
	}
	p := e.pollCode(r.LoginId, 0, code)
	if p.Status != "complete" || p.Token == nil || str(p.Subject) != "user:uid-alice@example.local" {
		t.Fatalf("poll = %+v", p)
	}
	if again := e.pollCode(r.LoginId, 0, code); again.Status != "complete" || again.Token != nil {
		t.Fatalf("second poll = %+v", again)
	}

	who, err := e.client(*p.Token).WhoAmI(ctx, api.WhoAmIRequest{})
	if err != nil || who.Subject != "user:uid-alice@example.local" || str(who.Name) != "Alice" || who.Kind != "user" {
		t.Fatalf("who-am-i = %+v, %v", who, err)
	}
	exp := time.UnixMilli(int64(who.ExpiresMs))
	if d := time.Until(exp); d < 11*time.Hour || d > 12*time.Hour+time.Minute {
		t.Fatalf("token lifetime %v", d)
	}

	t.Run("replayed callback", func(t *testing.T) {
		resp, page := b.do("GET", cb, nil, "")
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("replay %d: %s", resp.StatusCode, page)
		}
	})
	t.Run("refresh", func(t *testing.T) {
		ttl := uint64(3600)
		nt, err := e.client(*p.Token).Refresh(ctx, api.RefreshRequest{TtlSeconds: &ttl})
		if err != nil || nt.Subject != "user:uid-alice@example.local" {
			t.Fatalf("refresh = %+v, %v", nt, err)
		}
		// Seven days later the session has ended.
		e.offset.Store((7*24*time.Hour + time.Minute).Milliseconds())
		defer e.offset.Store(0)
		if _, err := e.client(nt.Token).Refresh(ctx, api.RefreshRequest{}); rpc.CodeOf(err) != 403 {
			t.Fatalf("refresh after session end err = %v", err)
		}
	})
	t.Run("start link works once", func(t *testing.T) {
		resp, _ := b.do("GET", r.Url, nil, "")
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("reused start link %d", resp.StatusCode)
		}
	})
}

func TestCallbackFailures(t *testing.T) {
	e := setup(t)

	t.Run("wrong state", func(t *testing.T) {
		resp, _ := e.browser().do("GET", public+"/login/callback?lk_state=nope&encrypted_token=x", nil, "")
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("other browser", func(t *testing.T) {
		r := e.begin("bob@example.local")
		cb, err := e.d.Authorize(e.browser().start(r.Url), "uid-bob", "bob")
		if err != nil {
			t.Fatal(err)
		}
		resp, _ := e.browser().do("GET", cb, nil, "")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if p := e.poll(r.LoginId, 0); p.Status != "failed" || p.Token != nil {
			t.Fatalf("poll = %+v", p)
		}
	})
	t.Run("expired", func(t *testing.T) {
		r := e.begin("carol@example.local")
		b := e.browser()
		idp := b.start(r.Url)
		e.offset.Store((11 * time.Minute).Milliseconds())
		defer e.offset.Store(0)
		if p := e.poll(r.LoginId, 0); p.Status != "expired" {
			t.Fatalf("poll = %+v", p)
		}
		cb, _ := e.d.Authorize(idp, "uid-carol", "carol")
		if resp, _ := b.do("GET", cb, nil, ""); resp.StatusCode != http.StatusGone {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("expired start link", func(t *testing.T) {
		r := e.begin("dan@example.local")
		e.offset.Store((11 * time.Minute).Milliseconds())
		defer e.offset.Store(0)
		if resp, _ := e.browser().do("GET", r.Url, nil, ""); resp.StatusCode != http.StatusGone {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("cross-site start post", func(t *testing.T) {
		r := e.begin("erin@example.local")
		tok := strings.TrimPrefix(r.Url, public+"/login/start?t=")
		b := e.browser()
		for _, origin := range []string{"", "https://evil.example"} {
			if resp, _ := b.do("POST", public+"/login/start", url.Values{"t": {tok}}, origin); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("origin %q status %d", origin, resp.StatusCode)
			}
		}
	})
	t.Run("invalid handle", func(t *testing.T) {
		r := e.begin("frank@example.local")
		_, resp, _ := e.login(e.browser(), r, "frank smith")
		if resp.StatusCode != http.StatusForbidden || e.poll(r.LoginId, 0).Status != "failed" {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("rejected assertion", func(t *testing.T) {
		r := e.begin("gina@example.local")
		e.d.Unverified = true
		defer func() { e.d.Unverified = false }()
		_, resp, page := e.login(e.browser(), r, "gina")
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "assertion-unverified") {
			t.Fatalf("status %d: %s", resp.StatusCode, page)
		}
	})
}

func TestAdmission(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, err := e.client("").BeginLogin(ctx, api.BeginLoginRequest{Identity: "eve@evil.example"})
	if rpc.CodeOf(err) != 403 {
		t.Fatalf("disallowed domain err = %v", err)
	}
	if _, err := e.client("").BeginLogin(ctx, api.BeginLoginRequest{Identity: "not an identity"}); rpc.CodeOf(err) != 400 {
		t.Fatalf("bad identity err = %v", err)
	}
	// The domain policy is checked again on the verified identity.
	r := e.begin("alice@example.local")
	e.svc.Domains = map[string]bool{}
	_, resp, page := e.login(e.browser(), r, "alice")
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "not admitted") {
		t.Fatalf("callback %d: %s", resp.StatusCode, page)
	}
	if p := e.poll(r.LoginId, 0); p.Status != "failed" {
		t.Fatalf("poll = %+v", p)
	}
	if _, err := e.client("").PollLogin(ctx, api.PollLoginRequest{LoginId: "lgn_unknown"}); rpc.CodeOf(err) != 404 {
		t.Fatalf("unknown login err = %v", err)
	}
}

func TestTokens(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := e.client(e.keys.Token(t, "user:alice@example.local"))
	admin := e.client(e.keys.Token(t, "service:ops", server.ScopeAdmin))
	req := api.IssueServiceTokenRequest{Subject: "service:worker", Scopes: []string{"workflow:act-as"}, TtlSeconds: 3600}
	if _, err := user.IssueServiceToken(ctx, req); rpc.CodeOf(err) != 403 {
		t.Fatalf("non-admin err = %v", err)
	}
	if _, err := admin.IssueServiceToken(ctx, api.IssueServiceTokenRequest{Subject: "user:x@example.local", TtlSeconds: 60}); rpc.CodeOf(err) != 400 {
		t.Fatalf("user subject err = %v", err)
	}
	tok, err := admin.IssueServiceToken(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	who, err := e.client(tok.Token).WhoAmI(ctx, api.WhoAmIRequest{})
	if err != nil || who.Kind != "service" || who.Subject != "service:worker" || len(who.Scopes) != 1 {
		t.Fatalf("who-am-i = %+v, %v", who, err)
	}
	if _, err := e.client(tok.Token).Refresh(ctx, api.RefreshRequest{}); rpc.CodeOf(err) != 403 {
		t.Fatalf("service refresh err = %v", err)
	}
	// A user token without a login session cannot be refreshed.
	if _, err := user.Refresh(ctx, api.RefreshRequest{}); rpc.CodeOf(err) != 403 {
		t.Fatalf("sessionless refresh err = %v", err)
	}
	if _, err := e.client("").WhoAmI(ctx, api.WhoAmIRequest{}); rpc.CodeOf(err) != 401 {
		t.Fatalf("anonymous who-am-i err = %v", err)
	}
}

func TestReturnURLSameTab(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for _, bad := range []string{"https://evil.example/", "http://" + strings.TrimPrefix(public, "https://") + "/", public + "/#x",
		"http://localhost:8123/", "https://127.0.0.1:8123/", "http://127.0.0.1/", "http://127.0.0.2:8123/", "http://user@127.0.0.1:8123/",
		"javascript:alert(1)", "data:text/html,x", "magi:/done", "org.example.app://host/done"} {
		_, err := e.client("").BeginLogin(ctx, api.BeginLoginRequest{Identity: "alice@example.local", ReturnUrl: &bad})
		if rpc.CodeOf(err) != 400 {
			t.Errorf("return_url %s: err = %v", bad, err)
		}
	}
	back := public + "/?signed-in"
	r, err := e.client("").BeginLogin(ctx, api.BeginLoginRequest{Identity: "alice@example.local", ReturnUrl: &back})
	if err != nil {
		t.Fatal(err)
	}
	_, resp, page := e.login(e.browser(), r, "Alice")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback %d %q: %s", resp.StatusCode, resp.Header.Get("Location"), page)
	}
	code := redirectCode(t, resp.Header.Get("Location"), back)
	if p := e.pollCode(r.LoginId, 0, code); p.Status != "complete" || p.Token == nil {
		t.Fatalf("poll = %+v", p)
	}
}

// The person who signs in gets the code, not the program that started the
// login. Without the code, a sign-in link sent to another person is useless.
func TestWrongCodeFailsLogin(t *testing.T) {
	e := setup(t)
	r := e.begin("alice@example.local")
	_, _, page := e.login(e.browser(), r, "Alice")
	code := pageCodeRE.FindString(page)
	if p := e.poll(r.LoginId, 0); p.Status != "needs_code" || p.Token != nil {
		t.Fatalf("poll without code = %+v", p)
	}
	if p := e.pollCode(r.LoginId, 0, "lcd_"+strings.Repeat("A", 32)); p.Status != "failed" || p.Token != nil {
		t.Fatalf("wrong code = %+v", p)
	}
	if p := e.pollCode(r.LoginId, 0, code); p.Status != "failed" || p.Token != nil {
		t.Fatalf("right code after a wrong one = %+v", p)
	}
}

func TestReturnURLLoopback(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for _, back := range []string{"http://127.0.0.1:49152/done", "http://[::1]:49152/done", "org.example.app:/done"} {
		r, err := e.client("").BeginLogin(ctx, api.BeginLoginRequest{Identity: "alice@example.local", ReturnUrl: &back})
		if err != nil {
			t.Fatalf("%s: %v", back, err)
		}
		_, resp, page := e.login(e.browser(), r, "Alice")
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("callback %d %q: %s", resp.StatusCode, resp.Header.Get("Location"), page)
		}
		code := redirectCode(t, resp.Header.Get("Location"), back)
		if p := e.pollCode(r.LoginId, 0, code); p.Status != "complete" || p.Token == nil {
			t.Fatalf("poll = %+v", p)
		}
	}
}
