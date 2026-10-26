import type { ActAsCredential, ActAsGrant, ActAsGrantRequest, ActAsGrantRevocation, ActAsRefreshRequest, ActAsScopeSet, ApplicationRef, Claim, DomainPublicKey, GranteeProof, GranteeRef, LocalRpDescriptor, SignedActAsGrant, SignedActAsGrantRequest, SignedActAsGrantRevocation, SignedActAsRefreshRequest, SignedActAsScopeSet, SignedLocalRpDescriptor } from "../generated/types.gen.ts";
import { type ApplicationKeyRef, type VerifiedApplicationKeySet } from "./applicationKeys.ts";
/** The audience's signature over the scope set it offers one grantee. */
export declare const ACT_AS_SCOPE_SET_TAG = "linkkeys-act-as-scope-set-v1alpha";
/** The home domain's signature over a grant. */
export declare const ACT_AS_GRANT_TAG = "linkkeys-act-as-grant-v1alpha";
/** The grantee's signature over a grant request. */
export declare const ACT_AS_GRANT_REQUEST_TAG = "linkkeys-act-as-grant-request-v1alpha";
/** The grantee's signature over a refresh request. */
export declare const ACT_AS_REFRESH_REQUEST_TAG = "linkkeys-act-as-refresh-request-v1alpha";
/** The grantee's signature over one presentation to the audience. */
export declare const ACT_AS_PRESENTATION_TAG = "linkkeys-act-as-presentation-v1alpha";
/** The home domain's signature over a grant revocation. */
export declare const ACT_AS_GRANT_REVOCATION_TAG = "linkkeys-act-as-grant-revocation-v1alpha";
/** A local RP's signature over its own descriptor (`local_rp.rs`). */
export declare const LOCAL_RP_DESCRIPTOR_TAG = "linkkeys-local-rp-descriptor-v1alpha";
/** Default grant lifetime: one hour. */
export declare const ACT_AS_DEFAULT_LIFETIME_SECONDS = 3600;
/** Default largest lifetime a user can choose: one day. */
export declare const ACT_AS_DEFAULT_MAX_LIFETIME_SECONDS = 86400;
/** Default renewal window: no renewal. */
export declare const ACT_AS_DEFAULT_RENEWAL_WINDOW_SECONDS = 0;
/** Default largest renewal window a user can choose: 30 days. */
export declare const ACT_AS_DEFAULT_MAX_RENEWAL_WINDOW_SECONDS: number;
/** Most grant ids one revocation read accepts. */
export declare const ACT_AS_MAX_REVOCATION_LOOKUP_IDS = 100;
/** Most entries one scope set can carry. */
export declare const ACT_AS_MAX_SCOPE_ENTRIES = 64;
/** Longest scope string, in UTF-8 bytes. */
export declare const ACT_AS_MAX_SCOPE_BYTES = 256;
/** Longest scope description, in UTF-8 bytes. */
export declare const ACT_AS_MAX_DESCRIPTION_BYTES = 1024;
/**
 * Largest encoded credential `decodeActAsCredential` accepts. A full scope
 * set (64 entries at the byte limits) is about 82 KiB, and a credential
 * carries it once inside the grant, so 256 KiB leaves room without letting a
 * caller hand the decoder an unbounded buffer.
 */
