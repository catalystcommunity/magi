// This checks retained evidence. It does not grant release authority.
import {hash, need, same, wire} from '../release/policy.mjs';

export const taskUUID = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/;
export const taskQueue = /^release\/[0-9a-f]{64}$/;
export const taskOperation = /^opal-[0-9a-f]{64}$/;
const digest = value => hash(value).toString('hex');
const bytes = value => {need(typeof value === 'string' && value.length > 0, 'task-proof-bytes'); return Buffer.from(value, 'base64');};
const map = value => {need(value instanceof Map, 'task-proof-map'); return value;};
const field = (value, name) => map(value).get(name);
const cbor = value => map(wire.decode(bytes(value)));
const exact = (actual, expected, name) => need(same(actual, expected), `task-proof-${name}`);
const requestKey = (installation, id) => `opal-${digest(`opal-corndogs-intent-v1\0${installation}\0${id}`)}`;
const app = a => ({identity: {domain: a.identity.domain, account_id: a.identity.account_id}, application_id: a.application_id, instance_id: a.instance_id});
export const releaseQueue = binding => `release/${digest(JSON.stringify({Controller: app(binding.controller), Target: binding.target, Candidate: binding.candidateId}))}`;

function remoteResponse(query, operation, request) {
  exact(query.operation, operation, 'remote-operation');
  exact(query.request, request, 'remote-request');
  need(query.tls?.authorized === true && query.tls.server === 'release-corndogs' && ['TLSv1.2', 'TLSv1.3'].includes(query.tls.protocol), 'task-proof-remote-tls');
  const response = cbor(query.response), payload = response.get('payload');
  need(response.size === 5 && response.get('v') === 1 && response.get('id') === 1 && response.get('status') === 0 && response.get('variant') === `${operation}Response` && payload?.tag === 24 && payload.value instanceof Uint8Array, 'task-proof-remote-envelope');
  return map(wire.decode(payload.value));
}

