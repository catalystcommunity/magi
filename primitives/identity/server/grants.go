package server

// Act-as grants: a person lets Magi act as them at another application
// (the audience), such as a calendar service. Magi is the grantee. The
// LinkKeys regular-RP Go SDK makes and checks the signed messages:
//
//  1. The audience signs a scope set for Magi (the composer gets it from
//     the audience and calls begin-grant).
//  2. begin-grant checks the scope set and signs a grant request with Magi's
//     application key. The person approves it at their home domain.
//  3. The home domain sends the browser to /login/grant/callback with the
//     grant id and the nonce. Magi fetches the grant through its RP, checks
//     that it names the person and the audience, and stores it.
//  4. present-grant checks the grant again at the home domain (a revoked
//     grant fails) and signs a presentation for one audience request.
//
// Credentials are for systems that have no act-as grants: Magi keeps them
// encrypted with AES-256-GCM.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	rp "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/identity/api"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

// Scopes for services.
const (
	ScopeActAs   = "identity:act-as"  // name another owner in begin-grant and put-credential
	ScopePresent = "identity:present" // present grants and read credential values
)

// GrantRequestTTL is the time a person has to approve a grant.
const GrantRequestTTL = 15 * time.Minute

// AppKeys is the private file of Magi's LinkKeys application instance. Seeds
// are base64 Ed25519 seeds. The format is the same as the one that
// catalystcal and Longhouse use.
type AppKeys struct {
	Application struct {
		SubjectDomain string `json:"subject_domain"`
		SubjectUserID string `json:"subject_user_id"`
		ApplicationID string `json:"application_id"`
	} `json:"application"`
	InstanceID string `json:"instance_id"`
	Keys       []struct {
		KeyID string `json:"key_id"`
		Seed  string `json:"seed"`
	} `json:"keys"`
}

// LoadAppKeys reads and checks an application key file. Errors never
// contain key material.
func LoadAppKeys(path string) (AppKeys, error) {
	var k AppKeys
	b, err := os.ReadFile(path)
	if err != nil {
		return k, err
	}
	if json.Unmarshal(b, &k) != nil {
		return k, errors.New("application keys: invalid JSON")
	}
	a := k.Application
	if a.SubjectDomain == "" || a.SubjectUserID == "" || a.ApplicationID == "" || k.InstanceID == "" || len(k.Keys) == 0 {
		return k, errors.New("application keys: identity, instance, and keys are required")
	}
	for _, key := range k.Keys {
		seed, err := base64.StdEncoding.DecodeString(key.Seed)
		if err != nil || len(seed) != ed25519.SeedSize || key.KeyID == "" {
			return k, errors.New("application keys: a key is not valid")
		}
	}
	return k, nil
}

func (k AppKeys) signer(i int) rp.ApplicationSigner {
	seed, _ := base64.StdEncoding.DecodeString(k.Keys[i].Seed)
	return rp.ApplicationSigner{KeyID: k.Keys[i].KeyID, Algorithm: "ed25519", PrivateKeyBytes: seed}
}

func (k AppKeys) public(i int) ed25519.PublicKey {
	seed, _ := base64.StdEncoding.DecodeString(k.Keys[i].Seed)
	return ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
}

// Grantee holds Magi's application keys and its RP connection.
type Grantee struct {
	Keys AppKeys
	// RP is Magi's own LinkKeys RP (API key). Grant refreshes and key
	// lookups go through it.
	RP lk.Transport
	// Home is the home domain of the application account. Attestation
	// renewal goes there; it uses application signatures, not an API key.
	Home     lk.Transport
	signer   rp.GranteeSigner
	resolver *rp.CachedResolver
}

// NewGrantee makes a Grantee. home may be nil (no renewal).
func NewGrantee(keys AppKeys, rpTransport, home lk.Transport) *Grantee {
	return &Grantee{Keys: keys, RP: rpTransport, Home: home,
		signer:   rp.NewApplicationGranteeSigner(keys.InstanceID, keys.signer(0)),
		resolver: rp.NewCachedResolver(rpTransport, rp.CachedResolverOptions{})}
}

