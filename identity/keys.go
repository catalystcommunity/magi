// Package identity resolves LinkKeys application keys for independent services.
package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	localrp "github.com/catalystcommunity/linkkeys/sdks/local-rp/go"
	localwire "github.com/catalystcommunity/linkkeys/sdks/local-rp/go/generated"
	regularrp "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
	w "github.com/catalystcommunity/magi/generated/go"
	"net"
	"regexp"
	"strings"
	"time"
)

var ErrDenied = errors.New("application authentication denied")
var ErrUnavailable = errors.New("identity evidence unavailable")
var name = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)
var domain = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)+$`)

func validApplication(a w.ApplicationRef) bool {
	return len(a.Identity.Domain) <= 253 && domain.MatchString(a.Identity.Domain) && name.MatchString(a.Identity.AccountId) && name.MatchString(a.ApplicationId) && name.MatchString(a.InstanceId)
}

type VerifiedKey struct {
	Public               ed25519.PublicKey
	CreatedAt, ExpiresAt time.Time
}
type KeyResolver interface {
	Resolve(context.Context, w.ApplicationRef, string) (VerifiedKey, error)
}

// DomainTrust is independent of the RP key response. Only configured domains
// can cause network requests. Endpoints and pins come from the administrator.
type DomainTrust struct {
	Domain       string   `json:"domain"`
	TCPAddress   string   `json:"tcp_address"`
	Fingerprints []string `json:"fingerprints"`
}
type trustedDNS []DomainTrust

func (d trustedDNS) TxtLookup(name string) ([]string, error) {
	for _, v := range d {
		if name == "_linkkeys."+v.Domain {
			return []string{"v=lk1 fp=" + strings.Join(v.Fingerprints, " fp=")}, nil
		}
		if name == "_linkkeys_apis."+v.Domain {
			return []string{"v=lk1 tcp=" + v.TCPAddress}, nil
		}
	}
	return nil, ErrDenied
}

type contextTransport struct{ ctx context.Context }
type cancelConn struct {
	net.Conn
	stop func() bool
}

func (c *cancelConn) Close() error { c.stop(); return c.Conn.Close() }
func (t contextTransport) Dial(address string) (net.Conn, error) {
	var d net.Dialer
	c, e := d.DialContext(t.ctx, "tcp", address)
	if e != nil {
		return nil, e
	}
	return &cancelConn{Conn: c, stop: context.AfterFunc(t.ctx, func() { c.Close() })}, nil
}

type LinkKeysResolver struct {
	RP      *lk.RpClient
	Domains []DomainTrust
}

func (r *LinkKeysResolver) Resolve(ctx context.Context, a w.ApplicationRef, keyID string) (VerifiedKey, error) {
	var empty VerifiedKey
	if r.RP == nil || !validApplication(a) {
		return empty, ErrDenied
	}
	if _, e := (trustedDNS(r.Domains)).TxtLookup("_linkkeys." + a.Identity.Domain); e != nil {
		return empty, ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	anchors, e := localrp.FetchDomainKeys(contextTransport{ctx}, trustedDNS(r.Domains), a.Identity.Domain)
	if e != nil {
		return empty, ErrUnavailable
	}
	// The SDKs use separate generated Go types for the same LinkKeys records.
	domainKeys := make([]lk.DomainPublicKey, 0, len(anchors))
	for _, k := range anchors {
		v, e := lk.DecodeDomainPublicKey(localwire.EncodeDomainPublicKey(k))
		if e != nil {
			return empty, ErrDenied
		}
		domainKeys = append(domainKeys, v)
	}
	age := int64(1)
	v, e := r.RP.ResolveApplicationKeys(ctx, lk.RpResolveApplicationKeysRequest{SubjectUserId: a.Identity.AccountId, SubjectDomain: a.Identity.Domain, ApplicationId: a.ApplicationId, InstanceId: a.InstanceId, MaxCacheAgeSeconds: &age})
	if e != nil {
		return empty, ErrUnavailable
	}
	if v.SubjectUserId != a.Identity.AccountId || v.SubjectDomain != a.Identity.Domain || v.ApplicationId != a.ApplicationId || v.InstanceId != a.InstanceId {
		return empty, ErrDenied
	}
	if v.CacheStatus != "fresh" && v.CacheStatus != "refreshed" {
		return empty, ErrUnavailable
	}
	at := time.Now()
	for _, text := range []string{v.FetchedAt, v.RevocationsCheckedAt} {
		t, e := time.Parse(time.RFC3339Nano, text)
		if e != nil || t.After(at) || at.Sub(t) > 5*time.Second {
			return empty, ErrUnavailable
		}
	}
	set := regularrp.VerifyApplicationKeySet(v.ApplicationKeys, v.ApplicationKeyRevocations, domainKeys, regularrp.InstanceRef{SubjectUserID: a.Identity.AccountId, SubjectDomain: a.Identity.Domain, ApplicationID: a.ApplicationId, InstanceID: a.InstanceId}, at, 0)
	if len(set.Rejected) > 0 {
		return empty, ErrDenied
	}
	k, e := set.KeyForUse(keyID, "sign", "ed25519")
	if e != nil {
		return empty, ErrDenied
	}
	created, e := time.Parse(time.RFC3339Nano, k.KeyCreatedAt)
	if e != nil || created.After(at) {
		return empty, ErrDenied
	}
	attested, e := time.Parse(time.RFC3339Nano, k.AttestedAt)
	if e != nil || attested.After(at) {
		return empty, ErrDenied
	}
	expires, e := time.Parse(time.RFC3339Nano, k.KeyExpiresAt)
	if e != nil {
		return empty, ErrDenied
	}
	return VerifiedKey{Public: ed25519.PublicKey(bytes.Clone(k.PublicKey)), CreatedAt: created, ExpiresAt: expires}, nil
}
