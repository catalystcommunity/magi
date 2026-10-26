import type { Claim, DomainPublicKey } from "../generated/types.gen.ts";
export type ClaimErrorCode = "signature-invalid" | "key-not-found" | "key-revoked" | "key-expired" | "revoked" | "expired" | "bad-expiry" | "unsigned" | "domain-keys-unavailable" | "domain-unverified";
export declare class ClaimError extends Error {
    readonly code: ClaimErrorCode;
    readonly detail?: string;
    constructor(code: ClaimErrorCode, detail?: string);
}
/**
 * Canonical bytes a single signature covers for a claim. The subject is the
 * full identity `user_id@subject_domain` (never a bare user_id), and
 * `signing_domain` is bound per-signature, so a signature from one domain
 * can't be relabeled as another's, and a claim about one subject domain
 * can't be replayed against a different one. `claimValue` is a raw byte
 * string (bstr) in the payload, never text — see `claims.json`'s
 * `claim_non_utf8_binary_value` conformance case. Exported so conformance
 * tests can byte-compare against `claims.json`'s `signed_payload_cbor_hex`.
 */
export declare function claimSignPayload(claimId: string, claimType: string, claimValue: Uint8Array, userId: string, subjectDomain: string, signingDomain: string, expiresAt: string | undefined, attestedAt: string): Uint8Array;
export interface ClaimSpec {
    claimId: string;
    claimType: string;
    claimValue: Uint8Array;
    userId: string;
    subjectDomain: string;
    expiresAt?: string;
    attestedAt: string;
}
export interface ClaimSigner {
    domain: string;
    keyId: string;
    privateKeySeed: Uint8Array;
}
/** Sign a claim with one or more keys, producing a `Claim` carrying one `ClaimSignature` per signer. */
export declare function signClaim(spec: ClaimSpec, signers: readonly ClaimSigner[]): Claim;
/** A domain and the set of its currently-known public keys, as supplied to `verifyClaim`. */
export interface DomainKeySet {
    domain: string;
    keys: readonly DomainPublicKey[];
}
/** Verify only the cryptographic per-domain quorum (not revocation/expiry — see `verifyClaim`). */
export declare function verifyClaimSignatures(claim: Claim, subjectDomain: string, domainKeys: readonly DomainKeySet[]): void;
/** Full claim verification: the cryptographic quorum plus the claim's own revocation and expiry. */
export declare function verifyClaim(claim: Claim, subjectDomain: string, domainKeys: readonly DomainKeySet[], now?: Date): void;
