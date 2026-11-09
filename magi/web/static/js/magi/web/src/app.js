import { jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// Browser client of the Magi API: the shell, the sign in, and the views.
import { createSignal } from "solid-js";
import { For, Show, Switch, Match, aria } from "./flow.js";
import { render } from "solid-js/web";
import { identity, me, setMe, hasToken, setToken, signOut, route, go, notice, questionCount, networkCount, refreshCounts, errText, personaName } from "./api.js";
import { Conversation, PersonaList, PersonaOrb, startConversation } from "./conversation.js";
import { Questions, Network } from "./review.js";
import { Tasks, TaskView } from "./tasks.js";
import { PageView } from "./page.js";
import { TerminalView } from "./terminal.js";
import { Knowledge, Words } from "./knowledge.js";
import { tray, dismiss, startAttention, askForNotifications } from "./attention.js";
// --- Sign in ----------------------------------------------------------------
// The sign in is a redirect flow in one tab. The page posts the start token
// to the identity service, LinkKeys sends the browser back to this page,
// and the page then takes the token with the login_id that it kept in
// sessionStorage. The login_id never leaves this origin.
const PENDING_KEY = "magi.login";
const pending = {
    get() { try {
        return JSON.parse(sessionStorage.getItem(PENDING_KEY) ?? "null");
    }
    catch {
        return null;
    } },
    set(v) { try {
        v ? sessionStorage.setItem(PENDING_KEY, JSON.stringify(v)) : sessionStorage.removeItem(PENDING_KEY);
    }
    catch { /* unavailable */ } },
};
const [phase, setPhase] = createSignal("start");
const [signInStatus, setSignInStatus] = createSignal("");
async function beginLogin(id) {
    setSignInStatus("Opening your identity provider.");
    try {
        const b = await identity.beginLogin({ identity: id, client: "web", returnUrl: location.origin + location.pathname });
        // The return URL cannot have a hash; keep the view (a page or a
        // terminal link) to open it after the sign in.
        pending.set({ loginId: b.loginId, expiresMs: b.expiresMs, hash: location.hash || undefined });
        const u = new URL(b.url);
        const form = document.createElement("form");
        form.method = "post";
        form.action = u.origin + u.pathname;
        form.hidden = true;
        const t = document.createElement("input");
        t.type = "hidden";
        t.name = "t";
        t.value = u.searchParams.get("t") ?? "";
        form.append(t);
        document.body.append(form);
        form.submit();
    }
    catch (e) {
        setSignInStatus(errText(e));
    }
}
// finishLogin takes the token of a login that this tab started. The
// identity service adds ?code= to the return URL; the token needs it.
async function finishLogin(p) {
    setPhase("signin");
    setSignInStatus("Finishing the sign in.");
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
            pending.set(null);
            if (r.status !== "complete" || !r.token)
                throw new Error(r.message ?? `The sign in is ${r.status}.`);
            setToken(r.token);
            setSignInStatus("");
            if (p.hash && !location.hash)
                location.hash = p.hash;
            return start();
        }
        pending.set(null);
        throw new Error("The sign in expired. Sign in again.");
    }
    catch (e) {
        setSignInStatus(errText(e));
    }
}
async function start() {
    try {
        const w = await identity.whoAmI({});
        setMe({ subject: w.subject, name: w.name ?? w.subject.replace(/^user:/, "") });
    }
    catch {
        setMe(null);
        return setPhase("signin");
    }
    setPhase("app");
    startConversation();
    startAttention();
    refreshCounts();
}
function SignIn() {
    let input;
    return _jsxs("main", { class: "signin", children: [_jsx("div", { class: "signin-sky", "aria-hidden": "true", children: _jsx("span", { class: "orb huge" }) }), _jsxs("section", { class: "signin-card", children: [_jsx("h1", { class: "wordmark", children: "Magi" }), _jsx("p", { class: "signin-lede", children: "A household of personas that remember what you said, do work for you, and ask before they share." }), _jsxs("form", { onSubmit: (e) => { e.preventDefault(); beginLogin(input.value.trim()); }, children: [_jsx("label", { for: "identity", children: "Your LinkKeys identity" }), _jsx("input", { id: "identity", ref: (el) => (input = el), placeholder: "name@example.com", autocomplete: "username", required: true }), _jsx("button", { class: "primary", children: "Sign in" })] }), _jsx(Show, { when: signInStatus, children: _jsx("p", { class: "signin-status", role: "status", children: signInStatus }) })] })] });
}
const tabs = [
    { view: "talk", label: "Talk" },
    { view: "questions", label: "Questions", count: questionCount },
    { view: "network", label: "Network", count: networkCount },
    { view: "tasks", label: "Tasks" },
    { view: "knowledge", label: "Knowledge" },
    { view: "words", label: "Words" },
];
function Shell() {
    const active = (v) => route().view === v || (v === "tasks" && (route().view === "task" || route().view === "terminal"));
    return _jsxs("div", { class: () => "shell" + (route().view === "terminal" ? " terminal-mode" : ""), children: [_jsxs("header", { class: "top", children: [_jsxs("a", { class: "brand", href: "#/talk", "aria-label": "Magi, talk", children: [_jsx(PersonaOrb, {}), _jsx("span", { class: "wordmark", children: "Magi" })] }), _jsxs("nav", { class: "tabs", "aria-label": "Sections", children: [_jsx(For, { each: tabs.filter((t) => t.view !== "network"), children: (t) => _jsx(TabLink, { t: t, active: active }) }), _jsx(Show, { when: () => networkCount() !== null, children: _jsx(TabLink, { t: tabs[2], active: active }) })] }), _jsxs("div", { class: "me", children: [_jsx("span", { class: "me-name", title: () => me()?.subject ?? "", children: () => me()?.name }), _jsx("button", { class: "text-button", onClick: signOut, children: "Sign out" })] })] }), _jsxs("div", { class: "body", children: [_jsx("aside", { class: "rail", children: _jsx(PersonaList, {}) }), _jsxs("main", { class: "main", children: [_jsxs(Switch, { children: [_jsx(Match, { when: () => route().view === "questions", children: _jsx(Questions, {}) }), _jsx(Match, { when: () => route().view === "network", children: _jsx(Network, {}) }), _jsx(Match, { when: () => route().view === "tasks", children: _jsx(Tasks, {}) }), _jsx(Match, { when: () => route().view === "task", children: () => _jsx(TaskView, { id: route().arg }) }), _jsx(Match, { when: () => route().view === "knowledge", children: _jsx(Knowledge, {}) }), _jsx(Match, { when: () => route().view === "words", children: _jsx(Words, {}) }), _jsx(Match, { when: () => route().view === "page", children: () => _jsx(PageView, { id: route().arg }) }), _jsx(Match, { when: () => route().view === "terminal", children: () => _jsx(TerminalView, { id: route().arg }) })] }), _jsx("div", { class: "talk-wrap", hidden: () => route().view !== "talk", children: _jsx(Conversation, {}) })] })] }), _jsx(Show, { when: notice, children: _jsx("div", { class: () => "notice" + (notice()?.bad ? " bad" : ""), role: "status", children: () => notice()?.text }) }), _jsx(NoticeTray, {})] });
}
// The notices of the personas: the newest at the bottom, until the person
// dismisses them here or on another device.
function NoticeTray() {
    const [open, setOpen] = createSignal(null);
    const canAsk = () => "Notification" in window && Notification.permission === "default";
    return _jsx(Show, { when: () => tray().length > 0, children: _jsxs("aside", { class: "tray", "aria-label": "Notices", "aria-live": "polite", children: [_jsx(For, { each: tray, children: (n) => _jsxs("article", { class: "tray-item " + n.urgency, children: [_jsxs("header", { children: [_jsx("span", { class: "tray-from", children: personaName(n.persona) }), _jsx("h2", { class: "tray-title", children: n.title })] }), _jsx("p", { class: () => "tray-text" + (open() === n.id ? " open" : ""), onClick: () => setOpen(open() === n.id ? null : n.id), children: n.text }), _jsxs("div", { class: "tray-actions", children: [_jsx("button", { class: "text-button", onClick: () => setOpen(open() === n.id ? null : n.id), children: () => (open() === n.id ? "Less" : "Read") }), _jsx("button", { class: "text-button", onClick: () => dismiss(n.id), children: "Dismiss" })] })] }) }), _jsx(Show, { when: canAsk, children: _jsx("button", { class: "text-button tray-ask", onClick: askForNotifications, children: "Show notices when this tab is hidden" }) })] }) });
}
function TabLink(props) {
    const t = props.t;
    return _jsxs("a", { class: () => "tab" + (props.active(t.view) ? " active" : ""), href: "#/" + t.view, ...aria("current", () => (props.active(t.view) ? "page" : undefined)), onClick: () => { if (t.view === "talk")
            go("talk"); }, children: [t.label, t.count ? _jsx(Show, { when: () => (t.count() ?? 0) > 0, children: _jsx("span", { class: "count", children: () => t.count() }) }) : ""] });
}
function App() {
    return _jsxs(Switch, { children: [_jsx(Match, { when: () => phase() === "app" && me() !== null, children: _jsx(Shell, {}) }), _jsx(Match, { when: () => phase() !== "start", children: _jsx(SignIn, {}) }), _jsx(Match, { when: () => phase() === "start", children: _jsx("main", { class: "loading", "aria-label": "Loading", children: _jsx("span", { class: "orb large speaking" }) }) })] });
}
render(App, document.getElementById("root"));
const p = pending.get();
if (p)
    finishLogin(p);
else if (hasToken())
    start();
else
    setPhase("signin");
