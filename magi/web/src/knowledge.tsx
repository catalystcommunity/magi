// What Magi knows about the household, and how Magi says and hears words.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.ts";
import type { KnowledgeChange, KnowledgeFact, KnowledgeItem, KnowledgeRecord, LexiconWord } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, fail, errText, shortDay, who } from "./api.ts";

export function Knowledge() {
  const [items, setItems] = createSignal<KnowledgeItem[] | null>(null);
  const [records, setRecords] = createSignal<KnowledgeRecord[]>([]);
  const [query, setQuery] = createSignal("");
  const [past, setPast] = createSignal(false);
  const [error, setError] = createSignal("");
  const [note, setNote] = createSignal("");
  const [history, setHistory] = createSignal<{ id: string; changes: KnowledgeChange[] } | null>(null);
  let q: HTMLInputElement | undefined;
  let add: HTMLFormElement | undefined;
  const search = (text: string) => {
    setQuery(text);
    if (q) q.value = text;
    setError("");
    magi.queryKnowledge({ text: text || undefined, limit: 50, includeEnded: past() || undefined })
      .then((r) => { setItems(r.items); setRecords(r.records); }, (e) => setError(errText(e)));
  };
  search("");
  const remember = async (e: SubmitEvent) => {
    e.preventDefault();
    const f = new FormData(add);
    const v = (k: string) => String(f.get(k) ?? "").trim() || undefined;
    try {
      const r = await magi.remember({ subject: v("subject")!, relation: v("relation")!, object: v("object"), value: v("value") });
      setNote(r.note ?? "");
      if (r.recorded) add!.reset();
      search(r.item.label);
    } catch (err) { fail("Could not remember it")(err); }
  };
  // fix: it was wrong. update: it was true and changed. retract: it was
  // never true, or forget it.
  const change = async (id: string, operation: "fix" | "update" | "retract", current: string) => {
    let value: string | undefined;
    if (operation !== "retract") {
      const v = prompt(operation === "fix" ? "The correct value (the old value was wrong):" : "The new value (the old value was true until now):", current);
      if (v === null || !v.trim()) return;
      value = v.trim();
    } else if (!confirm("Retract this record? Magi will not use it again.")) return;
    try {
      await magi.changeKnowledge({ id, operation, value });
      search(query());
    } catch (err) { fail("Could not change it")(err); }
  };
  const showHistory = (id: string) => magi.knowledgeHistory({ id }).then((r) => setHistory({ id, changes: r.changes }), fail("Could not read the history"));
  const factText = (it: KnowledgeItem, f: KnowledgeFact) => {
    const other = f.other ?? f.value ?? "";
    return f.direction === "in" ? `${other} ${f.relation} ${it.label}` : `${f.relation} ${other}`;
  };
  return <section class="page">
    <h1>Knowledge</h1>
    <form class="search-row" role="search" onSubmit={(e: SubmitEvent) => { e.preventDefault(); search(q!.value.trim()); }}>
      <input ref={(el: HTMLInputElement) => (q = el)} type="search" placeholder="Search what Magi knows" aria-label="Search what Magi knows" />
      <button>Search</button>
    </form>
    <label class="quiet"><input type="checkbox" onChange={(e: Event) => { setPast((e.target as HTMLInputElement).checked); search(query()); }} /> Also facts that are no longer true</label>
    <Show when={error}><p class="error">Could not search: {error}</p></Show>
    <Show when={items} fallback={<Show when={() => !error()}><p class="quiet">Loading.</p></Show>}>
      <Show when={() => items()!.length + records().length} fallback={<p class="empty">{() => query()
        ? `Magi knows nothing that matches “${query()}”.`
        : "Magi knows nothing about you yet. Tell Magi a fact, for example “Remember that my dentist is Dr. Rivera”, or add one below."}</p>}>
        <ul class="facts">
          <For each={() => items()!}>{(it: KnowledgeItem) =>
            <li class="fact">
              <p><span class="fact-label">{it.label}</span> <span class="kind">{it.kind}</span>{it.aliases.length ? <span class="quiet"> also {it.aliases.join(", ")}</span> : ""}</p>
              {it.summary ? <p>{it.summary}</p> : ""}
              {it.system ? <p class="quiet">In {it.system}: {it.locator ?? ""}</p> : ""}
              <ul class="relations">
                <For each={it.facts}>{(f: KnowledgeFact) =>
                  <li class={f.status === "current" ? "" : "quiet"}>
                    <button class="about" onClick={() => search(f.other ?? f.value ?? "")}>{factText(it, f)}</button>
                    {f.status !== "current" ? <span class="quiet"> no longer true</span> : ""}
                    {!f.supported ? <span class="quiet"> no evidence</span> : ""}
                    {f.stale ? <span class="quiet"> from a fact that changed</span> : ""}
                    {f.disputed ? <span class="quiet"> disputed</span> : ""}
                    <Show when={() => f.mine && f.status === "current"}>
                      <span class="word-actions">
                        <button class="text-button" title="It was wrong" onClick={() => change(f.id, "fix", f.other ?? f.value ?? "")}>Fix</button>
                        <button class="text-button" title="It was true and changed" onClick={() => change(f.id, "update", f.other ?? f.value ?? "")}>Update</button>
                        <button class="text-button" title="It was never true, or forget it" onClick={() => change(f.id, "retract", "")}>Retract</button>
                        <button class="text-button" onClick={() => showHistory(f.id)}>History</button>
                      </span>
                    </Show>
                  </li>}
                </For>
              </ul>
              <Show when={() => it.mine}>
                <span class="word-actions">
                  <button class="text-button" title="The name was wrong" onClick={() => change(it.id, "fix", it.label)}>Rename</button>
                  <button class="text-button" onClick={() => change(it.id, "retract", "")}>Retract</button>
                  <button class="text-button" onClick={() => showHistory(it.id)}>History</button>
                </span>
              </Show>
            </li>}
          </For>
          <For each={records}>{(r: KnowledgeRecord) =>
            <li class="fact"><p><span class="fact-label">{r.title}</span> <span class="kind">{r.kind}</span> <span class="quiet">{shortDay(r.atMs)}</span></p>{r.summary ? <p>{r.summary}</p> : ""}</li>}
          </For>
        </ul>
      </Show>
    </Show>
    <Show when={history}>
      <section class="add">
        <h2>Changes</h2>
        <ul class="facts">
          <For each={() => history()!.changes}>{(c: KnowledgeChange) =>
            <li class="fact"><p>{c.operation} by {who(c.actor)} on {shortDay(c.atMs)}{c.reason ? `: ${c.reason}` : ""}</p>
              {c.before ? <p class="quiet">Before: {c.before}</p> : ""}{c.after ? <p class="quiet">After: {c.after}</p> : ""}</li>}
          </For>
        </ul>
        <button class="text-button" onClick={() => setHistory(null)}>Close</button>
      </section>
    </Show>
    <details class="add">
      <summary>Add a fact</summary>
      <form class="grid-form" ref={(el: HTMLFormElement) => (add = el)} onSubmit={remember}>
        <label>About<input name="subject" required placeholder="me, Dana, the router" /></label>
        <label>Relation<input name="relation" required placeholder="dentist, lives-in, located-in" /></label>
        <label>Name of a thing<input name="object" placeholder="Dr. Rivera (or give a value)" /></label>
        <label>Value<input name="value" placeholder="March 3, the garage (or give a thing)" /></label>
        <div class="actions"><button class="primary">Remember</button></div>
      </form>
      <Show when={note}><p class="quiet">{note}</p></Show>
    </details>
  </section>;
}

