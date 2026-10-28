package linkkeys

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claim signature checks for claims that the subject's own domain signs.
// Ported from linkkeys/sdks/regular-rp/go claims.go and domainkeys.go
// (liblinkkeys claims.rs and revocation.rs).

const (
	claimPayloadTag  = "linkkeys-claim-v1alpha"
	revocationTag    = "linkkeys-key-revocation-v1alpha"
	revocationQuorum = 2
	keyUsageSign     = "sign"
	algorithmEd25519 = "ed25519"
)

// DomainKeys returns the signing keys of domain. The RP server supplies the
// keys and revocation certificates. A key is kept only when its recomputed
// fingerprint is in the DNS record _linkkeys.<domain>, so the RP response is
// not its own trust anchor. Quorum-signed revocations are applied.
func (c *Client) DomainKeys(ctx context.Context, domain string) ([]DomainPublicKey, error) {
	pins, err := c.dnsFingerprints(ctx, domain)
	if err != nil {
		return nil, err
	}
	resp, err := c.RP.ResolveDomainKeys(ctx, RpResolveDomainKeysRequest{Domain: domain})
	if err != nil {
		return nil, fmt.Errorf("linkkeys resolve-domain-keys: %w", err)
	}
	if resp.Domain != domain || resp.CacheStatus == "stale" {
		return nil, fail("unauthorized-claim", "domain keys for %s are not current", domain)
	}
	var keys []DomainPublicKey
	for _, k := range resp.Keys {
		if pins[Fingerprint(k.PublicKey)] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, fail("unauthorized-claim", "no domain key of %s matches DNS", domain)
	}
	return applyRevocations(keys, resp.Revocations, domain), nil
}

func (c *Client) dnsFingerprints(ctx context.Context, domain string) (map[string]bool, error) {
	txts, err := c.dns().LookupTXT(ctx, "_linkkeys."+domain)
	if err != nil {
		return nil, fmt.Errorf("linkkeys dns _linkkeys.%s: %w", domain, err)
	}
	pins := map[string]bool{}
	for _, txt := range txts {
		f := strings.Fields(txt)
		if !hasField(f, "v=lk1") {
			continue
		}
		for _, p := range f {
			if v, ok := strings.CutPrefix(p, "fp="); ok && len(v) == 64 {
				pins[strings.ToLower(v)] = true
			}
		}
	}
	if len(pins) == 0 {
		return nil, fail("unauthorized-claim", "no fingerprints in _linkkeys.%s", domain)
	}
	return pins, nil
}

// ClaimPayload is the byte string that one claim signature covers.
func ClaimPayload(cl Claim, subjectDomain, signingDomain string) []byte {
	var expires cborValue = cborNull{}
	if cl.ExpiresAt != nil {
		expires = cborText(*cl.ExpiresAt)
	}
	return cborEncode(cborArray{
		cborText(claimPayloadTag), cborText(cl.ClaimId), cborText(cl.ClaimType), cborBytes(cl.ClaimValue),
		cborText(cl.UserId + "@" + subjectDomain), cborText(signingDomain), expires, cborText(cl.AttestedAt),
	})
}

// RevocationPayload is the byte string that one revocation signature covers.
func RevocationPayload(keyID, fingerprint, revokedAt, signingDomain string) []byte {
	return cborEncode(cborArray{cborText(revocationTag), cborText(keyID), cborText(fingerprint), cborText(revokedAt), cborText(signingDomain)})
}

func findKey(keys []DomainPublicKey, id string) *DomainPublicKey {
	for i := range keys {
		if keys[i].KeyId == id {
			return &keys[i]
		}
	}
	return nil
}

func verifySig(k *DomainPublicKey, msg, sig []byte) bool {
	return k.Algorithm == algorithmEd25519 && len(k.PublicKey) == ed25519.PublicKeySize && ed25519.Verify(k.PublicKey, msg, sig)
}

// keyValidAt: not revoked and not expired at t. Neither is retroactive.
func keyValidAt(k *DomainPublicKey, t time.Time) bool {
	if k.KeyUsage != keyUsageSign {
		return false
	}
	if k.RevokedAt != nil {
		r, err := time.Parse(time.RFC3339, *k.RevokedAt)
		if err != nil || !t.Before(r) {
			return false
		}
	}
	e, err := time.Parse(time.RFC3339, k.ExpiresAt)
	return err == nil && t.Before(e)
}

// verifyDomainClaim accepts one valid signature by domain, made by a key that
// was valid at attested_at. Signatures by other domains are ignored.
func verifyDomainClaim(cl Claim, domain string, keys []DomainPublicKey, now time.Time) error {
	at, err := time.Parse(time.RFC3339, cl.AttestedAt)
	if err != nil {
		return errors.New("claim has an invalid attested_at")
	}
	if at.After(now.Add(time.Minute)) {
		return errors.New("claim attested in the future")
	}
	payload := ClaimPayload(cl, domain, domain)
	// The error names the last reason, so an operator can tell a missing
	// key from a bad signature. A bad signature by a current key usually
	// means that the IDP signed the claim with an older payload format.
	why := fmt.Errorf("no valid signature by %s", domain)
	ok := false
	for _, s := range cl.Signatures {
		if s.Domain != domain {
			continue
		}
		k := findKey(keys, s.SignedByKeyId)
		switch {
		case k == nil:
			why = fmt.Errorf("signing key %s of %s is not in its current key set", s.SignedByKeyId, domain)
		case !keyValidAt(k, at):
			why = fmt.Errorf("signing key %s was not valid at %s", s.SignedByKeyId, cl.AttestedAt)
		case !verifySig(k, payload, s.Signature):
			why = fmt.Errorf("signature by key %s does not match the claim (attested %s)", s.SignedByKeyId, cl.AttestedAt)
		default:
			ok = true
		}
		if ok {
			break
		}
	}
	if !ok {
		return why
	}
	if cl.RevokedAt != nil {
		return errors.New("claim has been revoked")
	}
	if cl.ExpiresAt != nil {
		e, err := time.Parse(time.RFC3339, *cl.ExpiresAt)
		if err != nil || now.After(e) {
			return errors.New("claim has expired")
		}
	}
	return nil
}

// applyRevocations marks keys that a quorum of other current signing keys
// of domain revoked. It does not change its input.
func applyRevocations(keys []DomainPublicKey, certs []RevocationCertificate, domain string) []DomainPublicKey {
	out := append([]DomainPublicKey(nil), keys...)
	now := time.Now()
	for _, cert := range certs {
		signers := map[string]bool{}
		for _, s := range cert.Signatures {
			if s.SignedByKeyId == cert.TargetKeyId || s.Domain != domain {
				continue
			}
			k := findKey(keys, s.SignedByKeyId)
			if k == nil || k.RevokedAt != nil || !keyValidAt(k, now) {
				continue
			}
			if verifySig(k, RevocationPayload(cert.TargetKeyId, cert.TargetFingerprint, cert.RevokedAt, s.Domain), s.Signature) {
				signers[s.SignedByKeyId] = true
			}
		}
		if len(signers) < revocationQuorum {
			continue
		}
		for i := range out {
			if out[i].KeyId == cert.TargetKeyId && out[i].RevokedAt == nil {
				r := cert.RevokedAt
				out[i].RevokedAt = &r
			}
		}
	}
	return out
}
