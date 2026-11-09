// Browser client of the Magi API: the shell, the sign in, and the views.
import { createSignal } from "solid-js";
import { For, Show, Switch, Match, aria } from "./flow.ts";
import { render } from "solid-js/web";
import { identity, me, setMe, hasToken, setToken, signOut, route, go, notice, questionCount, networkCount, refreshCounts, errText, personaName, type View } from "./api.ts";
import { Conversation, PersonaList, PersonaOrb, startConversation } from "./conversation.tsx";
import { Questions, Network } from "./review.tsx";
import { Tasks, TaskView } from "./tasks.tsx";
import { PageView } from "./page.tsx";
import { TerminalView } from "./terminal.tsx";
import { Knowledge, Words } from "./knowledge.tsx";
import { tray, dismiss, startAttention, askForNotifications } from "./attention.ts";
import type { Notice } from "../../../clients/typescript/magi/types.gen.ts";

// --- Sign in ----------------------------------------------------------------
// The sign in is a redirect flow in one tab. The page posts the start token
// to the identity service, LinkKeys sends the browser back to this page,
// and the page then takes the token with the login_id that it kept in
// sessionStorage. The login_id never leaves this origin.
const PENDING_KEY = "magi.login";
type Pending = { loginId: string; expiresMs: number; hash?: string };
const pending = {
  get(): Pending | null { try { return JSON.parse(sessionStorage.getItem(PENDING_KEY) ?? "null"); } catch { return null; } },
  set(v: Pending | null) { try { v ? sessionStorage.setItem(PENDING_KEY, JSON.stringify(v)) : sessionStorage.removeItem(PENDING_KEY); } catch { /* unavailable */ } },
};

const [phase, setPhase] = createSignal<"start" | "signin" | "app">("start");
const [signInStatus, setSignInStatus] = createSignal("");

async function beginLogin(id: string) {
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
    t.type = "hidden"; t.name = "t"; t.value = u.searchParams.get("t") ?? "";
    form.append(t);
    document.body.append(form);
    form.submit();
  } catch (e) { setSignInStatus(errText(e)); }
}

// finishLogin takes the token of a login that this tab started. The
// identity service adds ?code= to the return URL; the token needs it.
async function finishLogin(p: Pending) {
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
      try { r = await identity.pollLogin({ loginId: p.loginId, waitMs: 5000, code }); }
      catch { await new Promise((ok) => setTimeout(ok, 2000)); continue; }
      if (r.status === "pending") continue;
      pending.set(null);
      if (r.status !== "complete" || !r.token) throw new Error(r.message ?? `The sign in is ${r.status}.`);
      setToken(r.token);
      setSignInStatus("");
      if (p.hash && !location.hash) location.hash = p.hash;
      return start();
    }
    pending.set(null);
    throw new Error("The sign in expired. Sign in again.");
  } catch (e) { setSignInStatus(errText(e)); }
}

async function start() {
  try {
    const w = await identity.whoAmI({});
    setMe({ subject: w.subject, name: w.name ?? w.subject.replace(/^user:/, "") });
  } catch { setMe(null); return setPhase("signin"); }
  setPhase("app");
  startConversation();
  startAttention();
  refreshCounts();
}

function SignIn() {
  let input: HTMLInputElement | undefined;
  return <main class="signin">
    <div class="signin-sky" aria-hidden="true"><span class="orb huge" /></div>
    <section class="signin-card">
      <h1 class="wordmark">Magi</h1>
      <p class="signin-lede">A household of personas that remember what you said, do work for you, and ask before they share.</p>
      <form onSubmit={(e: SubmitEvent) => { e.preventDefault(); beginLogin(input!.value.trim()); }}>
        <label for="identity">Your LinkKeys identity</label>
        <input id="identity" ref={(el: HTMLInputElement) => (input = el)} placeholder="name@example.com" autocomplete="username" required />
        <button class="primary">Sign in</button>
      </form>
      <Show when={signInStatus}><p class="signin-status" role="status">{signInStatus}</p></Show>
    </section>
  </main>;
}

// --- Shell --------------------------------------------------------------------
type Tab = { view: View; label: string; count?: () => number | null };
const tabs: Tab[] = [
  { view: "talk", label: "Talk" },
  { view: "questions", label: "Questions", count: questionCount },
  { view: "network", label: "Network", count: networkCount },
  { view: "tasks", label: "Tasks" },
  { view: "knowledge", label: "Knowledge" },
  { view: "words", label: "Words" },
];