func (g *Grantee) ref() lk.GranteeRef {
	a := g.Keys.Application
	return lk.GranteeRef{Application: &lk.ApplicationRef{SubjectDomain: a.SubjectDomain, SubjectUserId: a.SubjectUserID, ApplicationId: a.ApplicationID}}
}

// RenewKeys renews the attestations of every key at the home domain.
func (g *Grantee) RenewKeys(ctx context.Context) error {
	if g.Home == nil {
		return nil
	}
	c := lk.NewApplicationKeysClient(g.Home)
	a := g.Keys.Application
	for i := range g.Keys.Keys {
		signer := g.Keys.signer(i)
		ch, err := c.StartKeyChallenge(ctx, lk.StartApplicationKeyChallengeRequest{SubjectUserId: a.SubjectUserID, ApplicationId: a.ApplicationID, InstanceId: g.Keys.InstanceID, Purpose: "renew", KeyUsage: "sign", Algorithm: "ed25519", PublicKey: g.Keys.public(i)})
		if err != nil {
			return err
		}
		if ch.Challenge == nil {
			return errors.New("home domain did not return a signing challenge")
		}
		req, err := rp.SignRenewal(lk.ApplicationKeyRenewal{SubjectUserId: a.SubjectUserID, SubjectDomain: a.SubjectDomain, ApplicationId: a.ApplicationID, InstanceId: g.Keys.InstanceID, KeyId: g.Keys.Keys[i].KeyID, ChallengeId: ch.ChallengeId, Challenge: *ch.Challenge, RequestedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: ch.ExpiresAt}, nil, &signer)
		if err != nil {
			return err
		}
		if _, err = c.RenewAttestation(ctx, lk.RenewApplicationKeyAttestationRequest{Request: req}); err != nil {
			return err
		}
	}
	return nil
}

// Enroll adds the instance and its keys to the application account. The
// transport authenticates with the account's API key.
func (g *Grantee) Enroll(ctx context.Context, account lk.Transport) error {
	c := lk.NewApplicationKeysClient(account)
	a := g.Keys.Application
	var signed []lk.SignedApplicationKeyAddition
	for i := range g.Keys.Keys {
		signer := g.Keys.signer(i)
		pub := g.Keys.public(i)
		ch, err := c.StartKeyChallenge(ctx, lk.StartApplicationKeyChallengeRequest{SubjectUserId: a.SubjectUserID, ApplicationId: a.ApplicationID, InstanceId: g.Keys.InstanceID, Purpose: "add", KeyUsage: "sign", Algorithm: "ed25519", PublicKey: pub})
		if err != nil {
			return err
		}
		if ch.Challenge == nil {
			return errors.New("home domain did not return a signing challenge")
		}
		add, err := rp.SignAddition(lk.ApplicationKeyAddition{SubjectUserId: a.SubjectUserID, SubjectDomain: a.SubjectDomain, ApplicationId: a.ApplicationID, InstanceId: g.Keys.InstanceID, KeyId: g.Keys.Keys[i].KeyID, KeyUsage: "sign", Algorithm: "ed25519", PublicKey: pub, Fingerprint: rp.Fingerprint(pub), RequestedKeyLifetimeSeconds: 365 * 24 * 3600, ChallengeId: ch.ChallengeId, Challenge: *ch.Challenge, RequestedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: ch.ExpiresAt}, nil, &signer)
		if err != nil {
			return err
		}
		signed = append(signed, add)
	}
	r, err := lk.NewAccountClient(account).EnrollApplicationInstance(ctx, lk.EnrollApplicationInstanceRequest{ApplicationId: a.ApplicationID, InstanceId: g.Keys.InstanceID, Keys: signed})
	if err != nil {
		return err
	}
	if r.SubjectDomain != a.SubjectDomain || r.SubjectUserId != a.SubjectUserID || r.ApplicationId != a.ApplicationID || r.InstanceId != g.Keys.InstanceID {
		return errors.New("home domain returned another application identity")
	}
	return nil
}

