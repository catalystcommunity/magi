import { jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// The admin views: what an operator sees. Each admin call checks isOperator
// on the server again; the Admin tab only shows the links.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.js";
import { magi, errText, time, shortDay } from "./api.js";
export function Admin() {
    const [runners, setRunners] = createSignal(null);
    const [release, setRelease] = createSignal("");
    const [error, setError] = createSignal("");
    magi.listRunners({}).then((r) => { setRunners(r.runners); setRelease(r.release ?? ""); }, (e) => setError(errText(e)));
    return _jsxs("section", { class: "page", children: [_jsx("h1", { children: "Admin" }), _jsx("p", { class: "lede", children: "The state of this Magi. Each call here checks your operator rights on the server." }), _jsx("h2", { children: "Runners" }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the runners: ", error] }) }), _jsx(Show, { when: () => release(), children: _jsxs("p", { class: "quiet", children: ["Runner release ", () => release(), "."] }) }), _jsx(Show, { when: runners, fallback: _jsx(Show, { when: () => !error(), children: _jsx("p", { class: "quiet", children: "Loading." }) }), children: _jsx(Show, { when: () => runners().length, fallback: _jsx("p", { class: "empty", children: "No runner is connected." }), children: _jsx("div", { class: "page-body", children: _jsx("div", { class: "table-wrap", children: _jsxs("table", { children: [_jsx("thead", { children: _jsxs("tr", { children: [_jsx("th", { children: "Name" }), _jsx("th", { children: "Version" }), _jsx("th", { children: "Platform" }), _jsx("th", { children: "State" }), _jsx("th", { children: "Seen" }), _jsx("th", { children: "Update" })] }) }), _jsx("tbody", { children: _jsx(For, { each: () => runners(), children: (r) => _jsxs("tr", { children: [_jsx("td", { children: r.name }), _jsx("td", { children: r.version }), _jsxs("td", { children: [r.os, "/", r.arch] }), _jsx("td", { children: r.busy ? "Busy" : "Idle" }), _jsxs("td", { children: [time(r.seenMs), " on ", shortDay(r.seenMs)] }), _jsx("td", { children: r.updateError ?? "" })] }) }) })] }) }) }) }) })] });
}
