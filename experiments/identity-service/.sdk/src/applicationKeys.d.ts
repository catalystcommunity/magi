import type { ApplicationKeyAttestation, ApplicationKeyRevocation, DomainPublicKey, SignedApplicationKeyAttestation } from "../generated/types.gen.ts";
export declare const ATTESTATION_TAG = "linkkeys-application-key-attestation-v1alpha";
export declare const REVOCATION_TAG = "linkkeys-application-key-revocation-v1alpha";
/** Distinct valid sibling signing keys required to revoke a key. */
export declare const REVOCATION_QUORUM = 2;
/** Key use: an Ed25519 signing key. */
export declare const KEY_USAGE_SIGN = "sign";
/** Key use: an X25519 key-agreement key. */
export declare const KEY_USAGE_AGREE = "agree";
/**
 * Bytes a home-domain signature over an attestation covers:
 * `CBOR([ATTESTATION_TAG, attestation_bytes])` — the same two-element
 * envelope every signed structure in this protocol uses (see
 * `application_keys.rs`'s `envelope_signature_input`), except
 * `ApplicationKeyRevocation` (see `revocationPayload` below).
 */
export declare function attestationSignatureInput(attestationBytes: Uint8Array): Uint8Array;
/**
 * Bytes a sibling signature over a revocation covers. Unlike the
 * attestation, a revocation is verified from its FIELDS rather than from
 * stored bytes — the tag-first tuple pattern
 * (`CBOR([REVOCATION_TAG, subject_user_id, subject_domain, application_id,
 * instance_id, target_key_id, target_fingerprint, revoked_at])`, an
 * eight-element array), so a revocation can never move between subjects,
 * applications, or instances.
 */
export declare function revocationPayload(subjectUserId: string, subjectDomain: string, applicationId: string, instanceId: string, targetKeyId: string, targetFingerprint: string, revokedAt: string): Uint8Array;
/**
 * A key's stated use, algorithm, public key length, and fingerprint must be
 * internally consistent before anything trusts them (mirrors
 * `application_keys::check_key_shape`).
 */
export declare function checkKeyShape(keyUsage: string, algorithm: string, publicKey: Uint8Array, fingerprintHex: string): boolean;
/**
 * Verify a home-domain attestation and return what it says, or `undefined`
 * if no currently-valid signing key of `expectedDomain` produced a valid
 * signature over the exact stored bytes (mirrors
 * `application_keys::verify_attestation_signature`).
 *
 * This is a domain assertion, not a peer quorum: the domain's own key set is
 * already pinned via the RP (or DNS, for a DNS-less caller), so one valid
 * signature is the proof. Whether the attestation is CURRENT (not expired)
 * is a separate question — see `classifyKey`.
 */
export declare function verifyAttestationSignature(signed: SignedApplicationKeyAttestation, domainKeys: readonly DomainPublicKey[], expectedDomain: string, now?: Date): ApplicationKeyAttestation | undefined;
/** One application public key as the verifying side knows it, built from an
 * attested record (mirrors `application_keys::ApplicationKeyRef`, the
 * subset this module needs). `attestedKeyRef` leaves `revokedAt` unset;
 * `verifyApplicationKeySet` layers revocation on separately. A caller that
 * already knows a revocation instant (for example, the act-as checks in
 * `actAs.ts`) can set it. */
export interface ApplicationKeyRef {
    keyId: string;
    keyUsage: string;
    algorithm: string;
    publicKey: Uint8Array;
    fingerprint: string;
    createdAt: string;
    expiresAt: string;
    /** When the key stopped being valid. An unparsable value means invalid. */
    revokedAt?: string;
}
/**
 * Count DISTINCT valid application signing keys (excluding the revocation's
 * own target) that signed `rev`'s canonical payload, judged by whether each
 * signer `wasValidAt` the revocation's effective time — a revocation must
 * stay verifiable after its signers themselves later expire or rotate out
 * (mirrors `application_keys::count_distinct_signers` as
 * `verify_revocation` uses it).
 */
export declare function countApplicationKeyRevocationSigners(rev: ApplicationKeyRevocation, keys: readonly ApplicationKeyRef[]): number;
/** Verify a revocation against the instance's attested sibling keys. Requires
 * `REVOCATION_QUORUM` distinct signers; the target never counts. */
export declare function verifyApplicationKeyRevocation(rev: ApplicationKeyRevocation, keys: readonly ApplicationKeyRef[]): boolean;
export type KeyStatus = {
    kind: "usable";
} | {
    kind: "attestation_expired";
} | {
    kind: "key_expired";
} | {
    kind: "revoked";
    revokedAt: string;
};
export interface VerifiedApplicationKey {
    attestation: ApplicationKeyAttestation;
    status: KeyStatus;
}
export interface RejectedRecord {
    what: string;
    reason: string;
}
export interface VerifiedApplicationKeySet {
    keys: VerifiedApplicationKey[];
    revokedKeyIds: Set<string>;
    rejected: RejectedRecord[];
}
/** Decide the status of one verified attestation at `now` (mirrors
 * `application_keys::classify_key`). */
export declare function classifyKey(attestation: ApplicationKeyAttestation, revokedAt: string | undefined, now: Date, skewSeconds: number): KeyStatus;
/**
 * Verify a whole application-key response the way this SDK must before
 * trusting it (mirrors `application_keys::verify_application_key_set`):
 *
 * 1. Verify every attestation against the home domain's pinned key set.
 * 2. Verify every revocation against those attested SIBLING keys.
 * 3. Classify each key against the accepted revocations and `now`.
 *
 * A bad record is rejected individually (recorded in `rejected`, with a
 * reason) rather than failing the whole set — a caller that needs
 * completeness checks `rejected.length === 0`.
 */
export declare function verifyApplicationKeySet(signedAttestations: readonly SignedApplicationKeyAttestation[], revocations: readonly ApplicationKeyRevocation[], domainKeys: readonly DomainPublicKey[], instance: {
    subjectUserId: string;
    subjectDomain: string;
    applicationId: string;
    instanceId: string;
}, now: Date, skewSeconds: number): VerifiedApplicationKeySet;
/** Every currently usable key of one use. All such keys are equal — there is
 * no preferred key, and array order carries no meaning. */
export declare function usableKeys(set: VerifiedApplicationKeySet, keyUsage: string): ApplicationKeyAttestation[];
/** Look up one key by id and confirm it is acceptable for a specific
 * operation — fails closed on an unknown, expired, unattested, mismatched,
 * or revoked key. */
export declare function keyForUse(set: VerifiedApplicationKeySet, keyId: string, keyUsage: string, algorithm: string): ApplicationKeyAttestation | undefined;