// GenerateAppKeys writes a new key file with three keys (mode 600). It
// refuses to replace a file.
func GenerateAppKeys(path, domain, userID, appID, instance string) error {
	if domain == "" || userID == "" || appID == "" || instance == "" {
		return errors.New("domain, user id, application id, and instance id are required")
	}
	var k AppKeys
	k.Application.SubjectDomain, k.Application.SubjectUserID, k.Application.ApplicationID = domain, userID, appID
	k.InstanceID = instance
	for i := 1; i <= 3; i++ {
		_, seed, _, err := rp.NewSigningKeyPair()
		if err != nil {
			return err
		}
		k.Keys = append(k.Keys, struct {
			KeyID string `json:"key_id"`
			Seed  string `json:"seed"`
		}{fmt.Sprintf("sign-%d", i), base64.StdEncoding.EncodeToString(seed[:])})
	}
	b, _ := json.MarshalIndent(k, "", "  ")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// The audience text form, "<user_id>@<domain>/<app_id>".
func audienceKey(a api.AppRef) string {
	return a.UserId + "@" + strings.ToLower(a.Domain) + "/" + a.AppId
}

func parseAudience(s string) api.AppRef {
	user, rest, _ := strings.Cut(s, "@")
	domain, app, _ := strings.Cut(rest, "/")
	return api.AppRef{UserId: user, Domain: domain, AppId: app}
}

func lkApp(a api.AppRef) lk.ApplicationRef {
	return lk.ApplicationRef{SubjectDomain: strings.ToLower(a.Domain), SubjectUserId: a.UserId, ApplicationId: a.AppId}
}

var userSubjectRE = regexp.MustCompile(`^user:([0-9a-fA-F-]{36})@([a-z0-9.-]+)$`)

// splitUser returns the LinkKeys user id and domain of a user subject.
func splitUser(subject string) (string, string, bool) {
	m := userSubjectRE.FindStringSubmatch(subject)
	if m == nil {
		return "", "", false
	}
	return strings.ToLower(m[1]), m[2], true
}

// owner returns the owner that the caller acts for: the caller itself, or
// the named owner when the caller has one of scopes.
func owner(p authn.Principal, named *string, scopes ...string) (string, error) {
	if named == nil || *named == "" || *named == p.Subject {
		if p.Kind != authn.KindUser {
			return "", rpc.Invalid("owner is required")
		}
		return p.Subject, nil
	}
	for _, s := range scopes {
		if p.Has(s) {
			return *named, nil
		}
	}
	return "", rpc.Denied("only the owner can do this")
}

func (s *Service) grantee() (*Grantee, error) {
	if s.Grantee == nil {
		return nil, rpc.Unavailable("act-as grants are not configured")
	}
	return s.Grantee, nil
}

func (s *Service) Application(ctx context.Context, _ api.ApplicationRequest) (api.AppRef, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.AppRef{}, err
	}
	g, err := s.grantee()
	if err != nil {
		return api.AppRef{}, err
	}
	a := g.Keys.Application
	return api.AppRef{Domain: a.SubjectDomain, UserId: a.SubjectUserID, AppId: a.ApplicationID}, nil
}

