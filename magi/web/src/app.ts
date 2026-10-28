// Browser client of the Magi API. It uses the generated clients and has no
// access that other clients do not have.
import { MagiAsyncClient } from "../../../clients/typescript/magi/client.async.gen.ts";
import { IdentityAsyncClient } from "../../../clients/typescript/identity/client.async.gen.ts";
import type { Message, Task, Chat } from "../../../clients/typescript/magi/types.gen.ts";

const TOKEN_KEY = "magi.token";
const store = {
  get(k: string): string | null { try { return localStorage.getItem(k); } catch { return null; } },
  set(k: string, v: string | null) { try { v === null ? localStorage.removeItem(k) : localStorage.setItem(k, v); } catch { /* storage unavailable */ } },
};

class RpcError extends Error { constructor(public code: number, message: string) { super(message); } }

let token = store.get(TOKEN_KEY);
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
const magi = new MagiAsyncClient(transport);
const identity = new IdentityAsyncClient(transport);

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;
function el<K extends keyof HTMLElementTagNameMap>(tag: K, attrs: Record<string, string> = {}, ...kids: (Node | string)[]) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) k === "class" ? (e.className = v) : e.setAttribute(k, v);
  for (const k of kids) e.append(k);
  return e;
}
const time = (ms: number) => new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
function show(view: string) {
  document.querySelectorAll<HTMLElement>("[data-view]").forEach((v) => (v.hidden = v.dataset.view !== view));
  document.querySelectorAll<HTMLElement>("[data-tab]").forEach((t) => t.classList.toggle("active", t.dataset.tab === view));
}
function signOut() { token = null; store.set(TOKEN_KEY, null); showLogin(); }

// --- Login ---------------------------------------------------------------
function showLogin() {
  $("#app").hidden = true;
  $("#login").hidden = false;
}

// The login is a redirect flow in one tab. The page posts the start token
// to the identity service, LinkKeys sends the browser back to this page,
// and the page then takes the token with the login_id that it kept in
// sessionStorage. The login_id never leaves this origin.
const PENDING_KEY = "magi.login";
const session = {
  get(): { loginId: string; expiresMs: number } | null {
    try { return JSON.parse(sessionStorage.getItem(PENDING_KEY) ?? "null"); } catch { return null; }
  },
  set(v: { loginId: string; expiresMs: number } | null) {
    try { v ? sessionStorage.setItem(PENDING_KEY, JSON.stringify(v)) : sessionStorage.removeItem(PENDING_KEY); } catch { /* storage unavailable */ }
  },
};

async function login(ev: Event) {
  ev.preventDefault();
  const id = ($("#identity") as HTMLInputElement).value.trim();
  const status = $("#login-status");
  try {
    const b = await identity.beginLogin({ identity: id, client: "web", returnUrl: location.origin + location.pathname });
    session.set({ loginId: b.loginId, expiresMs: b.expiresMs });
    const u = new URL(b.url);
    const form = el("form", { method: "post", action: u.origin + u.pathname, hidden: "" },
      el("input", { type: "hidden", name: "t", value: u.searchParams.get("t") ?? "" }));
    document.body.append(form);
    form.submit();
  } catch (e) {
    status.textContent = String((e as Error).message);
  }
}

// finishLogin takes the token of a login that this tab started.
async function finishLogin(p: { loginId: string; expiresMs: number }) {
  const status = $("#login-status");
  showLogin();
  status.textContent = "Finishing the sign in...";
  try {
    while (Date.now() < p.expiresMs) {
      let r;
      try { r = await identity.pollLogin({ loginId: p.loginId, waitMs: 5000 }); }
      catch { await new Promise((ok) => setTimeout(ok, 2000)); continue; }
      if (r.status === "pending") continue;
      session.set(null);
      if (r.status !== "complete" || !r.token) throw new Error(r.message ?? `The sign in is ${r.status}.`);
      token = r.token;
      store.set(TOKEN_KEY, token);
      status.textContent = "";
      return start();
    }
    session.set(null);
    throw new Error("The sign in expired.");
  } catch (e) {
    status.textContent = String((e as Error).message);
  }
}

// --- Chats ---------------------------------------------------------------
let chat: string | undefined;
let lastSeq = 0;
let readGen = 0;

