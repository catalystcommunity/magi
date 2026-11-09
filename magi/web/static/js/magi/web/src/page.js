import { jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// A page that a persona made (get-page): Markdown with mermaid diagrams.
// The renderer makes DOM nodes, never HTML text, so a page cannot run a
// script. Mermaid (vendored) loads only when a page has a diagram; it runs
// with securityLevel "strict".
import { createSignal } from "solid-js";
import { Show } from "./flow.js";
import { magi, go, errText, time, personaName } from "./api.js";
import { el, markdown } from "./markdown.js";
let mermaidLoad = null;
function loadMermaid() {
    mermaidLoad ??= new Promise((ok, fail) => {
        const s = document.createElement("script");
        // The vendor directory is beside js/ under the same content-hash prefix.
        s.src = new URL("../../../../vendor/mermaid/mermaid.min.js", import.meta.url).href;
        s.onload = () => {
            const m = window.mermaid;
            const dark = matchMedia("(prefers-color-scheme: dark)").matches;
            m.initialize({ startOnLoad: false, securityLevel: "strict", theme: dark ? "dark" : "neutral" });
            ok(m);
        };
        s.onerror = () => fail(new Error("the diagram library did not load"));
        document.head.append(s);
    });
    return mermaidLoad;
}
let seq = 0;
async function draw(diagrams) {
    if (!diagrams.length)
        return;
    let m;
    try {
        m = await loadMermaid();
    }
    catch (e) {
        for (const d of diagrams)
            d.box.textContent = errText(e);
        return;
    }
    for (const d of diagrams) {
        try {
            const { svg } = await m.render("magi-diagram-" + ++seq, d.code);
            // securityLevel "strict" makes mermaid sanitize the SVG.
            d.box.innerHTML = svg;
        }
        catch (e) {
            d.box.textContent = "";
            const pre = el("pre");
            pre.append(el("code", "", d.code));
            d.box.append(el("p", "error", "The diagram has an error: " + errText(e)), pre);
        }
    }
}
export function PageView(props) {
    const [page, setPage] = createSignal(null);
    const [error, setError] = createSignal("");
    magi.getPage({ id: props.id }).then(setPage, (e) => setError(errText(e)));
    return _jsxs("section", { class: "page-view", children: [_jsx("button", { class: "back", onClick: (_) => (history.length > 1 ? history.back() : go("talk")), children: "Back" }), _jsx(Show, { when: () => error(), children: _jsx("p", { class: "error", children: error }) }), _jsx(Show, { when: () => page(), children: _jsxs("article", { class: "page", children: [_jsx("h1", { children: () => page().title }), _jsx("p", { class: "page-meta", children: () => personaName(page().persona || "persona:magi") + " · " + time(page().createdMs) }), _jsx("div", { class: "page-body", ref: (e) => { const p = page(); if (p)
                                draw(markdown(e, p.text)); } })] }) })] });
}