export declare const ACT_AS_MAX_CREDENTIAL_BYTES: number;
export type ActAsErrorCode = "decode" | "bad-timestamp" | "malformed-grantee" | "proof-does-not-match-grantee" | "untrusted-signer" | "bad-signature" | "no-valid-signature" | "bad-handle-claim" | "mismatch" | "bad-scope-set" | "bad-approved-scope" | "request-expired" | "scope-set-expired" | "grant-expired" | "grant-revoked" | "bad-terms" | "device-binding-unsupported";
export declare class ActAsError extends Error {
    readonly code: ActAsErrorCode;
    constructor(code: ActAsErrorCode, detail?: string);
}
/** Parse a strict RFC3339 timestamp to epoch milliseconds, or undefined. */
export declare function parseRfc3339(value: string): number | undefined;
/** Whole-second RFC3339 in UTC with a trailing `Z`, so a timestamp survives storage unchanged. */
export declare function formatActAsTime(t: Date | number): string;
/** `CBOR([tag, payload_bytes])`: the bytes every act-as signature covers. */
export declare function actAsSignatureInput(tag: string, payload: Uint8Array): Uint8Array;
/** SHA-256 of a grant's signed bytes. A presentation binds this value. */
export declare function grantHash(grantBytes: Uint8Array): Uint8Array;
/** Two application references name the same application. */
export declare function sameApplication(a: ApplicationRef, b: ApplicationRef): boolean;
/** Two grantee references name the same grantee. */
export declare function sameGrantee(a: GranteeRef, b: GranteeRef): boolean;
/** A GranteeRef must carry exactly one non-empty form. */
export declare function checkGrantee(grantee: GranteeRef): void;
/**
 * Every attestation-verified key of a set, INCLUDING keys that expired or were
 * revoked since, each with its `revokedAt` (the reference server's
 * `attested_key_refs`). The verifier's validity rule decides which key can
 * vouch for a signature.
 *
 * Use it for `verifyScopeSet` and `AudienceContext.ownScopeSetKeys`: a scope
 * set is checked against the keys that were valid when it was issued, so a
 * list of usable keys only refuses sets that were signed before a rotation.
 */
export declare function attestedKeyRefs(set: VerifiedApplicationKeySet): ApplicationKeyRef[];
/**
 * The usable keys of a verified application-key set, in the form the act-as
 * verifiers take (the reference server's `usable_key_refs`). Use it on the
 * result of `resolveApplicationKeys` for the grantee instance that signed a
 * request or a presentation. Do NOT use it for a scope set: use
 * `attestedKeyRefs`.
 */
export declare function usableKeyRefs(set: VerifiedApplicationKeySet): ApplicationKeyRef[];
/** An application's own Ed25519 signing key (an attested key of one instance). */
export interface ApplicationKeySigner {
    keyId: string;
    /** Raw 32-byte Ed25519 seed. */
    privateKey: Uint8Array;
}
/** A grantee's signing material, as the grantee holds it. */
export type GranteeSigner = {
    kind: "application";
    /** The enrolled instance whose attested key signs. */
    instanceId: string;
    signer: ApplicationKeySigner;
} | {
    kind: "localRp";
    descriptor: SignedLocalRpDescriptor;
    /** SHA-256 hex of the descriptor signing key. */
    fingerprint: string;
    /** Raw 32-byte Ed25519 seed of the descriptor signing key. */
    signingPrivateKey: Uint8Array;
};
/** Sign `message` with a grantee key and wrap the signature in a proof. */
export declare function granteeProve(signer: GranteeSigner, message: Uint8Array): GranteeProof;
/**
 * Verify a local RP's self-signed descriptor (port of
 * `local_rp::verify_local_rp_descriptor`): both keys are 32 bytes, the
 * fingerprint is SHA-256 hex of the signing key, the signing key signed
 * `CBOR([LOCAL_RP_DESCRIPTOR_TAG, descriptor_bytes])`, and `now` is inside
 * the validity window, with `skewSeconds` on each side.
 */
export declare function verifyLocalRpDescriptor(signed: SignedLocalRpDescriptor, now: Date, skewSeconds: number): LocalRpDescriptor;
/** Who signed a proof, once the proof is verified. */
export interface VerifiedGranteeSigner {
    /** The application instance, for an application grantee. */
    instanceId?: string;
    /** The key id or descriptor fingerprint that signed. */
    keyId: string;
}
/**
 * Verify that a grantee key signed `message`.
 *
 * For an application grantee, `instanceKeys` are the attested keys of the
 * instance that the proof names (`proof.applicationInstanceId`). The caller
 * MUST have verified those attestations against the grantee's home domain,
 * for the grantee's `ApplicationRef`. The signing key must be a signing key
 * that is valid at `now`.
 *
 * For a local-RP grantee, `instanceKeys` is ignored. The descriptor in the
 * proof must verify, its fingerprint must equal the grantee's, and its
 * signing key must have signed.
 */
