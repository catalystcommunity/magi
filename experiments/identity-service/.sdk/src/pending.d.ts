import type { ClaimRequest } from "../generated/types.gen.ts";
export interface PendingLogin {
    state: string;
    nonce: string;
    identityDomain: string;
    idpApiBase: string;
    callbackUrl: string;
    browserBindingHash: string;
    requestedClaims?: ClaimRequest;
    createdAt: string;
    expiresAt: string;
}
export interface PendingLoginStore {
    /** Save only if state is absent. Fail if state already exists. */
    save(login: PendingLogin): Promise<void>;
    /** Return and delete a pending login as one atomic operation. */
    take(state: string): Promise<PendingLogin | undefined>;
}
/** A development store. Use a shared atomic store in a multi-process service. */
export declare class MemoryPendingLoginStore implements PendingLoginStore {
    private readonly logins;
    save(login: PendingLogin): Promise<void>;
    take(state: string): Promise<PendingLogin | undefined>;
}
