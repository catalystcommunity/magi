import type { DomainPublicKey, RevocationCertificate } from "../generated/types.gen.ts";
/** Minimum number of distinct sibling signatures required to revoke a key. */
export declare const REVOCATION_QUORUM = 2;
/** Domain-separation tag / version for the signed revocation payload. */
export declare const REVOCATION_TAG = "linkkeys-key-revocation-v1alpha";
/**
 * Canonical bytes one sibling signature covers:
 * `CBOR([tag, target_key_id, target_fingerprint, revoked_at,
 * signing_domain])` — a FIVE-element CBOR array with the domain-separation
 * tag first (the older house tuple pattern; NOT the local-RP envelopes'
 * two-element `CBOR([context, payload])` framing). The signing domain is
 * bound per-signature so a signature can never be replayed for another
 * domain. Exported so conformance tests can byte-compare against
 * `revocations.json`'s `signed_payload_cbor_hex`.
 */
export declare function revocationPayload(targetKeyId: string, targetFingerprint: string, revokedAt: string, signingDomain: string): Uint8Array;
/**
 * Count the DISTINCT signer key ids on `cert` that survive every filtering
 * rule (the conformance vectors' `expected_counted_signers`):
 *
 * 1. Skip any signature whose `signedByKeyId` equals the certificate's
 *    `targetKeyId` (a key never authorizes its own revocation), any whose
 *    wire `domain` field differs from the domain being verified, and any
 *    whose signer key is absent from the fetched key list or is not a
 *    currently-valid signing key (wrong `keyUsage`, expired, or itself
 *    revoked).
 * 2. For the rest, recompute the payload using the signature's **wire**
 *    `domain` field (the `domain` parameter only filters — this is what
 *    stops a signature minted for another domain from being re-labeled) and
 *    verify the Ed25519 signature with the signer's public key.
 * 3. Distinctness is by signer key id.
 */
export declare function countRevocationCertificateSigners(cert: RevocationCertificate, domainKeys: readonly DomainPublicKey[], domain: string, now?: Date): number;
/**
 * Verify a revocation certificate against a domain's public key set.
 * Requires at least `REVOCATION_QUORUM` DISTINCT signing keys of `domain`,
 * each currently valid and NOT the target key, to have signed the canonical
 * payload. Returns `true` when the quorum is met.
 */
export declare function verifyRevocationCertificate(cert: RevocationCertificate, domainKeys: readonly DomainPublicKey[], domain: string, now?: Date): boolean;
