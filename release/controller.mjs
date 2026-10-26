import {database, transaction, authorizeRelease} from './approval.mjs';
import {wire, decode, hash, equal, need, same, policy, candidate} from './policy.mjs';

// These are deployment observations and effect receipts, not workflow states.
// A Corndogs task can call admit/step/read. Only this component owns the target.
const terminal = new Set(['healthy', 'restored', 'recovery_required']);
const encode = value => JSON.stringify(value);
const stable = value => ({uid: value.uid, generation: value.generation, version: value.version, image: value.image, configuration: value.configuration, marker: value.marker});
export const matches = (actual, expected) => same({...stable(actual), version: ''}, {...stable(expected), version: ''});
// Deployment annotations, including our operation marker, also advance the
// generation. A same-image restore fence is still one generation change.
const after = (actual, before, image, marker) => actual.uid === before.uid && actual.generation === before.generation + 1 && actual.image === image && actual.configuration === before.configuration && actual.marker === marker;

export class ReleaseController {
  constructor({path, config, backend, authorization, now = () => new Date(), fault = () => {}, tasks}) {
    this.config = policy(config); Object.assign(this, {backend, authorization, now, fault, tasks});
    need(typeof backend.inspect === 'function' && typeof backend.replace === 'function' && typeof backend.probe === 'function', 'backend');
    this.db = database(path);
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS binding (id INTEGER PRIMARY KEY CHECK(id=1), body TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS releases (id TEXT PRIMARY KEY, approval_id TEXT NOT NULL UNIQUE, target TEXT NOT NULL, packet BLOB NOT NULL, candidate BLOB NOT NULL, version INTEGER NOT NULL, body TEXT NOT NULL, terminal INTEGER NOT NULL);
      CREATE UNIQUE INDEX IF NOT EXISTS active_target ON releases(target) WHERE terminal=0;
      CREATE TABLE IF NOT EXISTS observations (sequence INTEGER PRIMARY KEY, release_id TEXT NOT NULL REFERENCES releases(id), at INTEGER NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL);
    `);
    const binding = encode({config: this.config, recorder: authorization.recorder, controller: authorization.controller});
    transaction(this.db, () => {
      this.db.prepare('INSERT OR IGNORE INTO binding VALUES(1,?)').run(binding);
      need(this.db.prepare('SELECT body FROM binding').get().body === binding, 'controller-binding-changed');
    });
    try {
      if (tasks) tasks.attach(this);
      else if (this.db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name='release_task_binding'").get()) need(!this.db.prepare('SELECT 1 FROM release_task_binding').get(), 'task-adapter-required');
    } catch (error) {this.db.close(); throw error;}
  }
  close() {this.db.close();}
  read(id) {
    const row = this.db.prepare('SELECT * FROM releases WHERE id=?').get(id); need(row, 'unknown-release');
    return {...row, body: JSON.parse(row.body), value: decode('ReleaseCandidate', row.candidate)};
  }
  evidence(id) {return this.db.prepare('SELECT sequence,at,kind,body FROM observations WHERE release_id=? ORDER BY sequence').all(id).map(row => ({...row, body: JSON.parse(row.body)}));}
  save(row, body, kind, detail = {}) {
    return transaction(this.db, () => {
      const changed = this.db.prepare('UPDATE releases SET version=version+1,body=?,terminal=? WHERE id=? AND version=?').run(encode(body), terminal.has(body.phase) ? 1 : 0, row.id, row.version);
      if (!changed.changes) return {...this.read(row.id), updated: false};
      this.db.prepare('INSERT INTO observations(release_id,at,kind,body) VALUES (?,?,?,?)').run(row.id, +this.now(), kind, encode(detail));
      this.fault('before-receipt-commit');
      return {...this.read(row.id), updated: true};
    });
  }
  async admit(packet) {
    const a = await authorizeRelease({...this.authorization, packet, config: this.config, now: this.now});
    const c = candidate(a.candidate, this.config, this.now()), bytes = wire.toSignedRecordCbor(packet);
    const old = this.db.prepare('SELECT id,packet FROM releases WHERE id=? OR approval_id=?').get(c.candidateId, a.approvalId);
    if (old) {need(equal(old.packet, bytes), 'release-conflict'); return this.read(old.id);}
    const baseline = stable(await this.backend.inspect());
    need(baseline.uid === c.target.resourceUid && baseline.generation === c.target.generation && baseline.image === c.previousImage && baseline.configuration === Buffer.from(c.configurationDigest).toString('hex'), 'target-changed');
    // A known-good digest alone is not a recovery test. Check its actual service.
    const probe = await this.backend.probe(baseline, c.checks, true);
    need(probe.readiness === true && probe.operation === true && probe.bound === true, 'baseline-not-healthy');
    need(matches(await this.backend.inspect(), baseline), 'baseline-changed');
    // The first mutation checks status again; no admission-time cache grants it.
    candidate(a.candidate, this.config, this.now());
    return transaction(this.db, () => {
      const previous = this.db.prepare('SELECT id,packet FROM releases WHERE id=? OR approval_id=?').get(c.candidateId, a.approvalId);
      if (previous) {need(equal(previous.packet, bytes), 'release-conflict'); return this.read(previous.id);}
      const at = +this.now();
      const body = {phase: 'prepared', baseline, lastAt: at, deadline: at + c.checks.observationSeconds * 1000, successes: 0, failures: 0, lastProbe: 0};
      this.db.prepare('INSERT INTO releases VALUES (?,?,?,?,?,0,?,0)').run(c.candidateId, a.approvalId, c.target.targetId, bytes, a.candidate, encode(body));
      this.db.prepare('INSERT INTO observations(release_id,at,kind,body) VALUES (?,?,?,?)').run(c.candidateId, at, 'admitted', encode({approvalId: a.approvalId, candidateDigest: hash(a.candidate).toString('hex'), baseline, probe}));
      return this.read(c.candidateId);
    });
  }
  // An operator can retry a stopped admission only if no change was attempted.
  // Keep the failure evidence. Revalidate the same approval and original target;
  // a previous promotion intent or any other terminal reason forbids this path.
  async retryUnstarted(id) {
    const row = this.read(id), b = row.body;
    if (this.tasks) need(!this.db.prepare('SELECT outcome FROM release_task_links WHERE candidate_id=?').get(id)?.outcome, 'task-outcome-already-recorded');
    need(row.terminal && b.phase === 'recovery_required' && b.reason === 'approval-unavailable', 'retry-started-or-wrong-reason');
    need(this.evidence(id).every(e => ['admitted', 'inspection-unavailable', 'unstarted-retry'].includes(e.kind) || e.kind === 'recovery-required' && e.body.reason === 'approval-unavailable'), 'retry-effect-history');
    const a = await authorizeRelease({...this.authorization, packet: decode('SignedRecord', row.packet), config: this.config, now: this.now});
    const c = candidate(a.candidate, this.config, this.now());
    const baseline = stable(await this.backend.inspect());
    need(matches(baseline, b.baseline), 'retry-target-changed');
    const probe = await this.backend.probe(baseline, c.checks, true);
    need(probe.readiness === true && probe.operation === true && probe.bound === true, 'baseline-not-healthy');
    need(matches(await this.backend.inspect(), baseline), 'baseline-changed');
    candidate(a.candidate, this.config, this.now());
    const at = +this.now(); need(at >= b.lastAt, 'clock-reversal');
    return this.save(row, {phase: 'prepared', baseline, lastAt: at, deadline: at + c.checks.observationSeconds * 1000, successes: 0, failures: 0, lastProbe: 0},
      'unstarted-retry', {approvalId: row.approval_id, previousDeadline: b.deadline, baseline, probe});
  }
  stop(row, reason) {return this.save(row, {...row.body, phase: 'recovery_required', reason, lastAt: +this.now()}, 'recovery-required', {reason});}
  rollback(row, snapshot, reason) {
    const at = +this.now();
    return this.save(row, {...row.body, phase: 'restoring', restoreFrom: stable(snapshot), restoreDeadline: at + row.value.checks.rollbackSeconds * 1000, successes: 0, failures: 0, lastProbe: 0, lastAt: at, reason}, 'restore-intent', {before: stable(snapshot), image: row.value.previousImage, reason});
  }
  async step(id) {
    let row = this.read(id);
    if (terminal.has(row.body.phase)) return row;
    if (+this.now() < row.body.lastAt) return this.stop(row, 'clock-reversal');
    const c = row.value, b = row.body;
    let dispatch;
    if (this.tasks && b.phase === 'prepared') {
      if (+this.now() >= b.deadline || +this.now() >= +c.expiresAt) return this.stop(row, 'task-dispatch-deadline');
      try {dispatch = await this.tasks.start(row);}
      catch (error) {
        if (error.code === 'reconcile_required') return this.stop(row, 'task-reconciliation-required');
        return this.save(row, {...b, lastAt: +this.now()}, 'task-dispatch-unavailable');
      }
      // A slow queue reply grants no extension and no same-image restore.
      if (+this.now() >= b.deadline || +this.now() >= +c.expiresAt) return this.stop(row, 'task-dispatch-deadline');
    }
    let actual;
    try {actual = stable(await this.backend.inspect());}
    catch { // An unavailable API gives no permission to repeat an effect.
      if (+this.now() >= (b.restoreDeadline ?? b.deadline)) return this.stop(row, 'target-unavailable-at-deadline');
      return this.save(row, {...b, lastAt: +this.now()}, 'inspection-unavailable');
    }
    const promotion = `${id}/promote`, restoration = `${id}/restore`;
    if (b.phase === 'prepared' || b.phase === 'applying') {
      if (after(actual, b.baseline, c.image, promotion)) {
        if (+this.now() >= b.deadline) return this.rollback(row, actual, 'observation-deadline');
        return this.save(row, {...b, phase: 'observing', deployed: actual, lastAt: +this.now()}, 'promotion-observed', actual);
      }
      if (!matches(actual, b.baseline)) return this.stop(row, 'target-changed-before-promotion');
      if (+this.now() >= b.deadline) return this.tasks && b.phase === 'prepared' ? this.stop(row, 'task-dispatch-deadline') : this.rollback(row, actual, 'promotion-deadline');
      try {await authorizeRelease({...this.authorization, packet: decode('SignedRecord', row.packet), config: this.config, now: this.now});}
      catch {return b.phase === 'applying' ? this.rollback(row, actual, 'approval-unavailable') : this.stop(row, 'approval-unavailable');}
      if (+this.now() >= b.deadline) return this.tasks && b.phase === 'prepared' ? this.stop(row, 'task-dispatch-deadline') : this.rollback(row, actual, 'promotion-deadline');
      if (b.phase === 'prepared') {
        return this.save(row, {...b, phase: 'applying', baseline: actual, lastAt: +this.now()}, 'promotion-intent', {before: actual, image: c.image, marker: promotion, ...(dispatch ? {task: dispatch} : {})});
      }
      if (actual.version !== b.baseline.version) return this.save(row, {...b, baseline: actual, lastAt: +this.now()}, 'promotion-refenced', actual);
      // The exact original resource version is a CAS precondition. A delayed
      // request cannot replace a later restore or an operator's change.
      try {await this.backend.replace(b.baseline, c.image, promotion);}
      catch {return this.save(row, {...b, lastAt: +this.now()}, 'promotion-outcome-unknown');}
      this.fault('after-promotion-effect');
      return this.save(row, {...b, lastAt: +this.now()}, 'promotion-send-returned');
    }
    if (b.phase === 'restoring') {
      if (after(actual, b.restoreFrom, c.previousImage, restoration)) {
        return this.save(row, {...b, phase: 'checking-restore', restored: actual, lastAt: +this.now()}, 'restore-observed', actual);
      }
      if (!matches(actual, b.restoreFrom)) return this.stop(row, 'target-changed-during-restore');
      if (+this.now() >= b.restoreDeadline) return this.stop(row, 'restore-deadline');
      if (actual.version !== b.restoreFrom.version) return this.save(row, {...b, restoreFrom: actual, lastAt: +this.now()}, 'restore-refenced', actual);
      try {await this.backend.replace(b.restoreFrom, c.previousImage, restoration);}
      catch {return this.save(row, {...b, lastAt: +this.now()}, 'restore-outcome-unknown');}
      this.fault('after-restore-effect');
      return this.save(row, {...b, lastAt: +this.now()}, 'restore-send-returned');
    }
    const restoring = b.phase === 'checking-restore';
    need(restoring || b.phase === 'observing', 'controller-phase');
    const expected = restoring ? b.restored : b.deployed;
    if (!matches(actual, expected)) return this.stop(row, 'target-changed-during-check');
    const deadline = restoring ? b.restoreDeadline : b.deadline;
    if (+this.now() >= deadline) return restoring ? this.stop(row, 'restore-check-deadline') : this.rollback(row, actual, 'observation-deadline');
    if (+this.now() - b.lastProbe < 1000) return row;
    let probe;
    try {probe = await this.backend.probe(expected, c.checks, restoring);}
    catch {probe = {readiness: false, operation: false, bound: false};}
    // Late successful replies do not extend the approved deadline.
    if (+this.now() >= deadline) return restoring ? this.stop(row, 'restore-check-deadline') : this.rollback(row, actual, 'observation-deadline');
    const good = probe.readiness === true && probe.operation === true && probe.bound === true;
    const successes = good ? b.successes + 1 : 0, failures = good ? 0 : b.failures + 1;
    row = this.save(row, {...b, successes, failures, lastProbe: +this.now(), lastAt: +this.now()}, 'probe', {restoring, ...probe});
    // Another controller may have advanced the record while a probe was pending.
    if (!row.updated) return row;
    if (successes >= c.checks.successCount) {
      try {if (!matches(await this.backend.inspect(), expected)) return this.stop(row, 'target-changed-after-check');}
      catch {return this.stop(row, 'target-unavailable-after-check');}
      if (+this.now() >= deadline) return restoring ? this.stop(row, 'restore-check-deadline') : this.rollback(row, expected, 'observation-deadline');
      return this.save(row, {...row.body, phase: restoring ? 'restored' : 'healthy', lastAt: +this.now()}, restoring ? 'restored' : 'healthy');
    }
    if (failures >= c.checks.failureCount) return restoring ? this.stop(row, 'restore-check-failed') : this.rollback(row, expected, 'candidate-check-failed');
    return row;
  }
}
