// The conversation. There are no chats: the person has one stream with
// each persona. The librarian links each message to the earlier messages it
// relates to, and the view shows those links under the message.
import { createSignal } from "solid-js";
import { For, Show, aria } from "./flow.ts";
import type { Message, MessageLink } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, me, persona, setPersona, personas, personaName, loadPersonas, refreshCounts, go, store, fail, errText, time, day, shortDay, who } from "./api.ts";
import { speak, speaking, word, unlock, interrupt } from "./speech.ts";
import { handsFree, hearing, startListening, stopListening } from "./listen.ts";

const [messages, setMessages] = createSignal<Message[]>([]);
const [hasEarlier, setHasEarlier] = createSignal(false);
const [loadError, setLoadError] = createSignal("");
const [speakReplies, setSpeakReplies] = createSignal(store.get("magi.speak") === "1");
const [clipPlaying, setClipPlaying] = createSignal(false);
export const playing = () => clipPlaying() || speaking() !== null;
// The orb listens while hands-free talk is on.
export const recording = () => handsFree();

type Context = { title: string; messages: Message[]; loading?: boolean; error?: string };
const [context, setContext] = createSignal<Context | null>(null);

let firstSeq = 0;
let lastSeq = 0;
let gen = 0;
let scroller: HTMLElement | undefined;
const boxes = new Map<string, HTMLElement>();
const atEnd = () => !scroller || scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
const toEnd = () => requestAnimationFrame(() => scroller && (scroller.scrollTop = scroller.scrollHeight));

export async function openStream(id: string) {
  setPersona(id);
  store.set("magi.persona", id);
  const g = ++gen;
  firstSeq = lastSeq = 0;
  boxes.clear();
  setMessages([]);
  setLoadError("");
  try {
    const res = await magi.read({ persona: id, beforeSeq: 2 ** 52, limit: 50 });
    if (g !== gen) return;
    setMessages(res.messages);
    lastSeq = res.lastSeq;
    firstSeq = res.messages[0]?.seq ?? 0;
    setHasEarlier(firstSeq > 1);
    toEnd();
  } catch (e) {
    setLoadError(errText(e));
  }
  refreshCounts();
  follow(g);
}

async function follow(g: number) {
  while (g === gen) {
    try {
      const res = await magi.read({ persona: persona(), afterSeq: lastSeq, waitMs: 25000 });
      if (g !== gen) return;
      const fresh = res.messages.filter((m) => !boxes.has(m.id) && !messages().some((x) => x.id === m.id));
      if (fresh.length) {
        const follow = atEnd() || fresh.some((m) => m.authorKind === "human");
        setMessages([...messages(), ...fresh]);
        if (follow) toEnd();
        // A new spoken reply plays while Magi makes its parts.
        for (const m of fresh) if (spoken(m) && speakReplies() && Date.now() - m.createdMs < 60000) speak(m);
      }
      for (const m of res.messages) lastSeq = Math.max(lastSeq, m.seq);
    } catch {
      await new Promise((r) => setTimeout(r, 3000));
    }
  }
}

async function loadEarlier() {
  if (firstSeq <= 1) return;
  try {
    const res = await magi.read({ persona: persona(), beforeSeq: firstSeq, limit: 50 });
    const before = scroller?.scrollHeight ?? 0;
    setMessages([...res.messages, ...messages()]);
    firstSeq = res.messages[0]?.seq ?? firstSeq;
    setHasEarlier(firstSeq > 1);
    requestAnimationFrame(() => scroller && (scroller.scrollTop += scroller.scrollHeight - before));
  } catch (e) { fail("Could not load earlier messages")(e); }
}

function replace(m: Message) { setMessages(messages().map((x) => (x.id === m.id ? m : x))); }

// --- Audio ------------------------------------------------------------------
// A spoken persona reply plays in parts (speech.ts); a voice message of a
// person plays as one clip.
const spoken = (m: Message) => m.authorKind === "persona" && m.modality === "voice";
let audio: HTMLAudioElement | null = null;
async function play(digest: string) {
  try {
    const a = await magi.getAudio({ digest });
    audio?.pause();
    if (audio) URL.revokeObjectURL(audio.src);
    audio = new Audio(URL.createObjectURL(new Blob([a.body as BlobPart], { type: a.mediaType })));
    audio.onplay = () => setClipPlaying(true);
    audio.onpause = audio.onended = () => setClipPlaying(false);
    await audio.play();
  } catch (e) { fail("Could not play the reply")(e); }
}

