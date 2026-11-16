// The admin views: what an operator sees. Each admin call checks the role of
// the caller on the server again; the Admin tab only shows the links.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.ts";
import type { RunnerInfo, AdminRunSummary, AdminRun, TaskOutput, TaskDiff } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, errText, time, shortDay, who, fail, store, RpcError } from "./api.ts";
import { Status, Steps } from "./tasks.tsx";

export function Admin() {
  const [runners, setRunners] = createSignal<RunnerInfo[] | null>(null);
  const [release, setRelease] = createSignal("");
  const [error, setError] = createSignal("");
  magi.listRunners({}).then((r) => { setRunners(r.runners); setRelease(r.release ?? ""); },
    (e) => setError(errText(e)));
  return <section class="page">
    <h1>Admin</h1>
    <p class="lede">The state of this Magi. Each call here checks your operator rights on the server.</p>
    <h2>Runners</h2>
    <p><a href="#/admin-runs">Runs</a>: the runs of all people, with their steps and outputs.</p>
    <Show when={error}><p class="error">Could not read the runners: {error}</p></Show>
    <Show when={() => release()}><p class="quiet">Runner release {() => release()}.</p></Show>
    <Show when={runners} fallback={<Show when={() => !error()}><p class="quiet">Loading.</p></Show>}>
      <Show when={() => runners()!.length} fallback={<p class="empty">No runner is connected.</p>}>
        <div class="page-body"><div class="table-wrap"><table>
          <thead><tr><th>Name</th><th>Version</th><th>Platform</th><th>State</th><th>Seen</th><th>Update</th></tr></thead>
          <tbody>
            <For each={() => runners()!}>{(r: RunnerInfo) =>
              <tr>
                <td>{r.name}</td>
                <td>{r.version}</td>
                <td>{r.os}/{r.arch}</td>
                <td>{r.busy ? "Busy" : "Idle"}</td>
                <td>{time(r.seenMs)} on {shortDay(r.seenMs)}</td>
                <td>{r.updateError ?? ""}</td>
              </tr>}
            </For>
          </tbody>
        </table></div></div>
      </Show>
    </Show>
  </section>;
}

// The kinds of runs that the workflow service knows, and the states that a
// run can have. The server checks both again.
const kinds = ["", "code-change", "persona-turn", "todo"];
const states = ["", "running", "waiting", "succeeded", "failed", "cancelled"];

// All runs of all people. The workflow service returns them newest first.
export function AdminRuns() {
  const [runs, setRuns] = createSignal<AdminRunSummary[] | null>(null);
  const [error, setError] = createSignal("");
  const [kind, setKind] = createSignal(store.get("magi.runs.kind") ?? "");
  const [state, setState] = createSignal(store.get("magi.runs.state") ?? "");
  const load = () => magi.adminListRuns({ definition: kind() || undefined, status: state() || undefined }).then(
    (r) => setRuns(r.runs), (e) => setError(errText(e)));
  load();
  // The filters stay for the next visit, as the persona does.
  const pick = (what: string, set: (v: string) => void, v: string) => { set(v); store.set("magi.runs." + what, v || null); load(); };
  return <section class="page">
    <a class="back" href="#/admin">Admin</a>
    <h1>Runs</h1>
    <p class="lede">The runs of all people. Select a kind or a state to narrow the list.</p>
    <form class="actions filters" onSubmit={(e) => e.preventDefault()}>
      <label>Kind <select value={kind} onChange={(e) => pick("kind", setKind, e.currentTarget.value)}>
        <For each={kinds}>{(k: string) => <option value={k}>{k || "All kinds"}</option>}</For>
      </select></label>
      <label>State <select value={state} onChange={(e) => pick("state", setState, e.currentTarget.value)}>
        <For each={states}>{(s: string) => <option value={s}>{s || "All states"}</option>}</For>
      </select></label>
    </form>
    <Show when={error}><p class="error">Could not read the runs: {error}</p></Show>
    <Show when={runs} fallback={<Show when={() => !error()}><p class="quiet">Loading.</p></Show>}>
      <Show when={() => runs()!.length} fallback={<p class="empty">No run has this kind and state.</p>}>
        <ul class="task-list">
          <For each={() => runs()!}>{(r: AdminRunSummary) =>
            <li><a href={"#/admin-run/" + encodeURIComponent(r.id)}>
              <Status s={r.status} />
              <span class="task-title">{r.title || r.id}</span>
              <span class="quiet">{who(r.owner)}</span>
              <span class="quiet">{time(r.updatedMs)} on {shortDay(r.updatedMs)}</span>
            </a></li>}
          </For>
        </ul>
      </Show>
    </Show>
  </section>;
}

// One run of any person: the summary, the steps, the run data, and the
// outputs that the run made.
export function AdminRunView(props: { id: string }) {
  const [run, setRun] = createSignal<AdminRun | null>(null);
  const [error, setError] = createSignal("");
  const [text, setText] = createSignal<string | null>(null);
  const load = () => magi.adminGetRun({ id: props.id }).then(setRun, (e) => setError(errText(e)));
  load();
  // A 404 means that this run has no such output; the other errors are real.
  const show = (label: string) => (e: unknown) => {
    if (e instanceof RpcError && e.code === 404) { setText(`The run has no ${label}.`); return; }
    fail(`Could not read the ${label}`)(e);
  };
  const output = (name: string, label: string) => magi.adminGetRunOutput({ id: props.id, name }).then(
    (o: TaskOutput) => setText(o.text), show(label));
  const change = () => magi.adminGetRunDiff({ id: props.id }).then((d: TaskDiff) => setText(d.patch), show("change"));
  return <section class="page">
    <a class="back" href="#/admin-runs">All runs</a>
    <Show when={error}><p class="error">Could not read the run: {error}</p></Show>
    <Show when={run}>{(r: () => AdminRun) => <>
      <div class="page-head"><h1>{() => r().summary.title || r().summary.id}</h1><Status s={r().summary.status} /></div>
      <p class="quiet">{() => `${r().summary.definition} of ${who(r().summary.owner)}. ` +
        (r().summary.currentStep ? `Step ${r().summary.currentStep}. ` : "") + `Changed ${time(r().summary.updatedMs)} on ${shortDay(r().summary.updatedMs)}.`}</p>
      {() => r().summary.error ? <p class="error">{r().summary.error}</p> : ""}
      <Steps steps={() => r().steps} />
      <details><summary>Run data</summary>
        <div class="table-wrap"><table>
          <tbody><For each={Object.keys(r().vars).sort()}>{(k: string) =>
            <tr><th>{k}</th><td><code>{() => r().vars[k]}</code></td></tr>}
          </For></tbody>
        </table></div>
      </details>
      <div class="actions">
        <button onClick={() => output("check.output", "check output")}>Check output</button>
        <button onClick={() => output("apprentice.transcript", "transcript")}>Transcript</button>
        <button onClick={change}>Change</button>
        <button onClick={load}>Refresh</button>
      </div>
      <Show when={() => text() !== null}><pre class="output">{() => text()}</pre></Show>
    </>}</Show>
  </section>;
}
