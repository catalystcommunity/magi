export declare class DnsLookupError extends Error {
    constructor(message: string, options?: {
        cause?: unknown;
    });
}
/**
 * Resolve TXT records for a fully-qualified name (e.g.
 * `_linkkeys.example.com`). Each returned string is one TXT record's full
 * content — the concatenation of its character-strings — matching the Rust
 * reference SDK's `DnsResolver` contract, so
 * `parseLinkkeysTxt`/`parseLinkkeysApisTxt` can parse it unchanged.
 */
export interface DnsResolver {
    txtLookup(name: string): Promise<string[]>;
}
/**
 * Default `DnsResolver`: the OS-configured resolver (`dns.promises.resolveTxt`,
 * Node's own stdlib, itself backed by c-ares which reads `/etc/resolv.conf`
 * or the platform equivalent). Per the design doc's "Decided" section:
 * resolver spoofing on a LAN is an accepted, documented tradeoff for this
 * mode; operators wanting hardening can inject their own `DnsResolver` (e.g.
 * a DoH client) instead. `servers` optionally overrides which resolver(s) to
 * query (design doc: "configurable ... e.g. a DoH endpoint" — a plain
 * resolver-IP override is the simplest form of that for Node's stdlib).
 */
export declare class SystemDnsResolver implements DnsResolver {
    private readonly servers?;
    constructor(options?: {
        servers?: readonly string[];
    });
    txtLookup(name: string): Promise<string[]>;
}