async function send(text: string | undefined, audioBody?: Uint8Array, audioType?: string) {
  await magi.say({ persona: persona(), text, audio: audioBody, audioType, speakReply: speakReplies() });
}

// toggleMic turns hands-free talk on or off (listen.ts). Each turn of the
// person goes to Magi as a voice message; the person can talk over the
// persona to stop it.
async function toggleMic() {
  if (handsFree()) { stopListening(); return; }
  try {
    await startListening((wav) => send(undefined, wav, "audio/wav").catch(fail("Could not send what you said")));
    if (!speakReplies()) { setSpeakReplies(true); store.set("magi.speak", "1"); }
  } catch (e) { fail("Magi cannot use the microphone")(e); }
}

// --- Navigation inside the conversation -------------------------------------
// goTo shows an earlier message: in place when it is on the page, else in
// the context panel with the messages around it.
function goTo(id: string) {
  const box = boxes.get(id);
  if (box) {
    box.scrollIntoView({ behavior: "smooth", block: "center" });
    box.classList.add("flash");
    setTimeout(() => box.classList.remove("flash"), 1600);
    return;
  }
  openTrace(id);
}

async function openTrace(id: string) {
  setContext({ title: "Context", messages: [], loading: true });
  try {
    const t = await magi.trace({ message: id, direction: "both", depth: 2 });
    setContext({ title: "Context", messages: t.messages.sort((a, b) => a.createdMs - b.createdMs) });
  } catch (e) { setContext({ title: "Context", messages: [], error: errText(e) }); }
}

async function search(q: string) {
  setContext({ title: "Search", messages: [], loading: true });
  try {
    const res = await magi.search({ text: q, limit: 30 });
    setContext({ title: res.messages.length === 1 ? "1 message" : `${res.messages.length} messages`, messages: res.messages });
  } catch (e) { setContext({ title: "Search", messages: [], error: errText(e) }); }
}

// spokenText shows the text of a message; while Magi speaks it, the word
// that the person hears is marked, and the words before it are heard.
function spokenText(m: Message) {
  const w = word();
  if (speaking() !== m.id || !w) return m.text;
  const rs = Array.from(m.text);
  return [<span class="heard">{rs.slice(0, w.start).join("")}</span>, <mark class="now">{rs.slice(w.start, w.end).join("")}</mark>,
    <span class="ahead">{rs.slice(w.end).join("")}</span>];
}

// --- Message ----------------------------------------------------------------
function About(props: { l: MessageLink }) {
  const l = props.l;
  const label = l.kind.replace(/-/g, " ");
  if (l.target.kind === "utterance") {
    return <button class="about" onClick={() => goTo(l.target.ref)}>
      <span class="about-kind">{label}</span> “{l.snippet ?? "an earlier message"}”{l.targetMs ? <span class="about-when"> {shortDay(l.targetMs)}</span> : ""}
    </button>;
  }
  if (l.target.kind === "workflow-run") {
    return <button class="about" onClick={() => go("task", l.target.ref)}><span class="about-kind">{label}</span> task {l.target.title ?? l.target.ref}</button>;
  }
  return <span class="about"><span class="about-kind">{label}</span> {l.target.title ?? l.target.ref}</span>;
}

