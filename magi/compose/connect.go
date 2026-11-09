package compose

// Connections: Magi acts for a person in other applications.
//
//   - calendar: catalystcal. The person approves a LinkKeys act-as grant
//     (Magi is the grantee, catalystcal the audience). The identity
//     primitive keeps the grant and signs each presentation; this file
//     exchanges a presentation for a short catalystcal bearer.
//   - longhouse: Longhouse has no act-as grants. The person approves a
//     Longhouse CLI session for "Magi". The identity primitive keeps the
//     session (encrypted). Longhouse revokes a session when a used refresh
//     token comes back, so only one replica refreshes, under a lease.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	identityapi "github.com/catalystcommunity/magi/primitives/identity/api"

	cal "github.com/catalystcommunity/catalystcal/clients/go"
	lh "github.com/catalystcommunity/longhouse/clients/go"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// errNotConnected: the person did not connect the system. The text is for
// the model: it tells the person how to connect.
type errNotConnected struct{ system string }

func (e errNotConnected) Error() string {
	return e.system + " is not connected for this person. Call connect_account with system " + e.system + " and give the person the link"
}

// Calendar is the catalystcal adapter.
type Calendar struct {
	URL string
	// HTTP sends the calls (through the egress proxy when it is set).
	HTTP *http.Client
	s    *System

	mu       sync.Mutex
	audience *cal.ApplicationRef
	sessions map[string]*calTokens
}

// Scopes that Magi asks for at catalystcal.
var calendarScopes = []string{"calendars.read", "events.write", "events.respond"}

func (c *Calendar) public() *cal.HTTPTransport { return c.http(nil) }

func (c *Calendar) http(tokens cal.TokenSource) *cal.HTTPTransport {
	t := cal.NewHTTPTransport(c.URL, tokens)
	t.HTTP = c.HTTP
	return t
}

func (c *Calendar) aud(ctx context.Context) (cal.ApplicationRef, error) {
	c.mu.Lock()
	a := c.audience
	c.mu.Unlock()
	if a != nil {
		return *a, nil
	}
	r, err := cal.NewAuthClient(c.public()).GetApplicationIdentity(ctx, cal.EmptyRequest{})
	if err != nil {
		return cal.ApplicationRef{}, fmt.Errorf("catalystcal identity: %w", err)
	}
	c.mu.Lock()
	c.audience = &r.Audience
	c.mu.Unlock()
	return r.Audience, nil
}

func idApp(a cal.ApplicationRef) identityapi.AppRef {
	return identityapi.AppRef{Domain: a.SubjectDomain, UserId: a.SubjectUserId, AppId: a.ApplicationId}
}

// connect starts an act-as grant for owner. It returns the consent URL.
func (c *Calendar) connect(ctx context.Context, owner string) (string, uint64, error) {
	aud, err := c.aud(ctx)
	if err != nil {
		return "", 0, err
	}
	me, err := c.s.Identity.Application(c.s.AsService(ctx), identityapi.ApplicationRequest{})
	if err != nil {
		return "", 0, fmt.Errorf("magi application: %w", err)
	}
	set, err := cal.NewAuthClient(c.public()).GetActAsScopeSet(ctx, cal.ScopeSetRequest{GranteeDomain: me.Domain, GranteeUserId: me.UserId, GranteeApplicationId: me.AppId, Scopes: calendarScopes})
	if err != nil {
		return "", 0, fmt.Errorf("catalystcal scope set: %w", err)
	}
	r, err := c.s.Identity.BeginGrant(c.s.AsService(ctx), identityapi.BeginGrantRequest{Audience: idApp(aud), SignedScopeSet: set.SignedScopeSet, Owner: &owner})
	if err != nil {
		return "", 0, err
	}
	return r.Url, r.ExpiresMs, nil
}