func (s *Service) BeginGrant(ctx context.Context, req api.BeginGrantRequest) (api.BeginGrantResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.BeginGrantResponse{}, err
	}
	g, err := s.grantee()
	if err != nil {
		return api.BeginGrantResponse{}, err
	}
	who, err := owner(p, req.Owner, ScopeActAs, ScopeAdmin)
	if err != nil {
		return api.BeginGrantResponse{}, err
	}
	userID, domain, ok := splitUser(who)
	if !ok {
		return api.BeginGrantResponse{}, rpc.Invalid("owner must be user:<LinkKeys user id>@<domain>")
	}
	returnURL := ""
	if req.ReturnUrl != nil && *req.ReturnUrl != "" {
		u, err := url.Parse(*req.ReturnUrl)
		if err != nil || u.Scheme+"://"+u.Host != s.origin() || u.User != nil || len(*req.ReturnUrl) > 512 {
			return api.BeginGrantResponse{}, rpc.Invalid("return_url must be a page on %s", s.origin())
		}
		returnURL = u.String()
	}
	signed, err := lk.DecodeSignedActAsScopeSet(req.SignedScopeSet)
	if err != nil {
		return api.BeginGrantResponse{}, rpc.Invalid("signed_scope_set does not decode")
	}
	audience := lkApp(req.Audience)
	keys, err := g.resolver.Resolve(ctx, rp.InstanceRef{SubjectDomain: audience.SubjectDomain, SubjectUserID: audience.SubjectUserId, ApplicationID: audience.ApplicationId, InstanceID: signed.SignerInstanceId}, nil)
	if err != nil {
		s.log().Warn("begin grant: audience keys", "audience", audienceKey(req.Audience), "err", err)
		return api.BeginGrantResponse{}, rpc.Unavailable("the keys of the audience are not available")
	}
	if keys.Freshness == rp.FreshnessStale {
		return api.BeginGrantResponse{}, rpc.Unavailable("the keys of the audience are not current")
	}
	set, err := rp.VerifyScopeSet(signed, audience, keys.Keys.AttestedKeyRefs(), rp.AcceptBeforeRevocation)
	if err != nil {
		return api.BeginGrantResponse{}, rpc.Invalid("the scope set does not verify: %v", err)
	}
	if err := rp.CheckScopeSetCurrent(set, s.now(), 300); err != nil {
		return api.BeginGrantResponse{}, rpc.Invalid("the scope set is not current: %v", err)
	}
	if !rp.SameGrantee(set.Grantee, g.ref()) {
		return api.BeginGrantResponse{}, rpc.Invalid("the scope set names another grantee")
	}
	if !s.allow() {
		return api.BeginGrantResponse{}, rpc.Errorf(429, "too many requests; try again soon")
	}
	nonce := token()
	now := s.now().UTC()
	exp := now.Add(GrantRequestTTL)
	// Ask for the longest terms; the home domain gives its maximum or less.
	// Without them a grant lives one hour and cannot be renewed.
	life, window := int64(GrantLifetime/time.Second), int64(GrantRenewalWindow/time.Second)
	signedReq, err := rp.SignGrantRequest(lk.ActAsGrantRequest{Grantee: g.ref(), ScopeSet: signed, CallbackUrl: s.PublicURL + "/login/grant/callback", Nonce: nonce, RequestedAt: now.Format(time.RFC3339), ExpiresAt: exp.Format(time.RFC3339),
		RequestedLifetimeSeconds: &life, RequestedRenewalWindowSeconds: &window}, g.signer)
	if err != nil {
		return api.BeginGrantResponse{}, err
	}
	link, err := rp.GrantRequestURL(s.LinkKeys.BrowserBase(ctx, domain), signedReq)
	if err != nil {
		return api.BeginGrantResponse{}, rpc.Unavailable("the home domain of %s has no browser address", domain)
	}
	err = s.DB.CreateGrantRequest(ctx, store.GrantRequest{NonceHash: store.Hash(nonce), Owner: who, Domain: domain, UserID: userID,
		Audience: audienceKey(req.Audience), ReturnURL: returnURL, CreatedMs: now.UnixMilli(), ExpiresMs: exp.UnixMilli()})
	if err != nil {
		return api.BeginGrantResponse{}, err
	}
	s.log().Info("grant requested", "owner", who, "audience", audienceKey(req.Audience))
	return api.BeginGrantResponse{Url: link, ExpiresMs: uint64(exp.UnixMilli())}, nil
}

var grantIDRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// grantCallback is GET /login/grant/callback?act_as_grant_id=...&nonce=...
func (s *Service) grantCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fail := func(code int, text string) {
		render(w, code, view{Title: "Magi was not connected", Text: text})
	}
	if len(q["act_as_grant_id"]) != 1 || len(q["nonce"]) != 1 {
		if q.Get("error") != "" {
			fail(http.StatusOK, "You did not approve the request. Nothing changed.")
			return
		}
		fail(http.StatusBadRequest, "The callback must have one grant id and one nonce.")
		return
	}
	grantID, nonce := q.Get("act_as_grant_id"), q.Get("nonce")
	if !grantIDRE.MatchString(grantID) || nonce == "" || len(nonce) > 256 {
		fail(http.StatusBadRequest, "The callback is not valid.")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	g, err := s.grantee()
	if err != nil {
		fail(http.StatusServiceUnavailable, "Act-as grants are not configured.")
		return
	}
	now := s.now()
	req, err := s.DB.TakeGrantRequest(ctx, store.Hash(nonce), grantID, now.UnixMilli())
	if errors.Is(err, store.ErrNotFound) {
		fail(http.StatusConflict, "This request is unknown, expired, or was already used. Start again from Magi.")
		return
	}
	if err != nil {
		fail(http.StatusServiceUnavailable, "Try again later.")
		return
	}
	grant, err := s.refresh(ctx, g, req.Domain, grantID)
	if err != nil {
		s.log().Warn("grant callback: refresh", "owner", req.Owner, "err", err)
		fail(http.StatusBadGateway, "Magi could not get the approval from your home domain. Start again from Magi.")
		return
	}
	d := grant.Decoded
	if !strings.EqualFold(d.UserId, req.UserID) || !strings.EqualFold(d.SubjectDomain, req.Domain) || d.Audience != lkApp(parseAudience(req.Audience)) {
		s.log().Warn("grant callback: grant does not match the request", "owner", req.Owner, "grant_user", d.UserId, "audience", req.Audience)
		fail(http.StatusForbidden, "The approval names another person or application. Sign in at your home domain as the person who asked Magi, and start again.")
		return
	}
	row, err := grantRow(req.Owner, req.Domain, req.Audience, grant, now)
	if err == nil {
		err = s.DB.PutGrant(ctx, row)
	}
	if err != nil {
		s.log().Error("grant callback: store", "err", err)
		fail(http.StatusServiceUnavailable, "Try again later.")
		return
	}
	s.log().Info("grant stored", "owner", req.Owner, "audience", req.Audience, "grant_id", grantID, "scopes", d.ApprovedScope)
	render(w, http.StatusOK, view{Title: "Magi is connected",
		Text: "Magi can now act for you at " + parseAudience(req.Audience).AppId + " (" + strings.Join(d.ApprovedScope, ", ") + "). You can close this page.",
		Back: req.ReturnURL})
}

func ms(t string) int64 {
	v, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return 0
	}
	return v.UnixMilli()
}

func grantRow(owner, domain, audience string, g rp.RefreshedGrant, now time.Time) (store.Grant, error) {
	return store.Grant{ID: g.Decoded.GrantId, Owner: owner, Domain: domain, Audience: audience, State: "active",
		Scopes: g.Decoded.ApprovedScope, Bytes: lk.EncodeSignedActAsGrant(g.Grant),
		ExpiresMs: ms(g.Decoded.ExpiresAt), RenewableUntilMs: ms(g.Decoded.RenewableUntil),
		CreatedMs: now.UnixMilli(), UpdatedMs: now.UnixMilli()}, nil
}

// GrantLifetime and GrantRenewalWindow are the terms that Magi asks for (the
// LinkKeys maximums). A grant then lives a day; RenewGrants renews it until
// the end of the window, after which the person connects again.
const (
	GrantLifetime      = 24 * time.Hour
	GrantRenewalWindow = 30 * 24 * time.Hour
)

// RenewGrants renews the active grants that have less than half of their
// lifetime left. The home domain renews only after half the lifetime, so a
// grant that nobody uses does not expire. It gives the number renewed.
func (s *Service) RenewGrants(ctx context.Context) (int, error) {
	g, err := s.grantee()
	if err != nil {
		return 0, err
	}
	now := s.now()
	rows, err := s.DB.RenewableGrants(ctx, now.Add(GrantLifetime/2).UnixMilli(), now.UnixMilli())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, use := range rows {
		fresh, err := s.refresh(ctx, g, use.Domain, use.ID)
		if errors.Is(err, errGrantGone) {
			s.DB.SetGrantState(ctx, use.ID, "revoked", now.UnixMilli())
			s.log().Info("grant ended", "owner", use.Owner, "grant_id", use.ID, "err", err)
			continue
		}
		if err != nil {
			s.log().Warn("renew grant", "owner", use.Owner, "grant_id", use.ID, "err", err)
			continue
		}
		if fresh.Decoded.GrantId != use.ID || !strings.EqualFold(fresh.Decoded.SubjectDomain, use.Domain) {
			continue
		}
		if fresh.Signed {
			if row, err := grantRow(use.Owner, use.Domain, use.Audience, fresh, now); err == nil {
				row.CreatedMs = use.CreatedMs
				if s.DB.PutGrant(ctx, row) == nil {
					n++
				}
			}
		}
	}
	return n, nil
}