export declare function verifyGranteeProof(proof: GranteeProof, message: Uint8Array, grantee: GranteeRef, instanceKeys: readonly ApplicationKeyRef[], now: Date, skewSeconds: number): VerifiedGranteeSigner;
/**
 * How a verifier treats a signature whose key was revoked after it signed.
 * Revocation invalidates one key, never the others. Each party chooses:
 *
 * - `"acceptBeforeRevocation"` (the default, invariant I-7): a signature made
 *   before the key's revocation time stays valid.
 * - `"refuseRevoked"`: any signature by a key that is now revoked is refused.
 */
export type RevokedKeyPolicy = "acceptBeforeRevocation" | "refuseRevoked";
/** The default revoked-key policy. */
export declare const DEFAULT_REVOKED_KEY_POLICY: RevokedKeyPolicy;
/**
 * The clock skew allowed between a signer and the home domain that recorded
 * its key's creation time. Mirrors `liblinkkeys::act_as::KEY_CLOCK_SKEW_SECONDS`.
 */
export declare const ACT_AS_KEY_CLOCK_SKEW_SECONDS = 300;
/** Check a scope set's shape: bounded, non-empty, no repeated scope, RFC3339 times. */
export declare function checkScopeSetShape(set: ActAsScopeSet): void;
/**
 * The audience signs the scope set it offers one grantee, with every current
 * signing key of the instance. A verifier needs one signature from a valid
 * key, so one expired or revoked key does not break the set. `signers` must
 * be non-empty, with no key id listed twice.
 */
export declare function signScopeSet(set: ActAsScopeSet, signerInstanceId: string, signers: readonly ApplicationKeySigner[]): SignedActAsScopeSet;
/**
 * Verify a scope set's signatures and shape.
 *
 * `audienceKeys` are the attested keys of the instance that
 * `signed.signerInstanceId` names, INCLUDING keys that expired or were
 * revoked since, each with its `revokedAt` (use `attestedKeyRefs`, not
 * `usableKeyRefs`). The caller MUST have verified them for
 * `expectedAudience`. One signature by a key that was valid at the set's
 * `issued_at`, and that `policy` accepts, is enough. A later key rotation
 * does not invalidate a set that a user already approved.
 *
 * When no signature is acceptable, this throws `no-valid-signature`. The
 * message names each signing key and why it was refused.
 *
 * This does not check the set's expiry. Only approval does that, with
 * `checkScopeSetCurrent`.
 */
export declare function verifyScopeSet(signed: SignedActAsScopeSet, expectedAudience: ApplicationRef, audienceKeys: readonly ApplicationKeyRef[], policy?: RevokedKeyPolicy): ActAsScopeSet;
/** The claim type a handle claim carries. */
export declare const ACT_AS_HANDLE_CLAIM_TYPE = "handle";
/**
 * Verify a handle claim about `party`'s enrolling account and return the
 * handle. Only signatures by the party's own `subjectDomain` count: a handle
 * is that domain's statement. Signatures by other domains are ignored.
 * `domainKeys` are that domain's signing keys. The claim must also not be
 * revoked or expired at `now`.
 *
 * A claim that does not verify is not shown. It does not block consent.
 */
export declare function verifyHandleClaim(claim: Claim, party: ApplicationRef, domainKeys: readonly DomainPublicKey[], now?: Date): string;
/** At approval only: the scope set has not expired. */
export declare function checkScopeSetCurrent(set: ActAsScopeSet, now: Date, skewSeconds: number): void;
/** The approved scope is a non-empty subset of the set, with no repeats. */
export declare function checkApprovedScope(set: ActAsScopeSet, approved: readonly string[]): void;
/**
 * The grantee signs a grant request. To show your handle on the consent
 * screen, set `granteeHandleClaim` to a signed `handle` claim about the
 * account that enrolled your application, signed by that account's domain.
 * A local-RP grantee cannot carry a handle claim.
 */
export declare function signGrantRequest(request: ActAsGrantRequest, signer: GranteeSigner): SignedActAsGrantRequest;
/** Decode a grant request without verifying it. Never trust the result before `verifyGrantRequest`. */
export declare function decodeGrantRequest(signed: SignedActAsGrantRequest): ActAsGrantRequest;
/**
 * Verify a grant request's signature, window, and shape. This is the home
 * domain's check; a grantee can use it to self-test. The caller still owns
 * the scope-set signature, the scope set's expiry, and nonce single-use.
 */
