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
)

// errUnsignedTime: LinkKeys signs a time as whole Unix seconds. A time with
// a fraction of a second was never signed, so it is an error and not a
// rounded value (liblinkkeys timestamp::SignedTime).
var errUnsignedTime = errors.New("a time with a fraction of a second cannot be in a signed payload")

func signedTime(t time.Time) (cborValue, error) {
	if t.Nanosecond() != 0 {
		return nil, errUnsignedTime
	}
	return cborInt(t.Unix()), nil
}

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
	if resp.Domain != domain || resp.CacheStatus == RpCacheStatusStale {
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

// ClaimPayload is the byte string that one claim signature covers: the
// array [tag, claim_id, claim_type, claim_value, "user_id@subject_domain",
// signing_domain, expires_at (seconds or null), attested_at (seconds)].
func ClaimPayload(cl Claim, subjectDomain, signingDomain string) ([]byte, error) {
	var expires cborValue = cborNull{}
	if cl.ExpiresAt != nil {
		e, err := signedTime(*cl.ExpiresAt)
		if err != nil {
			return nil, err
		}
		expires = e
	}
	attested, err := signedTime(cl.AttestedAt)
	if err != nil {
		return nil, err
	}
	return cborEncode(cborArray{
		cborText(claimPayloadTag), cborText(cl.ClaimId), cborText(cl.ClaimType), cborBytes(cl.ClaimValue),
		cborText(cl.UserId + "@" + subjectDomain), cborText(signingDomain), expires, attested,
	}), nil
}

// RevocationPayload is the byte string that one revocation signature covers.
func RevocationPayload(keyID, fingerprint string, revokedAt time.Time, signingDomain string) ([]byte, error) {
	at, err := signedTime(revokedAt)
	if err != nil {
		return nil, err
	}
	return cborEncode(cborArray{cborText(revocationTag), cborText(keyID), cborText(fingerprint), at, cborText(signingDomain)}), nil
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
	return k.Algorithm == KeyAlgorithmEd25519 && len(k.PublicKey) == ed25519.PublicKeySize && ed25519.Verify(k.PublicKey, msg, sig)
}

// keyValidAt: not revoked and not expired at t. Neither is retroactive.
func keyValidAt(k *DomainPublicKey, t time.Time) bool {
	if k.KeyUsage != KeyUsageSign {
		return false
	}
	if k.RevokedAt != nil && !t.Before(*k.RevokedAt) {
		return false
	}
	return t.Before(k.ExpiresAt)
}

// verifyDomainClaim accepts one valid signature by domain, made by a key that
// was valid at attested_at. Signatures by other domains are ignored.
func verifyDomainClaim(cl Claim, domain string, keys []DomainPublicKey, now time.Time) error {
	at := cl.AttestedAt
	if at.After(now.Add(time.Minute)) {
		return errors.New("claim attested in the future")
	}
	payload, err := ClaimPayload(cl, domain, domain)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	attested := at.UTC().Format(time.RFC3339)
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
			why = fmt.Errorf("signing key %s was not valid at %s", s.SignedByKeyId, attested)
		case !verifySig(k, payload, s.Signature):
			why = fmt.Errorf("signature by key %s does not match the claim (attested %s)", s.SignedByKeyId, attested)
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
	if cl.ExpiresAt != nil && now.After(*cl.ExpiresAt) {
		return errors.New("claim has expired")
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
			// A revocation time with a fraction of a second was never signed.
			payload, err := RevocationPayload(cert.TargetKeyId, cert.TargetFingerprint, cert.RevokedAt, s.Domain)
			if err == nil && verifySig(k, payload, s.Signature) {
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
