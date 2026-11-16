import { Fragment as _Fragment, jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// The admin views: what an operator sees. Each admin call checks the role of
// the caller on the server again; the Admin tab only shows the links.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.js";
import { magi, errText, time, shortDay, who, fail, store, RpcError } from "./api.js";
import { Status, Steps } from "./tasks.js";
export function Admin() {
    const [runners, setRunners] = createSignal(null);
    const [release, setRelease] = createSignal("");
    const [error, setError] = createSignal("");
    magi.listRunners({}).then((r) => { setRunners(r.runners); setRelease(r.release ?? ""); }, (e) => setError(errText(e)));
    return _jsxs("section", { class: "page", children: [_jsx("h1", { children: "Admin" }), _jsx("p", { class: "lede", children: "The state of this Magi. Each call here checks your operator rights on the server." }), _jsx("h2", { children: "Runners" }), _jsxs("p", { children: [_jsx("a", { href: "#/admin-runs", children: "Runs" }), ": the runs of all people, with their steps and outputs."] }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the runners: ", error] }) }), _jsx(Show, { when: () => release(), children: _jsxs("p", { class: "quiet", children: ["Runner release ", () => release(), "."] }) }), _jsx(Show, { when: runners, fallback: _jsx(Show, { when: () => !error(), children: _jsx("p", { class: "quiet", children: "Loading." }) }), children: _jsx(Show, { when: () => runners().length, fallback: _jsx("p", { class: "empty", children: "No runner is connected." }), children: _jsx("div", { class: "page-body", children: _jsx("div", { class: "table-wrap", children: _jsxs("table", { children: [_jsx("thead", { children: _jsxs("tr", { children: [_jsx("th", { children: "Name" }), _jsx("th", { children: "Version" }), _jsx("th", { children: "Platform" }), _jsx("th", { children: "State" }), _jsx("th", { children: "Seen" }), _jsx("th", { children: "Update" })] }) }), _jsx("tbody", { children: _jsx(For, { each: () => runners(), children: (r) => _jsxs("tr", { children: [_jsx("td", { children: r.name }), _jsx("td", { children: r.version }), _jsxs("td", { children: [r.os, "/", r.arch] }), _jsx("td", { children: r.busy ? "Busy" : "Idle" }), _jsxs("td", { children: [time(r.seenMs), " on ", shortDay(r.seenMs)] }), _jsx("td", { children: r.updateError ?? "" })] }) }) })] }) }) }) }) })] });
}
// The kinds of runs that the workflow service knows, and the states that a
// run can have. The server checks both again.
const kinds = ["", "code-change", "persona-turn", "todo"];
const states = ["", "running", "waiting", "succeeded", "failed", "cancelled"];
// All runs of all people. The workflow service returns them newest first.
export function AdminRuns() {
    const [runs, setRuns] = createSignal(null);
    const [error, setError] = createSignal("");
    const [kind, setKind] = createSignal(store.get("magi.runs.kind") ?? "");
    const [state, setState] = createSignal(store.get("magi.runs.state") ?? "");
    const load = () => magi.adminListRuns({ definition: kind() || undefined, status: state() || undefined }).then((r) => setRuns(r.runs), (e) => setError(errText(e)));
    load();
    // The filters stay for the next visit, as the persona does.
    const pick = (what, set, v) => { set(v); store.set("magi.runs." + what, v || null); load(); };
    return _jsxs("section", { class: "page", children: [_jsx("a", { class: "back", href: "#/admin", children: "Admin" }), _jsx("h1", { children: "Runs" }), _jsx("p", { class: "lede", children: "The runs of all people. Select a kind or a state to narrow the list." }), _jsxs("form", { class: "actions filters", onSubmit: (e) => e.preventDefault(), children: [_jsxs("label", { children: ["Kind ", _jsx("select", { value: kind, onChange: (e) => pick("kind", setKind, e.currentTarget.value), children: _jsx(For, { each: kinds, children: (k) => _jsx("option", { value: k, children: k || "All kinds" }) }) })] }), _jsxs("label", { children: ["State ", _jsx("select", { value: state, onChange: (e) => pick("state", setState, e.currentTarget.value), children: _jsx(For, { each: states, children: (s) => _jsx("option", { value: s, children: s || "All states" }) }) })] })] }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the runs: ", error] }) }), _jsx(Show, { when: runs, fallback: _jsx(Show, { when: () => !error(), children: _jsx("p", { class: "quiet", children: "Loading." }) }), children: _jsx(Show, { when: () => runs().length, fallback: _jsx("p", { class: "empty", children: "No run has this kind and state." }), children: _jsx("ul", { class: "task-list", children: _jsx(For, { each: () => runs(), children: (r) => _jsx("li", { children: _jsxs("a", { href: "#/admin-run/" + encodeURIComponent(r.id), children: [_jsx(Status, { s: r.status }), _jsx("span", { class: "task-title", children: r.title || r.id }), _jsx("span", { class: "quiet", children: who(r.owner) }), _jsxs("span", { class: "quiet", children: [time(r.updatedMs), " on ", shortDay(r.updatedMs)] })] }) }) }) }) }) })] });
}
// One run of any person: the summary, the steps, the run data, and the
// outputs that the run made.
export function AdminRunView(props) {
    const [run, setRun] = createSignal(null);
    const [error, setError] = createSignal("");
    const [text, setText] = createSignal(null);
    const load = () => magi.adminGetRun({ id: props.id }).then(setRun, (e) => setError(errText(e)));
    load();
    // A 404 means that this run has no such output; the other errors are real.
    const show = (label) => (e) => {
        if (e instanceof RpcError && e.code === 404) {
            setText(`The run has no ${label}.`);
            return;
        }
        fail(`Could not read the ${label}`)(e);
    };
    const output = (name, label) => magi.adminGetRunOutput({ id: props.id, name }).then((o) => setText(o.text), show(label));
    const change = () => magi.adminGetRunDiff({ id: props.id }).then((d) => setText(d.patch), show("change"));
    return _jsxs("section", { class: "page", children: [_jsx("a", { class: "back", href: "#/admin-runs", children: "All runs" }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the run: ", error] }) }), _jsx(Show, { when: run, children: (r) => _jsxs(_Fragment, { children: [_jsxs("div", { class: "page-head", children: [_jsx("h1", { children: () => r().summary.title || r().summary.id }), _jsx(Status, { s: r().summary.status })] }), _jsx("p", { class: "quiet", children: () => `${r().summary.definition} of ${who(r().summary.owner)}. ` +
                                (r().summary.currentStep ? `Step ${r().summary.currentStep}. ` : "") + `Changed ${time(r().summary.updatedMs)} on ${shortDay(r().summary.updatedMs)}.` }), () => r().summary.error ? _jsx("p", { class: "error", children: r().summary.error }) : "", _jsx(Steps, { steps: () => r().steps }), _jsxs("details", { children: [_jsx("summary", { children: "Run data" }), _jsx("div", { class: "table-wrap", children: _jsx("table", { children: _jsx("tbody", { children: _jsx(For, { each: Object.keys(r().vars).sort(), children: (k) => _jsxs("tr", { children: [_jsx("th", { children: k }), _jsx("td", { children: _jsx("code", { children: () => r().vars[k] }) })] }) }) }) }) })] }), _jsxs("div", { class: "actions", children: [_jsx("button", { onClick: () => output("check.output", "check output"), children: "Check output" }), _jsx("button", { onClick: () => output("apprentice.transcript", "transcript"), children: "Transcript" }), _jsx("button", { onClick: change, children: "Change" }), _jsx("button", { onClick: load, children: "Refresh" })] }), _jsx(Show, { when: () => text() !== null, children: _jsx("pre", { class: "output", children: () => text() }) })] }) })] });
}
