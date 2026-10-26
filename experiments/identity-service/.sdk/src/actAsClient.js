// Act-as network helpers. An application calls its OWN RP server with its
// API key (the same pinned CSIL-RPC transport as `PinnedApplicationKeyOperations`).
// The RP forwards the call to the user's home domain. This SDK verifies every
// signed record that comes back; it never trusts the RP's word alone.
//
// Also builds the browser URL that sends a user to the home domain's act-as
// consent page.
import { fromGetActAsGrantRevocationsResponseCbor, fromRefreshActAsGrantResponseCbor, fromRpResolveDomainKeysResponseCbor, toRpActAsRefreshRequestCbor, toRpResolveActAsRevocationsRequestCbor, toRpResolveDomainKeysRequestCbor, toSignedActAsGrantRequestCbor, } from "../generated/codec.gen.js";
import { ACT_AS_MAX_REVOCATION_LOOKUP_IDS, ActAsError, decodeGrant, decodeRefreshRequest, sameGrantee, verifyGrantRevocation, } from "./actAs.js";
import { buildBrowserUrl, resolveBrowserBase } from "./browserDiscovery.js";
import { verifyRevocationCertificate } from "./revocation.js";
/** The home domain's browser route for an act-as grant request. */
export const ACT_AS_ROUTE = "/auth/act-as";
function validDomain(domain) {
    if (domain.length === 0 || domain.length > 253 || !domain.includes("."))
        return false;
    return domain.split(".").every((label) => label.length > 0 && label.length <= 63 && !label.startsWith("-") && !label.endsWith("-") && /^[A-Za-z0-9-]+$/.test(label));
}
function requireDomain(domain) {
    if (!validDomain(domain))
        throw new TypeError("subjectDomain must be a DNS domain name");
}
/** ASCII-only case-insensitive domain comparison. */
function sameDomain(a, b) {
    return a.length === b.length && a.replace(/[A-Z]/g, (c) => c.toLowerCase()) === b.replace(/[A-Z]/g, (c) => c.toLowerCase());
}
/** Act-as operations through the application's own RP server (API-key authenticated). */
export class ActAsRpClient {
    transport;
    constructor(transport) {
        this.transport = transport;
    }
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
    async refreshGrant(subjectDomain, request, options) {
        requireDomain(subjectDomain);
        const sent = decodeRefreshRequest(request);
        const response = fromRefreshActAsGrantResponseCbor(await this.transport.callWithOptions("Rp", "act-as-refresh-grant", toRpActAsRefreshRequestCbor({ subjectDomain, request }), options));
        const grant = decodeGrant(response.grant);
        if (grant.grantId !== sent.grantId)
            throw new ActAsError("mismatch", "grant_id");
        if (!sameGrantee(grant.grantee, sent.grantee))
            throw new ActAsError("mismatch", "grantee");
        if (!sameDomain(grant.subjectDomain, subjectDomain))
            throw new ActAsError("mismatch", "subject_domain");
        return response;
    }
    /**
     * Ask the RP for the signed revocations of `grantIds`
     * (`Rp/resolve-act-as-revocations`), and for the subject domain's keys
     * (`Rp/resolve-domain-keys`). Return only revocations that verify against
     * those keys and name a requested grant.
     */
    async resolveGrantRevocations(subjectDomain, grantIds, options = {}) {
        requireDomain(subjectDomain);
        const ids = [...new Set(grantIds)];
        if (ids.length === 0 || ids.length > ACT_AS_MAX_REVOCATION_LOOKUP_IDS || ids.some((id) => id.length === 0)) {
            throw new TypeError(`grantIds must hold 1 to ${ACT_AS_MAX_REVOCATION_LOOKUP_IDS} non-empty ids`);
        }
        const now = options.now ?? new Date();
        const callOptions = { signal: options.signal, timeoutMs: options.timeoutMs };
        const [revocationBytes, keyBytes] = await Promise.all([
            this.transport.callWithOptions("Rp", "resolve-act-as-revocations", toRpResolveActAsRevocationsRequestCbor({ subjectDomain, grantIds: ids }), callOptions),
            this.transport.callWithOptions("Rp", "resolve-domain-keys", toRpResolveDomainKeysRequestCbor({ domain: subjectDomain, maxCacheAgeSeconds: options.maxCacheAgeSeconds }), callOptions),
        ]);
        const signed = fromGetActAsGrantRevocationsResponseCbor(revocationBytes).revocations;
        const keyResponse = fromRpResolveDomainKeysResponseCbor(keyBytes);
        if (keyResponse.domain !== subjectDomain) {
            throw new ActAsError("mismatch", "resolve-domain-keys answered for another domain");
        }
        // Apply quorum-verified key revocations, the same as resolveApplicationKeys.
        let domainKeys = keyResponse.keys;
        for (const cert of keyResponse.revocations) {
            if (verifyRevocationCertificate(cert, domainKeys, subjectDomain, now)) {
                domainKeys = domainKeys.map((k) => (k.keyId === cert.targetKeyId ? { ...k, revokedAt: cert.revokedAt } : k));
            }
        }
        const wanted = new Set(ids);
        const revocations = [];
        let rejectedCount = 0;
        for (const record of signed) {
            let revocation;
            try {
                revocation = verifyGrantRevocation(record, domainKeys, subjectDomain, now);
            }
            catch {
                rejectedCount++;
                continue;
            }
            if (!wanted.has(revocation.grantId)) {
                rejectedCount++;
                continue;
            }
            revocations.push(revocation);
        }
        return {
            revocations,
            revokedGrantIds: [...new Set(revocations.map((r) => r.grantId))],
            rejectedCount,
            domainKeysCacheStatus: keyResponse.cacheStatus,
        };
    }
}
/** The `signed_request` query value: base64url, no padding, of CBOR(SignedActAsGrantRequest). */
export function encodeSignedGrantRequestParam(signed) {
    return Buffer.from(toSignedActAsGrantRequestCbor(signed)).toString("base64url");
}
/**
 * Build the browser URL that sends the user to their home domain's act-as
 * consent page. The browser base comes from `_linkkeys_apis.<subjectDomain>`
 * (`https=`), with `https://<subjectDomain>` as the defined fallback.
 */
export async function actAsRequestUrl(dns, subjectDomain, signed) {
    requireDomain(subjectDomain);
    const url = buildBrowserUrl(await resolveBrowserBase(dns, subjectDomain), ACT_AS_ROUTE);
    url.searchParams.set("signed_request", encodeSignedGrantRequestParam(signed));
    return url.href;
}