function Shell() {
  const active = (v: View) => route().view === v || (v === "tasks" && (route().view === "task" || route().view === "terminal"));
  return <div class={() => "shell" + (route().view === "terminal" ? " terminal-mode" : "")}>
    <header class="top">
      <a class="brand" href="#/talk" aria-label="Magi, talk"><PersonaOrb /><span class="wordmark">Magi</span></a>
      <nav class="tabs" aria-label="Sections">
        <For each={tabs.filter((t) => t.view !== "network")}>{(t: Tab) => <TabLink t={t} active={active} />}</For>
        <Show when={() => networkCount() !== null}><TabLink t={tabs[2]} active={active} /></Show>
      </nav>
      <div class="me">
        <span class="me-name" title={() => me()?.subject ?? ""}>{() => me()?.name}</span>
        <button class="text-button" onClick={signOut}>Sign out</button>
      </div>
    </header>
    <div class="body">
      <aside class="rail"><PersonaList /></aside>
      <main class="main">
        <Switch>
          <Match when={() => route().view === "questions"}><Questions /></Match>
          <Match when={() => route().view === "network"}><Network /></Match>
          <Match when={() => route().view === "tasks"}><Tasks /></Match>
          <Match when={() => route().view === "task"}>{() => <TaskView id={route().arg} />}</Match>
          <Match when={() => route().view === "knowledge"}><Knowledge /></Match>
          <Match when={() => route().view === "words"}><Words /></Match>
          <Match when={() => route().view === "page"}>{() => <PageView id={route().arg} />}</Match>
          <Match when={() => route().view === "terminal"}>{() => <TerminalView id={route().arg} />}</Match>
        </Switch>
        {/* The conversation stays mounted, so its long poll and scroll position stay. */}
        <div class="talk-wrap" hidden={() => route().view !== "talk"}><Conversation /></div>
      </main>
    </div>
    <Show when={notice}>
      <div class={() => "notice" + (notice()?.bad ? " bad" : "")} role="status">{() => notice()?.text}</div>
    </Show>
    <NoticeTray />
  </div>;
}

// The notices of the personas: the newest at the bottom, until the person
// dismisses them here or on another device.
function NoticeTray() {
  const [open, setOpen] = createSignal<string | null>(null);
  const canAsk = () => "Notification" in window && Notification.permission === "default";
  return <Show when={() => tray().length > 0}>
    <aside class="tray" aria-label="Notices" aria-live="polite">
      <For each={tray}>{(n: Notice) =>
        <article class={"tray-item " + n.urgency}>
          <header>
            <span class="tray-from">{personaName(n.persona)}</span>
            <h2 class="tray-title">{n.title}</h2>
          </header>
          <p class={() => "tray-text" + (open() === n.id ? " open" : "")} onClick={() => setOpen(open() === n.id ? null : n.id)}>{n.text}</p>
          <div class="tray-actions">
            <button class="text-button" onClick={() => setOpen(open() === n.id ? null : n.id)}>{() => (open() === n.id ? "Less" : "Read")}</button>
            <button class="text-button" onClick={() => dismiss(n.id)}>Dismiss</button>
          </div>
        </article>}
      </For>
      <Show when={canAsk}>
        <button class="text-button tray-ask" onClick={askForNotifications}>Show notices when this tab is hidden</button>
      </Show>
    </aside>
  </Show>;
}

function TabLink(props: { t: Tab; active: (v: View) => boolean }) {
  const t = props.t;
  return <a class={() => "tab" + (props.active(t.view) ? " active" : "")} href={"#/" + t.view} {...aria("current", () => (props.active(t.view) ? "page" : undefined))}
    onClick={() => { if (t.view === "talk") go("talk"); }}>
    {t.label}
    {t.count ? <Show when={() => (t.count!() ?? 0) > 0}><span class="count">{() => t.count!()}</span></Show> : ""}
  </a>;
}

function App() {
  return <Switch>
    <Match when={() => phase() === "app" && me() !== null}><Shell /></Match>
    <Match when={() => phase() !== "start"}><SignIn /></Match>
    <Match when={() => phase() === "start"}><main class="loading" aria-label="Loading"><span class="orb large speaking" /></main></Match>
  </Switch>;
}

render(App, document.getElementById("root")!);
const p = pending.get();
if (p) finishLogin(p);
else if (hasToken()) start();
else setPhase("signin");
