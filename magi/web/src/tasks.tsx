// Tasks: code changes that Magi makes in a task environment, with the
// decisions that wait for a person.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.ts";
import type { Task } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, go, fail, errText } from "./api.ts";

// The status is the run status, or the result of a finished task.
const statusName: Record<string, string> = { waiting: "Waits for you", succeeded: "Done", cancelled: "Stopped", "gave-up": "Gave up",
  "release-rolled-back": "Rolled back", "release-failed": "Release failed" };
const statusLabel = (s: string) => statusName[s] ?? s.charAt(0).toUpperCase() + s.slice(1).replace(/-/g, " ");
const decisionName: Record<string, string> = { approve: "Approve", revise: "Ask for changes", reject: "Reject" };
const Status = (props: { s: string }) => <span class={"status " + props.s}>{statusLabel(props.s)}</span>;

export function Tasks() {
  const [tasks, setTasks] = createSignal<Task[] | null>(null);
  const [error, setError] = createSignal("");
  magi.listTasks({ limit: 30 }).then((r) => setTasks(r.tasks), (e) => setError(errText(e)));
  let form: HTMLFormElement | undefined;
  const start = async (e: SubmitEvent) => {
    e.preventDefault();
    const f = new FormData(form);
    try {
      const t = await magi.startTask({ kind: "code-change", instructions: String(f.get("instructions")).trim(), title: String(f.get("title")).trim() || undefined });
      form!.reset();
      go("task", t.id);
    } catch (err) { fail("Could not start the task")(err); }
  };
  return <section class="page">
    <h1>Tasks</h1>
    <form class="task-form" ref={(el: HTMLFormElement) => (form = el)} onSubmit={start}>
      <textarea name="instructions" rows="3" required aria-label="The change" placeholder="Describe the change to the Magi repository" />
      <div class="actions">
        <input name="title" placeholder="Title (optional)" aria-label="Title" />
        <button class="primary">Start the task</button>
      </div>
    </form>
    <Show when={error}><p class="error">Could not read the tasks: {error}</p></Show>
    <Show when={tasks} fallback={<Show when={() => !error()}><p class="quiet">Loading.</p></Show>}>
      <Show when={() => tasks()!.length} fallback={<p class="empty">You have no tasks. Start one here, or ask Magi to change its code.</p>}>
        <ul class="task-list">
          <For each={() => tasks()!}>{(t: Task) =>
            <li><a href={"#/task/" + encodeURIComponent(t.id)}>
              <Status s={t.status} />
              <span class="task-title">{t.title}</span>
              <span class="quiet">{t.currentStep}</span>
            </a></li>}
          </For>
        </ul>
      </Show>
    </Show>
  </section>;
}

export function TaskView(props: { id: string }) {
  const [t, setT] = createSignal<Task | null>(null);
  const [error, setError] = createSignal("");
  const [patch, setPatch] = createSignal<string | null>(null);
  const load = () => magi.getTask({ id: props.id }).then(setT, (e) => setError(errText(e)));
  load();
  let comment: HTMLTextAreaElement | undefined;
  const decide = (outcome: string) => magi.decide({ task: props.id, outcome, comment: comment?.value || undefined }).then(load, fail("Could not save the decision"));
  const showChange = () => magi.getTaskDiff({ id: props.id }).then((d) => {
    setPatch(d.patch);
    requestAnimationFrame(() => document.querySelector(".diff")?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }, fail("Could not read the change"));
  // For a risky step the server tells the risk first, and the person must agree.
  const stop = async () => {
    try {
      try { await magi.cancelTask({ task: props.id }); }
      catch (e) {
        const risk = /confirm: (.*)/.exec(errText(e))?.[1];
        if (!risk) throw e;
        if (!confirm("Stopping this task is risky: " + risk + "\n\nStop it anyway?")) return;
        await magi.cancelTask({ task: props.id, confirm: true });
      }
    } catch (e) { fail("Could not stop the task")(e); }
    load();
  };
  return <section class="page">
    <a class="back" href="#/tasks">All tasks</a>
    <Show when={error}><p class="error">Could not read the task: {error}</p></Show>
    <Show when={t}>{(task: () => Task) => <>
      <div class="page-head"><h1>{() => task().title}</h1><Status s={task().status} /></div>
      <p class="quiet">{() => (task().currentStep ? `Step ${task().currentStep}. ` : "") + (task().branch ? `Branch ${task().branch}.` : "No branch yet.")}</p>
      {() => task().error ? <p class="error">{task().error}</p> : ""}
      <ol class="steps">
        <For each={() => task().steps}>{(s) =>
          <li class={"step " + s.status}>
            <span class="step-name">{s.step}</span> <span class="quiet">{s.status}{s.outcome && s.outcome !== s.status ? `, ${s.outcome}` : ""}</span>
            {/* The last note is the result of the step; the earlier notes are progress. */}
            {s.notes.length > 2 ? <details><summary>Progress ({s.notes.length - 1} notes)</summary>
              <For each={s.notes.slice(0, -1)}>{(n: string) => <p class="step-note">{n}</p>}</For></details> : ""}
            <For each={s.notes.length > 2 ? s.notes.slice(-1) : s.notes}>{(n: string) => <p class="step-note">{n}</p>}</For>
          </li>}
        </For>
      </ol>
      {() => task().waitingFor ? <div class="decision">
        <p>{task().waitingFor}</p>
        <textarea ref={(el: HTMLTextAreaElement) => (comment = el)} rows="2" placeholder="Comment (necessary to ask for changes)" aria-label="Comment" />
        <div class="actions"><For each={task().decisions ?? []}>{(d: string) =>
          <button class={d === "approve" ? "primary" : d === "reject" ? "danger" : ""} onClick={() => decide(d)}>{decisionName[d] ?? d}</button>}</For></div>
      </div> : ""}
      <div class="actions">
        {() => task().terminal ? <button onClick={() => go("terminal", props.id)} title="A shell in the sandbox of the task">Open terminal</button> : ""}
        <button onClick={showChange}>Show the change</button>
        <button onClick={load}>Refresh</button>
        {() => task().status === "running" || task().status === "waiting" ? <button class="danger" onClick={stop}>Stop the task</button> : ""}
      </div>
      <Show when={() => patch() !== null}>
        <pre class="diff">{() => patch()!.split("\n").map((l) =>
          <span class={l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : l.startsWith("@@") ? "hunk" : ""}>{l + "\n"}</span>)}</pre>
      </Show>
    </>}</Show>
  </section>;
}
