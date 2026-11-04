import { Fragment as _Fragment, jsx as _jsx, jsxs as _jsxs } from "solid-js/h/jsx-runtime";
// Tasks: code changes that Magi makes in a task environment, with the
// decisions that wait for a person.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.js";
import { magi, go, fail, errText } from "./api.js";
// The status is the run status, or the result of a finished task.
const statusName = { waiting: "Waits for you", succeeded: "Done", cancelled: "Stopped", "gave-up": "Gave up",
    "release-rolled-back": "Rolled back", "release-failed": "Release failed" };
const statusLabel = (s) => statusName[s] ?? s.charAt(0).toUpperCase() + s.slice(1).replace(/-/g, " ");
const decisionName = { approve: "Approve", revise: "Ask for changes", reject: "Reject" };
const Status = (props) => _jsx("span", { class: "status " + props.s, children: statusLabel(props.s) });
export function Tasks() {
    const [tasks, setTasks] = createSignal(null);
    const [error, setError] = createSignal("");
    magi.listTasks({ limit: 30 }).then((r) => setTasks(r.tasks), (e) => setError(errText(e)));
    let form;
    const start = async (e) => {
        e.preventDefault();
        const f = new FormData(form);
        try {
            const t = await magi.startTask({ kind: "code-change", instructions: String(f.get("instructions")).trim(), title: String(f.get("title")).trim() || undefined });
            form.reset();
            go("task", t.id);
        }
        catch (err) {
            fail("Could not start the task")(err);
        }
    };
    return _jsxs("section", { class: "page", children: [_jsx("h1", { children: "Tasks" }), _jsxs("form", { class: "task-form", ref: (el) => (form = el), onSubmit: start, children: [_jsx("textarea", { name: "instructions", rows: "3", required: true, "aria-label": "The change", placeholder: "Describe the change to the Magi repository" }), _jsxs("div", { class: "actions", children: [_jsx("input", { name: "title", placeholder: "Title (optional)", "aria-label": "Title" }), _jsx("button", { class: "primary", children: "Start the task" })] })] }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the tasks: ", error] }) }), _jsx(Show, { when: tasks, fallback: _jsx(Show, { when: () => !error(), children: _jsx("p", { class: "quiet", children: "Loading." }) }), children: _jsx(Show, { when: () => tasks().length, fallback: _jsx("p", { class: "empty", children: "You have no tasks. Start one here, or ask Magi to change its code." }), children: _jsx("ul", { class: "task-list", children: _jsx(For, { each: () => tasks(), children: (t) => _jsx("li", { children: _jsxs("a", { href: "#/task/" + encodeURIComponent(t.id), children: [_jsx(Status, { s: t.status }), _jsx("span", { class: "task-title", children: t.title }), _jsx("span", { class: "quiet", children: t.currentStep })] }) }) }) }) }) })] });
}
export function TaskView(props) {
    const [t, setT] = createSignal(null);
    const [error, setError] = createSignal("");
    const [patch, setPatch] = createSignal(null);
    const load = () => magi.getTask({ id: props.id }).then(setT, (e) => setError(errText(e)));
    load();
    let comment;
    const decide = (outcome) => magi.decide({ task: props.id, outcome, comment: comment?.value || undefined }).then(load, fail("Could not save the decision"));
    const showChange = () => magi.getTaskDiff({ id: props.id }).then((d) => {
        setPatch(d.patch);
        requestAnimationFrame(() => document.querySelector(".diff")?.scrollIntoView({ behavior: "smooth", block: "start" }));
    }, fail("Could not read the change"));
    // For a risky step the server tells the risk first, and the person must agree.
    const stop = async () => {
        try {
            try {
                await magi.cancelTask({ task: props.id });
            }
            catch (e) {
                const risk = /confirm: (.*)/.exec(errText(e))?.[1];
                if (!risk)
                    throw e;
                if (!confirm("Stopping this task is risky: " + risk + "\n\nStop it anyway?"))
                    return;
                await magi.cancelTask({ task: props.id, confirm: true });
            }
        }
        catch (e) {
            fail("Could not stop the task")(e);
        }
        load();
    };
    return _jsxs("section", { class: "page", children: [_jsx("a", { class: "back", href: "#/tasks", children: "All tasks" }), _jsx(Show, { when: error, children: _jsxs("p", { class: "error", children: ["Could not read the task: ", error] }) }), _jsx(Show, { when: t, children: (task) => _jsxs(_Fragment, { children: [_jsxs("div", { class: "page-head", children: [_jsx("h1", { children: () => task().title }), _jsx(Status, { s: task().status })] }), _jsx("p", { class: "quiet", children: () => (task().currentStep ? `Step ${task().currentStep}. ` : "") + (task().branch ? `Branch ${task().branch}.` : "No branch yet.") }), () => task().error ? _jsx("p", { class: "error", children: task().error }) : "", _jsx("ol", { class: "steps", children: _jsx(For, { each: () => task().steps, children: (s) => _jsxs("li", { class: "step " + s.status, children: [_jsx("span", { class: "step-name", children: s.step }), " ", _jsxs("span", { class: "quiet", children: [s.status, s.outcome && s.outcome !== s.status ? `, ${s.outcome}` : ""] }), s.notes.length > 2 ? _jsxs("details", { children: [_jsxs("summary", { children: ["Progress (", s.notes.length - 1, " notes)"] }), _jsx(For, { each: s.notes.slice(0, -1), children: (n) => _jsx("p", { class: "step-note", children: n }) })] }) : "", _jsx(For, { each: s.notes.length > 2 ? s.notes.slice(-1) : s.notes, children: (n) => _jsx("p", { class: "step-note", children: n }) })] }) }) }), () => task().waitingFor ? _jsxs("div", { class: "decision", children: [_jsx("p", { children: task().waitingFor }), _jsx("textarea", { ref: (el) => (comment = el), rows: "2", placeholder: "Comment (necessary to ask for changes)", "aria-label": "Comment" }), _jsx("div", { class: "actions", children: _jsx(For, { each: task().decisions ?? [], children: (d) => _jsx("button", { class: d === "approve" ? "primary" : d === "reject" ? "danger" : "", onClick: () => decide(d), children: decisionName[d] ?? d }) }) })] }) : "", _jsxs("div", { class: "actions", children: [_jsx("button", { onClick: showChange, children: "Show the change" }), _jsx("button", { onClick: load, children: "Refresh" }), () => task().status === "running" || task().status === "waiting" ? _jsx("button", { class: "danger", onClick: stop, children: "Stop the task" }) : ""] }), _jsx(Show, { when: () => patch() !== null, children: _jsx("pre", { class: "diff", children: () => patch().split("\n").map((l) => _jsx("span", { class: l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : l.startsWith("@@") ? "hunk" : "", children: l + "\n" })) }) })] }) })] });
}