// transport is a catalystcal transport that acts as owner.
func (c *Calendar) transport(ctx context.Context, owner string) (cal.Transport, error) {
	aud, err := c.aud(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions == nil {
		c.sessions = map[string]*calTokens{}
	}
	sess := c.sessions[owner]
	if sess == nil {
		sess = &calTokens{c: c, owner: owner, aud: aud}
		c.sessions[owner] = sess
	}
	return c.http(sess), nil
}

// calTokens is the catalystcal bearer of one person. A new bearer needs a
// new presentation: identity checks the grant at the home domain first.
type calTokens struct {
	c     *Calendar
	owner string
	aud   cal.ApplicationRef
	mu    sync.Mutex
	token string
	exp   time.Time
}

func (t *calTokens) Invalidate(token string) {
	t.mu.Lock()
	if t.token == token {
		t.token = ""
	}
	t.mu.Unlock()
}

func (t *calTokens) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.exp) > time.Minute {
		return t.token, nil
	}
	s := t.c.s
	r, err := s.Identity.PresentGrant(s.AsService(ctx), identityapi.PresentGrantRequest{Owner: t.owner, Audience: idApp(t.aud), RequestDigest: cal.Digest(cal.ActAsDigestInput)})
	var re *rpc.Error
	if errors.As(err, &re) && (re.Code == 404 || re.Code == 403) {
		return "", errNotConnected{"calendar"}
	}
	if err != nil {
		return "", err
	}
	lr, err := cal.NewAuthClient(t.c.public()).ExchangeActAsCredential(ctx, cal.ExchangeActAsCredentialRequest{Credential: r.Credential})
	if err != nil {
		return "", fmt.Errorf("catalystcal exchange: %w", err)
	}
	t.token, t.exp = lr.Token, time.UnixMilli(tsMs(string(lr.ExpiresAt)))
	return t.token, nil
}

// forget drops the cached bearer of owner.
func (c *Calendar) forget(owner string) {
	c.mu.Lock()
	delete(c.sessions, owner)
	c.mu.Unlock()
}

// Longhouse is the Longhouse adapter.
type Longhouse struct {
	URL  string
	HTTP *http.Client
	s    *System

	mu     sync.Mutex
	tokens map[string]*lhTokens
}

// The stored value of the "longhouse" credential.
type lhSession struct {
	Refresh  string `json:"refresh"`
	Session  string `json:"session,omitempty"`
	Token    string `json:"token,omitempty"`
	Expires  int64  `json:"expires_ms,omitempty"`
	Lease    int64  `json:"lease_ms,omitempty"`
	RefreshE int64  `json:"refresh_expires_ms,omitempty"`
}

const lhSystem = "longhouse"

func (l *Longhouse) public() *cal.HTTPTransport { return l.http(nil) }

func (l *Longhouse) http(tokens cal.TokenSource) *cal.HTTPTransport {
	t := cal.NewHTTPTransport(l.URL, tokens)
	t.HTTP = l.HTTP
	return t
}

// connect starts a CLI login. A goroutine waits for the approval and
// stores the session.
func (l *Longhouse) connect(ctx context.Context, owner string) (magiapi.ConnectResponse, error) {
	b, err := lh.NewAuthClient(l.public()).BeginCliLogin(ctx, lh.BeginCliLoginRequest{ClientName: "Magi"})
	if err != nil {
		return magiapi.ConnectResponse{}, fmt.Errorf("longhouse: %w", err)
	}
	exp := tsMs(string(b.ExpiresAt))
	go l.await(context.WithoutCancel(ctx), owner, b.DeviceCode, time.Duration(max(2, b.IntervalSeconds))*time.Second, time.UnixMilli(exp))
	return magiapi.ConnectResponse{System: lhSystem, Url: b.VerificationUrl, UserCode: &b.UserCode, ExpiresMs: uint64(exp),
		Message: "Open the link, sign in to Longhouse, and approve \"Magi\" with the code " + b.UserCode + "."}, nil
}

