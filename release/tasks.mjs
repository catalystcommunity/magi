import {execFile} from 'node:child_process';
import {isAbsolute} from 'node:path';
import {hash, need} from './policy.mjs';
import {transaction} from './approval.mjs';

const app = a => ({identity: {domain: a.identity.domain, account_id: a.identity.accountId}, application_id: a.applicationId, instance_id: a.instanceId});
const uuid = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/;
const identifier = /^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$/;

// A fixed operator-installed executable and private profile. No shell, user
// arguments, inherited service credentials, or public listener are involved.
export function taskCommand({binary, profile}) {
  need(isAbsolute(binary) && isAbsolute(profile), 'task-command-paths');
  return request => new Promise((resolve, reject) => {
    const child = execFile(binary, ['--request', profile], {env: {PATH: '/usr/bin:/bin'}, timeout: 12000, killSignal: 'SIGKILL', maxBuffer: 4096}, (error, stdout, stderr) => {
      if (error) {
        const failure = Error('release-task-unavailable');
        if (stderr.trim() === 'Release task operation failed: reconcile_required') failure.code = 'reconcile_required';
        reject(failure); return;
      }
      try {resolve(JSON.parse(stdout));} catch {reject(Error('release-task-response'));}
    });
    child.stdin.on('error', () => {});
    child.stdin.end(JSON.stringify(request));
  });
}

export class ReleaseTasks {
  constructor({installation, call, binary, profile}) {
    need(typeof installation === 'string' && identifier.test(installation), 'task-installation');
    this.installation = installation; this.call = call ?? taskCommand({binary, profile});
  }
  attach(controller) {
    this.controller = controller;
    const db = controller.db;
    db.exec(`CREATE TABLE IF NOT EXISTS release_task_binding (id INTEGER PRIMARY KEY CHECK(id=1), installation TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS release_task_links (candidate_id TEXT PRIMARY KEY REFERENCES releases(id), body TEXT NOT NULL, dispatch TEXT, outcome TEXT, completion TEXT, blocked TEXT)`);
    transaction(db, () => {
      const previous = db.prepare('SELECT installation FROM release_task_binding WHERE id=1').get();
      if (!previous) {
        need(db.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n === 0, 'task-enable-requires-idle-controller');
        db.prepare('INSERT INTO release_task_binding VALUES(1,?)').run(this.installation);
      } else need(previous.installation === this.installation, 'task-installation-changed');
    });
  }
  binding(row) {
    const a = this.controller.authorization;
    return {candidateId: row.id, candidateDigest: hash(row.candidate).toString('hex'), approvalId: row.approval_id, approvalDigest: hash(row.packet).toString('hex'), controller: app(a.controller), recorder: app(a.recorder), target: row.target};
  }
  receipt(r) {
    need(r && Object.keys(r).sort().join() === 'installation,queue,revision,taskId' && r.installation === this.installation && typeof r.queue === 'string' && /^release\/[0-9a-f]{64}$/.test(r.queue) && typeof r.taskId === 'string' && uuid.test(r.taskId) && Number.isSafeInteger(r.revision) && r.revision > 0, 'task-receipt');
    return JSON.stringify({installation: r.installation, queue: r.queue, taskId: r.taskId, revision: r.revision});
  }
  async start(row) {
    const db = this.controller.db, binding = this.binding(row), body = JSON.stringify(binding);
    transaction(db, () => {
      db.prepare('INSERT OR IGNORE INTO release_task_links(candidate_id,body) VALUES(?,?)').run(row.id, body);
      const previous = db.prepare('SELECT body,outcome FROM release_task_links WHERE candidate_id=?').get(row.id);
      need(previous.body === body && !previous.outcome, 'task-binding-changed-or-finished');
    });
    const r = await this.call({operation: 'start', binding}), receipt = this.receipt(r);
    transaction(db, () => {
      const previous = db.prepare('SELECT dispatch FROM release_task_links WHERE candidate_id=?').get(row.id);
      need(!previous.dispatch || previous.dispatch === receipt, 'task-dispatch-changed');
      db.prepare('UPDATE release_task_links SET dispatch=? WHERE candidate_id=?').run(receipt, row.id);
    });
    return JSON.parse(receipt);
  }
  // Queue publication must not consume a rollback deadline. Flush at most one
  // retained result only when no release is active. A lost reply is retried
  // with the identical binding and outcome, without repeating deployment.
  async flush() {
    const c = this.controller, db = c.db;
    if (db.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n) return [];
    const link = db.prepare('SELECT l.* FROM release_task_links l JOIN releases r ON r.id=l.candidate_id WHERE r.terminal=1 AND l.completion IS NULL AND l.blocked IS NULL ORDER BY l.candidate_id LIMIT 1').get();
    if (!link) return [];
    const row = c.read(link.candidate_id);
    const outcome = JSON.stringify({phase: row.body.phase, reason: row.body.reason ?? '', auditDigest: hash(JSON.stringify(c.evidence(row.id))).toString('hex'), completedAtMs: row.body.lastAt});
    need(!link.outcome || link.outcome === outcome, 'task-outcome-changed');
    db.prepare('UPDATE release_task_links SET outcome=? WHERE candidate_id=?').run(outcome, row.id);
    try {
      const response = await this.call({operation: 'finish', binding: JSON.parse(link.body), outcome: JSON.parse(outcome)});
      const receipt = this.receipt(response);
      if (link.dispatch) {
        const dispatch = JSON.parse(link.dispatch);
        need(response.taskId === dispatch.taskId && response.queue === dispatch.queue && response.revision === dispatch.revision + 1, 'task-completion-binding');
      }
      db.prepare('UPDATE release_task_links SET completion=? WHERE candidate_id=?').run(receipt, row.id);
      return [{candidateId: row.id, task: 'completed', outcome: row.body.phase}];
    } catch (error) {
      if (error.code === 'reconcile_required') {
        db.prepare('UPDATE release_task_links SET blocked=? WHERE candidate_id=?').run('reconcile_required', row.id);
        return [{candidateId: row.id, task: 'completion-requires-reconciliation'}];
      }
      return [{candidateId: row.id, task: 'completion-pending'}];
    }
  }
}
