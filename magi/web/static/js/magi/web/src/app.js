// Browser client of the Magi API. It uses the generated clients and has no
// access that other clients do not have.
import { MagiAsyncClient } from "../../../clients/typescript/magi/client.async.gen.js";
import { IdentityAsyncClient } from "../../../clients/typescript/identity/client.async.gen.js";
const TOKEN_KEY = "magi.token";
const store = {
    get(k) { try {
        return localStorage.getItem(k);
    }
    catch {
        return null;
    } },
    set(k, v) { try {
        v === null ? localStorage.removeItem(k) : localStorage.setItem(k, v);
    }
    catch { /* storage unavailable */ } },
};
class RpcError extends Error {
    code;
    constructor(code, message) {
        super(message);
        this.code = code;
    }
}
let token = store.get(TOKEN_KEY);
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
const magi = new MagiAsyncClient(transport);
const identity = new IdentityAsyncClient(transport);
const $ = (sel) => document.querySelector(sel);
function el(tag, attrs = {}, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs))
        k === "class" ? (e.className = v) : e.setAttribute(k, v);
    for (const k of kids)
        e.append(k);
    return e;
}
const time = (ms) => new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
function show(view) {
    document.querySelectorAll("[data-view]").forEach((v) => (v.hidden = v.dataset.view !== view));
    document.querySelectorAll("[data-tab]").forEach((t) => t.classList.toggle("active", t.dataset.tab === view));
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
    get() {
        try {
            return JSON.parse(sessionStorage.getItem(PENDING_KEY) ?? "null");
        }
        catch {
            return null;
        }
    },
    set(v) {
        try {
            v ? sessionStorage.setItem(PENDING_KEY, JSON.stringify(v)) : sessionStorage.removeItem(PENDING_KEY);
        }
        catch { /* storage unavailable */ }
    },
};
async function login(ev) {
    ev.preventDefault();
    const id = $("#identity").value.trim();
    const status = $("#login-status");
    try {
        const b = await identity.beginLogin({ identity: id, client: "web", returnUrl: location.origin + location.pathname });
        session.set({ loginId: b.loginId, expiresMs: b.expiresMs });
        const u = new URL(b.url);
        const form = el("form", { method: "post", action: u.origin + u.pathname, hidden: "" }, el("input", { type: "hidden", name: "t", value: u.searchParams.get("t") ?? "" }));
        document.body.append(form);
        form.submit();
    }
    catch (e) {
        status.textContent = String(e.message);
    }
}
// finishLogin takes the token of a login that this tab started. The
// identity service adds ?code= to the return URL; the token needs it.
async function finishLogin(p) {
    const status = $("#login-status");
    showLogin();
    status.textContent = "Finishing the sign in...";
    const here = new URL(location.href);
    const code = here.searchParams.get("code") ?? undefined;
    if (code) {
        here.searchParams.delete("code");
        history.replaceState(null, "", here.pathname + here.search + here.hash);
    }
    try {
        while (Date.now() < p.expiresMs) {
            let r;
            try {
                r = await identity.pollLogin({ loginId: p.loginId, waitMs: 5000, code });
            }
            catch {
                await new Promise((ok) => setTimeout(ok, 2000));
                continue;
            }
            if (r.status === "pending")
                continue;
            session.set(null);
            if (r.status !== "complete" || !r.token)
                throw new Error(r.message ?? `The sign in is ${r.status}.`);
            token = r.token;
            store.set(TOKEN_KEY, token);
            status.textContent = "";
            return start();
        }
        session.set(null);
        throw new Error("The sign in expired.");
    }
    catch (e) {
        status.textContent = String(e.message);
    }
}
// --- Chats ---------------------------------------------------------------
let chat;
let lastSeq = 0;
let readGen = 0;
async function loadChats() {
    const res = await magi.listChats({ limit: 50 });
    const list = $("#chats");
    list.replaceChildren(...res.chats.map((c) => {
        const b = el("button", { class: "chat" + (c.id === chat ? " active" : "") }, c.title || "Untitled");
        b.onclick = () => openChat(c.id);
        return b;
    }));
}
async function openChat(id) {
    chat = id;
    lastSeq = 0;
    $("#messages").replaceChildren();
    show("chat");
    loadChats();
    if (id)
        follow(++readGen);
}
async function follow(gen) {
    while (gen === readGen && chat) {
        try {
            const res = await magi.read({ chat, afterSeq: lastSeq, waitMs: 25000 });
            if (gen !== readGen)
                return;
            for (const m of res.messages) {
                renderMessage(m);
                lastSeq = m.seq;
            }
        }
        catch {
            await new Promise((r) => setTimeout(r, 3000));
        }
    }
}
function renderMessage(m) {
    const mine = m.authorKind === "human";
    const body = el("div", { class: "text" }, m.text);
    const box = el("div", { class: "msg " + (mine ? "mine" : m.authorKind) }, el("div", { class: "meta" }, (mine ? "You" : "Magi") + " · " + time(m.createdMs) + (m.modality === "voice" ? " · voice" : "") + (m.generatedBy ? " · " + m.generatedBy : "")), body);
    if (m.audio) {
        const btn = el("button", { class: "small" }, "Play");
        btn.onclick = async () => {
            const a = await magi.getAudio({ digest: m.audio });
            new Audio(URL.createObjectURL(new Blob([a.body], { type: a.mediaType }))).play();
        };
        box.append(btn);
        if (!mine && $("#speak").checked && Date.now() - m.createdMs < 60000)
            btn.click();
    }
    for (const r of m.refs) {
        if (r.kind === "workflow-run") {
            const link = el("button", { class: "ref" }, "Task: " + (r.title ?? r.ref));
            link.onclick = () => openTask(r.ref);
            box.append(link);
        }
        else if (r.kind === "knowledge-node") {
            box.append(el("span", { class: "ref" }, "Knowledge: " + (r.title ?? r.ref)));
        }
    }
    // Follow new messages, unless the person scrolled up to read.
    const view = $("#messages").parentElement;
    const atEnd = view.scrollHeight - view.scrollTop - view.clientHeight < 80;
    $("#messages").append(box);
    if (atEnd || mine)
        view.scrollTop = view.scrollHeight;
}
async function send(text, audio, audioType) {
    const speakReply = $("#speak").checked;
    const res = await magi.say({ chat, text, audio, audioType, speakReply });
    if (res.chat !== chat) {
        await openChat(res.chat);
    }
}
async function onSend(ev) {
    ev.preventDefault();
    const input = $("#input");
    const text = input.value.trim();
    if (!text)
        return;
    input.value = "";
    try {
        await send(text);
    }
    catch (e) {
        alert(e.message);
        input.value = text;
    }
}
// --- Voice ---------------------------------------------------------------
let recorder;
async function toggleMic() {
    const btn = $("#mic");
    if (recorder) {
        recorder.stop();
        return;
    }
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const chunks = [];
    recorder = new MediaRecorder(stream);
    recorder.ondataavailable = (e) => chunks.push(e.data);
    recorder.onstop = async () => {
        stream.getTracks().forEach((t) => t.stop());
        btn.textContent = "Mic";
        btn.classList.remove("rec");
        const type = recorder.mimeType || "audio/webm";
        recorder = undefined;
        const blob = new Blob(chunks, { type });
        try {
            await send(undefined, new Uint8Array(await blob.arrayBuffer()), type.split(";")[0]);
        }
        catch (e) {
            alert(e.message);
        }
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
        list.replaceChildren(...res.tasks.map((t) => {
            const b = el("button", { class: "task" }, el("span", { class: "status " + t.status }, t.status), " ", t.title, el("small", {}, " · " + t.currentStep));
            b.onclick = () => openTask(t.id);
            return b;
        }));
    }
    catch (e) {
        list.replaceChildren(el("p", { class: "error" }, String(e.message)));
    }
}
async function startTask(ev) {
    ev.preventDefault();
    const title = $("#task-title").value.trim();
    const instructions = $("#task-instructions").value.trim();
    const status = $("#task-status");
    try {
        const t = await magi.startTask({ kind: "code-change", instructions, title: title || undefined });
        $("#task-form").reset();
        status.textContent = "";
        openTask(t.id);
    }
    catch (e) {
        status.textContent = String(e.message);
    }
}
async function openTask(id) {
    show("task");
    const t = await magi.getTask({ id });
    const view = $("#task");
    const steps = el("ol", { class: "steps" }, ...t.steps.map((s) => el("li", {}, el("b", {}, s.step), ` ${s.status}${s.outcome ? " → " + s.outcome : ""}`, ...s.notes.map((n) => el("div", { class: "note" }, n)))));
    const parts = [el("h2", {}, t.title), el("p", {}, `Status ${t.status}. Step ${t.currentStep}. Branch ${t.branch ?? "-"}.`)];
    if (t.error)
        parts.push(el("p", { class: "error" }, t.error));
    parts.push(steps);
    if (t.waitingFor) {
        const comment = el("textarea", { placeholder: "Comment (needed for revise)" });
        const decide = (outcome) => async () => {
            try {
                await magi.decide({ task: t.id, outcome, comment: comment.value || undefined });
                openTask(t.id);
            }
            catch (e) {
                alert(e.message);
            }
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
        }
        catch (e) {
            diffBox.textContent = e.message;
        }
    };
    const refresh = el("button", {}, "Refresh");
    refresh.onclick = () => openTask(t.id);
    const controls = [diffBtn, " ", refresh];
    if (t.status === "running" || t.status === "waiting") {
        const stop = el("button", { class: "decide reject" }, "Stop task");
        stop.onclick = () => cancelTask(t.id);
        controls.push(" ", stop);
    }
    parts.push(el("div", {}, ...controls), diffBox);
    view.replaceChildren(...parts);
}
// cancelTask stops a task. For a risky step the server tells the risk
// first, and the person must agree.
async function cancelTask(id) {
    try {
        try {
            await magi.cancelTask({ task: id });
        }
        catch (e) {
            const risk = /confirm: (.*)/.exec(e.message)?.[1];
            if (!risk)
                throw e;
            if (!confirm("Stopping this task is risky: " + risk + "\n\nStop it anyway?"))
                return;
            await magi.cancelTask({ task: id, confirm: true });
        }
    }
    catch (e) {
        alert(e.message);
    }
    openTask(id);
}
// --- Knowledge -----------------------------------------------------------
async function searchKnowledge(ev) {
    ev?.preventDefault();
    show("knowledge");
    const q = $("#kq").value.trim();
    const status = $("#k-status");
    status.textContent = "Searching…";
    status.className = "note";
    try {
        const res = await magi.queryKnowledge({ text: q || undefined, limit: 50 });
        status.textContent = res.items.length ? "" : q
            ? `Nothing matches "${q}".`
            : "Magi knows nothing about you yet. Tell Magi a fact in a chat (for example \"Remember that my dentist is Dr. Rivera\"), or add one below.";
        $("#k-list").replaceChildren(...res.items.map((it) => el("div", { class: "kitem" }, el("b", {}, it.label), el("small", {}, ` ${it.kind}`), el("div", {}, it.summary), ...(it.system ? [el("div", { class: "note" }, `In ${it.system}: ${it.locator ?? ""}`)] : []), ...it.relations.map((r) => relationLink(r)))));
    }
    catch (e) {
        status.textContent = "Search failed: " + e.message;
        status.className = "error";
    }
}
// relationLink shows a relation such as "works-at Maple Street Dental" or
// "← hosted-in Maple Street Dental". A click searches for the other item.
function relationLink(r) {
    const other = r.replace(/^← /, "").replace(/^\S+\s+/, "");
    const b = el("button", { class: "ref" }, r);
    b.onclick = () => { $("#kq").value = other; searchKnowledge(); };
    return b;
}
async function addKnowledge(ev) {
    ev.preventDefault();
    const v = (id) => $(id).value.trim();
    const status = $("#k-status");
    try {
        const it = await magi.remember({
            kind: v("#ka-kind"), label: v("#ka-label"), summary: v("#ka-summary") || undefined,
            relatedTo: v("#ka-related") || undefined, relation: v("#ka-relation") || undefined,
        });
        $("#k-add-form").reset();
        $("#kq").value = it.label;
        await searchKnowledge();
    }
    catch (e) {
        status.textContent = "Could not remember: " + e.message;
        status.className = "error";
    }
}
// --- Start ---------------------------------------------------------------
async function start() {
    try {
        const me = await identity.whoAmI({});
        $("#me").textContent = me.name ?? me.subject.replace(/^user:/, "");
    }
    catch {
        return showLogin();
    }
    $("#login").hidden = true;
    $("#app").hidden = false;
    await openChat(undefined);
}
window.addEventListener("DOMContentLoaded", () => {
    $("#login-form").addEventListener("submit", login);
    $("#composer").addEventListener("submit", onSend);
    $("#input").addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        onSend(e);
    } });
    $("#mic").addEventListener("click", toggleMic);
    $("#new-chat").addEventListener("click", () => openChat(undefined));
    $("#tab-tasks").addEventListener("click", loadTasks);
    $("#tab-chat").addEventListener("click", () => show("chat"));
    $("#tab-knowledge").addEventListener("click", () => searchKnowledge());
    $("#k-form").addEventListener("submit", searchKnowledge);
    $("#k-add-form").addEventListener("submit", addKnowledge);
    $("#task-form").addEventListener("submit", startTask);
    $("#signout").addEventListener("click", signOut);
    const pending = session.get();
    if (pending)
        finishLogin(pending);
    else
        token ? start() : showLogin();
});