// Everybody can read the words and hear them. Lexicon editors (set for
// each installation) can change them.
export function Words() {
  const [words, setWords] = createSignal<LexiconWord[] | null>(null);
  const [canEdit, setCanEdit] = createSignal(false);
  const [error, setError] = createSignal("");
  const [spoken, setSpoken] = createSignal("");
  const [editing, setEditing] = createSignal("");
  let form: HTMLFormElement | undefined;
  let tryText: HTMLInputElement | undefined;
  const load = () => magi.listLexicon({}).then((r) => { setWords(r.words); setCanEdit(r.canEdit); }, (e) => setError(errText(e)));
  load();
  let audio: HTMLAudioElement | null = null;
  const hear = async (text: string) => {
    try {
      const res = await magi.speak({ text });
      setSpoken(res.spoken);
      audio?.pause();
      if (audio) URL.revokeObjectURL(audio.src);
      audio = new Audio(URL.createObjectURL(new Blob([res.audio as BlobPart], { type: res.mediaType })));
      await audio.play();
    } catch (e) { fail("Could not speak it")(e); }
  };
  const fill = (w: LexiconWord) => {
    const set = (k: string, v: string) => ((form!.elements.namedItem(k) as HTMLInputElement).value = v);
    set("written", w.written); set("spoken", w.spoken ?? ""); set("ipa", w.ipa ?? ""); set("heard", w.heardAs.join(", ")); set("note", w.note ?? "");
    setEditing(w.written);
    (form!.elements.namedItem("written") as HTMLInputElement).focus();
  };
  const clear = () => { form!.reset(); setEditing(""); };
  const save = async (e: SubmitEvent) => {
    e.preventDefault();
    const f = new FormData(form);
    const v = (k: string) => String(f.get(k) ?? "").trim();
    try {
      await magi.putLexiconWord({
        written: v("written"), spoken: v("spoken") || undefined, ipa: v("ipa").replace(/^\/|\/$/g, "") || undefined,
        heardAs: v("heard").split(",").map((h) => h.trim()).filter(Boolean), note: v("note") || undefined,
      });
      clear();
      load();
    } catch (err) { fail("Could not save the word")(err); }
  };
  const remove = async (w: LexiconWord) => {
    if (!confirm(`Remove “${w.written}” from the words?`)) return;
    magi.deleteLexiconWord({ written: w.written }).then(load, fail("Could not remove the word"));
  };
  return <section class="page">
    <h1>Words</h1>
    <p class="lede">How Magi says words, and how it corrects what it hears. The words apply to all people in the household.</p>
    <form class="search-row" onSubmit={(e: SubmitEvent) => { e.preventDefault(); const t = tryText!.value.trim(); if (t) hear(t); }}>
      <input ref={(el: HTMLInputElement) => (tryText = el)} required aria-label="Text to hear" placeholder="Type text to hear it, for example: Read the README, it takes 4-6 minutes." />
      <button>Hear it</button>
    </form>
    <Show when={spoken}><p class="quiet">Magi said: {spoken}</p></Show>
    <Show when={error}><p class="error">Could not read the words: {error}</p></Show>
    <Show when={words}>
      <Show when={() => words()!.length} fallback={<p class="empty">There are no words yet.</p>}>
        <ul class="words">
          <For each={() => words()!}>{(w: LexiconWord) =>
            <li class="word">
              <span class="word-written">{w.written}</span>
              <span class="word-how">
                {w.spoken ? <>says “{w.spoken}”</> : ""}{w.ipa ? <span class="ipa"> /{w.ipa}/</span> : ""}
                {w.heardAs.length ? <span class="quiet">{w.spoken || w.ipa ? "; " : ""}heard as {w.heardAs.join(", ")}</span> : ""}
              </span>
              <span class="word-actions">
                <button class="text-button" onClick={() => hear(w.written)}>Hear</button>
                <Show when={canEdit}><button class="text-button" onClick={() => fill(w)}>Change</button><button class="text-button" onClick={() => remove(w)}>Remove</button></Show>
              </span>
              <span class="word-note quiet">{w.note ? w.note + ". " : ""}Changed by {who(w.updatedBy)} on {shortDay(w.updatedMs)}.</span>
            </li>}
          </For>
        </ul>
      </Show>
      <Show when={canEdit} fallback={<p class="quiet">Only a lexicon editor can change the words.</p>}>
        <form class="grid-form word-form" ref={(el: HTMLFormElement) => (form = el)} onSubmit={save}>
          <h2>{() => (editing() ? `Change “${editing()}”` : "Add a word")}</h2>
          <label>Written<input name="written" required placeholder="README" /></label>
          <label>Say it as<input name="spoken" placeholder="readme (optional)" /></label>
          <label>Phonemes<input name="ipa" placeholder="misaki IPA, for example ɹˈidmi (optional)" /></label>
          <label>Heard as<input name="heard" placeholder="What speech recognition writes, comma-separated (optional)" /></label>
          <label>Note<input name="note" placeholder="Optional" /></label>
          <div class="actions"><button class="primary">Save</button><button type="button" class="text-button" onClick={clear}>Clear</button></div>
        </form>
      </Show>
    </Show>
  </section>;
}
