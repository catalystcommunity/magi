// What Magi knows about the household, and how Magi says and hears words.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.ts";
import type { KnowledgeItem, LexiconWord } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, fail, errText, shortDay, who } from "./api.ts";

export function Knowledge() {
  const [items, setItems] = createSignal<KnowledgeItem[] | null>(null);
  const [query, setQuery] = createSignal("");
  const [error, setError] = createSignal("");
  let q: HTMLInputElement | undefined;
  let add: HTMLFormElement | undefined;
  const search = (text: string) => {
    setQuery(text);
    if (q) q.value = text;
    setError("");
    magi.queryKnowledge({ text: text || undefined, limit: 50 }).then((r) => setItems(r.items), (e) => setError(errText(e)));
  };
  search("");
  const remember = async (e: SubmitEvent) => {
    e.preventDefault();
    const f = new FormData(add);
    const v = (k: string) => String(f.get(k) ?? "").trim() || undefined;
    try {
      const it = await magi.remember({ kind: v("kind")!, label: v("label")!, summary: v("summary"), relatedTo: v("related"), relation: v("relation") });
      add!.reset();
      search(it.label);
    } catch (err) { fail("Could not remember it")(err); }
  };
  // A relation such as "works-at Maple Street Dental" or "← hosted-in
  // Maple Street Dental". A click searches for the other item.
  const other = (r: string) => r.replace(/^← /, "").replace(/^\S+\s+/, "");
  return <section class="page">
    <h1>Knowledge</h1>
    <form class="search-row" role="search" onSubmit={(e: SubmitEvent) => { e.preventDefault(); search(q!.value.trim()); }}>
      <input ref={(el: HTMLInputElement) => (q = el)} type="search" placeholder="Search what Magi knows" aria-label="Search what Magi knows" />
      <button>Search</button>
    </form>
    <Show when={error}><p class="error">Could not search: {error}</p></Show>
    <Show when={items} fallback={<Show when={() => !error()}><p class="quiet">Loading.</p></Show>}>
      <Show when={() => items()!.length} fallback={<p class="empty">{() => query()
        ? `Magi knows nothing that matches “${query()}”.`
        : "Magi knows nothing about you yet. Tell Magi a fact, for example “Remember that my dentist is Dr. Rivera”, or add one below."}</p>}>
        <ul class="facts">
          <For each={() => items()!}>{(it: KnowledgeItem) =>
            <li class="fact">
              <p><span class="fact-label">{it.label}</span> <span class="kind">{it.kind}</span></p>
              {it.summary ? <p>{it.summary}</p> : ""}
              {it.system ? <p class="quiet">In {it.system}: {it.locator ?? ""}</p> : ""}
              <div class="relations"><For each={it.relations}>{(r: string) => <button class="about" onClick={() => search(other(r))}>{r}</button>}</For></div>
            </li>}
          </For>
        </ul>
      </Show>
    </Show>
    <details class="add">
      <summary>Add a fact</summary>
      <form class="grid-form" ref={(el: HTMLFormElement) => (add = el)} onSubmit={remember}>
        <label>Kind<select name="kind">
          <option>person</option><option>place</option><option>system</option><option>calendar</option><option>thing</option><option>fact</option>
        </select></label>
        <label>Name<input name="label" required placeholder="Dr. Rivera" /></label>
        <label>Summary<input name="summary" placeholder="My dentist" /></label>
        <label>Related to<input name="related" placeholder="Name of another item (optional)" /></label>
        <label>Relation<input name="relation" placeholder="works-at (optional)" /></label>
        <div class="actions"><button class="primary">Remember</button></div>
      </form>
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