export declare function verifyGrantRequest(signed: SignedActAsGrantRequest, granteeInstanceKeys: readonly ApplicationKeyRef[], now: Date, skewSeconds: number): ActAsGrantRequest;
/** The home domain's bounds for one grant. */
export interface DomainTermBounds {
    defaultLifetimeSeconds: number;
    maxLifetimeSeconds: number;
    maxRenewalWindowSeconds: number;
}
/** The reference server's default bounds. */
export declare const DEFAULT_DOMAIN_TERM_BOUNDS: Readonly<DomainTermBounds>;
/** What the consent screen offers: starting values and the most the user can choose. */
export interface OfferedTerms {
    defaultLifetimeSeconds: number;
    maxLifetimeSeconds: number;
    defaultRenewalWindowSeconds: number;
    maxRenewalWindowSeconds: number;
}
/**
 * The consent screen's starting values and limits. The grantee's request is
 * a ceiling, never a floor. An absent requested lifetime sets no ceiling, and
 * the screen starts at the domain default. An absent renewal window means 0.
 */
export declare function offeredTerms(requestedLifetimeSeconds: number | undefined, requestedRenewalWindowSeconds: number | undefined, bounds?: DomainTermBounds): OfferedTerms;
/**
 * The issued lifetime and renewal window: the user's choice, never above the
 * offered maximum. A choice above the maximum is an error, not a silent cap.
 */
export declare function issuedTerms(offered: OfferedTerms, chosenLifetimeSeconds: number, chosenRenewalWindowSeconds: number): {
    lifetimeSeconds: number;
    renewalWindowSeconds: number;
};
/** Decode a grant without verifying it. */
export declare function decodeGrant(signed: SignedActAsGrant): ActAsGrant;
/**
 * Verify a grant's home-domain signature and internal consistency.
 * `domainKeys` are the keys of the grant's `subject_domain`. This does not
 * check expiry, revocation, the audience, or the scope set. `verifyCredential`
 * does all of those. A grant with a device binding is refused: device keys
 * are Reserved, so no verifier can check one yet.
 */
export declare function verifyGrantSignature(signed: SignedActAsGrant, domainKeys: readonly DomainPublicKey[], now?: Date): ActAsGrant;
/** What a refresh returns: the stored grant, or a renewed grant with these times. */
export type RefreshDecision = {
    kind: "stored";
} | {
    kind: "renew";
    issuedAt: string;
    expiresAt: string;
};
/**
 * Decide whether a refresh renews the grant (the home domain's rule). A
 * grantee uses it to predict what a refresh returns and when to call.
 *
 * An expired grant cannot be renewed (`grant-expired`). While more than one
 * half of the life remains, the stored grant is returned. A renewal happens
 * only before `renewable_until`, and only when it extends the expiry. A
 * renewed grant expires at `min(now + lifetime, renewable_until)`.
 */
export declare function refreshDecision(grant: Pick<ActAsGrant, "issuedAt" | "expiresAt" | "renewableUntil">, lifetimeSeconds: number, now: Date): RefreshDecision;
/**
 * The earliest time a refresh can renew the grant: the half-life point. A
 * grantee SHOULD refresh after this time and before `expires_at`.
 */
export declare function refreshDueAt(grant: Pick<ActAsGrant, "issuedAt" | "expiresAt">): Date;
/** The grantee signs a refresh request. */
export declare function signRefreshRequest(request: ActAsRefreshRequest, signer: GranteeSigner): SignedActAsRefreshRequest;
/** Decode a refresh request without verifying it. */
export declare function decodeRefreshRequest(signed: SignedActAsRefreshRequest): ActAsRefreshRequest;
/** Verify a refresh request against the stored grant's grantee (the home domain's check). */
export declare function verifyRefreshRequest(signed: SignedActAsRefreshRequest, grantGrantee: GranteeRef, granteeInstanceKeys: readonly ApplicationKeyRef[], now: Date, skewSeconds: number): ActAsRefreshRequest;
/**
 * The grantee builds the credential for one call to the audience.
 * `presentedAt` is written as whole-second RFC3339 UTC. `nonce` must be
 * unique per presentation; the audience uses it for replay protection.
 */
