export declare class BrowserLoginError extends Error {
    constructor(message: string);
}
export interface BrowserLoginOptions {
    /** An application endpoint that calls RegularRpClient.beginLogin. */
    backendUrl: string;
    identity: string;
    fetch?: typeof globalThis.fetch;
    navigate?: (url: string) => void;
    signal?: AbortSignal;
}
/** Ask the application backend to begin login. This function never contacts an IDP directly. */
export declare function requestLoginRedirect(options: BrowserLoginOptions): Promise<string>;
/** Begin login and move the current browser page to the LinkKeys IDP. */
export declare function startLinkKeysLogin(options: BrowserLoginOptions): Promise<void>;
