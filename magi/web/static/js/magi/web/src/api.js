// The API clients and the state that all views share. The web client uses
// the generated clients and has no access that other clients do not have.
import { createSignal } from "solid-js";
import { MagiAsyncClient } from "../../../clients/typescript/magi/client.async.gen.js";
import { IdentityAsyncClient } from "../../../clients/typescript/identity/client.async.gen.js";
// Browser storage can be unavailable (private windows, blocked site data).
export const store = {
    get(k) { try {
        return localStorage.getItem(k);
    }
    catch {
        return null;
    } },
    set(k, v) { try {
        v === null ? localStorage.removeItem(k) : localStorage.setItem(k, v);
    }
    catch { /* unavailable */ } },
};
export class RpcError extends Error {
    code;
    constructor(code, message) {
        super(message);
        this.code = code;
    }
}
const TOKEN_KEY = "magi.token";
let token = store.get(TOKEN_KEY);
export const hasToken = () => token !== null;
export function setToken(t) { token = t; store.set(TOKEN_KEY, t); }
const transport = {
    async call(service, op, req) {
        const headers = { "Content-Type": "application/cbor" };
        if (token)
            headers["Authorization"] = "Bearer " + token;
        const r = await fetch(`/rpc/${service}/${op}`, { method: "POST", headers, body: req });
        if (!r.ok) {
            let msg = r.statusText;
            try {
                msg = (await r.json()).message;
            }
            catch { /* not JSON */ }
            if (r.status === 401 && service !== "IdentityService")
                signOut();
            throw new RpcError(r.status, msg);
        }
        return new Uint8Array(await r.arrayBuffer());
    },
};
export const magi = new MagiAsyncClient(transport);
export const identity = new IdentityAsyncClient(transport);
export const [me, setMe] = createSignal(null);
export function signOut() { setToken(null); setMe(null); }
function parse() {
    const [v, ...rest] = location.hash.replace(/^#\/?/, "").split("/");
    const views = ["talk", "questions", "network", "tasks", "task", "knowledge", "words"];
    return { view: views.includes(v) ? v : "talk", arg: decodeURIComponent(rest.join("/")) };
}
export const [route, setRoute] = createSignal(parse());
window.addEventListener("hashchange", () => setRoute(parse()));
export function go(view, arg = "") { location.hash = "/" + view + (arg ? "/" + encodeURIComponent(arg) : ""); }
// --- Personas ---------------------------------------------------------------
export const [personas, setPersonas] = createSignal([]);
export const [persona, setPersona] = createSignal(store.get("magi.persona") ?? "persona:magi");
export const personaName = (id) => personas().find((p) => p.id === id)?.name ?? id.replace(/^persona:/, "");
export async function loadPersonas() {
    try {
        setPersonas((await magi.listPersonas({})).personas);
    }
    catch {
        setPersonas([]);
    }
}
// --- Counts in the navigation ---------------------------------------------
export const [questionCount, setQuestionCount] = createSignal(0);
// null: the person cannot review network requests (not an admin, or no proxy).
export const [networkCount, setNetworkCount] = createSignal(null);
export async function refreshCounts() {
    try {
        setQuestionCount((await magi.listReviews({ limit: 100 })).reviews.length);
    }
    catch { /* optional */ }
    try {
        setNetworkCount((await magi.listEgressReviews({ limit: 100 })).reviews.length);
    }
    catch {
        setNetworkCount(null);
    }
}
// --- Notices --------------------------------------------------------------
export const [notice, setNotice] = createSignal(null);
let noticeTimer = 0;
export function notify(text, bad = false) {
    setNotice({ text, bad });
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => setNotice(null), bad ? 9000 : 4000);
}
export const errText = (e) => (e instanceof Error ? e.message : String(e));
export function fail(what) { return (e) => notify(`${what}: ${errText(e)}`, true); }
// --- Formats --------------------------------------------------------------
export const time = (ms) => new Date(ms).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
export const day = (ms) => new Date(ms).toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" });
export const shortDay = (ms) => new Date(ms).toLocaleDateString([], { month: "short", day: "numeric" });
export const who = (subject) => subject.replace(/^(user|service|system|persona):/, "");
