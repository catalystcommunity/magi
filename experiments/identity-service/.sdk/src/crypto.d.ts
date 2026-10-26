export declare class CryptoError extends Error {
    constructor(message: string, options?: {
        cause?: unknown;
    });
}
/** Return the LinkKeys SHA-256 fingerprint for raw public-key bytes. */
export declare function fingerprint(publicKeyBytes: Uint8Array): string;
/** Derive a raw Ed25519 public key from a raw 32-byte seed. */
export declare function derivePublicKeyFromEd25519PrivateKey(seed: Uint8Array): Uint8Array;
/** Sign bytes with a raw Ed25519 seed. */
export declare function signEd25519(message: Uint8Array, seed: Uint8Array): Uint8Array;
/** Verify an Ed25519 signature. Return false for malformed input. */
export declare function verifyEd25519(message: Uint8Array, signature: Uint8Array, rawPublicKey: Uint8Array): boolean;
