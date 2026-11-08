import { jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// The conversation. There are no chats: the person has one stream with
// each persona. The librarian links each message to the earlier messages it
// relates to, and the view shows those links under the message.
import { createSignal, createEffect, createRoot, onCleanup } from "solid-js";
import { For, Show, aria } from "./flow.js";
import { magi, me, persona, setPersona, personas, personaName, loadPersonas, refreshCounts, go, store, fail, errText, time, day, shortDay, who } from "./api.js";
import { speak, speaking, word, unlock, interrupt, ack } from "./speech.js";
import { handsFree, hearing, startListening, stopListening } from "./listen.js";
const [messages, setMessages] = createSignal([]);
const [hasEarlier, setHasEarlier] = createSignal(false);
const [loadError, setLoadError] = createSignal("");
const [speakReplies, setSpeakReplies] = createSignal(store.get("magi.speak") === "1");
const [clipPlaying, setClipPlaying] = createSignal(false);
// Wake word: in hands-free talk, Magi takes only speech that starts with
// the name of a persona ("Computer, ..."), and the answer to a reply in
// the 8 s after the reply ends. Other talk in the room is not kept.
const [wakeWord, setWakeWord] = createSignal(store.get("magi.wake") === "1");
const followUpMs = 8000;
let lastReplyEnd = 0;
createRoot(() => createEffect((was) => { const now = speaking(); if (was && !now)
    lastReplyEnd = Date.now(); return now; }, null));
export const playing = () => clipPlaying() || speaking() !== null;
// The orb listens while hands-free talk is on.
export const recording = () => handsFree();
// working: Magi builds a reply to the newest message of a person. A reply
// links to the message that it answers (reply-to); a notice does not
// answer. After 3 minutes without a reply the view stops waiting. All the
// clients see the same stream, so they all show the same state.
const workingMs = 3 * 60 * 1000;
const [now, setNow] = createSignal(Date.now());
export const working = () => {
    const ms = messages();
    for (let i = ms.length - 1; i >= 0; i--) {
        const m = ms[i];
        if (m.authorKind === "persona" && m.modality !== "notice")
            return false;
        if (m.authorKind === "human")
            return now() - m.createdMs < workingMs;
    }
    return false;
};
createRoot(() => createEffect(() => {
    if (!working())
        return;
    const t = setInterval(() => setNow(Date.now()), 5000);
    onCleanup(() => clearInterval(t));
}));
const [context, setContext] = createSignal(null);
let firstSeq = 0;
let lastSeq = 0;
let gen = 0;
let scroller;
const boxes = new Map();
const atEnd = () => !scroller || scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
const toEnd = () => requestAnimationFrame(() => scroller && (scroller.scrollTop = scroller.scrollHeight));
export async function openStream(id) {
    setPersona(id);
    store.set("magi.persona", id);
    const g = ++gen;
    firstSeq = lastSeq = 0;
    boxes.clear();
    setMessages([]);
    setLoadError("");
    try {
        const res = await magi.read({ persona: id, beforeSeq: 2 ** 52, limit: 50 });
        if (g !== gen)
            return;
        setMessages(res.messages);
        lastSeq = res.lastSeq;
        firstSeq = res.messages[0]?.seq ?? 0;
        setHasEarlier(firstSeq > 1);
        toEnd();
    }
    catch (e) {
        setLoadError(errText(e));
    }
    refreshCounts();
    follow(g);
}
async function follow(g) {
    while (g === gen) {
        try {
            const res = await magi.read({ persona: persona(), afterSeq: lastSeq, waitMs: 25000 });
            if (g !== gen)
                return;
            const fresh = res.messages.filter((m) => !boxes.has(m.id) && !messages().some((x) => x.id === m.id));
            if (fresh.length) {
                const follow = atEnd() || fresh.some((m) => m.authorKind === "human");
                setMessages([...messages(), ...fresh]);
                if (follow)
                    toEnd();
                // A new spoken reply plays while Magi makes its parts.
                for (const m of fresh)
                    if (m.modality === "voice" && spoken(m) && (speakReplies() || handsFree()) && Date.now() - m.createdMs < 60000)
                        speak(m);
            }
            for (const m of res.messages)
                lastSeq = Math.max(lastSeq, m.seq);
        }
        catch {
            await new Promise((r) => setTimeout(r, 3000));
        }
    }
}
async function loadEarlier() {
    if (firstSeq <= 1)
        return;
    try {
        const res = await magi.read({ persona: persona(), beforeSeq: firstSeq, limit: 50 });
        const before = scroller?.scrollHeight ?? 0;
        setMessages([...res.messages, ...messages()]);
        firstSeq = res.messages[0]?.seq ?? firstSeq;
        setHasEarlier(firstSeq > 1);
        requestAnimationFrame(() => scroller && (scroller.scrollTop += scroller.scrollHeight - before));
    }
    catch (e) {
        fail("Could not load earlier messages")(e);
    }
}
function replace(m) { setMessages(messages().map((x) => (x.id === m.id ? m : x))); }
// --- Audio ------------------------------------------------------------------
// A spoken persona reply plays in parts (speech.ts); a voice message of a
// person plays as one clip.
// A notice is said by the notice loop (attention.ts), not as a reply; its
// play button replays it.
const spoken = (m) => m.authorKind === "persona" && (m.modality === "voice" || m.modality === "notice");
let audio = null;
async function play(digest) {
    try {
        const a = await magi.getAudio({ digest });
        audio?.pause();
        if (audio)
            URL.revokeObjectURL(audio.src);
        audio = new Audio(URL.createObjectURL(new Blob([a.body], { type: a.mediaType })));
        audio.onplay = () => setClipPlaying(true);
        audio.onpause = audio.onended = () => setClipPlaying(false);
        await audio.play();
    }
    catch (e) {
        fail("Could not play the reply")(e);
    }
}
// A message that names another persona ("Engineering, ...") goes to that
// persona; the view follows it.
async function send(text, audioBody, audioType, wake = false) {
    const r = await magi.say({ persona: persona(), text, audio: audioBody, audioType, speakReply: speakReplies() || !!audioBody, wakeWord: wake || undefined });
    if (r.ignored)
        return;
    setNow(Date.now());
    // The tone says that Magi heard the person and builds the reply: for
    // speech, and for text when replies are spoken.
    if (audioBody || speakReplies())
        ack();
    if (r.message.persona && r.message.persona !== persona())
        openStream(r.message.persona);
}
// toggleMic turns hands-free talk on or off (listen.ts). Each turn of the
// person goes to Magi as a voice message; the person can talk over the
// persona to stop it.
async function toggleMic() {
    if (handsFree()) {
        stopListening();
        return;
    }
    try {
        await startListening((wav) => send(undefined, wav, "audio/wav", wakeWord() && Date.now() - lastReplyEnd > followUpMs).catch(fail("Could not send what you said")));
        if (!speakReplies()) {
            setSpeakReplies(true);
            store.set("magi.speak", "1");
        }
    }
    catch (e) {
        fail("Magi cannot use the microphone")(e);
    }
}
// --- Navigation inside the conversation -------------------------------------
// goTo shows an earlier message: in place when it is on the page, else in
// the context panel with the messages around it.
function goTo(id) {
    const box = boxes.get(id);
    if (box) {
        box.scrollIntoView({ behavior: "smooth", block: "center" });
        box.classList.add("flash");
        setTimeout(() => box.classList.remove("flash"), 1600);
        return;
    }
    openTrace(id);
}
async function openTrace(id) {
    setContext({ title: "Context", messages: [], loading: true });
    try {
        const t = await magi.trace({ message: id, direction: "both", depth: 2 });
        setContext({ title: "Context", messages: t.messages.sort((a, b) => a.createdMs - b.createdMs) });
    }
    catch (e) {
        setContext({ title: "Context", messages: [], error: errText(e) });
    }
}
async function search(q) {
    setContext({ title: "Search", messages: [], loading: true });
    try {
        const res = await magi.search({ text: q, limit: 30 });
        setContext({ title: res.messages.length === 1 ? "1 message" : `${res.messages.length} messages`, messages: res.messages });
    }
    catch (e) {
        setContext({ title: "Search", messages: [], error: errText(e) });
    }
}
// spokenText shows the text of a message; while Magi speaks it, the word
// that the person hears is marked, and the words before it are heard.
function spokenText(m) {
    const w = word();
    if (speaking() !== m.id || !w)
        return m.text;
    const rs = Array.from(m.text);
    return [_jsx("span", { class: "heard", children: rs.slice(0, w.start).join("") }), _jsx("mark", { class: "now", children: rs.slice(w.start, w.end).join("") }), _jsx("span", { class: "ahead", children: rs.slice(w.end).join("") })];
}
// --- Message ----------------------------------------------------------------
function About(props) {
    const l = props.l;
    const label = l.kind.replace(/-/g, " ");
    if (l.target.kind === "utterance") {
        return _jsxs("button", { class: "about", onClick: () => goTo(l.target.ref), children: [_jsx("span", { class: "about-kind", children: label }), " \u201C", l.snippet ?? "an earlier message", "\u201D", l.targetMs ? _jsxs("span", { class: "about-when", children: [" ", shortDay(l.targetMs)] }) : ""] });
    }
    if (l.target.kind === "workflow-run") {
        return _jsxs("button", { class: "about", onClick: () => go("task", l.target.ref), children: [_jsx("span", { class: "about-kind", children: label }), " task ", l.target.title ?? l.target.ref] });
    }
    return _jsxs("span", { class: "about", children: [_jsx("span", { class: "about-kind", children: label }), " ", l.target.title ?? l.target.ref] });
}
export function MessageView(props) {
    const m = props.m;
    const mine = m.authorKind === "human";
    const author = mine ? (m.author === me()?.subject ? "You" : who(m.author)) : personaName(m.persona || m.author);
    const [details, setDetails] = createSignal(false);
    const shown = new Set(messages().map((x) => x.id));
    const links = m.links.filter((l) => l.kind !== "reply-to" || !shown.has(l.target.ref));
    const runs = new Set(m.links.filter((l) => l.target.kind === "workflow-run").map((l) => l.target.ref));
    return _jsxs("article", { class: "msg " + (mine ? "mine" : "theirs"), ref: (el) => { if (!props.compact)
            boxes.set(m.id, el); }, onClick: props.pick ? () => props.pick(m.id) : undefined, children: [_jsxs("header", { class: "msg-head", children: [_jsx("span", { class: "msg-author", children: author }), _jsxs("time", { class: "msg-time", title: new Date(m.createdMs).toLocaleString(), children: [props.compact ? shortDay(m.createdMs) + ", " : "", time(m.createdMs)] }), m.modality === "voice" ? _jsx("span", { class: "tag", children: "spoken" }) : m.modality === "notice" ? _jsx("span", { class: "tag notice-tag", children: "notice" }) : "", m.visibility === "shared" ? _jsx("span", { class: "tag shared", children: "household" }) : "", props.compact ? "" : _jsx("button", { class: "msg-more", title: "Links and who can read this", ...aria("expanded", () => String(details())), onClick: () => setDetails(!details()), children: "Details" })] }), _jsx("div", { class: "msg-text", children: props.compact ? m.text : () => spokenText(m) }), !props.compact && (spoken(m) || m.audio)
                ? _jsx("button", { class: "play", onClick: () => (spoken(m) ? (speaking() === m.id ? interrupt("user") : speak(m, 0)) : play(m.audio)), children: () => (speaking() === m.id ? "Stop" : "Play") })
                : "", _jsx(For, { each: props.compact ? [] : links, children: (l) => _jsx(About, { l: l }) }), _jsx(For, { each: props.compact ? [] : m.refs.filter((r) => r.kind === "knowledge-node" || (r.kind === "workflow-run" && !runs.has(r.ref))), children: (r) => r.kind === "knowledge-node"
                    ? _jsxs("span", { class: "about", children: [_jsx("span", { class: "about-kind", children: "knows" }), " ", r.title ?? r.ref] })
                    : _jsxs("button", { class: "about", onClick: () => go("task", r.ref), children: [_jsx("span", { class: "about-kind", children: "task" }), " ", r.title ?? r.ref] }) }), _jsx(Show, { when: () => details(), children: _jsx(Details, { m: m, close: (_) => setDetails(false) }) }), m.generatedBy && !props.compact && !mine ? _jsx("footer", { class: "msg-model", children: m.generatedBy }) : ""] });
}
// Details shows the links of a message and who can read it. The person can
// remove a wrong link and change the visibility; the librarian learns from
// each correction.
function Details(props) {
    const m = props.m;
    const [links, setLinks] = createSignal(null);
    const [error, setError] = createSignal("");
    const load = () => magi.listLinks({ message: m.id, all: true }).then((r) => setLinks(r.links), (e) => setError(errText(e)));
    load();
    const p = personas().find((x) => x.id === (m.persona || persona()));
    const canShare = p && p.sharing !== "never" && m.stream;
    const other = m.visibility === "shared" ? "private" : "shared";
    return _jsxs("div", { class: "details", children: [_jsx(Show, { when: () => error(), children: _jsx("p", { class: "error", children: error }) }), _jsx(Show, { when: () => links(), fallback: _jsx("p", { class: "quiet", children: "Loading the links." }), children: _jsx(Show, { when: () => links().length, fallback: _jsx("p", { class: "quiet", children: "The librarian linked this message to nothing." }), children: _jsx("ul", { class: "links", children: _jsx(For, { each: () => links(), children: (l) => _jsxs("li", { class: "link " + l.state, children: [_jsxs("span", { children: [l.kind.replace(/-/g, " "), " ", l.snippet ? "“" + l.snippet + "”" : (l.target.title ?? l.target.kind + " " + l.target.ref)] }), _jsxs("span", { class: "quiet", children: [" ", l.state, ", by ", who(l.author), ", ", Math.round(l.confidence * 100), "% sure"] }), l.state === "current" || l.state === "tentative"
                                        ? _jsx("button", { class: "text-button", onClick: () => magi.unlink({ link: l.id }).then(load, fail("Could not remove the link")), children: "Not related" }) : ""] }) }) }) }) }), canShare ? _jsxs("p", { class: "visibility", children: [m.visibility === "shared" ? "The household can read this." : "Only you can read this.", _jsx("span", { class: "quiet", children: m.visibilityBy ? ` Set by ${who(m.visibilityBy)}.` : " Not classified yet." }), _jsx("button", { class: "text-button", onClick: () => magi.setVisibility({ message: m.id, visibility: other }).then(replace, fail("Could not change who can read it")), children: other === "shared" ? "Share with the household" : "Make private" })] }) : "", _jsx("button", { class: "text-button", onClick: () => props.close(null), children: "Close" })] });
}
// --- The view -----------------------------------------------------------
export function PersonaOrb(props) {
    return _jsx("span", { class: () => "orb" + (props.size ? " " + props.size : "") + (playing() ? " speaking" : "") + (recording() ? " listening" : "") + (hearing() ? " hearing" : "") + (working() && !playing() ? " working" : ""), "aria-hidden": "true" });
}
export function PersonaList() {
    let q;
    return _jsxs("div", { class: "rail-inner", children: [_jsx("nav", { class: "personas", "aria-label": "Personas", children: _jsx(For, { each: personas, children: (p) => _jsxs("button", { class: () => "persona" + (p.id === persona() ? " active" : ""), onClick: () => { go("talk"); openStream(p.id); }, children: [_jsx("span", { class: "persona-dot" }), _jsx("span", { class: "persona-name", children: p.name }), p.sharing === "public" ? _jsx("span", { class: "persona-note", children: "household" }) : ""] }) }) }), _jsx("form", { class: "search", role: "search", onSubmit: (e) => { e.preventDefault(); const v = q.value.trim(); if (v) {
                    go("talk");
                    search(v);
                } }, children: _jsx("input", { ref: (el) => (q = el), type: "search", placeholder: "Search what was said", "aria-label": "Search what was said" }) })] });
}
export function Conversation() {
    let input;
    const grow = () => { if (input) {
        input.style.height = "auto";
        input.style.height = Math.min(input.scrollHeight, 200) + "px";
    } };
    const submit = async (e) => {
        e?.preventDefault();
        unlock(); // a reply that comes later can play
        const text = input.value.trim();
        if (!text)
            return;
        input.value = "";
        grow();
        try {
            await send(text);
        }
        catch (err) {
            input.value = text;
            grow();
            fail("Could not send")(err);
        }
    };
    const current = () => personas().find((p) => p.id === persona());
    return _jsxs("div", { class: "talk", children: [_jsxs("section", { class: "stream", "aria-label": "Conversation", children: [_jsxs("header", { class: "stream-head", children: [_jsx(PersonaOrb, { size: "large" }), _jsxs("div", { children: [_jsx("h1", { class: "stream-name", children: () => personaName(persona()) }), _jsx("p", { class: "stream-note", children: () => current()?.sharing === "public" ? "The household talks with this persona. Magi asks before it shares a message." : "Only you talk with this persona." })] })] }), _jsxs("div", { class: "scroller", ref: (el) => (scroller = el), children: [_jsx(Show, { when: hasEarlier, children: _jsx("button", { class: "earlier", onClick: loadEarlier, children: "Show earlier messages" }) }), _jsx(Show, { when: loadError, children: _jsxs("p", { class: "error", children: ["Could not read the conversation: ", loadError] }) }), _jsx(Show, { when: () => !loadError() && messages().length === 0, children: _jsxs("p", { class: "empty", children: ["Say hello to ", () => personaName(persona()), ". Type below, or hold a conversation with the microphone."] }) }), _jsxs("div", { class: "turns", children: [_jsx(For, { each: messages, children: (m, i) => _jsxs("div", { class: "turn " + (m.authorKind === "human" ? "mine" : "theirs"), children: [_jsx(Show, { when: () => { const prev = messages()[i() - 1]; return !prev || day(prev.createdMs) !== day(m.createdMs); }, children: _jsx("div", { class: "day", children: _jsx("span", { children: day(m.createdMs) }) }) }), _jsx(MessageView, { m: m })] }) }), _jsx(Show, { when: working, children: _jsx("div", { class: "turn theirs", children: _jsxs("p", { class: "working", role: "status", children: [_jsx(PersonaOrb, { size: "small" }), " ", () => personaName(persona()), " is working on it"] }) }) })] })] }), _jsxs("form", { class: "composer", onSubmit: submit, children: [_jsx("textarea", { ref: (el) => (input = el), rows: "1", "aria-label": "Message", placeholder: () => "Message " + personaName(persona()), onInput: () => { grow(); interrupt("typing"); }, onKeyDown: (e) => { if (e.key === "Enter" && !e.shiftKey)
                                    submit(e); } }), _jsx("button", { class: () => "mic" + (recording() ? " on" : ""), type: "button", onClick: toggleMic, ...aria("pressed", () => String(recording())), children: () => (handsFree() ? (hearing() ? "Hearing you" : "Listening") : "Talk") }), _jsx("button", { class: "send", children: "Send" }), _jsx(Show, { when: handsFree, children: _jsxs("label", { class: "speak-toggle", title: "Magi takes only what starts with a name, such as \u201CComputer, \u2026\u201D", children: [_jsx("input", { type: "checkbox", checked: wakeWord(), onChange: (e) => { const v = e.target.checked; setWakeWord(v); store.set("magi.wake", v ? "1" : "0"); } }), "Wake word"] }) }), _jsxs("label", { class: "speak-toggle", children: [_jsx("input", { type: "checkbox", checked: speakReplies(), onChange: (e) => { const v = e.target.checked; unlock(); setSpeakReplies(v); store.set("magi.speak", v ? "1" : "0"); } }), "Speak replies"] })] })] }), _jsx(Show, { when: context, children: _jsxs("aside", { class: "context", "aria-label": "Context", children: [_jsxs("header", { class: "context-head", children: [_jsx("h2", { children: () => context().title }), _jsx("button", { class: "text-button", onClick: () => setContext(null), children: "Close" })] }), _jsx(Show, { when: () => context().loading, children: _jsx("p", { class: "quiet", children: "Loading." }) }), _jsx(Show, { when: () => context().error, children: _jsx("p", { class: "error", children: () => context().error }) }), _jsx(For, { each: () => context().messages, children: (m) => _jsx(MessageView, { m: m, compact: true, pick: (id) => goTo(id) }) })] }) })] });
}
export async function startConversation() {
    await loadPersonas();
    if (!personas().some((p) => p.id === persona()) && personas().length)
        setPersona(personas()[0].id);
    openStream(persona());
}
