// Package fakelk is a fake LinkKeys domain for tests. It plays the RP server
// over real pinned TLS and the IDP browser step, and signs handle claims
// with a real Ed25519 domain key.
package fakelk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	lk "github.com/catalystcommunity/magi/primitives/identity/linkkeys"
)

// Domain is a fake LinkKeys domain that is also the RP server.
type Domain struct {
	Name    string
	APIKey  string
	Addr    string
	Pins    []string
	key     ed25519.PrivateKey
	keyID   string
	mu      sync.Mutex
	pending map[string]lk.RpSignRequest // nonce -> signed request
	tokens  map[string]lk.IdentityAssertion
	handles map[string]string // user id -> handle
	n       int
	// Tamper changes an assertion before verify-assertion returns it.
	Tamper func(*lk.IdentityAssertion)
	// Unverified makes verify-assertion report verified=false.
	Unverified bool
}

// New starts a fake domain on a loopback TLS listener.
func New(t testing.TB, name string) *Domain {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); l.Close() })
	d := &Domain{Name: name, APIKey: "test-api-key", Addr: l.Addr().String(), Pins: []string{lk.Fingerprint(pub)},
		key: priv, keyID: "dk1", pending: map[string]lk.RpSignRequest{}, tokens: map[string]lk.IdentityAssertion{}, handles: map[string]string{}}
	go lk.Serve(ctx, l, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, d.APIKey, d)
	return d
}

// Runtime returns the RP connection settings for this domain.
func (d *Domain) Runtime() lk.Runtime {
	return lk.Runtime{RPDomain: d.Name, TCPAddress: d.Addr, Fingerprints: d.Pins, APIKey: d.APIKey}
}

// LookupTXT serves _linkkeys and _linkkeys_apis records for the domain.
func (d *Domain) LookupTXT(_ context.Context, name string) ([]string, error) {
	switch name {
	case "_linkkeys." + d.Name:
		return []string{"v=lk1 fp=" + d.Pins[0]}, nil
	case "_linkkeys_apis." + d.Name:
		return []string{"v=lk1 tcp=" + d.Addr + " https=idp." + d.Name + "/lk"}, nil
	}
	return nil, errors.New("no such record")
}

// Authorize plays the IDP: the user authenticates and the IDP redirects to
// the signed callback URL with an encrypted token. It returns that URL.
func (d *Domain) Authorize(idpURL, userID, handle string) (string, error) {
	u, err := url.Parse(idpURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	d.mu.Lock()
	defer d.mu.Unlock()
	req, ok := d.pending[q.Get("nonce")]
	if !ok || q.Get("signed_request") == "" || req.CallbackUrl != q.Get("callback_url") {
		return "", errors.New("unknown signed request")
	}
	delete(d.pending, q.Get("nonce"))
	d.n++
	tok := fmt.Sprintf("enc_%d", d.n)
	now := time.Now().UTC()
	d.tokens[tok] = lk.IdentityAssertion{UserId: userID, Domain: d.Name, Audience: d.Name, Nonce: req.Nonce,
		IssuedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339), AuthorizedClaims: []string{lk.HandleClaim}}
	d.handles[userID] = handle
	cb, _ := url.Parse(req.CallbackUrl)
	cq := cb.Query()
	cq.Set("encrypted_token", tok)
	cb.RawQuery = cq.Encode()
	return cb.String(), nil
}

func (d *Domain) SignRequest(_ context.Context, r lk.RpSignRequest) (lk.RpSignResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending[r.Nonce] = r
	return lk.RpSignResponse{SignedRequest: "sr_" + r.Nonce[:8]}, nil
}

func (d *Domain) DecryptToken(_ context.Context, r lk.RpDecryptRequest) (lk.RpDecryptResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.tokens[r.EncryptedToken]; !ok {
		return lk.RpDecryptResponse{}, errors.New("bad token")
	}
	return lk.RpDecryptResponse{SignedAssertion: "sa_" + r.EncryptedToken}, nil
}

func (d *Domain) assertion(signed string) (lk.IdentityAssertion, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.tokens[signed[3:]]
	return a, ok
}

func (d *Domain) VerifyAssertion(_ context.Context, r lk.RpVerifyRequest) (lk.RpVerifyResponse, error) {
	a, ok := d.assertion(r.SignedAssertion)
	if !ok || a.Domain != r.ExpectedDomain {
		return lk.RpVerifyResponse{Assertion: a}, nil
	}
	if d.Tamper != nil {
		d.Tamper(&a)
	}
	return lk.RpVerifyResponse{Assertion: a, Verified: !d.Unverified}, nil
}

// SignClaim returns a claim signed by the domain key.
func (d *Domain) SignClaim(userID, claimType, value string) lk.Claim {
	now := time.Now().UTC().Format(time.RFC3339)
	c := lk.Claim{ClaimId: "clm_" + userID, UserId: userID, ClaimType: claimType, ClaimValue: []byte(value), AttestedAt: now, CreatedAt: now}
	c.Signatures = []lk.ClaimSignature{{Domain: d.Name, SignedByKeyId: d.keyID, Signature: ed25519.Sign(d.key, lk.ClaimPayload(c, d.Name, d.Name))}}
	return c
}

func (d *Domain) UserinfoFetch(_ context.Context, r lk.RpUserInfoRequest) (lk.UserInfo, error) {
	a, ok := d.assertion(r.Token)
	if !ok {
		return lk.UserInfo{}, errors.New("bad token")
	}
	d.mu.Lock()
	h := d.handles[a.UserId]
	d.mu.Unlock()
	info := lk.UserInfo{UserId: a.UserId, Domain: a.Domain, DisplayName: h, Claims: []lk.Claim{}}
	if h != "" {
		info.Claims = append(info.Claims, d.SignClaim(a.UserId, lk.HandleClaim, h))
	}
	return info, nil
}

func (d *Domain) ResolveDomainKeys(_ context.Context, r lk.RpResolveDomainKeysRequest) (lk.RpResolveDomainKeysResponse, error) {
	now := time.Now().UTC()
	pub := d.key.Public().(ed25519.PublicKey)
	return lk.RpResolveDomainKeysResponse{
		Domain: r.Domain,
		Keys: []lk.DomainPublicKey{{KeyId: d.keyID, PublicKey: pub, Fingerprint: lk.Fingerprint(pub), Algorithm: "ed25519", KeyUsage: "sign",
			CreatedAt: now.Add(-time.Hour).Format(time.RFC3339), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339)}},
		Revocations: []lk.RevocationCertificate{}, FetchedAt: now.Format(time.RFC3339), RevocationsCheckedAt: now.Format(time.RFC3339), CacheStatus: "fresh",
	}, nil
}