export function releaseTaskProof({expected, rollback, row, observations, link, installation, journal, remote}) {
  need(taskUUID.test(row.id) && row.terminal === 1 && row.phase === 'restored' && row.reason === 'candidate-check-failed', 'task-proof-terminal');
  const binding = {candidateId: row.id, candidateDigest: digest(bytes(row.candidate)), approvalId: row.approvalId, approvalDigest: digest(bytes(row.packet)), controller: expected.controller, recorder: expected.recorder, target: expected.target};
  need(taskUUID.test(binding.approvalId) && row.target === expected.target && installation === expected.installation, 'task-proof-authority');
  exact(link.candidateId, row.id, 'link-id'); exact(link.binding, binding, 'binding');
  need(link.blocked === null, 'task-proof-blocked');
  need(rollback.candidateId === row.id && rollback.approvalId === binding.approvalId && rollback.candidateDigest === binding.candidateDigest && rollback.phase === row.phase && rollback.reason === row.reason, 'task-proof-rollback-binding');
  exact(rollback.observations, observations, 'rollback-observations');
  const queue = releaseQueue(binding), taskId = link.dispatch?.taskId;
  need(taskUUID.test(taskId), 'task-proof-task-id');
  exact(link.dispatch, {installation, queue, taskId, revision: 2}, 'dispatch');
  exact(link.completion, {installation, queue, taskId, revision: 3}, 'completion');
  const promotions = observations.filter(o => o.kind === 'promotion-intent');
  need(promotions.length === 1, 'task-proof-promotion-count'); exact(promotions[0].body.task, link.dispatch, 'promotion-task');
  const outcome = {phase: row.phase, reason: row.reason, auditDigest: digest(JSON.stringify(observations)), completedAtMs: row.lastAt};
  need(Number.isSafeInteger(row.lastAt) && row.lastAt > 0, 'task-proof-completion-time');
  exact(link.outcome, outcome, 'outcome');
  exact(journal.installation, installation, 'journal-installation');
  need(journal.intents.length === 3, 'task-proof-intent-count');
  const provenance = {actor: binding.controller, admission: {authority: binding.recorder.identity, kind: 'release-approval', record_id: binding.approvalId}, cause: {authority: binding.controller.identity, kind: 'release-candidate', record_id: binding.candidateId}};
  const receipts = {}, attempts = {};
  for (const [suffix, method] of [['submit', 'SubmitKeyedTask'], ['start', 'UpdateGuardedTask'], ['finish', 'CompleteGuardedTask']]) {
    const id = `${queue}/${suffix}`, matches = journal.intents.filter(i => i.id === id);
    need(matches.length === 1, 'task-proof-intent-id'); const intent = matches[0], key = requestKey(installation, id);
    need(intent.installation === installation && intent.queue === queue && intent.method === method && intent.request_key === key && intent.state === 'confirmed' && intent.rejection_code === null, 'task-proof-intent');
    need(Number.isSafeInteger(intent.attempts) && intent.attempts > 0, 'task-proof-attempts'); attempts[suffix] = intent.attempts;
    exact(intent.provenance, provenance, 'provenance');
    const request = cbor(intent.request), receipt = cbor(intent.receipt);
    if (suffix === 'submit') {
      const payload = request.get('payload'); need(payload instanceof Uint8Array, 'task-proof-payload');
      exact(JSON.parse(Buffer.from(payload).toString('utf8')), binding, 'payload-binding');
      exact(request, new Map(Object.entries({submission_key: key, guarded: true, queue, current_state: 'queued', auto_target_state: 'queued', timeout: -1, payload, priority: 0})), 'submit-request');
      need(receipt.get('submission_key') === key && receipt.get('guarded') === true, 'task-proof-submission-receipt');
    } else {
      const start = suffix === 'start';
      const wanted = {queue, uuid: taskId, operation_id: key, expected_revision: start ? 1 : 2, expected_state: start ? 'queued' : 'running'};
      if (start) Object.assign(wanted, {new_state: 'running', auto_target_state: 'running', timeout: -1});
      exact(request, new Map(Object.entries(wanted)), `${suffix}-request`);
      need(receipt.get('operation_id') === key && receipt.get('op') === (start ? 'update' : 'complete') && receipt.get('result_state') === (start ? 'running' : 'completed') && receipt.get('result_revision') === (start ? 2 : 3), 'task-proof-operation-receipt');
    }
    need(receipt.get('queue') === queue && receipt.get('task_uuid') === taskId, 'task-proof-receipt-task');
    const at = receipt.get(suffix === 'submit' ? 'accepted_at' : 'at'), expires = receipt.get('expires_at');
    // Corndogs uses nanoseconds. Keep i64 values as bigint; do not round them.
    need((typeof at === 'bigint' || Number.isSafeInteger(at)) && at > 0 && (typeof expires === 'bigint' || Number.isSafeInteger(expires)) && expires > at, 'task-proof-receipt-time');
    receipts[suffix] = receipt;
  }
  need(journal.results.length === 1, 'task-proof-result-count');
  exact(journal.results[0], {installation, task_uuid: taskId, revision: 2, result: {binding, outcome}, evidence: `release/${row.id}`}, 'journal-result');
  need(remote.length === 2, 'task-proof-remote-count');
  const current = field(remoteResponse(remote[0], 'GetGuardedTask', {queue, uuid: taskId}), 'task');
  need(field(current, 'guarded') === true && current.get('terminal') === true && current.get('revision') === 3, 'task-proof-remote-terminal');
  const task = field(current, 'task');
  need(field(task, 'queue') === queue && task.get('uuid') === taskId && task.get('current_state') === 'completed', 'task-proof-remote-task');
  const completion = field(remoteResponse(remote[1], 'LookupOperation', {operation_id: receipts.finish.get('operation_id')}), 'receipt');
  exact(completion, receipts.finish, 'remote-receipt');
  return {candidateId: row.id, approvalId: binding.approvalId, installation, queue, taskId, reservedRevision: 2, completedRevision: 3, phase: outcome.phase, reason: outcome.reason, auditDigest: outcome.auditDigest, attempts, checks: ['exact approval and candidate', 'pre-promotion task reservation', 'three confirmed journal intents', 'retained outcome and audit digest', 'remote terminal task', 'remote completion receipt']};
}