func (l *Longhouse) await(ctx context.Context, owner, device string, every time.Duration, until time.Time) {
	auth := lh.NewAuthClient(l.public())
	for time.Now().Before(until) {
		time.Sleep(every)
		r, err := auth.ExchangeCliLogin(ctx, lh.ExchangeCliLoginRequest{DeviceCode: device})
		if err != nil {
			l.s.Log.Warn("longhouse connect", "owner", owner, "err", err)
			continue
		}
		switch r.Status {
		case "pending":
			continue
		case "complete":
			if r.Session == nil {
				return
			}
			// Anyone who has the code can approve it. The session must be
			// of the person's own account, or Magi would act as another
			// person in Longhouse.
			if err := l.sameAccount(ctx, owner, r.Session.Token); err != nil {
				l.s.Log.Warn("longhouse connect refused", "owner", owner, "err", err)
				lh.NewAuthClient(l.http(staticToken(r.Session.Token))).RevokeSession(ctx, lh.RevokeSessionRequest{SessionId: r.Session.SessionId})
				return
			}
			v := lhSession{Refresh: r.Session.RefreshToken, Session: string(r.Session.SessionId), Token: r.Session.Token,
				Expires: tsMs(string(r.Session.ExpiresAt)), RefreshE: tsMs(string(r.Session.RefreshExpiresAt))}
			b, _ := json.Marshal(v)
			_, err := l.s.Identity.PutCredential(l.s.AsService(ctx), identityapi.PutCredentialRequest{Owner: owner, System: lhSystem,
				Value: string(b), Label: ptr("Longhouse CLI session " + v.Session), ExpiresMs: ptr(uint64(v.RefreshE))})
			if err != nil {
				l.s.Log.Error("longhouse connect: store", "owner", owner, "err", err)
				return
			}
			l.mu.Lock()
			delete(l.tokens, owner)
			l.mu.Unlock()
			l.s.Log.Info("longhouse connected", "owner", owner, "session", v.Session)
			return
		default:
			l.s.Log.Info("longhouse connect ended", "owner", owner, "status", r.Status)
			return
		}
	}
}

// sameAccount: the Longhouse session of token is of owner
// (user:<id>@<domain>).
func (l *Longhouse) sameAccount(ctx context.Context, owner, token string) error {
	me, err := lh.NewAuthClient(l.http(staticToken(token))).Me(ctx, lh.EmptyRequest{})
	if err != nil {
		return fmt.Errorf("check the account: %w", err)
	}
	id, domain, _ := strings.Cut(strings.TrimPrefix(owner, "user:"), "@")
	if !strings.EqualFold(me.UserId, id) || !strings.EqualFold(me.Domain, domain) {
		return fmt.Errorf("the code was approved by %s@%s, not by %s", me.UserId, me.Domain, owner)
	}
	return nil
}

// staticToken is a bearer source with one token.
type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }
func (t staticToken) Invalidate(string)                     {}

// lhTokens is the bearer source of one person.
type lhTokens struct {
	l     *Longhouse
	owner string
	mu    sync.Mutex
	token string
	exp   time.Time
}

func (t *lhTokens) Invalidate(token string) {
	t.mu.Lock()
	if t.token == token {
		t.token = ""
	}
	t.mu.Unlock()
}

