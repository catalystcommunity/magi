// A page that a persona made (get-page): Markdown with mermaid diagrams.
// The renderer makes DOM nodes, never HTML text, so a page cannot run a
// script. Mermaid (vendored) loads only when a page has a diagram; it runs
// with securityLevel "strict".
import { createSignal } from "solid-js";
import { Show } from "./flow.ts";
import type { Page } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, go, errText, time, personaName } from "./api.ts";
import { el, markdown, type Diagram } from "./markdown.ts";

type Mermaid = { initialize(o: object): void; render(id: string, code: string): Promise<{ svg: string }> };
let mermaidLoad: Promise<Mermaid> | null = null;
function loadMermaid(): Promise<Mermaid> {
  mermaidLoad ??= new Promise((ok, fail) => {
    const s = document.createElement("script");
    // The vendor directory is beside js/ under the same content-hash prefix.
    s.src = new URL("../../../../vendor/mermaid/mermaid.min.js", import.meta.url).href;
    s.onload = () => {
      const m = (window as unknown as { mermaid: Mermaid }).mermaid;
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
async function draw(diagrams: Diagram[]) {
  if (!diagrams.length) return;
  let m: Mermaid;
  try { m = await loadMermaid(); } catch (e) { for (const d of diagrams) d.box.textContent = errText(e); return; }
  for (const d of diagrams) {
    try {
      const { svg } = await m.render("magi-diagram-" + ++seq, d.code);
      // securityLevel "strict" makes mermaid sanitize the SVG.
      d.box.innerHTML = svg;
    } catch (e) {
      d.box.textContent = "";
      const pre = el("pre");
      pre.append(el("code", "", d.code));
      d.box.append(el("p", "error", "The diagram has an error: " + errText(e)), pre);
    }
  }
}

export function PageView(props: { id: string }) {
  const [page, setPage] = createSignal<Page | null>(null);
  const [error, setError] = createSignal("");
  magi.getPage({ id: props.id }).then(setPage, (e) => setError(errText(e)));
  return <section class="page-view">
    <button class="back" onClick={(_: unknown) => (history.length > 1 ? history.back() : go("talk"))}>Back</button>
    <Show when={() => error()}><p class="error">{error}</p></Show>
    <Show when={() => page()}>
      <article class="page">
        <h1>{() => page()!.title}</h1>
        <p class="page-meta">{() => personaName(page()!.persona || "persona:magi") + " · " + time(page()!.createdMs)}</p>
        <div class="page-body" ref={(e: HTMLElement) => { const p = page(); if (p) draw(markdown(e, p.text)); }} />
      </article>
    </Show>
  </section>;
}
