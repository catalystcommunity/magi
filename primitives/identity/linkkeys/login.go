// Package linkkeys is a LinkKeys regular relying-party client for browser
// login. It ports the begin and complete steps of the LinkKeys TypeScript SDK
// (sdks/regular-rp/typescript/src/client.ts) to Go. The application stores
// the pending state; this package has no state.
package linkkeys

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// HandleClaim is an optional claim with a name for the user. Magi uses it
// only to show a name. It does not identify the user.
const HandleClaim = "handle"

// LoginError is a failed login check. Code uses the TypeScript SDK names.
type LoginError struct{ Code, Detail string }

func (e *LoginError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

func fail(code, format string, args ...any) error {
	return &LoginError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Resolver looks up DNS TXT records. *net.Resolver satisfies it.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Client begins and completes logins through one RP server.
type Client struct {
	RP        Rp     // usually NewRpClient(&PinnedTransport{...})
	RPDomain  string // the RP server's domain; the assertion audience
	DNS       Resolver
	Now       func() time.Time
	ClockSkew time.Duration
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) dns() Resolver {
	if c.DNS != nil {
		return c.DNS
	}
	return net.DefaultResolver
}

// Identity is a parsed "username@domain" or "domain" input.
type Identity struct {
	Username string // a browser prefill hint only; never an identity
	Domain   string // lowercase
}

var (
	usernameRE = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+\\-/=?^_`{|}~]+(\\.[A-Za-z0-9!#$%&'*+\\-/=?^_`{|}~]+)*$")
	labelRE    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

// ParseIdentity follows the LinkKeys shared identity-input contract.
func ParseIdentity(raw string) (Identity, error) {
	bad := errors.New("identity must be username@domain or domain")
	in := strings.TrimSpace(raw)
	if in == "" || len(in) > 330 {
		return Identity{}, bad
	}
	for _, r := range in {
		if r > 0x7f {
			return Identity{}, bad
		}
	}
	parts := strings.Split(in, "@")
	if len(parts) > 2 {
		return Identity{}, bad
	}
	var id Identity
	host := parts[len(parts)-1]
	if len(parts) == 2 {
		id.Username = parts[0]
		if len(id.Username) == 0 || len(id.Username) > 64 || !usernameRE.MatchString(id.Username) {
			return Identity{}, bad
		}
	}
	if !ValidDomain(host, true) {
		return Identity{}, bad
	}
	id.Domain = strings.ToLower(host)
	return id, nil
}

// ValidDomain checks an ASCII DNS name. With allowPort, a ":port" suffix is
// accepted, and a name without a dot is valid only with a port.
func ValidDomain(d string, allowPort bool) bool {
	host, port, hasPort := strings.Cut(d, ":")
	if hasPort {
		if !allowPort {
			return false
		}
		n := 0
		for _, r := range port {
			if r < '0' || r > '9' || n > 65535 {
				return false
			}
			n = n*10 + int(r-'0')
		}
		if port == "" || n < 1 || n > 65535 {
			return false
		}
	}
	if host == "" || len(host) > 253 || (!strings.Contains(host, ".") && !hasPort) {
		return false
	}
	for _, l := range strings.Split(host, ".") {
		if !labelRE.MatchString(l) {
			return false
		}
	}
	return true
}

// BrowserBase returns the IDP browser endpoint of an identity domain: the
// https= value of _linkkeys_apis.<domain>, else https://<domain>.
func (c *Client) BrowserBase(ctx context.Context, domain string) string {
	txts, err := c.dns().LookupTXT(ctx, "_linkkeys_apis."+domain)
	if err == nil {
		for _, txt := range txts {
			f := strings.Fields(txt)
			if !hasField(f, "v=lk1") {
				continue
			}
			for _, p := range f {
				if v, ok := strings.CutPrefix(p, "https="); ok && v != "" {
					if u, err := url.Parse("https://" + v); err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
						return strings.TrimRight(u.String(), "/")
					}
				}
			}
		}
	}
	return "https://" + domain
}

func hasField(f []string, want string) bool {
	for _, x := range f {
		if x == want {
			return true
		}
	}
	return false
}

// Pending is the state to keep between Begin and Complete. Store it with
// an atomic take keyed by State.
type Pending struct {
	State       string
	Nonce       string
	Domain      string
	APIBase     string
	CallbackURL string
}