func (t *lhTokens) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.exp) > 10*time.Minute {
		return t.token, nil
	}
	ids := t.l.s.Identity
	sctx := t.l.s.AsService(ctx)
	for attempt := 0; attempt < 20; attempt++ {
		c, err := ids.GetCredential(sctx, identityapi.CredentialRef{Owner: t.owner, System: lhSystem})
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == 404 {
			return "", errNotConnected{lhSystem}
		}
		if err != nil {
			return "", err
		}
		var v lhSession
		if c.Value == nil || json.Unmarshal([]byte(*c.Value), &v) != nil || v.Refresh == "" {
			return "", errNotConnected{lhSystem}
		}
		now := time.Now()
		if v.Token != "" && v.Expires-now.UnixMilli() > int64(10*time.Minute/time.Millisecond) {
			t.token, t.exp = v.Token, time.UnixMilli(v.Expires)
			return t.token, nil
		}
		if v.Lease > now.UnixMilli() {
			// Another replica refreshes now.
			time.Sleep(time.Second)
			continue
		}
		// Take the lease: the revision check makes one winner.
		v.Lease = now.Add(30 * time.Second).UnixMilli()
		b, _ := json.Marshal(v)
		leased, err := ids.PutCredential(sctx, identityapi.PutCredentialRequest{Owner: t.owner, System: lhSystem, Value: string(b), Label: &c.Label, ExpiresMs: ptr(c.ExpiresMs), ExpectedRevision: ptr(c.Revision)})
		if errors.As(err, &re) && re.Code == 409 {
			continue
		}
		if err != nil {
			return "", err
		}
		r, err := lh.NewAuthClient(t.l.public()).RefreshSession(ctx, lh.RefreshSessionRequest{RefreshToken: v.Refresh})
		if err != nil {
			var ce *cal.ClientError
			if errors.As(err, &ce) && ce.Code == 401 {
				// The session is gone (revoked, expired, or a reuse).
				ids.ForgetCredential(sctx, identityapi.CredentialRef{Owner: t.owner, System: lhSystem})
				return "", errNotConnected{lhSystem}
			}
			return "", fmt.Errorf("longhouse refresh: %w", err)
		}
		v = lhSession{Refresh: r.RefreshToken, Session: string(r.SessionId), Token: r.Token, Expires: tsMs(string(r.ExpiresAt)), RefreshE: tsMs(string(r.RefreshExpiresAt))}
		b, _ = json.Marshal(v)
		if _, err := ids.PutCredential(sctx, identityapi.PutCredentialRequest{Owner: t.owner, System: lhSystem, Value: string(b), Label: &c.Label, ExpiresMs: ptr(uint64(v.RefreshE)), ExpectedRevision: ptr(leased.Revision)}); err != nil {
			// The new refresh token is lost: the session must be connected again.
			t.l.s.Log.Error("longhouse refresh: store", "owner", t.owner, "err", err)
		}
		t.token, t.exp = v.Token, time.UnixMilli(v.Expires)
		return t.token, nil
	}
	return "", errors.New("longhouse: could not get a session; try again")
}

func (l *Longhouse) transport(owner string) cal.Transport {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tokens == nil {
		l.tokens = map[string]*lhTokens{}
	}
	t := l.tokens[owner]
	if t == nil {
		t = &lhTokens{l: l, owner: owner}
		l.tokens[owner] = t
	}
	return l.http(t)
}

func tsMs(v string) int64 {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// The MagiService operations.

func (m *Service) Connect(ctx context.Context, req magiapi.ConnectRequest) (magiapi.ConnectResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.ConnectResponse{}, err
	}
	if !strings.HasPrefix(p.Subject, "user:") {
		return magiapi.ConnectResponse{}, rpc.Invalid("only a person can connect an account")
	}
	return m.S.connect(ctx, p.Subject, req.System)
}

func (s *System) connect(ctx context.Context, owner, system string) (magiapi.ConnectResponse, error) {
	switch normalSystem(system) {
	case "calendar":
		if s.Calendar == nil {
			return magiapi.ConnectResponse{}, rpc.Unavailable("no calendar service is configured")
		}
		u, exp, err := s.Calendar.connect(ctx, owner)
		if err != nil {
			return magiapi.ConnectResponse{}, err
		}
		return magiapi.ConnectResponse{System: "calendar", Url: u, ExpiresMs: exp,
			Message: "Open the link, sign in at your home domain, and approve Magi for your calendar."}, nil
	case lhSystem:
		if s.Longhouse == nil {
			return magiapi.ConnectResponse{}, rpc.Unavailable("no Longhouse service is configured")
		}
		return s.Longhouse.connect(ctx, owner)
	}
	return magiapi.ConnectResponse{}, rpc.Invalid("system must be calendar or longhouse")
}

func normalSystem(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "calendar", "catalystcal", "calendars":
		return "calendar"
	case "longhouse", "tasks", "projects":
		return lhSystem
	}
	return v
}

