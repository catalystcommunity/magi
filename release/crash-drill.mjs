import {need} from './policy.mjs';
export function crashDrill(spec, stop = () => process.kill(process.pid, 'SIGKILL')) {
  if (!spec) return () => {};
  need(/^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(spec.candidateId) && spec.point === 'after-restore-effect', 'crash-drill-scope');
  return (point, controller) => {
    if (point !== spec.point) return;
    const row = controller.db.prepare('SELECT id,body FROM releases WHERE terminal=0').get();
    if (row?.id !== spec.candidateId) return;
    need(JSON.parse(row.body).phase === 'restoring', 'crash-drill-phase');
    // This hook is after the external effect, outside the receipt transaction.
    // The durable marker prevents another crash when supervision restarts us.
    controller.db.exec('CREATE TABLE IF NOT EXISTS supervision_faults (candidate_id TEXT PRIMARY KEY, point TEXT NOT NULL, fired_at INTEGER NOT NULL)');
    const first = controller.db.prepare('INSERT OR IGNORE INTO supervision_faults VALUES (?,?,?)').run(spec.candidateId, point, Date.now());
    if (first.changes) {console.log(JSON.stringify({drill: 'one-shot-controller-crash', candidateId: spec.candidateId, point})); stop();}
  };
}
