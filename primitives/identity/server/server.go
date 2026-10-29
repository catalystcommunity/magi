// Package server implements IdentityService and the browser login routes.
//
// Login flow and its bindings:
//
//  1. begin-login (anonymous) signs a LinkKeys request and stores a pending
//     login. It returns a secret login_id to the client, and a start URL on
//     the Magi public origin that carries a different one-time start token.
//  2. GET /login/start shows the identity and the client name. The person
//     continues with one click, so a link alone cannot sign a person in.
//     The web client posts the start form from its own page and skips this
//     page. POST /login/start (same origin only) sets a random browser-binding
//     cookie, stores its hash, and redirects to the IDP. It works once.
//  3. GET /login/callback takes the login by lk_state exactly once, requires
//     the binding cookie of the browser that continued at step 2, and
//     completes the LinkKeys checks.
//  4. poll-login (anonymous) with login_id returns the token once.
//
// The poller and the browser never share a secret: login_id never goes to
// the browser, and lk_state and the cookie never go to the poller. A
// callback from another browser fails the binding check (login CSRF), and a
// replayed callback finds no started login.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/identity/api"
	"github.com/catalystcommunity/magi/primitives/identity/linkkeys"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

// Scopes.
const ScopeAdmin = "identity:admin"

// Defaults.
const (
	DefaultTokenTTL   = 12 * time.Hour
	DefaultSessionTTL = 7 * 24 * time.Hour
	LoginTTL          = 10 * time.Minute
	MaxPollWait       = 30 * time.Second
	maxServiceTTL     = 365 * 24 * time.Hour
)

