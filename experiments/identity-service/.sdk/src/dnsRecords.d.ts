import type { DomainPublicKey } from "../generated/types.gen.ts";
/** Default TCP port for the LinkKeys protocol service. */
export declare const DEFAULT_TCP_PORT = 4987;
/** Maximum length of a single DNS TXT character-string (RFC 1035). */
export declare const MAX_TXT_STRING_LEN = 255;
export interface LinkKeysRecord {
    fingerprints: string[];
}
export interface LinkKeysApis {
    /** `host:port` for the TCP service, with the default port filled in. */
    tcp?: string;
    /** Full `https://host[:port][/path]` base for the HTTPS API. */
    httpsBase?: string;
}
export type DnsParseErrorCode = "no-linkkeys-record" | "missing-version" | "unsupported-version" | "missing-apis-endpoint" | "invalid-format";
export declare class DnsParseError extends Error {
    readonly code: DnsParseErrorCode;
    constructor(code: DnsParseErrorCode, message?: string);
}
export declare function linkkeysDnsName(domain: string): string;
export declare function linkkeysApisDnsName(domain: string): string;
/** Parse a single `_linkkeys` TXT record string. Errors if it isn't a LinkKeys v1 record. */
export declare function parseLinkkeysTxt(txt: string): LinkKeysRecord;
/** Parse a single `_linkkeys_apis` TXT record string. Errors if it isn't a LinkKeys v1 record or carries no endpoint. */
export declare function parseLinkkeysApisTxt(txt: string): LinkKeysApis;
/** True if `fp` is a syntactically valid key fingerprint: 64 hex chars (a SHA-256 digest). Case-insensitive. */
export declare function isValidFingerprint(fp: string): boolean;
/**
 * Pin fetched keys to the DNS-published fingerprint set. Recomputes
 * `fingerprint(publicKey)` for each candidate (never trusting the wire
 * `fingerprint` field, which is attacker-controlled) and keeps only keys
 * whose recomputed fingerprint is a member of `pinned`.
 */
export declare function pinKeysToFingerprints(keys: readonly DomainPublicKey[], pinned: readonly string[]): DomainPublicKey[];
/** Canonical bytes a signing key signs to vouch for an encryption key. */
export declare function keyVouchPayload(encFingerprint: string, encExpiresAt: string): Uint8Array;
/** Verify that `signingKey` vouches for `encKey`. The wire `fingerprint` field is never trusted — it is recomputed. */
export declare function verifyKeyVouch(encKey: DomainPublicKey, signingKey: DomainPublicKey, now?: Date): boolean;
/**
 * Establish the trusted key set from a fetched key list and the DNS-pinned
 * fingerprint set: signing keys are pinned directly; encryption keys are
 * trusted only when a pinned signing key vouches for them. An empty result
 * means "no trustworthy keys" — callers MUST fail closed.
 */
export declare function trustKeys(keys: readonly DomainPublicKey[], pinned: readonly string[], now?: Date): DomainPublicKey[];