// errGrantGone: the home domain says the grant is revoked or expired.
var errGrantGone = errors.New("the grant is revoked or expired")

func (s *Service) refresh(ctx context.Context, g *Grantee, domain, grantID string) (rp.RefreshedGrant, error) {
	now := s.now().UTC()
	req, err := rp.SignRefreshRequest(lk.ActAsRefreshRequest{GrantId: grantID, Grantee: g.ref(), RequestedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339), Nonce: token()}, g.signer)
	if err != nil {
		return rp.RefreshedGrant{}, err
	}
	out, err := rp.RefreshGrant(ctx, g.RP, domain, req)
	if err != nil {
		// LinkKeys also uses an authentication error for a rate limit.
		// Only an explicit revocation or expiry ends the grant.
		if m := err.Error(); strings.Contains(m, "revoked") || strings.Contains(m, "expired") {
			return out, fmt.Errorf("%w: %v", errGrantGone, err)
		}
	}
	return out, err
}

func (s *Service) state(g store.Grant, now int64) string {
	if g.State == "active" && g.ExpiresMs <= now {
		return "expired"
	}
	return g.State
}

func (s *Service) apiGrant(g store.Grant) api.Grant {
	return api.Grant{GrantId: g.ID, Owner: g.Owner, Audience: parseAudience(g.Audience), Scopes: append([]string{}, g.Scopes...),
		ExpiresMs: uint64(g.ExpiresMs), RenewableUntilMs: uint64(g.RenewableUntilMs), CreatedMs: uint64(g.CreatedMs),
		State: s.state(g, s.now().UnixMilli())}
}

func (s *Service) ListGrants(ctx context.Context, req api.ListGrantsRequest) (api.ListGrantsResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListGrantsResponse{}, err
	}
	who, err := owner(p, req.Owner, ScopePresent, ScopeActAs, ScopeAdmin)
	if err != nil {
		return api.ListGrantsResponse{}, err
	}
	aud := ""
	if req.Audience != nil {
		aud = audienceKey(*req.Audience)
	}
	rows, err := s.DB.Grants(ctx, who, aud)
	if err != nil {
		return api.ListGrantsResponse{}, err
	}
	out := api.ListGrantsResponse{Grants: []api.Grant{}}
	for _, g := range rows {
		out.Grants = append(out.Grants, s.apiGrant(g))
	}
	return out, nil
}

