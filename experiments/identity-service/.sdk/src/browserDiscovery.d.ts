import type { DnsResolver } from "./dns.ts";
/** Resolve the browser HTTPS endpoint. DNS errors use the identity domain. */
export declare function resolveBrowserBase(dns: DnsResolver, identityDomain: string): Promise<URL>;
/** Add a LinkKeys route without discarding an endpoint path prefix. */
export declare function buildBrowserUrl(base: URL, route: string): URL;
