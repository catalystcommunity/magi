var __rewriteRelativeImportExtension = (this && this.__rewriteRelativeImportExtension) || function (path, preserveJsx) {
    if (typeof path === "string" && /^\.\.?\//.test(path)) {
        return path.replace(/\.(tsx)$|((?:\.d)?)((?:\.[^./]+?)?)\.([cm]?)ts$/i, function (m, tsx, d, ext, cm) {
            return tsx ? preserveJsx ? ".jsx" : ".js" : d && (!ext || !cm) ? m : (d + ext + "." + cm.toLowerCase() + "js");
        });
    }
    return path;
};
import { jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// A terminal in the sandbox of a running task. The page opens the
// WebSocket /api/terminal?task=ID on this origin; the first message is the
// hello with the token and the size, and then each message is one terminal
// frame: a type byte and the payload (primitives/execution/envd/frame.go).
// xterm.js (vendored) loads only on this page. Phones get a row of keys
// that their keyboards do not have.
import { createSignal, onCleanup, onMount } from "solid-js";
import { For, Show } from "./flow.js";
import { magi, authToken, errText } from "./api.js";
let xtermLoad = null;
export function loadXterm() {
    xtermLoad ??= (async () => {
        // The vendor directory is beside js/ under the same content-hash prefix.
        const base = new URL("../../../../vendor/xterm/", import.meta.url).href;
        const css = document.createElement("link");
        css.rel = "stylesheet";
        css.href = base + "xterm.css";
        document.head.append(css);
        try {
            const [x, f] = await Promise.all([import(__rewriteRelativeImportExtension(base + "xterm.mjs")), import(__rewriteRelativeImportExtension(base + "addon-fit.mjs"))]);
            return { Terminal: x.Terminal, FitAddon: f.FitAddon };
        }
        catch {
            xtermLoad = null;
            throw new Error("the terminal library did not load");
        }
    })();
    return xtermLoad;
}
// Frame types.
const DATA = 0x64, RESIZE = 0x72, EXIT = 0x78, ERROR = 0x65, PING = 0x6b;
export function terminalURL(task) {
    const u = new URL("/api/terminal", location.href);
    u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
    u.search = "?task=" + encodeURIComponent(task);
    return u.href;
}
// ctrl gives the control character of a key (Ctrl and the key), or null.
export function ctrl(ch) {
    if (ch.length !== 1)
        return null;
    const c = ch.toUpperCase().charCodeAt(0);
    if (c >= 0x40 && c <= 0x5f)
        return String.fromCharCode(c & 0x1f);
    if (ch === " " || ch === "2")
        return "\x00";
    if (ch === "?")
        return "\x7f";
    return null;
}
const arrow = (c) => (app) => (app ? "\x1bO" : "\x1b[") + c;
export const extraKeys = [
    { label: "Esc", name: "Escape", seq: () => "\x1b" },
    { label: "Tab", name: "Tab", seq: () => "\t" },
    { label: "Ctrl", name: "Control (for the next key)" },
    { label: "←", name: "Left", seq: arrow("D") },
    { label: "↑", name: "Up", seq: arrow("A") },
    { label: "↓", name: "Down", seq: arrow("B") },
    { label: "→", name: "Right", seq: arrow("C") },
    { label: "|", name: "Pipe", seq: () => "|" },
    { label: "~", name: "Tilde", seq: () => "~" },
    { label: "/", name: "Slash", seq: () => "/" },
    { label: "-", name: "Dash", seq: () => "-" },
];
// Session is one connection: one shell. After it closes, a new Session
// starts a new shell.
export class Session {
    term;
    url;
    token;
    status;
    ws = null;
    ctrlNext = false;
    ping = 0;
    ended = false;
    enc = new TextEncoder();
    constructor(term, url, token, status) {
        this.term = term;
        this.url = url;
        this.token = token;
        this.status = status;
    }
    connect() {
        this.ended = false;
        this.status("Connecting to the sandbox.", "connecting");
        const ws = new WebSocket(this.url);
        this.ws = ws;
        ws.binaryType = "arraybuffer";
        let first = true;
        ws.onopen = () => {
            ws.send(JSON.stringify({ token: this.token() ?? "", cols: this.term.cols, rows: this.term.rows }));
            this.status("Waiting for the sandbox. A runner can take up to 20 seconds.", "connecting");
            this.ping = setInterval(() => this.send(PING, new Uint8Array()), 25000);
        };
        ws.onmessage = (ev) => {
            if (!(ev.data instanceof ArrayBuffer) || ev.data.byteLength === 0)
                return;
            const b = new Uint8Array(ev.data);
            const p = b.subarray(1);
            switch (b[0]) {
                case DATA:
                    if (first) {
                        first = false;
                        this.status("Connected. The shell runs in the sandbox of the task.", "open");
                    }
                    this.term.write(p);
                    break;
                case EXIT: {
                    const code = p.length === 4 ? new DataView(p.buffer, p.byteOffset, 4).getInt32(0) : -1;
                    this.end(`The shell exited with code ${code}.`);
                    break;
                }
                case ERROR:
                    this.end(new TextDecoder().decode(p));
                    break;
            }
        };
        ws.onclose = () => this.end("The connection closed.");
    }
    end(text) {
        clearInterval(this.ping);
        if (this.ended)
            return;
        this.ended = true;
        this.status(text, "closed");
        this.ws?.close();
    }
    send(type, payload) {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN || this.ended)
            return false;
        const b = new Uint8Array(1 + payload.length);
        b[0] = type;
        b.set(payload, 1);
        this.ws.send(b);
        return true;
    }
    // input sends text that the person typed. After the Ctrl key, a letter
    // becomes its control character.
    input(s) {
        if (this.ctrlNext) {
            this.ctrlNext = false;
            const c = ctrl(s);
            if (c !== null)
                s = c;
        }
        this.send(DATA, this.enc.encode(s));
    }
    // binary sends bytes in a string of 8-bit characters (mouse reports).
    binary(s) {
        this.send(DATA, Uint8Array.from(s, (c) => c.charCodeAt(0) & 0xff));
    }
    resize(cols, rows) {
        const p = new Uint8Array(4);
        new DataView(p.buffer).setUint16(0, cols);
        new DataView(p.buffer).setUint16(2, rows);
        this.send(RESIZE, p);
    }
    close() { this.end("You closed the terminal."); }
}
// openTerminal makes a terminal in box and connects it to url.
export async function openTerminal(box, url, token, status) {
    const { Terminal, FitAddon } = await loadXterm();
    const narrow = matchMedia("(max-width: 600px)").matches;
    // The colors of the page theme (the style.css tokens).
    const css = getComputedStyle(document.documentElement);
    const tok = (n, d) => css.getPropertyValue(n).trim() || d;
    const term = new Terminal({
        fontFamily: tok("--mono", "monospace"), fontSize: narrow ? 12 : 14, cursorBlink: true, scrollback: 5000,
        theme: { background: tok("--cloud", "#fafcfe"), foreground: tok("--harbor", "#1f3a5f"), cursor: tok("--blue", "#2f6fca"),
            cursorAccent: tok("--cloud", "#fafcfe"), selectionBackground: tok("--line", "#c9dbee") },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(box);
    fit.fit();
    const session = new Session(term, url, token, status);
    term.onData((d) => session.input(d));
    term.onBinary((d) => session.binary(d));
    term.onResize(({ cols, rows }) => session.resize(cols, rows));
    // Fit to the box when the window, the phone keyboard, or the layout
    // changes the size.
    const ro = new ResizeObserver(() => { try {
        fit.fit();
    }
    catch { /* not visible */ } });
    ro.observe(box);
    session.connect();
    term.focus();
    return { term, session, fit, dispose() { ro.disconnect(); session.close(); term.dispose(); } };
}
export function TerminalView(props) {
    const [status, setStatus] = createSignal("Loading the terminal.");
    const [state, setState] = createSignal("connecting");
    const [ctrlOn, setCtrlOn] = createSignal(false);
    const [title, setTitle] = createSignal("");
    magi.getTask({ id: props.id }).then((t) => setTitle(t.title), () => { });
    let box;
    let t = null;
    let disposed = false;
    const onStatus = (text, s) => { setStatus(text); setState(s); };
    // The phone keyboard makes the visual viewport smaller: the page takes
    // only the part above it.
    const vv = window.visualViewport;
    const sizeToViewport = () => {
        document.documentElement.style.setProperty("--vvh", (vv?.height ?? innerHeight) + "px");
        window.scrollTo(0, 0);
    };
    onMount(() => {
        sizeToViewport();
        vv?.addEventListener("resize", sizeToViewport);
        openTerminal(box, terminalURL(props.id), authToken, onStatus).then((x) => {
            if (disposed)
                x.dispose();
            else
                t = x;
        }, (e) => onStatus(errText(e), "closed"));
    });
    onCleanup(() => {
        disposed = true;
        vv?.removeEventListener("resize", sizeToViewport);
        document.documentElement.style.removeProperty("--vvh");
        t?.dispose();
    });
    const press = (k) => {
        if (!t)
            return;
        if (!k.seq) {
            t.session.ctrlNext = !t.session.ctrlNext;
            setCtrlOn(t.session.ctrlNext);
        }
        else {
            t.session.input(k.seq(t.term.modes.applicationCursorKeysMode));
            setCtrlOn(false);
        }
        t.term.focus();
    };
    const reconnect = () => { if (t) {
        t.term.write("\r\n");
        t.session.connect();
        t.term.focus();
    } };
    return _jsxs("section", { class: "term-page", children: [_jsxs("div", { class: "term-head", children: [_jsx("a", { class: "back", href: "#/task/" + encodeURIComponent(props.id), children: "Task" }), _jsx("h1", { class: "term-title", children: () => title() || props.id }), _jsx("span", { class: () => "term-state " + state(), children: () => (state() === "open" ? "Connected" : state() === "connecting" ? "Connecting" : "Closed") })] }), _jsx("div", { class: "term-box", ref: (el) => (box = el) }), _jsx("div", { class: "term-keys", role: "toolbar", "aria-label": "Extra keys", children: _jsx(For, { each: extraKeys, children: (k) => _jsx("button", { type: "button", class: () => "term-key" + (!k.seq && ctrlOn() ? " on" : ""), "aria-label": k.name, onPointerDown: (e) => e.preventDefault(), onClick: () => press(k), children: k.label }) }) }), _jsxs("div", { class: "term-foot", children: [_jsx("p", { class: "quiet term-status", role: "status", children: status }), _jsxs("div", { class: "actions", children: [_jsx(Show, { when: () => state() === "closed" && t !== null, children: _jsx("button", { class: "primary", onClick: reconnect, children: "Reconnect" }) }), _jsx(Show, { when: () => state() !== "closed", children: _jsx("button", { onClick: () => t?.session.close(), children: "Close" }) })] })] })] });
}
