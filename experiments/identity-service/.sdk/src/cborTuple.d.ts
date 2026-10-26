export type TupleElem = {
    readonly t: "text";
    readonly v: string;
} | {
    readonly t: "bytes";
    readonly v: Uint8Array;
} | {
    readonly t: "null";
};
export declare const text: (v: string) => TupleElem;
export declare const bytes: (v: Uint8Array) => TupleElem;
export declare const nullElem: () => TupleElem;
export declare const textOrNull: (v: string | undefined | null) => TupleElem;
/** Encode a fixed-order tuple of text/bytes/null elements as a definite-length CBOR array. */
export declare function encodeCborTuple(elems: readonly TupleElem[]): Uint8Array;
