import { type DnsResolver } from "./dns.ts";
import { type Transport } from "./socket.ts";
import type { DomainPublicKey } from "../generated/types.gen.ts";
import type { AsyncServiceTransport } from "../generated/client.async.gen.ts";
import { Status } from "./vendor/csilgen-transport/index.ts";
export declare class TlsError extends Error {
    constructor(message: string, options?: {
        cause?: unknown;
    });
}
export declare class ProtocolError extends Error {
    constructor(message: string, options?: {
        cause?: unknown;
    });
}
export declare class RpcTimeoutError extends Error {
    constructor(timeoutMs: number);
}
export declare class RpcServerError extends Error {
    readonly status: number;
    constructor(status: number, message: string);
}
export declare class NoTrustedDomainKeysError extends Error {
    constructor(domain: string);
}
/** Discovered endpoint for a domain: its pinned trust-anchor fingerprints (`_linkkeys`) and its CSIL-RPC TCP address (`_linkkeys_apis` `tcp=`). */
export interface DomainEndpoint {
    fingerprints: string[];
    tcpAddr: string;
}
/**
 * Look up a domain's trust anchor + TCP endpoint over DNS TXT. Fails closed:
 * a missing/unparseable record, or a `_linkkeys` record with no `fp=`
 * entries, or a `_linkkeys_apis` record with no `tcp=` entry, is an error —
 * this SDK never proceeds without a fingerprint set to pin to.
 */
export declare function discoverDomainEndpoint(dns: DnsResolver, domain: string): Promise<DomainEndpoint>;
/** Send one CSIL-RPC request over a fresh pinned-TLS connection and return the decoded success payload. A non-Ok status becomes `RpcServerError`. */
export declare function call(transport: Transport, endpoint: DomainEndpoint, service: string, op: string, payload: Uint8Array, auth?: string, options?: RpcCallOptions): Promise<Uint8Array>;
export interface RpcCallOptions {
    signal?: AbortSignal;
    timeoutMs?: number;
}
export interface PinnedRpcTransportOptions {
    tcpAddress: string;
    fingerprints: readonly string[];
    apiKey: string;
    socketTransport?: Transport;
    /** Maximum time for one complete RPC, including connect, TLS, write, and read. */
    requestTimeoutMs?: number;
}
/** A generated-client transport for one configured LinkKeys RP server. */
export declare class PinnedRpcTransport implements AsyncServiceTransport {
    private readonly endpoint;
    private readonly apiKey;
    private readonly socketTransport;
    private readonly requestTimeoutMs;
    constructor(options: PinnedRpcTransportOptions);
    call(service: string, op: string, req: Uint8Array): Promise<Uint8Array>;
    callWithOptions(service: string, op: string, req: Uint8Array, options?: RpcCallOptions): Promise<Uint8Array>;
}
/**
 * Fetch `domain`'s currently-trusted public keys:
 * `DomainKeys/get-domain-keys` over TCP CSIL-RPC, pinned to the domain's DNS
 * `fp=` set, with signing keys pinned directly and encryption keys trusted
 * only via a pinned signing key's vouch (`trustKeys`). ALWAYS also fetches
 * `DomainKeys/get-revocations` — regardless of the (unauthenticated,
 * server-asserted) `recentRevocationsAvailable` hint, which a
 * compromised/malicious IDP could simply omit to suppress delivery of a
 * revocation this client needs to see — and marks any key that a
 * quorum-verified sibling revocation certificate targets. The verifier can
 * then compare a stored claim's signed attestation time with that instant. A
 * `get-revocations` fetch/decode failure is FATAL (fail closed): this SDK
 * must never proceed on a domain's keys without having successfully asked
 * whether any of them were revoked. An empty revocations list is a normal,
 * successful outcome. An empty trusted-keys result (before or after
 * revocation application) is `NoTrustedDomainKeysError` — also fail closed.
 */
export declare function fetchDomainKeys(transport: Transport, dns: DnsResolver, domain: string, options?: RpcCallOptions): Promise<DomainPublicKey[]>;
export { Status };