func (s *System) connections(ctx context.Context, owner string) []magiapi.Connection {
	out := []magiapi.Connection{}
	sctx := s.AsService(ctx)
	if s.Calendar != nil {
		c := magiapi.Connection{System: "calendar", State: "none"}
		if aud, err := s.Calendar.aud(ctx); err == nil {
			a := idApp(aud)
			if g, err := s.Identity.ListGrants(sctx, identityapi.ListGrantsRequest{Owner: &owner, Audience: &a}); err == nil && len(g.Grants) > 0 {
				c.State, c.ExpiresMs = g.Grants[0].State, ptr(g.Grants[0].ExpiresMs)
				if c.State == "active" {
					c.State = "connected"
				}
				c.Detail = ptr(strings.Join(g.Grants[0].Scopes, ", "))
			}
		}
		out = append(out, c)
	}
	if s.Longhouse != nil {
		c := magiapi.Connection{System: lhSystem, State: "none"}
		if l, err := s.Identity.ListCredentials(sctx, identityapi.ListCredentialsRequest{Owner: &owner}); err == nil {
			for _, cr := range l.Credentials {
				if cr.System == lhSystem {
					c.State, c.ExpiresMs, c.Detail = "connected", ptr(cr.ExpiresMs), ptr(cr.Label)
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func (m *Service) ListConnections(ctx context.Context, _ magiapi.ListConnectionsRequest) (magiapi.ListConnectionsResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.ListConnectionsResponse{}, err
	}
	return magiapi.ListConnectionsResponse{Connections: m.S.connections(ctx, p.Subject)}, nil
}

func (m *Service) Disconnect(ctx context.Context, req magiapi.DisconnectRequest) (magiapi.Connection, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.Connection{}, err
	}
	s, sctx := m.S, m.S.AsService(ctx)
	switch normalSystem(req.System) {
	case "calendar":
		if s.Calendar == nil {
			return magiapi.Connection{}, rpc.Unavailable("no calendar service is configured")
		}
		aud, err := s.Calendar.aud(ctx)
		if err != nil {
			return magiapi.Connection{}, err
		}
		a := idApp(aud)
		g, err := s.Identity.ListGrants(sctx, identityapi.ListGrantsRequest{Owner: &p.Subject, Audience: &a})
		if err != nil {
			return magiapi.Connection{}, err
		}
		for _, gr := range g.Grants {
			s.Identity.ForgetGrant(sctx, identityapi.ForgetGrantRequest{GrantId: gr.GrantId, Owner: &p.Subject})
		}
		s.Calendar.forget(p.Subject)
		return magiapi.Connection{System: "calendar", State: "none", Detail: ptr("Magi forgot the grant. Revoke it on your LinkKeys account page to end it at the home domain.")}, nil
	case lhSystem:
		if s.Longhouse == nil {
			return magiapi.Connection{}, rpc.Unavailable("no Longhouse service is configured")
		}
		// End the session at Longhouse too (best effort), then forget it.
		detail := "Magi ended the Longhouse session and forgot it."
		if c, err := s.Identity.GetCredential(sctx, identityapi.CredentialRef{Owner: p.Subject, System: lhSystem}); err == nil && c.Value != nil {
			var v lhSession
			if json.Unmarshal([]byte(*c.Value), &v) == nil && v.Session != "" {
				if _, err := lh.NewAuthClient(s.Longhouse.transport(p.Subject)).RevokeSession(ctx, lh.RevokeSessionRequest{SessionId: lh.CliSessionID(v.Session)}); err != nil {
					s.Log.Warn("longhouse disconnect: revoke", "owner", p.Subject, "err", err)
					detail = "Magi forgot the session, but Longhouse did not end it. Revoke it on your Longhouse account page."
				}
			}
		}
		s.Identity.ForgetCredential(sctx, identityapi.CredentialRef{Owner: p.Subject, System: lhSystem})
		s.Longhouse.mu.Lock()
		delete(s.Longhouse.tokens, p.Subject)
		s.Longhouse.mu.Unlock()
		return magiapi.Connection{System: lhSystem, State: "none", Detail: ptr(detail)}, nil
	}
	return magiapi.Connection{}, rpc.Invalid("system must be calendar or longhouse")
}

// CalendarFor is a catalystcal transport that acts as owner (seed tools).
func (s *System) CalendarFor(ctx context.Context, owner string) (cal.Transport, error) {
	if s.Calendar == nil {
		return nil, errors.New("no calendar service is configured")
	}
	return s.Calendar.transport(ctx, owner)
}

// LonghouseFor is a Longhouse transport that acts as owner (seed tools).
func (s *System) LonghouseFor(owner string) (cal.Transport, error) {
	if s.Longhouse == nil {
		return nil, errors.New("Longhouse is not configured")
	}
	return s.Longhouse.transport(owner), nil
}
