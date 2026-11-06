import { Fragment as _Fragment, jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// What Magi knows about the household, and how Magi says and hears words.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.js";
import { magi, fail, errText, shortDay, who } from "./api.js";
export function Knowledge() {
    const [items, setItems] = createSignal(null);
    const [records, setRecords] = createSignal([]);
    const [query, setQuery] = createSignal("");
    const [past, setPast] = createSignal(false);
    const [error, setError] = createSignal("");
    const [note, setNote] = createSignal("");
    const [history, setHistory] = createSignal(null);
    let q;
    let add;
    const search = (text) => {
        setQuery(text);
        if (q)
            q.value = text;
        setError("");
        magi.queryKnowledge({ text: text || undefined, limit: 50, includeEnded: past() || undefined })
            .then((r) => { setItems(r.items); setRecords(r.records); }, (e) => setError(errText(e)));
    };
    search("");
    const remember = async (e) => {
        e.preventDefault();
        const f = new FormData(add);
        const v = (k) => String(f.get(k) ?? "").trim() || undefined;
        try {
            const r = await magi.remember({ subject: v("subject"), relation: v("relation"), object: v("object"), value: v("value") });
            setNote(r.note ?? "");
            if (r.recorded)
                add.reset();
            search(r.item.label);
        }
        catch (err) {
            fail("Could not remember it")(err);
        }
    };
    // fix: it was wrong. update: it was true and changed. retract: it was
    // never true, or forget it.
    const change = async (id, operation, current) => {
        let value;
        if (operation !== "retract") {
            const v = prompt(operation === "fix" ? "The correct value (the old value was wrong):" : "The new value (the old value was true until now):", current);
            if (v === null || !v.trim())
                return;
            value = v.trim();
        }
        else if (!confirm("Retract this record? Magi will not use it again."))
            return;
        try {
            await magi.changeKnowledge({ id, operation, value });
            search(query());
        }
        catch (err) {
            fail("Could not change it")(err);
        }
    };
    const showHistory = (id) => magi.knowledgeHistory({ id }).then((r) => setHistory({ id, changes: r.changes }), fail("Could not read the history"));
    const factText = (it, f) => {
        const other = f.other ?? f.value ?? "";
        return f.direction === "in" ? `${other} ${f.relation} ${it.label}` : `${f.relation} ${other}`;
    };
    return _jsxs("section", { class: "page", children: [_jsx("h1", { children: "Knowledge" }), _jsxs("form", { class: "search-row", role: "search", onSubmit: (e) => { e.preventDefault(); search(q.value.trim()); }, children: [_jsx("input", { ref: (el) => (q = el), type: "search", placeholder: "Search what Magi knows", "aria-label": "Search what Magi knows" }), _jsx("button", { children: "Search" })] }), _jsxs("label", { class: "quiet", children: [_jsx("input", { type: "checkbox", onChange: (e) => { setPast(e.target.checked); search(query()); } }), " Also facts that are no longer true"] }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not search: ", error] }) }), _jsx(Show, { when: items, fallback: _jsx(Show, { when: () => !error(), children: _jsx("p", { class: "quiet", children: "Loading." }) }), children: _jsx(Show, { when: () => items().length + records().length, fallback: _jsx("p", { class: "empty", children: () => query()
                            ? `Magi knows nothing that matches “${query()}”.`
                            : "Magi knows nothing about you yet. Tell Magi a fact, for example “Remember that my dentist is Dr. Rivera”, or add one below." }), children: _jsxs("ul", { class: "facts", children: [_jsx(For, { each: () => items(), children: (it) => _jsxs("li", { class: "fact", children: [_jsxs("p", { children: [_jsx("span", { class: "fact-label", children: it.label }), " ", _jsx("span", { class: "kind", children: it.kind }), it.aliases.length ? _jsxs("span", { class: "quiet", children: [" also ", it.aliases.join(", ")] }) : ""] }), it.summary ? _jsx("p", { children: it.summary }) : "", it.system ? _jsxs("p", { class: "quiet", children: ["In ", it.system, ": ", it.locator ?? ""] }) : "", _jsx("ul", { class: "relations", children: _jsx(For, { each: it.facts, children: (f) => _jsxs("li", { class: f.status === "current" ? "" : "quiet", children: [_jsx("button", { class: "about", onClick: () => search(f.other ?? f.value ?? ""), children: factText(it, f) }), f.status !== "current" ? _jsx("span", { class: "quiet", children: " no longer true" }) : "", !f.supported ? _jsx("span", { class: "quiet", children: " no evidence" }) : "", f.stale ? _jsx("span", { class: "quiet", children: " from a fact that changed" }) : "", f.disputed ? _jsx("span", { class: "quiet", children: " disputed" }) : "", _jsx(Show, { when: () => f.mine && f.status === "current", children: _jsxs("span", { class: "word-actions", children: [_jsx("button", { class: "text-button", title: "It was wrong", onClick: () => change(f.id, "fix", f.other ?? f.value ?? ""), children: "Fix" }), _jsx("button", { class: "text-button", title: "It was true and changed", onClick: () => change(f.id, "update", f.other ?? f.value ?? ""), children: "Update" }), _jsx("button", { class: "text-button", title: "It was never true, or forget it", onClick: () => change(f.id, "retract", ""), children: "Retract" }), _jsx("button", { class: "text-button", onClick: () => showHistory(f.id), children: "History" })] }) })] }) }) }), _jsx(Show, { when: () => it.mine, children: _jsxs("span", { class: "word-actions", children: [_jsx("button", { class: "text-button", title: "The name was wrong", onClick: () => change(it.id, "fix", it.label), children: "Rename" }), _jsx("button", { class: "text-button", onClick: () => change(it.id, "retract", ""), children: "Retract" }), _jsx("button", { class: "text-button", onClick: () => showHistory(it.id), children: "History" })] }) })] }) }), _jsx(For, { each: records, children: (r) => _jsxs("li", { class: "fact", children: [_jsxs("p", { children: [_jsx("span", { class: "fact-label", children: r.title }), " ", _jsx("span", { class: "kind", children: r.kind }), " ", _jsx("span", { class: "quiet", children: shortDay(r.atMs) })] }), r.summary ? _jsx("p", { children: r.summary }) : ""] }) })] }) }) }), _jsx(Show, { when: history, children: _jsxs("section", { class: "add", children: [_jsx("h2", { children: "Changes" }), _jsx("ul", { class: "facts", children: _jsx(For, { each: () => history().changes, children: (c) => _jsxs("li", { class: "fact", children: [_jsxs("p", { children: [c.operation, " by ", who(c.actor), " on ", shortDay(c.atMs), c.reason ? `: ${c.reason}` : ""] }), c.before ? _jsxs("p", { class: "quiet", children: ["Before: ", c.before] }) : "", c.after ? _jsxs("p", { class: "quiet", children: ["After: ", c.after] }) : ""] }) }) }), _jsx("button", { class: "text-button", onClick: () => setHistory(null), children: "Close" })] }) }), _jsxs("details", { class: "add", children: [_jsx("summary", { children: "Add a fact" }), _jsxs("form", { class: "grid-form", ref: (el) => (add = el), onSubmit: remember, children: [_jsxs("label", { children: ["About", _jsx("input", { name: "subject", required: true, placeholder: "me, Dana, the router" })] }), _jsxs("label", { children: ["Relation", _jsx("input", { name: "relation", required: true, placeholder: "dentist, lives-in, located-in" })] }), _jsxs("label", { children: ["Name of a thing", _jsx("input", { name: "object", placeholder: "Dr. Rivera (or give a value)" })] }), _jsxs("label", { children: ["Value", _jsx("input", { name: "value", placeholder: "March 3, the garage (or give a thing)" })] }), _jsx("div", { class: "actions", children: _jsx("button", { class: "primary", children: "Remember" }) })] }), _jsx(Show, { when: note, children: _jsx("p", { class: "quiet", children: note }) })] })] });
}
// Everybody can read the words and hear them. Lexicon editors (set for
// each installation) can change them.
export function Words() {
    const [words, setWords] = createSignal(null);
    const [canEdit, setCanEdit] = createSignal(false);
    const [error, setError] = createSignal("");
    const [spoken, setSpoken] = createSignal("");
    const [editing, setEditing] = createSignal("");
    let form;
    let tryText;
    const load = () => magi.listLexicon({}).then((r) => { setWords(r.words); setCanEdit(r.canEdit); }, (e) => setError(errText(e)));
    load();
    let audio = null;
    const hear = async (text) => {
        try {
            const res = await magi.speak({ text });
            setSpoken(res.spoken);
            audio?.pause();
            if (audio)
                URL.revokeObjectURL(audio.src);
            audio = new Audio(URL.createObjectURL(new Blob([res.audio], { type: res.mediaType })));
            await audio.play();
        }
        catch (e) {
            fail("Could not speak it")(e);
        }
    };
    const fill = (w) => {
        const set = (k, v) => (form.elements.namedItem(k).value = v);
        set("written", w.written);
        set("spoken", w.spoken ?? "");
        set("ipa", w.ipa ?? "");
        set("heard", w.heardAs.join(", "));
        set("note", w.note ?? "");
        setEditing(w.written);
        form.elements.namedItem("written").focus();
    };
    const clear = () => { form.reset(); setEditing(""); };
    const save = async (e) => {
        e.preventDefault();
        const f = new FormData(form);
        const v = (k) => String(f.get(k) ?? "").trim();
        try {
            await magi.putLexiconWord({
                written: v("written"), spoken: v("spoken") || undefined, ipa: v("ipa").replace(/^\/|\/$/g, "") || undefined,
                heardAs: v("heard").split(",").map((h) => h.trim()).filter(Boolean), note: v("note") || undefined,
            });
            clear();
            load();
        }
        catch (err) {
            fail("Could not save the word")(err);
        }
    };
    const remove = async (w) => {
        if (!confirm(`Remove “${w.written}” from the words?`))
            return;
        magi.deleteLexiconWord({ written: w.written }).then(load, fail("Could not remove the word"));
    };
    return _jsxs("section", { class: "page", children: [_jsx("h1", { children: "Words" }), _jsx("p", { class: "lede", children: "How Magi says words, and how it corrects what it hears. The words apply to all people in the household." }), _jsxs("form", { class: "search-row", onSubmit: (e) => { e.preventDefault(); const t = tryText.value.trim(); if (t)
                    hear(t); }, children: [_jsx("input", { ref: (el) => (tryText = el), required: true, "aria-label": "Text to hear", placeholder: "Type text to hear it, for example: Read the README, it takes 4-6 minutes." }), _jsx("button", { children: "Hear it" })] }), _jsx(Show, { when: spoken, children: _jsxs("p", { class: "quiet", children: ["Magi said: ", spoken] }) }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the words: ", error] }) }), _jsxs(Show, { when: words, children: [_jsx(Show, { when: () => words().length, fallback: _jsx("p", { class: "empty", children: "There are no words yet." }), children: _jsx("ul", { class: "words", children: _jsx(For, { each: () => words(), children: (w) => _jsxs("li", { class: "word", children: [_jsx("span", { class: "word-written", children: w.written }), _jsxs("span", { class: "word-how", children: [w.spoken ? _jsxs(_Fragment, { children: ["says \u201C", w.spoken, "\u201D"] }) : "", w.ipa ? _jsxs("span", { class: "ipa", children: [" /", w.ipa, "/"] }) : "", w.heardAs.length ? _jsxs("span", { class: "quiet", children: [w.spoken || w.ipa ? "; " : "", "heard as ", w.heardAs.join(", ")] }) : ""] }), _jsxs("span", { class: "word-actions", children: [_jsx("button", { class: "text-button", onClick: () => hear(w.written), children: "Hear" }), _jsxs(Show, { when: canEdit, children: [_jsx("button", { class: "text-button", onClick: () => fill(w), children: "Change" }), _jsx("button", { class: "text-button", onClick: () => remove(w), children: "Remove" })] })] }), _jsxs("span", { class: "word-note quiet", children: [w.note ? w.note + ". " : "", "Changed by ", who(w.updatedBy), " on ", shortDay(w.updatedMs), "."] })] }) }) }) }), _jsx(Show, { when: canEdit, fallback: _jsx("p", { class: "quiet", children: "Only a lexicon editor can change the words." }), children: _jsxs("form", { class: "grid-form word-form", ref: (el) => (form = el), onSubmit: save, children: [_jsx("h2", { children: () => (editing() ? `Change “${editing()}”` : "Add a word") }), _jsxs("label", { children: ["Written", _jsx("input", { name: "written", required: true, placeholder: "README" })] }), _jsxs("label", { children: ["Say it as", _jsx("input", { name: "spoken", placeholder: "readme (optional)" })] }), _jsxs("label", { children: ["Phonemes", _jsx("input", { name: "ipa", placeholder: "misaki IPA, for example \u0279\u02C8idmi (optional)" })] }), _jsxs("label", { children: ["Heard as", _jsx("input", { name: "heard", placeholder: "What speech recognition writes, comma-separated (optional)" })] }), _jsxs("label", { children: ["Note", _jsx("input", { name: "note", placeholder: "Optional" })] }), _jsxs("div", { class: "actions", children: [_jsx("button", { class: "primary", children: "Save" }), _jsx("button", { type: "button", class: "text-button", onClick: clear, children: "Clear" })] })] }) })] })] });
}