export function MessageView(props: { m: Message; compact?: boolean; pick?: (id: string) => void }) {
  const m = props.m;
  const mine = m.authorKind === "human";
  const author = mine ? (m.author === me()?.subject ? "You" : who(m.author)) : personaName(m.persona || m.author);
  const [details, setDetails] = createSignal(false);
  const shown = new Set(messages().map((x) => x.id));
  const links = m.links.filter((l) => l.kind !== "reply-to" || !shown.has(l.target.ref));
  const runs = new Set(m.links.filter((l) => l.target.kind === "workflow-run").map((l) => l.target.ref));
  return <article class={"msg " + (mine ? "mine" : "theirs")} ref={(el: HTMLElement) => { if (!props.compact) boxes.set(m.id, el); }}
    onClick={props.pick ? () => props.pick!(m.id) : undefined}>
    <header class="msg-head">
      <span class="msg-author">{author}</span>
      <time class="msg-time" title={new Date(m.createdMs).toLocaleString()}>{props.compact ? shortDay(m.createdMs) + ", " : ""}{time(m.createdMs)}</time>
      {m.modality === "voice" ? <span class="tag">spoken</span> : ""}
      {m.visibility === "shared" ? <span class="tag shared">household</span> : ""}
      {props.compact ? "" : <button class="msg-more" title="Links and who can read this" {...aria("expanded", () => String(details()))} onClick={() => setDetails(!details())}>Details</button>}
    </header>
    <div class="msg-text">{props.compact ? m.text : () => spokenText(m)}</div>
    {!props.compact && (spoken(m) || m.audio)
      ? <button class="play" onClick={() => (spoken(m) ? (speaking() === m.id ? interrupt("user") : speak(m, 0)) : play(m.audio!))}>
          {() => (speaking() === m.id ? "Stop" : "Play")}
        </button>
      : ""}
    <For each={props.compact ? [] : links}>{(l: MessageLink) => <About l={l} />}</For>
    <For each={props.compact ? [] : m.refs.filter((r) => r.kind === "knowledge-node" || (r.kind === "workflow-run" && !runs.has(r.ref)))}>{(r) =>
      r.kind === "knowledge-node"
        ? <span class="about"><span class="about-kind">knows</span> {r.title ?? r.ref}</span>
        : <button class="about" onClick={() => go("task", r.ref)}><span class="about-kind">task</span> {r.title ?? r.ref}</button>}
    </For>
    <Show when={() => details()}><Details m={m} close={(_: unknown) => setDetails(false)} /></Show>
    {m.generatedBy && !props.compact && !mine ? <footer class="msg-model">{m.generatedBy}</footer> : ""}
  </article>;
}

// Details shows the links of a message and who can read it. The person can
// remove a wrong link and change the visibility; the librarian learns from
// each correction.
function Details(props: { m: Message; close: (_: unknown) => void }) {
  const m = props.m;
  const [links, setLinks] = createSignal<MessageLink[] | null>(null);
  const [error, setError] = createSignal("");
  const load = () => magi.listLinks({ message: m.id, all: true }).then((r) => setLinks(r.links), (e) => setError(errText(e)));
  load();
  const p = personas().find((x) => x.id === (m.persona || persona()));
  const canShare = p && p.sharing !== "never" && m.stream;
  const other = m.visibility === "shared" ? "private" : "shared";
  return <div class="details">
    <Show when={() => error()}><p class="error">{error}</p></Show>
    <Show when={() => links()} fallback={<p class="quiet">Loading the links.</p>}>
      <Show when={() => links()!.length} fallback={<p class="quiet">The librarian linked this message to nothing.</p>}>
        <ul class="links">
          <For each={() => links()!}>{(l: MessageLink) =>
            <li class={"link " + l.state}>
              <span>{l.kind.replace(/-/g, " ")} {l.snippet ? "“" + l.snippet + "”" : (l.target.title ?? l.target.kind + " " + l.target.ref)}</span>
              <span class="quiet"> {l.state}, by {who(l.author)}, {Math.round(l.confidence * 100)}% sure</span>
              {l.state === "current" || l.state === "tentative"
                ? <button class="text-button" onClick={() => magi.unlink({ link: l.id }).then(load, fail("Could not remove the link"))}>Not related</button> : ""}
            </li>}
          </For>
        </ul>
      </Show>
    </Show>
    {canShare ? <p class="visibility">
      {m.visibility === "shared" ? "The household can read this." : "Only you can read this."}
      <span class="quiet">{m.visibilityBy ? ` Set by ${who(m.visibilityBy)}.` : " Not classified yet."}</span>
      <button class="text-button" onClick={() => magi.setVisibility({ message: m.id, visibility: other }).then(replace, fail("Could not change who can read it"))}>
        {other === "shared" ? "Share with the household" : "Make private"}
      </button>
    </p> : ""}
    <button class="text-button" onClick={() => props.close(null)}>Close</button>
  </div>;
}

// --- The view -----------------------------------------------------------
export function PersonaOrb(props: { size?: "large" }) {
  return <span class={() => "orb" + (props.size ? " " + props.size : "") + (playing() ? " speaking" : "") + (recording() ? " listening" : "") + (hearing() ? " hearing" : "")} aria-hidden="true" />;
}