func (s *Service) PresentGrant(ctx context.Context, req api.PresentGrantRequest) (api.PresentGrantResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.PresentGrantResponse{}, err
	}
	if !p.Has(ScopePresent) {
		return api.PresentGrantResponse{}, rpc.Denied("%s is required", ScopePresent)
	}
	g, err := s.grantee()
	if err != nil {
		return api.PresentGrantResponse{}, err
	}
	if len(req.RequestDigest) != 32 {
		return api.PresentGrantResponse{}, rpc.Invalid("request_digest must be 32 bytes")
	}
	rows, err := s.DB.Grants(ctx, req.Owner, audienceKey(req.Audience))
	if err != nil {
		return api.PresentGrantResponse{}, err
	}
	now := s.now()
	var use *store.Grant
	for i := range rows {
		if req.GrantId != nil && *req.GrantId != "" && rows[i].ID != *req.GrantId {
			continue
		}
		if rows[i].State == "active" {
			use = &rows[i]
			break
		}
	}
	if use == nil {
		return api.PresentGrantResponse{}, rpc.NotFound("%s has no active grant for %s", req.Owner, req.Audience.AppId)
	}
	fresh, err := s.refresh(ctx, g, use.Domain, use.ID)
	if errors.Is(err, errGrantGone) {
		s.DB.SetGrantState(context.WithoutCancel(ctx), use.ID, "revoked", now.UnixMilli())
		s.log().Info("grant ended", "owner", use.Owner, "grant_id", use.ID, "err", err)
		return api.PresentGrantResponse{}, rpc.Denied("the grant was revoked or expired; ask the person to connect again")
	}
	if err != nil {
		s.log().Warn("present grant: refresh", "owner", use.Owner, "err", err)
		return api.PresentGrantResponse{}, rpc.Unavailable("the home domain did not answer; try again soon")
	}
	if fresh.Decoded.GrantId != use.ID || !strings.EqualFold(fresh.Decoded.SubjectDomain, use.Domain) || fresh.Decoded.Audience != lkApp(req.Audience) {
		return api.PresentGrantResponse{}, rpc.Denied("the home domain returned another grant")
	}
	if fresh.Signed {
		if row, err := grantRow(use.Owner, use.Domain, use.Audience, fresh, now); err == nil {
			row.CreatedMs = use.CreatedMs
			s.DB.PutGrant(ctx, row)
		}
	}
	nonce := make([]byte, 32)
	rand.Read(nonce)
	cred, err := rp.Present(fresh.Grant, lkApp(req.Audience), req.RequestDigest, now.UTC(), nonce, g.signer)
	if err != nil {
		return api.PresentGrantResponse{}, err
	}
	return api.PresentGrantResponse{Credential: lk.EncodeActAsCredential(cred), GrantId: use.ID,
		Scopes: fresh.Decoded.ApprovedScope, ExpiresMs: uint64(ms(fresh.Decoded.ExpiresAt))}, nil
}

// ExpiringGrants lists the active grants of all owners that end within
// req.WithinMs (Magi reminds each owner to connect again).
func (s *Service) ExpiringGrants(ctx context.Context, req api.ExpiringGrantsRequest) (api.ListGrantsResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListGrantsResponse{}, err
	}
	if !p.Has(ScopeActAs) && !p.Has(ScopeAdmin) {
		return api.ListGrantsResponse{}, rpc.Denied("%s is required", ScopeActAs)
	}
	now := s.now()
	rows, err := s.DB.ExpiringGrants(ctx, now.Add(time.Duration(req.WithinMs)*time.Millisecond).UnixMilli(), now.UnixMilli())
	if err != nil {
		return api.ListGrantsResponse{}, err
	}
	out := api.ListGrantsResponse{Grants: []api.Grant{}}
	for _, g := range rows {
		out.Grants = append(out.Grants, s.apiGrant(g))
	}
	return out, nil
}

func (s *Service) ForgetGrant(ctx context.Context, req api.ForgetGrantRequest) (api.EmptyResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.EmptyResponse{}, err
	}
	who, err := owner(p, req.Owner, ScopeActAs, ScopeAdmin)
	if err != nil {
		return api.EmptyResponse{}, err
	}
	if err := s.DB.DeleteGrant(ctx, req.GrantId, who); errors.Is(err, store.ErrNotFound) {
		return api.EmptyResponse{}, rpc.NotFound("no such grant")
	} else if err != nil {
		return api.EmptyResponse{}, err
	}
	return api.EmptyResponse{}, nil
}

// Credentials.

var systemRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func (s *Service) aead() (cipher.AEAD, error) {
	if len(s.CredentialKey) != 32 {
		return nil, rpc.Unavailable("credentials are not configured")
	}
	b, err := aes.NewCipher(s.CredentialKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// LoadCredentialKey reads a base64 32-byte key file.
func LoadCredentialKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(k) != 32 {
		return nil, errors.New("credential key: the file must hold 32 bytes in base64")
	}
	return k, nil
}

func (s *Service) PutCredential(ctx context.Context, req api.PutCredentialRequest) (api.Credential, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Credential{}, err
	}
	who, err := owner(p, &req.Owner, ScopeActAs, ScopeAdmin)
	if err != nil {
		return api.Credential{}, err
	}
	if !systemRE.MatchString(req.System) || req.Value == "" || len(req.Value) > 8192 {
		return api.Credential{}, rpc.Invalid("system must be a short lower-case name, and value 1 to 8192 characters")
	}
	a, err := s.aead()
	if err != nil {
		return api.Credential{}, err
	}
	nonce := make([]byte, a.NonceSize())
	rand.Read(nonce)
	enc := a.Seal(nonce, nonce, []byte(req.Value), []byte(who+"\n"+req.System))
	label := ""
	if req.Label != nil {
		label = *req.Label
	}
	var exp int64
	if req.ExpiresMs != nil {
		exp = int64(*req.ExpiresMs)
	}
	expected := int64(-1)
	if req.ExpectedRevision != nil {
		expected = int64(*req.ExpectedRevision)
	}
	now := s.now().UnixMilli()
	rev, err := s.DB.PutCredential(ctx, store.Credential{Owner: who, System: req.System, Label: label, ValueEnc: enc, ExpiresMs: exp, UpdatedMs: now}, expected)
	if errors.Is(err, store.ErrConflict) {
		return api.Credential{}, rpc.Conflict("the credential changed; read it again")
	}
	if err != nil {
		return api.Credential{}, err
	}
	return api.Credential{Owner: who, System: req.System, Label: label, Revision: uint64(rev), ExpiresMs: uint64(exp), UpdatedMs: uint64(now)}, nil
}

func (s *Service) GetCredential(ctx context.Context, req api.CredentialRef) (api.Credential, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Credential{}, err
	}
	if !p.Has(ScopePresent) {
		return api.Credential{}, rpc.Denied("%s is required", ScopePresent)
	}
	c, err := s.DB.GetCredential(ctx, req.Owner, req.System)
	if errors.Is(err, store.ErrNotFound) {
		return api.Credential{}, rpc.NotFound("no %s credential for %s", req.System, req.Owner)
	}
	if err != nil {
		return api.Credential{}, err
	}
	a, err := s.aead()
	if err != nil {
		return api.Credential{}, err
	}
	n := a.NonceSize()
	if len(c.ValueEnc) < n {
		return api.Credential{}, errors.New("credential is damaged")
	}
	v, err := a.Open(nil, c.ValueEnc[:n], c.ValueEnc[n:], []byte(c.Owner+"\n"+c.System))
	if err != nil {
		return api.Credential{}, errors.New("credential does not decrypt")
	}
	value := string(v)
	return api.Credential{Owner: c.Owner, System: c.System, Label: c.Label, Revision: uint64(c.Revision), ExpiresMs: uint64(c.ExpiresMs), UpdatedMs: uint64(c.UpdatedMs), Value: &value}, nil
}

func (s *Service) ListCredentials(ctx context.Context, req api.ListCredentialsRequest) (api.ListCredentialsResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListCredentialsResponse{}, err
	}
	who, err := owner(p, req.Owner, ScopePresent, ScopeActAs, ScopeAdmin)
	if err != nil {
		return api.ListCredentialsResponse{}, err
	}
	rows, err := s.DB.Credentials(ctx, who)
	if err != nil {
		return api.ListCredentialsResponse{}, err
	}
	out := api.ListCredentialsResponse{Credentials: []api.Credential{}}
	for _, c := range rows {
		out.Credentials = append(out.Credentials, api.Credential{Owner: c.Owner, System: c.System, Label: c.Label, Revision: uint64(c.Revision), ExpiresMs: uint64(c.ExpiresMs), UpdatedMs: uint64(c.UpdatedMs)})
	}
	return out, nil
}

func (s *Service) ForgetCredential(ctx context.Context, req api.CredentialRef) (api.EmptyResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.EmptyResponse{}, err
	}
	who, err := owner(p, &req.Owner, ScopeActAs, ScopeAdmin)
	if err != nil {
		return api.EmptyResponse{}, err
	}
	if err := s.DB.DeleteCredential(ctx, who, req.System); errors.Is(err, store.ErrNotFound) {
		return api.EmptyResponse{}, rpc.NotFound("no such credential")
	} else if err != nil {
		return api.EmptyResponse{}, err
	}
	return api.EmptyResponse{}, nil
}
