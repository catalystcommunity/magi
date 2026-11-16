// The admin views: what an operator sees. Each admin call checks isOperator
// on the server again; the Admin tab only shows the links.
import { createSignal } from "solid-js";
import { For, Show } from "./flow.ts";
import type { RunnerInfo } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, errText, time, shortDay } from "./api.ts";

export function Admin() {
  const [runners, setRunners] = createSignal<RunnerInfo[] | null>(null);
  const [release, setRelease] = createSignal("");
  const [error, setError] = createSignal("");
  const load = () => magi.listRunners({}).then((r) => { setRunners(r.runners); setRelease(r.release ?? ""); },
    (e) => setError(errText(e)));
  load();
  return <section class="page">
    <h1>Admin</h1>
    <p class="lede">The state of this Magi. Each call here checks your operator rights on the server.</p>
    <h2>Runners</h2>
    <Show when={error}><p class="error">Could not read the runners: {error}</p></Show>
    <Show when={() => release()}><p class="quiet">Runner release {() => release()}.</p></Show>
    <Show when={runners} fallback={<Show when={() => !error()}><p class="quiet">Loading.</p></Show>}>
      <Show when={() => runners()!.length} fallback={<p class="empty">No runner is connected.</p>}>
        <div class="page-body"><div class="table-wrap"><table>
          <tr><th>Name</th><th>Version</th><th>Platform</th><th>State</th><th>Seen</th><th>Update</th></tr>
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
        </table></div></div>
      </Show>
    </Show>
  </section>;
}