async function loadChats() {
  const res = await magi.listChats({ limit: 50 });
  const list = $("#chats");
  list.replaceChildren(...res.chats.map((c: Chat) => {
    const b = el("button", { class: "chat" + (c.id === chat ? " active" : "") }, c.title || "Untitled");
    b.onclick = () => openChat(c.id);
    return b;
  }));
}

async function openChat(id: string | undefined) {
  chat = id;
  lastSeq = 0;
  $("#messages").replaceChildren();
  show("chat");
  loadChats();
  if (id) follow(++readGen);
}

async function follow(gen: number) {
  while (gen === readGen && chat) {
    try {
      const res = await magi.read({ chat, afterSeq: lastSeq, waitMs: 25000 });
      if (gen !== readGen) return;
      for (const m of res.messages) { renderMessage(m); lastSeq = m.seq; }
    } catch {
      await new Promise((r) => setTimeout(r, 3000));
    }
  }
}

function renderMessage(m: Message) {
  const mine = m.authorKind === "human";
  const body = el("div", { class: "text" }, m.text);
  const box = el("div", { class: "msg " + (mine ? "mine" : m.authorKind) }, el("div", { class: "meta" }, (mine ? "You" : "Magi") + " · " + time(m.createdMs) + (m.modality === "voice" ? " · voice" : "")), body);
  if (m.audio) {
    const btn = el("button", { class: "small" }, "Play");
    btn.onclick = async () => {
      const a = await magi.getAudio({ digest: m.audio! });
      new Audio(URL.createObjectURL(new Blob([a.body as BlobPart], { type: a.mediaType }))).play();
    };
    box.append(btn);
    if (!mine && ($("#speak") as HTMLInputElement).checked && Date.now() - m.createdMs < 60000) btn.click();
  }
  for (const r of m.refs) {
    if (r.kind === "workflow-run") {
      const link = el("button", { class: "ref" }, "Task: " + (r.title ?? r.ref));
      link.onclick = () => openTask(r.ref);
      box.append(link);
    } else if (r.kind === "knowledge-node") {
      box.append(el("span", { class: "ref" }, "Knowledge: " + (r.title ?? r.ref)));
    }
  }
  $("#messages").append(box);
  box.scrollIntoView({ block: "end" });
}

async function send(text: string | undefined, audio?: Uint8Array, audioType?: string) {
  const speakReply = ($("#speak") as HTMLInputElement).checked;
  const res = await magi.say({ chat, text, audio, audioType, speakReply });
  if (res.chat !== chat) { await openChat(res.chat); }
}

async function onSend(ev: Event) {
  ev.preventDefault();
  const input = $("#input") as HTMLTextAreaElement;
  const text = input.value.trim();
  if (!text) return;
  input.value = "";
  try { await send(text); } catch (e) { alert((e as Error).message); input.value = text; }
}

// --- Voice ---------------------------------------------------------------
let recorder: MediaRecorder | undefined;
async function toggleMic() {
  const btn = $("#mic");
  if (recorder) { recorder.stop(); return; }
  const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
  const chunks: Blob[] = [];
  recorder = new MediaRecorder(stream);
  recorder.ondataavailable = (e) => chunks.push(e.data);
  recorder.onstop = async () => {
    stream.getTracks().forEach((t) => t.stop());
    btn.textContent = "Mic";
    btn.classList.remove("rec");
    const type = recorder!.mimeType || "audio/webm";
    recorder = undefined;
    const blob = new Blob(chunks, { type });
    try { await send(undefined, new Uint8Array(await blob.arrayBuffer()), type.split(";")[0]); } catch (e) { alert((e as Error).message); }
  };
  recorder.start();
  btn.textContent = "Stop";
  btn.classList.add("rec");
}

// --- Tasks ---------------------------------------------------------------
async function loadTasks() {
  show("tasks");
  const list = $("#task-list");
  try {
    const res = await magi.listTasks({ limit: 30 });
    if (res.tasks.length === 0) {
      list.replaceChildren(el("p", { class: "note" }, "You have no tasks. Start one here, or ask Magi in a chat to change its code."));
      return;
    }
    list.replaceChildren(...res.tasks.map((t: Task) => {
      const b = el("button", { class: "task" }, el("span", { class: "status " + t.status }, t.status), " ", t.title, el("small", {}, " · " + t.currentStep));
      b.onclick = () => openTask(t.id);
      return b;
    }));
  } catch (e) {
    list.replaceChildren(el("p", { class: "error" }, String((e as Error).message)));
  }
}

