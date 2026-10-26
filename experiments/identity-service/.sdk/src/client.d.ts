import type { AuthFlowContext, AuthenticationRequirements, Claim, ClaimRequest, IdentityAssertion, RpDecryptRequest, RpDecryptResponse, RpSignRequest, RpSignResponse, RpUserInfoRequest, RpVerifyRequest, RpVerifyResponse, UserInfo } from "../generated/types.gen.ts";
import { type DnsResolver } from "./dns.ts";
import type { PendingLoginStore } from "./pending.ts";
import { type PinnedRpcTransportOptions, type RpcCallOptions } from "./rpc.ts";
import { type Transport } from "./socket.ts";
export type LoginErrorCode = "invalid-callback" | "missing-state" | "expired-state" | "session-mismatch" | "assertion-unverified" | "domain-mismatch" | "audience-mismatch" | "nonce-mismatch" | "invalid-assertion-time" | "userinfo-mismatch" | "unauthorized-claim" | "required-claim-missing" | "claim-subject-mismatch";
export declare class LoginError extends Error {
    readonly code: LoginErrorCode;
    constructor(code: LoginErrorCode, detail?: string);
}
export interface RegularRpOperations {
    signRequest(req: RpSignRequest, options?: RpcCallOptions): Promise<RpSignResponse>;
    decryptToken(req: RpDecryptRequest, options?: RpcCallOptions): Promise<RpDecryptResponse>;
    verifyAssertion(req: RpVerifyRequest, options?: RpcCallOptions): Promise<RpVerifyResponse>;
    userinfoFetch(req: RpUserInfoRequest, options?: RpcCallOptions): Promise<UserInfo>;
}
export interface VerifiedClaim {
    claim: Claim;
    signingDomains: string[];
}
export type ClaimVerifier = (claims: readonly Claim[], subjectDomain: string, now: Date, signal?: AbortSignal) => Promise<VerifiedClaim[]>;
export interface RegularRpClientOptions {
    rpDomain: string;
    pendingStore: PendingLoginStore;
    /** Use this seam for tests or a custom RP service adapter. */
    rpOperations?: RegularRpOperations;
    /** Required when rpOperations is absent. */
    rpServer?: PinnedRpcTransportOptions;
    dnsResolver?: DnsResolver;
    socketTransport?: Transport;
    claimVerifier?: ClaimVerifier;
    pendingLifetimeMs?: number;
    clockSkewMs?: number;
    now?: () => Date;
    stateFactory?: () => string;
}
export interface BeginLoginInput {
    identity: string;
    callbackUrl: string;
    /** A stable value from the application session that started this login. */
    sessionBinding: string;
    requestedClaims?: ClaimRequest;
    authenticationRequirements?: AuthenticationRequirements;
    flowContext?: AuthFlowContext;
    signal?: AbortSignal;
}
export interface BeginLoginResult {
    redirectUrl: string;
    state: string;
}
export interface CompleteLoginInput {
    state: string;
    encryptedToken: string;
    /** The same application session value that was used in beginLogin. */
    sessionBinding: string;
    signal?: AbortSignal;
}
export interface CompleteLoginResult {
    userId: string;
    domain: string;
    displayName: string;
    assertion: IdentityAssertion;
    claims: VerifiedClaim[];
}
export declare class RegularRpClient {
    private readonly rpDomain;
    private readonly pendingStore;
    private readonly rp;
    private readonly dns;
    private readonly socketTransport;
    private readonly claimVerifier;
    private readonly pendingLifetimeMs;
    private readonly clockSkewMs;
    private readonly now;
    private readonly stateFactory;
    constructor(options: RegularRpClientOptions);
    beginLogin(input: BeginLoginInput): Promise<BeginLoginResult>;
    completeLogin(input: CompleteLoginInput): Promise<CompleteLoginResult>;
    private checkUserInfo;
    private verifyClaimsFromDns;
}