// Begin signs a login request. It returns the IDP redirect URL and the
// pending state. callbackURL must be https on the RP domain or a subdomain.
func (c *Client) Begin(ctx context.Context, id Identity, callbackURL string) (string, Pending, error) {
	p := Pending{State: randomToken(), Nonce: randomToken(), Domain: id.Domain}
	cb, err := c.callbackFor(callbackURL, p.State)
	if err != nil {
		return "", p, err
	}
	p.CallbackURL = cb
	p.APIBase = c.BrowserBase(ctx, id.Domain)
	signed, err := c.RP.SignRequest(ctx, RpSignRequest{
		CallbackUrl:     cb,
		Nonce:           p.Nonce,
		RequestedClaims: &ClaimRequest{Required: []RequestedClaim{}, Optional: []RequestedClaim{{ClaimType: HandleClaim, Datatype: "text"}}},
	})
	if err != nil {
		return "", p, fmt.Errorf("linkkeys sign-request: %w", err)
	}
	u, err := url.Parse(p.APIBase + "/auth/authorize")
	if err != nil {
		return "", p, err
	}
	q := url.Values{}
	q.Set("callback_url", cb)
	q.Set("nonce", p.Nonce)
	if id.Username != "" {
		q.Set("username", id.Username)
	}
	q.Set("relying_party", c.RPDomain)
	q.Set("signed_request", signed.SignedRequest)
	u.RawQuery = q.Encode()
	return u.String(), p, nil
}

func (c *Client) callbackFor(raw, state string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fail("invalid-callback", "callback URL is not a URL")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	rp := strings.ToLower(c.RPDomain)
	if u.Scheme != "https" || (host != rp && !strings.HasSuffix(host, "."+rp)) {
		return "", fail("invalid-callback", "callback URL must use https on %s or a subdomain", rp)
	}
	q := u.Query()
	if u.User != nil || u.Fragment != "" || q.Has("lk_state") || q.Has("encrypted_token") {
		return "", fail("invalid-callback", "callback URL has a reserved or unsafe component")
	}
	q.Set("lk_state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Verified is a completed login.
type Verified struct {
	UserID      string // stable LinkKeys account ID
	Domain      string
	Handle      string // verified handle claim, as signed by Domain
	DisplayName string
}

// Complete checks a callback. The caller has already taken p atomically
// and checked the browser binding and the pending expiry.
func (c *Client) Complete(ctx context.Context, p Pending, encryptedToken string) (Verified, error) {
	if encryptedToken == "" || len(encryptedToken) > 2<<20 {
		return Verified{}, fail("assertion-unverified", "no encrypted token")
	}
	dec, err := c.RP.DecryptToken(ctx, RpDecryptRequest{EncryptedToken: encryptedToken})
	if err != nil {
		return Verified{}, fmt.Errorf("linkkeys decrypt-token: %w", err)
	}
	vr, err := c.RP.VerifyAssertion(ctx, RpVerifyRequest{SignedAssertion: dec.SignedAssertion, ExpectedDomain: p.Domain})
	if err != nil {
		return Verified{}, fmt.Errorf("linkkeys verify-assertion: %w", err)
	}
	a := vr.Assertion
	switch {
	case !vr.Verified:
		return Verified{}, fail("assertion-unverified", "")
	case a.Domain != p.Domain:
		return Verified{}, fail("domain-mismatch", "%s", a.Domain)
	case a.Audience != c.RPDomain:
		return Verified{}, fail("audience-mismatch", "%s", a.Audience)
	case a.Nonce != p.Nonce:
		return Verified{}, fail("nonce-mismatch", "")
	case a.UserId == "":
		return Verified{}, fail("userinfo-mismatch", "no user id")
	}
	skew := c.ClockSkew
	if skew == 0 {
		skew = time.Minute
	}
	now := c.now()
	iat, err1 := time.Parse(time.RFC3339, a.IssuedAt)
	exp, err2 := time.Parse(time.RFC3339, a.ExpiresAt)
	if err1 != nil || err2 != nil || iat.After(now.Add(skew)) || exp.Before(now) || exp.Before(iat) {
		return Verified{}, fail("invalid-assertion-time", "")
	}
	info, err := c.RP.UserinfoFetch(ctx, RpUserInfoRequest{Token: dec.SignedAssertion, ApiBase: p.APIBase, Domain: p.Domain})
	if err != nil {
		return Verified{}, fmt.Errorf("linkkeys userinfo-fetch: %w", err)
	}
	if info.UserId != a.UserId || info.Domain != a.Domain {
		return Verified{}, fail("userinfo-mismatch", "")
	}
	authorized := map[string]bool{}
	for _, t := range a.AuthorizedClaims {
		authorized[t] = true
	}
	// The identity is user_id@domain from the verified assertion. Claims
	// are only display data here, so their signatures are not checked.
	handle := ""
	for _, cl := range info.Claims {
		if cl.UserId != a.UserId {
			return Verified{}, fail("claim-subject-mismatch", "%s", cl.ClaimId)
		}
		if !authorized[cl.ClaimType] {
			return Verified{}, fail("unauthorized-claim", "%s", cl.ClaimType)
		}
		if cl.ClaimType == HandleClaim && handle == "" && utf8.Valid(cl.ClaimValue) {
			handle = string(cl.ClaimValue)
		}
	}
	return Verified{UserID: a.UserId, Domain: a.Domain, Handle: handle, DisplayName: info.DisplayName}, nil
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