export declare function present(grant: SignedActAsGrant, audience: ApplicationRef, requestDigest: Uint8Array, presentedAt: Date, nonce: Uint8Array, signer: GranteeSigner): ActAsCredential;
/** Decode a credential received from a grantee, with a size bound. */
export declare function decodeActAsCredential(encoded: Uint8Array, maxBytes?: number): ActAsCredential;
/**
 * What the audience supplies to verify a credential. Every key list is
 * already verified by the caller: domain keys from the issuer's anchor, and
 * application keys from verified attestations.
 */
export interface AudienceContext {
    /** The verifier's own application. Never read from the request. */
    ownApplication: ApplicationRef;
    /** The verifier's own attested keys, for the instance that signed the
     * scope set, INCLUDING keys that expired or were revoked since (use
     * `attestedKeyRefs`, not `usableKeyRefs`). Find the instance with
     * `credentialScopeSetSigner`. */
    ownScopeSetKeys: readonly ApplicationKeyRef[];
    /** Signing keys of the grant's `subject_domain`. */
    issuerDomainKeys: readonly DomainPublicKey[];
    /** Attested keys of the grantee instance that signed the presentation.
     * Find the instance with `credentialSignerInstance`. Ignored for a
     * local-RP grantee. */
    granteeInstanceKeys: readonly ApplicationKeyRef[];
    /** The digest of this request, as the audience's protocol defines it. */
    expectedRequestDigest: Uint8Array;
    /** Grant ids the verifier holds a valid revocation for. */
    revokedGrantIds: readonly string[];
    /** Largest accepted age of a presentation, in seconds. */
    maxPresentationAgeSeconds: number;
    now: Date;
    skewSeconds: number;
    /** How to treat scope-set signatures by keys revoked since. Default:
     * `"acceptBeforeRevocation"`. */
    revokedKeyPolicy?: RevokedKeyPolicy;
}
/** A credential the audience accepted. */
export interface VerifiedActAs {
    grantId: string;
    /** `user_id@subject_domain`. */
    userId: string;
    subjectDomain: string;
    grantee: GranteeRef;
    signer: VerifiedGranteeSigner;
    /** The only scope the audience's policy may act on. */
    approvedScope: string[];
    expiresAt: string;
    /** The presentation nonce. The audience owns replay protection. */
    nonce: Uint8Array;
}
/** The grantee instance whose key signed the presentation, or undefined for a local-RP grantee. */
export declare function credentialSignerInstance(credential: ActAsCredential): string | undefined;
/** The audience instance whose key signed the embedded scope set. */
export declare function credentialScopeSetSigner(credential: ActAsCredential): string;
/**
 * The audience's verification checklist, in this order. Every step must pass:
 *
 * 1. The grant's home-domain signature verifies.
 * 2. The grant's audience is the verifier.
 * 3. The grant is inside its validity window.
 * 4. The presentation was signed by a key of the grantee.
 * 5. The presentation binds this grant, this audience, and this request,
 *    and is fresh.
 * 6. The embedded scope set was signed by the verifier, names the same
 *    grantee, and contains every approved scope. Its expiry is NOT checked.
 * 7. The verifier holds no revocation for the grant.
 *
 * Throws `ActAsError` on the first failed step. The caller still owns replay
 * protection: record `nonce` until the accepted presentation age passes.
 */
export declare function verifyCredential(credential: ActAsCredential, ctx: AudienceContext): VerifiedActAs;
/** Verify a grant revocation against the issuing domain's keys. */
export declare function verifyGrantRevocation(signed: SignedActAsGrantRevocation, domainKeys: readonly DomainPublicKey[], expectedDomain: string, now?: Date): ActAsGrantRevocation;
/** The grant ids that a set of revocations validly revokes. Records that fail verification are ignored. */
export declare function revokedGrantIds(revocations: readonly SignedActAsGrantRevocation[], domainKeys: readonly DomainPublicKey[], expectedDomain: string, now?: Date): string[];
