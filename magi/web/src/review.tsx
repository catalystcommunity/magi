// Questions of the librarian, and network reviews of the egress proxy.
import { createSignal } from "solid-js";
import { For, Show, aria } from "./flow.ts";
import type { ReviewItem, EgressReview } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, refreshCounts, setQuestionCount, setNetworkCount, fail, errText, notify, who } from "./api.ts";
import { MessageView } from "./conversation.tsx";

// The librarian asks only when it is not sure. Each answer becomes an
// example for it, and moves the threshold at which it asks.
export function Questions() {
  const [items, setItems] = createSignal<ReviewItem[] | null>(null);
  const [error, setError] = createSignal("");
  const load = () => magi.listReviews({ limit: 50 }).then((r) => { setItems(r.reviews); setQuestionCount(r.reviews.length); }, (e) => setError(errText(e)));
  load();
  const done = (id: string) => { setItems(items()!.filter((r) => r.id !== id)); setQuestionCount(items()!.length); };
  return <section class="page">
    <h1>Questions</h1>
    <p class="lede">The librarian asks here when it is not sure how a message relates to an earlier one, or who can read it. Each answer teaches it.</p>
    <Show when={error}><p class="error">Could not read the questions: {error}</p></Show>
    <Show when={items} fallback={<p class="quiet">Loading.</p>}>
      <Show when={() => items()!.length} fallback={<p class="empty">No questions now.</p>}>
        <div class="items"><For each={() => items()!}>{(r: ReviewItem) => <Question r={r} done={done} />}</For></div>
      </Show>
    </Show>
  </section>;
}

function Question(props: { r: ReviewItem; done: (id: string) => void }) {
  const r = props.r;
  let note: HTMLInputElement | undefined;
  const answer = (a: string) => magi.answerReview({ id: r.id, answer: a, note: note!.value || undefined }).then(() => props.done(r.id), fail("Could not save the answer"));
  const label = (c: string) => r.task === "visibility" ? (c === "shared" ? "The household can read it" : "Only me")
    : c === "unrelated" ? "Not related" : "Yes, " + c.replace(/-/g, " ");
  return <article class="item">
    <p class="item-title">{r.question} <span class="quiet">{Math.round(r.confidence * 100)}% sure</span></p>
    <MessageView m={r.message} compact />
    {r.target ? <><p class="quiet">The earlier message:</p><MessageView m={r.target} compact /></> : ""}
    <div class="actions">
      <For each={r.choices}>{(c: string) => <button class={c === r.proposed ? "primary" : ""} onClick={() => answer(c)}>{label(c)}</button>}</For>
      <button class="text-button" onClick={() => answer("dismiss")}>Skip</button>
    </div>
    <input ref={(el: HTMLInputElement) => (note = el)} class="note-input" placeholder="Why (optional, the librarian learns from it)" aria-label="Why" />
  </article>;
}

// The egress proxy blocks a request at once when a rule, a plugin, or the
// review model asks for a person, or when the host is not in the policy. An
// admin decides later what the policy does with the next such requests.
// Placeholders stand in for secrets, so a review never shows a secret.
export function Network() {
  const [state, setState] = createSignal<"open" | "decided">("open");
  const [items, setItems] = createSignal<EgressReview[] | null>(null);
  const [error, setError] = createSignal("");
  const load = () => {
    setItems(null);
    setError("");
    magi.listEgressReviews({ state: state(), limit: 100 }).then((r) => {
      setItems(r.reviews);
      if (state() === "open") setNetworkCount(r.reviews.length);
    }, (e) => setError(errText(e)));
  };
  load();
  // A decided review leaves the list at once: an open review moves to
  // "Decided", and a removed rule leaves "Decided".
  const done = (id: string) => setItems(items()!.filter((r) => r.id !== id));
  return <section class="page">
    <div class="page-head">
      <h1>Network</h1>
      <div class="segmented" role="group" aria-label="Which reviews">
        <button {...aria("pressed", () => String(state() === "open"))} onClick={() => { setState("open"); load(); }}>Open</button>
        <button {...aria("pressed", () => String(state() === "decided"))} onClick={() => { setState("decided"); load(); }}>Decided</button>
      </div>
    </div>
    <p class="lede">The egress proxy blocked these requests of tasks. Your decision becomes a rule for the next requests of the same kind.</p>
    <Show when={error}><p class="error">Could not read the reviews: {error}</p></Show>
    <Show when={items} fallback={<Show when={() => !error()}><p class="quiet">Loading.</p></Show>}>
      <Show when={() => items()!.length} fallback={<p class="empty">{() => state() === "open" ? "No blocked requests wait for you." : "No decided reviews."}</p>}>
        <div class="items"><For each={() => items()!}>{(r: EgressReview) => <NetReview r={r} done={done} />}</For></div>
      </Show>
    </Show>
  </section>;
}

function bodyText(b: Uint8Array): string | undefined {
  try { return new TextDecoder("utf-8", { fatal: true }).decode(b); } catch { return undefined; }
}

function NetReview(props: { r: EgressReview; done: (id: string) => void }) {
  const r = props.r;
  let note: HTMLInputElement | undefined;
  const host = r.destination.replace(/:443$/, "");
  const decide = (d: string) => magi.decideEgressReview({ id: r.id, decision: d, note: note?.value || undefined }).then((res) => {
    notify(res.rule ? `Rule added: ${res.rule}.` : r.state === "open" ? "Dismissed. A new blocked request of this kind opens a new review." : "Rule removed.");
    props.done(r.id);
    refreshCounts();
  }, fail("Could not save the decision"));
  const choices: [string, string][] = r.kind === "destination"
    ? [[`Permit ${r.destination}`, "allow-host"], ["Permit at layer 4", "allow-layer4"], ["Block", "block"]]
    : [[`Allow ${r.method} to ${host}`, "allow-host"], [`Allow ${r.method} to this path`, "allow-path"], ["Block", "block"]];
  const text = bodyText(r.body);
  return <article class="item">
    <p class="item-title mono-wrap">{r.kind === "destination" ? r.destination : `${r.method} ${r.url}`}</p>
    <p>{r.reason}</p>
    <p class="quiet">Seen {r.count === 1 ? "once" : `${r.count} times`}, last {new Date(r.lastMs).toLocaleString()}. Stopped by {r.by}. Task {r.environment} of {r.owner}.</p>
    {r.headers.length ? <details><summary>Headers ({r.headers.length})</summary><pre>{r.headers.map((x) => `${x.name}: ${x.value}`).join("\n")}</pre></details> : ""}
    {r.body.length ? <details><summary>Body ({r.body.length} bytes)</summary><pre>{text ?? "(binary data)"}</pre></details> : ""}
    {r.state === "open"
      ? <>
          <div class="actions">
            <For each={choices}>{([label, d]: [string, string]) => <button class={d === "block" ? "danger" : ""} onClick={() => decide(d)}>{label}</button>}</For>
            <button class="text-button" onClick={() => decide("dismiss")}>Dismiss</button>
          </div>
          <input ref={(el: HTMLInputElement) => (note = el)} class="note-input" placeholder="Note (optional)" aria-label="Note" />
        </>
      : <>
          <p class="decided">Decided: {r.decision}, by {who(r.decidedBy ?? "")}{r.note ? `. ${r.note}` : ""}</p>
          {r.decision !== "dismiss" ? <div class="actions"><button onClick={() => decide("dismiss")}>Remove the rule</button></div> : ""}
        </>}
  </article>;
}