// Service implements api.IdentityService.
type Service struct {
	DB         *store.DB
	Issuer     *authn.Issuer
	LinkKeys   *linkkeys.Client
	PublicURL  string          // such as https://magi.example.local
	Domains    map[string]bool // admitted identity domains
	TokenTTL   time.Duration
	SessionTTL time.Duration
	Now        func() time.Time
	Log        *slog.Logger
	// PollInterval is the store check interval of a long poll.
	PollInterval time.Duration

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// Register adds the service to s and marks the login operations anonymous.
func Register(s *rpc.Server, svc *Service) {
	s.Register("IdentityService", api.IdentityServiceOps(svc))
	if s.Anonymous == nil {
		s.Anonymous = map[string]bool{}
	}
	s.Anonymous["IdentityService/begin-login"] = true
	s.Anonymous["IdentityService/poll-login"] = true
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// allow is a token bucket for anonymous begin-login: burst 10, 1 per second.
// Each begin-login costs one LinkKeys API-key call.
func (s *Service) allow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.last.IsZero() {
		s.tokens = 10
	} else {
		s.tokens = min(10, s.tokens+now.Sub(s.last).Seconds())
	}
	s.last = now
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

func (s *Service) BeginLogin(ctx context.Context, req api.BeginLoginRequest) (api.BeginLoginResponse, error) {
	id, err := linkkeys.ParseIdentity(req.Identity)
	if err != nil {
		return api.BeginLoginResponse{}, rpc.Invalid("%v", err)
	}
	if !s.Domains[id.Domain] {
		return api.BeginLoginResponse{}, rpc.Denied("identity domain %s is not admitted", id.Domain)
	}
	client := ""
	if req.Client != nil {
		client = strings.TrimSpace(*req.Client)
	}
	if len(client) > 80 {
		return api.BeginLoginResponse{}, rpc.Invalid("client is longer than 80 characters")
	}
	returnURL := ""
	if req.ReturnUrl != nil && *req.ReturnUrl != "" {
		u, err := url.Parse(*req.ReturnUrl)
		if err != nil || !(u.Scheme+"://"+u.Host == s.origin() || loopback(u) || privateScheme(u)) || u.User != nil || u.Fragment != "" || len(*req.ReturnUrl) > 512 {
			return api.BeginLoginResponse{}, rpc.Invalid("return_url must be a page on %s, a loopback address, or a reverse-DNS app scheme", s.origin())
		}
		returnURL = u.String()
	}
	if !s.allow() {
		return api.BeginLoginResponse{}, rpc.Errorf(429, "too many logins; try again soon")
	}
	idpURL, p, err := s.LinkKeys.Begin(ctx, id, s.PublicURL+"/login/callback")
	if err != nil {
		s.log().Error("begin login", "err", err)
		return api.BeginLoginResponse{}, rpc.Unavailable("LinkKeys is not available")
	}
	loginID, start := "lgn_"+token(), "lst_"+token()
	now := s.now()
	exp := now.Add(LoginTTL)
	err = s.DB.CreateLogin(ctx, store.Login{
		LoginHash: store.Hash(loginID), StartHash: store.Hash(start), StateHash: store.Hash(p.State),
		Identity: strings.ToLower(id.Username + "@" + id.Domain), Domain: id.Domain, Client: client, ReturnURL: returnURL,
		Nonce: p.Nonce, APIBase: p.APIBase, CallbackURL: p.CallbackURL, IDPURL: idpURL,
		Status: store.Pending, CreatedMs: now.UnixMilli(), ExpiresMs: exp.UnixMilli(),
	})
	if err != nil {
		return api.BeginLoginResponse{}, err
	}
	return api.BeginLoginResponse{
		LoginId: loginID, Url: s.PublicURL + "/login/start?t=" + start,
		ExpiresMs: uint64(exp.UnixMilli()),
	}, nil
}

// loopback reports whether u is a native-app loopback redirect (RFC 8252
// section 7.3): http on 127.0.0.1 or [::1] with any port. The redirect holds
// no secret; the program gets its session by polling with its login_id.
func loopback(u *url.URL) bool {
	h := u.Hostname()
	return u.Scheme == "http" && (h == "127.0.0.1" || h == "::1") && u.Port() != ""
}

var schemeRE = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z0-9-]+)+$`)

// privateScheme reports whether u uses a reverse-DNS private-use scheme of a
// native app (RFC 8252 section 7.1), for example "org.example.app:/done".
// Another app that claims the scheme gets only the code, which is useless
// without the login_id.
func privateScheme(u *url.URL) bool {
	return schemeRE.MatchString(u.Scheme) && u.Host == "" && u.Opaque == ""
}

func (s *Service) PollLogin(ctx context.Context, req api.PollLoginRequest) (api.PollLoginResponse, error) {
	if !strings.HasPrefix(req.LoginId, "lgn_") {
		return api.PollLoginResponse{}, rpc.NotFound("unknown login")
	}
	wait := 25 * time.Second
	if req.WaitMs != nil {
		wait = min(time.Duration(*req.WaitMs)*time.Millisecond, MaxPollWait)
	}
	interval := s.PollInterval
	if interval == 0 {
		interval = 250 * time.Millisecond
	}
	h := store.Hash(req.LoginId)
	deadline := time.Now().Add(wait)
	for {
		l, err := s.DB.LoginByID(ctx, h)
		if errors.Is(err, store.ErrNotFound) {
			return api.PollLoginResponse{}, rpc.NotFound("unknown login")
		}
		if err != nil {
			return api.PollLoginResponse{}, err
		}
		if resp, final, err := s.pollResult(ctx, l, req.Code); final || err != nil {
			return resp, err
		}
		if time.Now().After(deadline) {
			return api.PollLoginResponse{Status: "pending", ExpiresMs: ptr(uint64(l.ExpiresMs))}, nil
		}
		select {
		case <-ctx.Done():
			return api.PollLoginResponse{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

var codeRE = regexp.MustCompile(`^lcd_[A-Za-z0-9_-]{32}$`)

func (s *Service) pollResult(ctx context.Context, l store.Login, code *string) (api.PollLoginResponse, bool, error) {
	switch l.Status {
	case store.Failed:
		return api.PollLoginResponse{Status: "failed", Message: ptr(l.Message)}, true, nil
	case store.Delivered:
		return api.PollLoginResponse{Status: "complete", Subject: ptr(l.Subject), Message: ptr("the token was already returned")}, true, nil
	case store.Complete:
		if code == nil || *code == "" {
			return api.PollLoginResponse{Status: "needs_code", Subject: ptr(l.Subject), ExpiresMs: ptr(uint64(l.ExpiresMs)),
				Message: ptr("the browser sign in is complete; poll again with the code from the browser")}, true, nil
		}
		if !codeRE.MatchString(*code) || !equal(store.Hash(*code), l.CodeHash) {
			if err := s.DB.Reject(ctx, l.LoginHash, "wrong code"); err != nil {
				return api.PollLoginResponse{}, true, err
			}
			s.log().Warn("login code mismatch", "subject", l.Subject)
			return api.PollLoginResponse{Status: "failed", Message: ptr("wrong code; start the sign in again")}, true, nil
		}
		d, err := s.DB.Deliver(ctx, l.LoginHash, l.CodeHash)
		if errors.Is(err, store.ErrNotFound) { // another poll took it
			return api.PollLoginResponse{Status: "complete", Subject: ptr(l.Subject), Message: ptr("the token was already returned")}, true, nil
		}
		if err != nil {
			return api.PollLoginResponse{}, true, err
		}
		tok, exp, err := s.issueUser(ctx, d.Subject, s.tokenTTL(), s.now().UnixMilli()+1000)
		if err != nil {
			return api.PollLoginResponse{}, true, err
		}
		return api.PollLoginResponse{Status: "complete", Token: &tok, Subject: ptr(d.Subject), ExpiresMs: ptr(exp)}, true, nil
	}
	if s.now().UnixMilli() >= l.ExpiresMs {
		return api.PollLoginResponse{Status: "expired", Message: ptr("the login expired")}, true, nil
	}
	return api.PollLoginResponse{}, false, nil
}

func (s *Service) tokenTTL() time.Duration {
	if s.TokenTTL > 0 {
		return s.TokenTTL
	}
	return DefaultTokenTTL
}

func (s *Service) sessionTTL() time.Duration {
	if s.SessionTTL > 0 {
		return s.SessionTTL
	}
	return DefaultSessionTTL
}

// issueUser issues a user token that does not outlive the newest active
// session of subject that started before startedBeforeMs.
func (s *Service) issueUser(ctx context.Context, subject string, ttl time.Duration, startedBeforeMs int64) (string, uint64, error) {
	now := s.now()
	sess, err := s.DB.ActiveSession(ctx, subject, startedBeforeMs, now.UnixMilli())
	if errors.Is(err, store.ErrNotFound) {
		return "", 0, rpc.Denied("no active login session; log in again")
	}
	if err != nil {
		return "", 0, err
	}
	ttl = min(ttl, time.UnixMilli(sess.ExpiresMs).Sub(now))
	if ttl < time.Second {
		return "", 0, rpc.Denied("the login session ended; log in again")
	}
	tok, err := s.issuer(now).Issue(authn.Principal{Subject: subject, Name: sess.Name, Kind: authn.KindUser}, ttl)
	return tok, uint64(now.Add(ttl).Unix() * 1000), err
}

func (s *Service) issuer(now time.Time) *authn.Issuer {
	i := *s.Issuer
	i.Now = func() time.Time { return now }
	return &i
}

func (s *Service) WhoAmI(ctx context.Context, _ api.WhoAmIRequest) (api.WhoAmIResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.WhoAmIResponse{}, err
	}
	scopes := p.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return api.WhoAmIResponse{Subject: p.Subject, Name: optional(p.Name), Kind: p.Kind, Scopes: scopes, ExpiresMs: uint64(p.Expires * 1000)}, nil
}

func (s *Service) IssueServiceToken(ctx context.Context, req api.IssueServiceTokenRequest) (api.TokenResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.TokenResponse{}, err
	}
	if !p.Has(ScopeAdmin) {
		return api.TokenResponse{}, rpc.Denied("%s is required", ScopeAdmin)
	}
	name, ok := strings.CutPrefix(req.Subject, "service:")
	if !ok || name == "" || len(req.Subject) > 128 || strings.ContainsAny(name, " @/\t\n") {
		return api.TokenResponse{}, rpc.Invalid("subject must be service:<name>")
	}
	ttl := time.Duration(req.TtlSeconds) * time.Second
	if ttl <= 0 || ttl > maxServiceTTL {
		return api.TokenResponse{}, rpc.Invalid("ttl_seconds must be 1 to %d", int(maxServiceTTL.Seconds()))
	}
	now := s.now()
	tok, err := s.issuer(now).Issue(authn.Principal{Subject: req.Subject, Kind: authn.KindService, Scopes: req.Scopes}, ttl)
	if err != nil {
		return api.TokenResponse{}, err
	}
	s.log().Info("service token issued", "by", p.Subject, "subject", req.Subject, "scopes", req.Scopes, "ttl_s", req.TtlSeconds)
	return api.TokenResponse{Token: tok, Subject: req.Subject, ExpiresMs: uint64(now.Add(ttl).Unix() * 1000)}, nil
}

func (s *Service) Refresh(ctx context.Context, req api.RefreshRequest) (api.TokenResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.TokenResponse{}, err
	}
	if p.Kind != authn.KindUser {
		return api.TokenResponse{}, rpc.Denied("only user tokens can be refreshed")
	}
	ttl := s.tokenTTL()
	if req.TtlSeconds != nil && *req.TtlSeconds > 0 {
		ttl = min(ttl, time.Duration(*req.TtlSeconds)*time.Second)
	}
	tok, exp, err := s.issueUser(ctx, p.Subject, ttl, (p.IssuedAt+1)*1000)
	if err != nil {
		return api.TokenResponse{}, err
	}
	return api.TokenResponse{Token: tok, Subject: p.Subject, ExpiresMs: exp}, nil
}

func ptr[T any](v T) *T { return &v }

func token() string {
	b := make([]byte, 32)
	rand.Read(b)
	const a = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = a[c&63]
	}
	return string(out)
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