export function PersonaList() {
  let q: HTMLInputElement | undefined;
  return <div class="rail-inner">
    <nav class="personas" aria-label="Personas">
      <For each={personas}>{(p) =>
        <button class={() => "persona" + (p.id === persona() ? " active" : "")} onClick={() => { go("talk"); openStream(p.id); }}>
          <span class="persona-dot" />
          <span class="persona-name">{p.name}</span>
          {p.sharing === "public" ? <span class="persona-note">household</span> : ""}
        </button>}
      </For>
    </nav>
    <form class="search" role="search" onSubmit={(e: SubmitEvent) => { e.preventDefault(); const v = q!.value.trim(); if (v) { go("talk"); search(v); } }}>
      <input ref={(el: HTMLInputElement) => (q = el)} type="search" placeholder="Search what was said" aria-label="Search what was said" />
    </form>
  </div>;
}

export function Conversation() {
  let input: HTMLTextAreaElement | undefined;
  const grow = () => { if (input) { input.style.height = "auto"; input.style.height = Math.min(input.scrollHeight, 200) + "px"; } };
  const submit = async (e?: Event) => {
    e?.preventDefault();
    unlock(); // a reply that comes later can play
    const text = input!.value.trim();
    if (!text) return;
    input!.value = "";
    grow();
    try { await send(text); } catch (err) { input!.value = text; grow(); fail("Could not send")(err); }
  };
  const current = () => personas().find((p) => p.id === persona());
  return <div class="talk">
    <section class="stream" aria-label="Conversation">
      <header class="stream-head">
        <PersonaOrb size="large" />
        <div>
          <h1 class="stream-name">{() => personaName(persona())}</h1>
          <p class="stream-note">{() => current()?.sharing === "public" ? "The household talks with this persona. Magi asks before it shares a message." : "Only you talk with this persona."}</p>
        </div>
      </header>
      <div class="scroller" ref={(el: HTMLElement) => (scroller = el)}>
        <Show when={hasEarlier}><button class="earlier" onClick={loadEarlier}>Show earlier messages</button></Show>
        <Show when={loadError}><p class="error">Could not read the conversation: {loadError}</p></Show>
        <Show when={() => !loadError() && messages().length === 0}>
          <p class="empty">Say hello to {() => personaName(persona())}. Type below, or hold a conversation with the microphone.</p>
        </Show>
        <div class="turns">
          <For each={messages}>{(m: Message, i: () => number) =>
            <div class={"turn " + (m.authorKind === "human" ? "mine" : "theirs")}>
              <Show when={() => { const prev = messages()[i() - 1]; return !prev || day(prev.createdMs) !== day(m.createdMs); }}>
                <div class="day"><span>{day(m.createdMs)}</span></div>
              </Show>
              <MessageView m={m} />
            </div>}
          </For>
        </div>
      </div>
      <form class="composer" onSubmit={submit}>
        <textarea ref={(el: HTMLTextAreaElement) => (input = el)} rows="1" aria-label="Message"
          placeholder={() => "Message " + personaName(persona())} onInput={() => { grow(); interrupt("typing"); }}
          onKeyDown={(e: KeyboardEvent) => { if (e.key === "Enter" && !e.shiftKey) submit(e); }} />
        <button class={() => "mic" + (recording() ? " on" : "")} type="button" onClick={toggleMic} {...aria("pressed", () => String(recording()))}>
          {() => (handsFree() ? (hearing() ? "Hearing you" : "Listening") : "Talk")}
        </button>
        <button class="send">Send</button>
        <label class="speak-toggle">
          <input type="checkbox" checked={speakReplies()} onChange={(e: Event) => { const v = (e.target as HTMLInputElement).checked; unlock(); setSpeakReplies(v); store.set("magi.speak", v ? "1" : "0"); }} />
          Speak replies
        </label>
      </form>
    </section>
    <Show when={context}>
      <aside class="context" aria-label="Context">
        <header class="context-head"><h2>{() => context()!.title}</h2><button class="text-button" onClick={() => setContext(null)}>Close</button></header>
        <Show when={() => context()!.loading}><p class="quiet">Loading.</p></Show>
        <Show when={() => context()!.error}><p class="error">{() => context()!.error}</p></Show>
        <For each={() => context()!.messages}>{(m: Message) =>
          <MessageView m={m} compact pick={(id: string) => goTo(id)} />}
        </For>
      </aside>
    </Show>
  </div>;
}

export async function startConversation() {
  await loadPersonas();
  if (!personas().some((p) => p.id === persona()) && personas().length) setPersona(personas()[0].id);
  openStream(persona());
}
