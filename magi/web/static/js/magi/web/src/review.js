import { Fragment as _Fragment, jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// Questions of the librarian, and network reviews of the egress proxy.
import { createSignal } from "solid-js";
import { For, Show, aria } from "./flow.js";
import { magi, refreshCounts, setQuestionCount, setNetworkCount, fail, errText, notify, who } from "./api.js";
import { MessageView } from "./conversation.js";
// The librarian asks only when it is not sure. Each answer becomes an
// example for it, and moves the threshold at which it asks.
export function Questions() {
    const [items, setItems] = createSignal(null);
    const [error, setError] = createSignal("");
    const load = () => magi.listReviews({ limit: 50 }).then((r) => { setItems(r.reviews); setQuestionCount(r.reviews.length); }, (e) => setError(errText(e)));
    load();
    const done = (id) => { setItems(items().filter((r) => r.id !== id)); setQuestionCount(items().length); };
    return _jsxs("section", { class: "page", children: [_jsx("h1", { children: "Questions" }), _jsx("p", { class: "lede", children: "The librarian asks here when it is not sure how a message relates to an earlier one, or who can read it. Each answer teaches it." }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the questions: ", error] }) }), _jsx(Show, { when: items, fallback: _jsx("p", { class: "quiet", children: "Loading." }), children: _jsx(Show, { when: () => items().length, fallback: _jsx("p", { class: "empty", children: "No questions now." }), children: _jsx("div", { class: "items", children: _jsx(For, { each: () => items(), children: (r) => _jsx(Question, { r: r, done: done }) }) }) }) })] });
}
function Question(props) {
    const r = props.r;
    let note;
    const answer = (a) => magi.answerReview({ id: r.id, answer: a, note: note.value || undefined }).then(() => props.done(r.id), fail("Could not save the answer"));
    const label = (c) => r.task === "visibility" ? (c === "shared" ? "The household can read it" : "Only me")
        : c === "unrelated" ? "Not related" : "Yes, " + c.replace(/-/g, " ");
    return _jsxs("article", { class: "item", children: [_jsxs("p", { class: "item-title", children: [r.question, " ", _jsxs("span", { class: "quiet", children: [Math.round(r.confidence * 100), "% sure"] })] }), _jsx(MessageView, { m: r.message, compact: true }), r.target ? _jsxs(_Fragment, { children: [_jsx("p", { class: "quiet", children: "The earlier message:" }), _jsx(MessageView, { m: r.target, compact: true })] }) : "", _jsxs("div", { class: "actions", children: [_jsx(For, { each: r.choices, children: (c) => _jsx("button", { class: c === r.proposed ? "primary" : "", onClick: () => answer(c), children: label(c) }) }), _jsx("button", { class: "text-button", onClick: () => answer("dismiss"), children: "Skip" })] }), _jsx("input", { ref: (el) => (note = el), class: "note-input", placeholder: "Why (optional, the librarian learns from it)", "aria-label": "Why" })] });
}
// The egress proxy blocks a request at once when a rule, a plugin, or the
// review model asks for a person, or when the host is not in the policy. An
// admin decides later what the policy does with the next such requests.
// Placeholders stand in for secrets, so a review never shows a secret.
export function Network() {
    const [state, setState] = createSignal("open");
    const [items, setItems] = createSignal(null);
    const [error, setError] = createSignal("");
    const load = () => {
        setItems(null);
        setError("");
        magi.listEgressReviews({ state: state(), limit: 100 }).then((r) => {
            setItems(r.reviews);
            if (state() === "open")
                setNetworkCount(r.reviews.length);
        }, (e) => setError(errText(e)));
    };
    load();
    // A decided review leaves the list at once: an open review moves to
    // "Decided", and a removed rule leaves "Decided".
    const done = (id) => setItems(items().filter((r) => r.id !== id));
    return _jsxs("section", { class: "page", children: [_jsxs("div", { class: "page-head", children: [_jsx("h1", { children: "Network" }), _jsxs("div", { class: "segmented", role: "group", "aria-label": "Which reviews", children: [_jsx("button", { ...aria("pressed", () => String(state() === "open")), onClick: () => { setState("open"); load(); }, children: "Open" }), _jsx("button", { ...aria("pressed", () => String(state() === "decided")), onClick: () => { setState("decided"); load(); }, children: "Decided" })] })] }), _jsx("p", { class: "lede", children: "The egress proxy blocked these requests of tasks. Your decision becomes a rule for the next requests of the same kind." }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the reviews: ", error] }) }), _jsx(Show, { when: items, fallback: _jsx(Show, { when: () => !error(), children: _jsx("p", { class: "quiet", children: "Loading." }) }), children: _jsx(Show, { when: () => items().length, fallback: _jsx("p", { class: "empty", children: () => state() === "open" ? "No blocked requests wait for you." : "No decided reviews." }), children: _jsx("div", { class: "items", children: _jsx(For, { each: () => items(), children: (r) => _jsx(NetReview, { r: r, done: done }) }) }) }) })] });
}
function bodyText(b) {
    try {
        return new TextDecoder("utf-8", { fatal: true }).decode(b);
    }
    catch {
        return undefined;
    }
}
function NetReview(props) {
    const r = props.r;
    let note;
    const host = r.destination.replace(/:443$/, "");
    const decide = (d) => magi.decideEgressReview({ id: r.id, decision: d, note: note?.value || undefined }).then((res) => {
        notify(res.rule ? `Rule added: ${res.rule}.` : r.state === "open" ? "Dismissed. A new blocked request of this kind opens a new review." : "Rule removed.");
        props.done(r.id);
        refreshCounts();
    }, fail("Could not save the decision"));
    const choices = r.kind === "destination"
        ? [[`Permit ${r.destination}`, "allow-host"], ["Permit at layer 4", "allow-layer4"], ["Block", "block"]]
        : [[`Allow ${r.method} to ${host}`, "allow-host"], [`Allow ${r.method} to this path`, "allow-path"], ["Block", "block"]];
    const text = bodyText(r.body);
    return _jsxs("article", { class: "item", children: [_jsx("p", { class: "item-title mono-wrap", children: r.kind === "destination" ? r.destination : `${r.method} ${r.url}` }), _jsx("p", { children: r.reason }), _jsxs("p", { class: "quiet", children: ["Seen ", r.count === 1 ? "once" : `${r.count} times`, ", last ", new Date(r.lastMs).toLocaleString(), ". Stopped by ", r.by, ". Task ", r.environment, " of ", r.owner, "."] }), r.headers.length ? _jsxs("details", { children: [_jsxs("summary", { children: ["Headers (", r.headers.length, ")"] }), _jsx("pre", { children: r.headers.map((x) => `${x.name}: ${x.value}`).join("\n") })] }) : "", r.body.length ? _jsxs("details", { children: [_jsxs("summary", { children: ["Body (", r.body.length, " bytes)"] }), _jsx("pre", { children: text ?? "(binary data)" })] }) : "", r.state === "open"
                ? _jsxs(_Fragment, { children: [_jsxs("div", { class: "actions", children: [_jsx(For, { each: choices, children: ([label, d]) => _jsx("button", { class: d === "block" ? "danger" : "", onClick: () => decide(d), children: label }) }), _jsx("button", { class: "text-button", onClick: () => decide("dismiss"), children: "Dismiss" })] }), _jsx("input", { ref: (el) => (note = el), class: "note-input", placeholder: "Note (optional)", "aria-label": "Note" })] })
                : _jsxs(_Fragment, { children: [_jsxs("p", { class: "decided", children: ["Decided: ", r.decision, ", by ", who(r.decidedBy ?? ""), r.note ? `. ${r.note}` : ""] }), r.decision !== "dismiss" ? _jsx("div", { class: "actions", children: _jsx("button", { onClick: () => decide("dismiss"), children: "Remove the rule" }) }) : ""] })] });
}
