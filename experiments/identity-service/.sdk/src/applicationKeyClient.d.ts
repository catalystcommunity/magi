import type { RpResolveApplicationKeysRequest, RpResolveApplicationKeysResponse } from "../generated/types.gen.ts";
import type { ResolveApplicationKeysOperation } from "./applicationKeyCache.ts";
import type { PinnedRpcTransport, RpcCallOptions } from "./rpc.ts";
export declare class PinnedApplicationKeyOperations implements ResolveApplicationKeysOperation {
    private readonly transport;
    constructor(transport: PinnedRpcTransport);
    resolveApplicationKeys(req: RpResolveApplicationKeysRequest, options?: RpcCallOptions): Promise<RpResolveApplicationKeysResponse>;
}
