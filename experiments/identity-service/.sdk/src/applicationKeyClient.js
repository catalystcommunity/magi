// Production `ResolveApplicationKeysOperation`: calls `Rp/resolve-application-keys`
// over the same pinned CSIL-RPC transport `RegularRpClient` uses for its
// other RP operations (`src/client.ts`'s `PinnedRpOperations` is the model
// this mirrors).
import { toRpResolveApplicationKeysRequestCbor, fromRpResolveApplicationKeysResponseCbor } from "../generated/codec.gen.js";
export class PinnedApplicationKeyOperations {
    transport;
    constructor(transport) {
        this.transport = transport;
    }
    async resolveApplicationKeys(req, options) {
        const response = await this.transport.callWithOptions("Rp", "resolve-application-keys", toRpResolveApplicationKeysRequestCbor(req), options);
        return fromRpResolveApplicationKeysResponseCbor(response);
    }
}
