import type { ActAsGrantRevocation, RefreshActAsGrantResponse, SignedActAsGrantRequest, SignedActAsRefreshRequest } from "../generated/types.gen.ts";
import type { DnsResolver } from "./dns.ts";
import type { RpcCallOptions } from "./rpc.ts";
/** The home domain's browser route for an act-as grant request. */
export declare const ACT_AS_ROUTE = "/auth/act-as";
/**
 * The one transport method these helpers need. `PinnedRpcTransport`
 * implements it. Tests can supply a fake.
 */
export interface RpCallTransport {
    callWithOptions(service: string, op: string, req: Uint8Array, options?: RpcCallOptions): Promise<Uint8Array>;
}
export interface ResolveGrantRevocationsOptions extends RpcCallOptions {
    /** A stricter ceiling for the RP's domain-key cache, in seconds. */
    maxCacheAgeSeconds?: number;
    now?: Date;
}
/** Grant revocations that verified against the subject domain's keys. */
export interface ResolvedGrantRevocations {
    /** Verified revocations, only for the grant ids that the caller asked about. */
    revocations: ActAsGrantRevocation[];
    /** The grant ids of `revocations`. Pass these to `AudienceContext.revokedGrantIds`. */
    revokedGrantIds: string[];
    /** Records the RP returned that did not verify, or named another grant. */
    rejectedCount: number;
    /** The RP's `cache_status` for the domain keys: "fresh", "refreshed", or "stale". */
    domainKeysCacheStatus: string;
}
/** Act-as operations through the application's own RP server (API-key authenticated). */
export declare class ActAsRpClient {
    private readonly transport;
    constructor(transport: RpCallTransport);
    /**
     * Forward a signed refresh request to the user's home domain
     * (`Rp/act-as-refresh-grant`). The same call fetches a new grant after
     * consent and renews a current grant. `response.signed` is true when the
     * home domain signed a renewed grant.
     *
     * The returned grant must name the requested grant id, the requested
     * grantee, and `subjectDomain`, so a confused or hostile server cannot hand
     * this grantee another grant. The grant signature is not checked here; the
     * audience checks it.
     */
    refreshGrant(subjectDomain: string, request: SignedActAsRefreshRequest, options?: RpcCallOptions): Promise<RefreshActAsGrantResponse>;
    /**
     * Ask the RP for the signed revocations of `grantIds`
     * (`Rp/resolve-act-as-revocations`), and for the subject domain's keys
     * (`Rp/resolve-domain-keys`). Return only revocations that verify against
     * those keys and name a requested grant.
     */
    resolveGrantRevocations(subjectDomain: string, grantIds: readonly string[], options?: ResolveGrantRevocationsOptions): Promise<ResolvedGrantRevocations>;
}
/** The `signed_request` query value: base64url, no padding, of CBOR(SignedActAsGrantRequest). */
export declare function encodeSignedGrantRequestParam(signed: SignedActAsGrantRequest): string;
/**
 * Build the browser URL that sends the user to their home domain's act-as
 * consent page. The browser base comes from `_linkkeys_apis.<subjectDomain>`
 * (`https=`), with `https://<subjectDomain>` as the defined fallback.
 */
export declare function actAsRequestUrl(dns: DnsResolver, subjectDomain: string, signed: SignedActAsGrantRequest): Promise<string>;