async function startTask(ev: Event) {
  ev.preventDefault();
  const title = ($("#task-title") as HTMLInputElement).value.trim();
  const instructions = ($("#task-instructions") as HTMLTextAreaElement).value.trim();
  const status = $("#task-status");
  try {
    const t = await magi.startTask({ kind: "code-change", instructions, title: title || undefined });
    ($("#task-form") as HTMLFormElement).reset();
    status.textContent = "";
    openTask(t.id);
  } catch (e) {
    status.textContent = String((e as Error).message);
  }
}

async function openTask(id: string) {
  show("task");
  const t = await magi.getTask({ id });
  const view = $("#task");
  const steps = el("ol", { class: "steps" }, ...t.steps.map((s) =>
    el("li", {}, el("b", {}, s.step), ` ${s.status}${s.outcome ? " → " + s.outcome : ""}`, ...s.notes.map((n) => el("div", { class: "note" }, n)))));
  const parts: (Node | string)[] = [el("h2", {}, t.title), el("p", {}, `Status ${t.status}. Step ${t.currentStep}. Branch ${t.branch ?? "-"}.`)];
  if (t.error) parts.push(el("p", { class: "error" }, t.error));
  parts.push(steps);
  if (t.waitingFor) {
    const comment = el("textarea", { placeholder: "Comment (needed for revise)" }) as HTMLTextAreaElement;
    const decide = (outcome: string) => async () => {
      try { await magi.decide({ task: t.id, outcome, comment: comment.value || undefined }); openTask(t.id); } catch (e) { alert((e as Error).message); }
    };
    const buttons = (t.decisions ?? []).map((d) => { const b = el("button", { class: "decide " + d }, d); b.onclick = decide(d); return b; });
    parts.push(el("div", { class: "decision" }, el("p", {}, t.waitingFor), comment, el("div", {}, ...buttons)));
  }
  const diffBox = el("pre", { class: "diff" });
  const diffBtn = el("button", {}, "Show change");
  diffBtn.onclick = async () => {
    try {
      const d = await magi.getTaskDiff({ id: t.id });
      diffBox.replaceChildren(...d.patch.split("\n").map((l) => el("span", { class: l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : l.startsWith("@@") ? "hunk" : "" }, l + "\n")));
    } catch (e) { diffBox.textContent = (e as Error).message; }
  };
  const refresh = el("button", {}, "Refresh");
  refresh.onclick = () => openTask(t.id);
  parts.push(el("div", {}, diffBtn, " ", refresh), diffBox);
  view.replaceChildren(...parts);
}

// --- Knowledge -----------------------------------------------------------
async function searchKnowledge(ev?: Event) {
  ev?.preventDefault();
  show("knowledge");
  const q = ($("#kq") as HTMLInputElement).value.trim();
  const res = await magi.queryKnowledge({ text: q || undefined, limit: 50 });
  $("#k-list").replaceChildren(...res.items.map((it) => el("div", { class: "kitem" },
    el("b", {}, it.label), el("small", {}, ` ${it.kind}`), el("div", {}, it.summary),
    ...(it.system ? [el("div", { class: "note" }, `In ${it.system}: ${it.locator ?? ""}`)] : []),
    ...it.relations.map((r) => el("div", { class: "note" }, r)))));
}

// --- Start ---------------------------------------------------------------
async function start() {
  try {
    const me = await identity.whoAmI({});
    $("#me").textContent = me.name ?? me.subject.replace(/^user:/, "");
  } catch { return showLogin(); }
  $("#login").hidden = true;
  $("#app").hidden = false;
  await openChat(undefined);
}

window.addEventListener("DOMContentLoaded", () => {
  $("#login-form").addEventListener("submit", login);
  $("#composer").addEventListener("submit", onSend);
  ($("#input") as HTMLTextAreaElement).addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); onSend(e); } });
  $("#mic").addEventListener("click", toggleMic);
  $("#new-chat").addEventListener("click", () => openChat(undefined));
  $("#tab-tasks").addEventListener("click", loadTasks);
  $("#tab-chat").addEventListener("click", () => show("chat"));
  $("#tab-knowledge").addEventListener("click", () => searchKnowledge());
  $("#k-form").addEventListener("submit", searchKnowledge);
  $("#task-form").addEventListener("submit", startTask);
  $("#signout").addEventListener("click", signOut);
  const pending = session.get();
  if (pending) finishLogin(pending);
  else token ? start() : showLogin();
});
