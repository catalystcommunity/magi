export interface IdentityInput {
    username?: string;
    domain: string;
}
export declare class InvalidIdentityInputError extends Error {
    constructor();
}
/** Parse the identity field that an application collects before login. */
export declare function parseIdentityInput(raw: string): IdentityInput;
