import { createHash, randomUUID, timingSafeEqual } from "node:crypto";
import { fromRpDecryptResponseCbor, fromRpSignResponseCbor, fromRpVerifyResponseCbor, fromUserInfoCbor, toRpDecryptRequestCbor, toRpSignRequestCbor, toRpUserInfoRequestCbor, toRpVerifyRequestCbor, } from "../generated/codec.gen.js";
import { buildBrowserUrl, resolveBrowserBase } from "./browserDiscovery.js";
import { verifyClaim } from "./claims.js";
import { SystemDnsResolver } from "./dns.js";
import { parseIdentityInput } from "./identityInput.js";
import { fetchDomainKeys, PinnedRpcTransport } from "./rpc.js";
import { NodeTransport } from "./socket.js";
const DEFAULT_PENDING_LIFETIME_MS = 10 * 60 * 1000;
const DEFAULT_CLOCK_SKEW_MS = 60 * 1000;
export class LoginError extends Error {
    code;
    constructor(code, detail) {
        super(detail ? `${code}: ${detail}` : code);
        this.name = "LoginError";
        this.code = code;
    }
}
function normalizeRpDomain(domain) {
    const normalized = domain.trim().toLowerCase().replace(/\.$/, "");
    if (!normalized || normalized.includes(":") || !/^[a-z0-9.-]+$/.test(normalized)) {
        throw new LoginError("invalid-callback", "rpDomain must be a DNS domain without a port");
    }
    return normalized;
}
function callbackForState(raw, rpDomain, state) {
    let url;
    try {
        url = new URL(raw);
    }
    catch {
        throw new LoginError("invalid-callback", "callbackUrl is not a URL");
    }
    const host = url.hostname.toLowerCase().replace(/\.$/, "");
    if (url.protocol !== "https:" || (host !== rpDomain && !host.endsWith(`.${rpDomain}`))) {
        throw new LoginError("invalid-callback", "callbackUrl must use HTTPS on the RP domain or a subdomain");
    }
    if (url.username || url.password || url.hash || url.searchParams.has("lk_state") || url.searchParams.has("encrypted_token")) {
        throw new LoginError("invalid-callback", "callbackUrl contains a reserved or unsafe URL component");
    }
    url.searchParams.set("lk_state", state);
    return url;
}
function parseTime(value) {
    const time = Date.parse(value);
    return Number.isFinite(time) ? time : undefined;
}
function bindingHash(value) {
    if (value.length === 0 || value.length > 1024)
        throw new LoginError("session-mismatch");
    return createHash("sha256").update(value, "utf8").digest("hex");
}
function equalHash(left, right) {
    const a = Buffer.from(left, "hex");
    const b = Buffer.from(right, "hex");
    return a.length === b.length && timingSafeEqual(a, b);
}
function validSigningDomain(domain) {
    if (domain.length > 253 || !domain.includes("."))
        return false;
    return domain.split(".").every((label) => label.length > 0 && label.length <= 63 && !label.startsWith("-") && !label.endsWith("-") && /^[A-Za-z0-9-]+$/.test(label));
}
function requiredClaimTypes(request) {
    return new Set(request?.required.map((claim) => claim.claimType) ?? []);
}
class PinnedRpOperations {
    transport;
    constructor(transport) {
        this.transport = transport;
    }
    async signRequest(req, options) {
        const response = await this.transport.callWithOptions("Rp", "sign-request", toRpSignRequestCbor(req), options);
        return fromRpSignResponseCbor(response);
    }
    async decryptToken(req, options) {
        const response = await this.transport.callWithOptions("Rp", "decrypt-token", toRpDecryptRequestCbor(req), options);
        return fromRpDecryptResponseCbor(response);
    }
    async verifyAssertion(req, options) {
        const response = await this.transport.callWithOptions("Rp", "verify-assertion", toRpVerifyRequestCbor(req), options);
        return fromRpVerifyResponseCbor(response);
    }
    async userinfoFetch(req, options) {
        const response = await this.transport.callWithOptions("Rp", "userinfo-fetch", toRpUserInfoRequestCbor(req), options);
        return fromUserInfoCbor(response);
    }
}
export class RegularRpClient {
    rpDomain;
    pendingStore;
    rp;
    dns;
    socketTransport;
    claimVerifier;
    pendingLifetimeMs;
    clockSkewMs;
    now;
    stateFactory;
    constructor(options) {
        this.rpDomain = normalizeRpDomain(options.rpDomain);
        this.pendingStore = options.pendingStore;
        if (options.rpOperations) {
            this.rp = options.rpOperations;
        }
        else if (options.rpServer) {
            this.rp = new PinnedRpOperations(new PinnedRpcTransport(options.rpServer));
        }
        else {
            throw new TypeError("rpServer or rpOperations is required");
        }
        this.dns = options.dnsResolver ?? new SystemDnsResolver();
        this.socketTransport = options.socketTransport ?? new NodeTransport({ policy: "public-only" });
        this.pendingLifetimeMs = options.pendingLifetimeMs ?? DEFAULT_PENDING_LIFETIME_MS;
        this.clockSkewMs = options.clockSkewMs ?? DEFAULT_CLOCK_SKEW_MS;
        this.now = options.now ?? (() => new Date());
        this.stateFactory = options.stateFactory ?? randomUUID;
        this.claimVerifier = options.claimVerifier ?? this.verifyClaimsFromDns.bind(this);
    }
    async beginLogin(input) {
        input.signal?.throwIfAborted();
        const identity = parseIdentityInput(input.identity);
        const state = this.stateFactory();
        if (state.length === 0 || state.length > 256)
            throw new LoginError("missing-state");
        const nonce = randomUUID();
        const callbackUrl = callbackForState(input.callbackUrl, this.rpDomain, state);
        const apiBase = await resolveBrowserBase(this.dns, identity.domain);
        const now = this.now();
        const signed = await this.rp.signRequest({
            callbackUrl: callbackUrl.href,
            nonce,
            requestedClaims: input.requestedClaims,
            authenticationRequirements: input.authenticationRequirements,
            flowContext: input.flowContext,
        }, { signal: input.signal });
        const pending = {
            state,
            nonce,
            identityDomain: identity.domain,
            idpApiBase: apiBase.href.replace(/\/$/, ""),
            callbackUrl: callbackUrl.href,
            browserBindingHash: bindingHash(input.sessionBinding),
            requestedClaims: input.requestedClaims,
            createdAt: now.toISOString(),
            expiresAt: new Date(now.getTime() + this.pendingLifetimeMs).toISOString(),
        };
        await this.pendingStore.save(pending);
        const redirect = buildBrowserUrl(apiBase, "/auth/authorize");
        redirect.searchParams.set("callback_url", callbackUrl.href);
        redirect.searchParams.set("nonce", nonce);
        if (identity.username !== undefined)
            redirect.searchParams.set("username", identity.username);
        redirect.searchParams.set("relying_party", this.rpDomain);
        redirect.searchParams.set("signed_request", signed.signedRequest);
        return { redirectUrl: redirect.href, state };
    }
    async completeLogin(input) {
        input.signal?.throwIfAborted();
        if (input.state.length === 0 || input.state.length > 256)
            throw new LoginError("missing-state");
        if (input.encryptedToken.length === 0 || input.encryptedToken.length > 2 * 1024 * 1024) {
            throw new LoginError("assertion-unverified");
        }
        const pending = await this.pendingStore.take(input.state);
        if (!pending)
            throw new LoginError("missing-state");
        if (!equalHash(pending.browserBindingHash, bindingHash(input.sessionBinding))) {
            throw new LoginError("session-mismatch");
        }
        const now = this.now();
        const pendingExpiry = parseTime(pending.expiresAt);
        if (pendingExpiry === undefined || now.getTime() > pendingExpiry)
            throw new LoginError("expired-state");
        const decrypted = await this.rp.decryptToken({ encryptedToken: input.encryptedToken }, { signal: input.signal });
        const verified = await this.rp.verifyAssertion({
            signedAssertion: decrypted.signedAssertion,
            expectedDomain: pending.identityDomain,
        }, { signal: input.signal });
        if (!verified.verified)
            throw new LoginError("assertion-unverified");
        const assertion = verified.assertion;
        if (assertion.domain !== pending.identityDomain)
            throw new LoginError("domain-mismatch");
        if (assertion.audience !== this.rpDomain)
            throw new LoginError("audience-mismatch");
        if (assertion.nonce !== pending.nonce)
            throw new LoginError("nonce-mismatch");
        const issuedAt = parseTime(assertion.issuedAt);
        const expiresAt = parseTime(assertion.expiresAt);
        if (issuedAt === undefined ||
            expiresAt === undefined ||
            issuedAt > now.getTime() + this.clockSkewMs ||
            expiresAt < now.getTime() ||
            expiresAt < issuedAt) {
            throw new LoginError("invalid-assertion-time");
        }
        const userInfo = await this.rp.userinfoFetch({
            token: decrypted.signedAssertion,
            apiBase: pending.idpApiBase,
            domain: pending.identityDomain,
        }, { signal: input.signal });
        this.checkUserInfo(userInfo, assertion, pending);
        const claims = await this.claimVerifier(userInfo.claims, assertion.domain, now, input.signal);
        return {
            userId: assertion.userId,
            domain: assertion.domain,
            displayName: userInfo.displayName,
            assertion,
            claims,
        };
    }
    checkUserInfo(userInfo, assertion, pending) {
        if (!assertion.userId || userInfo.userId !== assertion.userId || userInfo.domain !== assertion.domain) {
            throw new LoginError("userinfo-mismatch");
        }
        const authorized = new Set(assertion.authorizedClaims);
        for (const claim of userInfo.claims) {
            if (claim.userId !== assertion.userId)
                throw new LoginError("claim-subject-mismatch", claim.claimId);
            if (!authorized.has(claim.claimType))
                throw new LoginError("unauthorized-claim", claim.claimType);
        }
        const returned = new Set(userInfo.claims.map((claim) => claim.claimType));
        for (const required of requiredClaimTypes(pending.requestedClaims)) {
            if (!authorized.has(required) || !returned.has(required)) {
                throw new LoginError("required-claim-missing", required);
            }
        }
    }
    async verifyClaimsFromDns(claims, subjectDomain, now, signal) {
        const domains = new Set(claims.flatMap((claim) => claim.signatures.map((signature) => signature.domain)));
        for (const domain of domains) {
            if (!validSigningDomain(domain))
                throw new LoginError("unauthorized-claim", "invalid signing domain");
        }
        const keys = new Map();
        await Promise.all([...domains].map(async (domain) => {
            keys.set(domain, await fetchDomainKeys(this.socketTransport, this.dns, domain, { signal }));
        }));
        return claims.map((claim) => {
            const signingDomains = [...new Set(claim.signatures.map((signature) => signature.domain))].sort();
            verifyClaim(claim, subjectDomain, signingDomains.map((domain) => ({ domain, keys: keys.get(domain) ?? [] })), now);
            return { claim, signingDomains };
        });
    }
}
