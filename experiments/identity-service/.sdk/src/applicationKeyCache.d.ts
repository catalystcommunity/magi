import type { DomainPublicKey, RpResolveApplicationKeysRequest, RpResolveApplicationKeysResponse } from "../generated/types.gen.ts";
import { type VerifiedApplicationKeySet } from "./applicationKeys.ts";
import type { RpcCallOptions } from "./rpc.ts";
/**
 * The RP operation this module needs. `RegularRpClient`'s own
 * `PinnedRpOperations`-style wrapper (in `applicationKeyClient.ts`) is the
 * production implementation; tests can supply a fake directly.
 */
export interface ResolveApplicationKeysOperation {
    resolveApplicationKeys(req: RpResolveApplicationKeysRequest, options?: RpcCallOptions): Promise<RpResolveApplicationKeysResponse>;
}
/**
 * How one `resolveApplicationKeys` result was produced. Mirrors the wire
 * `cache_status` values exactly (design doc: `"fresh"`, `"refreshed"`, or
 * `"stale"`); this SDK never invents a fourth state.
 */
export type CacheFreshness = "fresh" | "refreshed" | "stale";
/**
 * The result of `resolveApplicationKeys`. Deliberately NOT a bare key list:
 * `freshness` travels in the same object as `keys`, so a caller cannot
 * destructure only the keys and forget to check it — a `"stale"` result must
 * never be presented as current trust.
 */
export interface ResolvedApplicationKeys {
    keys: VerifiedApplicationKeySet;
    freshness: CacheFreshness;
    fetchedAt: string;
    revocationsCheckedAt: string;
}
export interface InstanceIdentity {
    subjectUserId: string;
    subjectDomain: string;
    applicationId: string;
    instanceId: string;
}
interface CacheEntry {
    request: RpResolveApplicationKeysResponse;
    domainKeys: DomainPublicKey[];
    fetchedAt: number;
}
/** Default bound for `ApplicationKeyClientCache`. Small on purpose: this
 * cache exists only to avoid re-asking the application's OWN RP on every
 * call within a short window, not to hold a large working set (design doc:
 * "Keeps a small bounded client-side cache"). */
export declare const DEFAULT_MAX_ENTRIES = 128;
/** Default local bound on how long to trust the last RP answer before
 * asking again. Short, and unrelated to the RP's OWN (much longer) cache
 * policy — this is purely a same-process call-deduplication window. */
export declare const DEFAULT_LOCAL_CACHE_AGE_MS = 30000;
/**
 * A small, bounded, least-recently-used cache keyed on the canonical
 * instance identity. It can never grow past `maxEntries`: inserting past the
 * bound evicts the least-recently-used entry first.
 */
export declare class ApplicationKeyClientCache {
    private readonly maxEntries;
    private readonly map;
    constructor(maxEntries?: number);
    get(instance: InstanceIdentity): CacheEntry | undefined;
    put(instance: InstanceIdentity, entry: CacheEntry): void;
    get size(): number;
}
export interface ResolveApplicationKeysOptions extends RpcCallOptions {
    /** A STRICTER ceiling than the RP operator's own policy, forwarded as
     * `maxCacheAgeSeconds` on the wire request. A weaker value has no effect —
     * the RP never loosens its own policy (design doc, "RP cache"). */
    maxCacheAgeSeconds?: number;
    /** Override the local dedupe window (`DEFAULT_LOCAL_CACHE_AGE_MS`). */
    localCacheAgeMs?: number;
    now?: () => Date;
}
/**
 * Resolve `instance`'s currently-usable application keys through the RP
 * named by `rp`, verifying the signed records again locally.
 *
 * A local `ApplicationKeyClientCache` hit within `localCacheAgeMs`
 * (default `DEFAULT_LOCAL_CACHE_AGE_MS`) is served WITHOUT an RP call, but
 * is still RE-CLASSIFIED against the current clock — never a replay of a
 * verdict computed at fetch time — so an attestation that has since expired
 * is caught even between RP calls.
 *
 * On a cache miss (or an entry past its local age), this calls
 * `Rp/resolve-application-keys`, then:
 *
 * 1. Applies `homeDomainKeyRevocations` to `homeDomainKeys` (the same
 *    quorum-verified certificate check `fetchDomainKeys` already uses for
 *    domain keys generally — `revocation.ts`'s `verifyRevocationCertificate`).
 * 2. Verifies every `applicationKeys` attestation and `applicationKeyRevocations`
 *    record against those keys with `verifyApplicationKeySet` — this
 *    function adds no verification rule of its own.
 *
 * The RP's own `cacheStatus` is passed straight through: this SDK never
 * upgrades a `"stale"` RP answer to `"fresh"`. If the RP call fails and a
 * local cache entry still exists, the last verified records are returned
 * marked `"stale"` rather than failing outright; with no cache entry to fall
 * back to, the RP error propagates.
 */
export declare function resolveApplicationKeys(rp: ResolveApplicationKeysOperation, cache: ApplicationKeyClientCache, instance: InstanceIdentity, options?: ResolveApplicationKeysOptions): Promise<ResolvedApplicationKeys>;
export {};
