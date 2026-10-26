import net from "node:net";
export type AddressPolicy = "permissive" | "public-only";
export declare class TransportError extends Error {
    constructor(message: string, options?: {
        cause?: unknown;
    });
}
export interface DialOptions {
    signal?: AbortSignal;
}
/** Dials `host:port` and returns a connected duplex socket. Injectable so tests can hand the RPC layer a fake/in-memory duplex instead of a real socket. */
export interface Transport {
    dial(hostPort: string, options?: DialOptions): Promise<net.Socket>;
}
export interface NodeTransportOptions {
    policy?: AddressPolicy;
    connectTimeoutMs?: number;
}
/** Default `Transport`: a plain Node `net.Socket`, gated only by `policy` (permissive unless the caller opts into `"public-only"`). */
export declare class NodeTransport implements Transport {
    readonly policy: AddressPolicy;
    private readonly connectTimeoutMs;
    constructor(options?: NodeTransportOptions);
    dial(hostPort: string, options?: DialOptions): Promise<net.Socket>;
}
