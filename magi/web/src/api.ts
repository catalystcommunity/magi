// The API clients and the state that all views share. The web client uses
// the generated clients and has no access that other clients do not have.
import { createSignal } from "solid-js";
import { MagiAsyncClient } from "../../../clients/typescript/magi/client.async.gen.ts";
import { IdentityAsyncClient } from "../../../clients/typescript/identity/client.async.gen.ts";
import type { PersonaInfo } from "../../../clients/typescript/magi/types.gen.ts";

// Browser storage can be unavailable (private windows, blocked site data).
export const store = {
  get(k: string): string | null { try { return localStorage.getItem(k); } catch { return null; } },
  set(k: string, v: string | null) { try { v === null ? localStorage.removeItem(k) : localStorage.setItem(k, v); } catch { /* unavailable */ } },
};

export class RpcError extends Error { constructor(public code: number, message: string) { super(message); } }

const TOKEN_KEY = "magi.token";
let token = store.get(TOKEN_KEY);
export const hasToken = () => token !== null;
export function setToken(t: string | null) { token = t; store.set(TOKEN_KEY, t); }

const transport = {
  async call(service: string, op: string, req: Uint8Array): Promise<Uint8Array> {
    const headers: Record<string, string> = { "Content-Type": "application/cbor" };
    if (token) headers["Authorization"] = "Bearer " + token;
    const r = await fetch(`/rpc/${service}/${op}`, { method: "POST", headers, body: req as BodyInit });
    if (!r.ok) {
      let msg = r.statusText;
      try { msg = (await r.json()).message; } catch { /* not JSON */ }
      if (r.status === 401 && service !== "IdentityService") signOut();
      throw new RpcError(r.status, msg);
    }
    return new Uint8Array(await r.arrayBuffer());
  },
};
export const magi = new MagiAsyncClient(transport);
export const identity = new IdentityAsyncClient(transport);

export type Me = { subject: string; name: string };
export const [me, setMe] = createSignal<Me | null>(null);
export function signOut() { setToken(null); setMe(null); }

// --- Views ----------------------------------------------------------------
// The view is in the URL hash, so the back button and links work.
export type View = "talk" | "questions" | "network" | "tasks" | "task" | "knowledge" | "words" | "page";
function parse(): { view: View; arg: string } {
  const [v, ...rest] = location.hash.replace(/^#\/?/, "").split("/");
  const views: View[] = ["talk", "questions", "network", "tasks", "task", "knowledge", "words", "page"];
  return { view: views.includes(v as View) ? (v as View) : "talk", arg: decodeURIComponent(rest.join("/")) };
}
export const [route, setRoute] = createSignal(parse());
window.addEventListener("hashchange", () => setRoute(parse()));
export function go(view: View, arg = "") { location.hash = "/" + view + (arg ? "/" + encodeURIComponent(arg) : ""); }

// --- Personas ---------------------------------------------------------------
export const [personas, setPersonas] = createSignal<PersonaInfo[]>([]);
export const [persona, setPersona] = createSignal(store.get("magi.persona") ?? "persona:magi");
export const personaName = (id: string) => personas().find((p) => p.id === id)?.name ?? id.replace(/^persona:/, "");
export async function loadPersonas() {
  try { setPersonas((await magi.listPersonas({})).personas); } catch { setPersonas([]); }
}

// --- Counts in the navigation ---------------------------------------------
export const [questionCount, setQuestionCount] = createSignal(0);
// null: the person cannot review network requests (not an admin, or no proxy).
export const [networkCount, setNetworkCount] = createSignal<number | null>(null);
export async function refreshCounts() {
  try { setQuestionCount((await magi.listReviews({ limit: 100 })).reviews.length); } catch { /* optional */ }
  try { setNetworkCount((await magi.listEgressReviews({ limit: 100 })).reviews.length); } catch { setNetworkCount(null); }
}

// --- Notices --------------------------------------------------------------
export const [notice, setNotice] = createSignal<{ text: string; bad: boolean } | null>(null);
let noticeTimer = 0;
export function notify(text: string, bad = false) {
  setNotice({ text, bad });
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => setNotice(null), bad ? 9000 : 4000);
}
export const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));
export function fail(what: string) { return (e: unknown) => notify(`${what}: ${errText(e)}`, true); }

// --- Formats --------------------------------------------------------------
export const time = (ms: number) => new Date(ms).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
export const day = (ms: number) => new Date(ms).toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" });
export const shortDay = (ms: number) => new Date(ms).toLocaleDateString([], { month: "short", day: "numeric" });
export const who = (subject: string) => subject.replace(/^(user|service|system|persona):/, "");
