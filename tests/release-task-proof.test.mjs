import test from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {hash, wire} from '../release/policy.mjs';
import {releaseTaskProof, releaseQueue} from '../scripts/release-task-proof.mjs';

const digest = value => hash(value).toString('hex');
const encode = value => Buffer.from(wire.encodeValue(value)).toString('base64');
const cbor = value => encode(new Map(Object.entries(value)));
const decode = value => wire.decode(Buffer.from(value, 'base64'));
const changeCBOR = (owner, key, change) => {const value = decode(owner[key]); change(value); owner[key] = encode(value);};
const response = (operation, request, payload) => ({operation, request, tls: {authorized: true, server: 'release-corndogs', protocol: 'TLSv1.3'}, response: cbor({v: 1, id: 1, status: 0, variant: `${operation}Response`, payload: {tag: 24, value: wire.encodeValue(new Map(Object.entries(payload)))}})});

function fixture() {
  const controller = {identity: {domain: 'catalystsquad.local', account_id: '11111111-1111-4111-8111-111111111111'}, application_id: 'opal-release-controller', instance_id: 'test-1'};
  const recorder = {...controller, application_id: 'opal-release-approval'}, installation = 'opal-release-test';
  const row = {id: '22222222-2222-4222-8222-222222222222', approvalId: '33333333-3333-4333-8333-333333333333', candidate: Buffer.from('candidate').toString('base64'), packet: Buffer.from('packet').toString('base64'), target: 'opal-stateless-canary-v1', terminal: 1, phase: 'restored', reason: 'candidate-check-failed', lastAt: 1792466818826};
  const binding = {candidateId: row.id, candidateDigest: digest('candidate'), approvalId: row.approvalId, approvalDigest: digest('packet'), controller, recorder, target: row.target};
  const queue = releaseQueue(binding), taskId = '44444444-4444-4444-8444-444444444444';
  const dispatch = {installation, queue, taskId, revision: 2}, completion = {...dispatch, revision: 3};
  const observations = [{sequence: 60, at: row.lastAt - 10000, kind: 'promotion-intent', body: {task: dispatch}}, {sequence: 61, at: row.lastAt, kind: 'restored', body: {reason: row.reason}}];
  const outcome = {phase: row.phase, reason: row.reason, auditDigest: digest(JSON.stringify(observations)), completedAtMs: row.lastAt};
  const provenance = {actor: controller, admission: {authority: recorder.identity, kind: 'release-approval', record_id: row.approvalId}, cause: {authority: controller.identity, kind: 'release-candidate', record_id: row.id}};
  const intents = [];
  for (const [index, [suffix, method]] of [['submit', 'SubmitKeyedTask'], ['start', 'UpdateGuardedTask'], ['finish', 'CompleteGuardedTask']].entries()) {
    const id = `${queue}/${suffix}`, key = `opal-${digest(`opal-corndogs-intent-v1\0${installation}\0${id}`)}`;
    const at = 1792466800000000001n + BigInt(index), expires_at = at + 86400000000000n;
    let request, receipt;
    if (suffix === 'submit') {
      request = {queue, submission_key: key, guarded: true, current_state: 'queued', auto_target_state: 'queued', timeout: -1, priority: 0, payload: Buffer.from(JSON.stringify(binding))};
      receipt = {queue, submission_key: key, task_uuid: taskId, guarded: true, accepted_at: at, expires_at};
    } else {
      const start = suffix === 'start';
      request = {queue, uuid: taskId, operation_id: key, expected_revision: start ? 1 : 2, expected_state: start ? 'queued' : 'running'};
      if (start) Object.assign(request, {new_state: 'running', auto_target_state: 'running', timeout: -1});
      receipt = {queue, operation_id: key, task_uuid: taskId, op: start ? 'update' : 'complete', result_revision: start ? 2 : 3, result_state: start ? 'running' : 'completed', at, expires_at};
    }
    intents.push({installation, id, method, queue, request_key: key, request: cbor(request), receipt: cbor(receipt), provenance, state: 'confirmed', attempts: 1, rejection_code: null});
  }
  return {
    expected: {controller, recorder, installation, target: row.target}, row, observations, installation,
    rollback: {candidateId: row.id, candidateDigest: binding.candidateDigest, approvalId: row.approvalId, phase: row.phase, reason: row.reason, observations},
    link: {candidateId: row.id, binding, dispatch, completion, outcome, blocked: null},
    journal: {installation, intents, results: [{installation, task_uuid: taskId, revision: 2, result: {binding, outcome}, evidence: `release/${row.id}`}]},
    remote: [response('GetGuardedTask', {queue, uuid: taskId}, {task: new Map(Object.entries({guarded: true, terminal: true, revision: 3, task: new Map(Object.entries({queue, uuid: taskId, current_state: 'completed'}))}))}), response('LookupOperation', {operation_id: intents[2].request_key}, {receipt: decode(intents[2].receipt)})],
  };
}

function refuse(changes) {
  for (const change of changes) {const evidence = structuredClone(fixture()); change(evidence); assert.throws(() => releaseTaskProof(evidence), /task-proof-/);}
}

test('cross-store proof distinguishes task completion from release success', () => {
  const e = fixture(), p = releaseTaskProof(e);
  assert.equal(p.phase, 'restored'); assert.equal(p.completedRevision, 3); assert.equal(p.reservedRevision, 2);
  assert.deepEqual(p.attempts, {submit: 1, start: 1, finish: 1}); assert.equal(p.checks.length, 6);
  const receipt = decode(e.journal.intents[2].receipt);
  assert.equal(receipt.get('at'), 1792466800000000003n);
  assert.notEqual(BigInt(Number(receipt.get('at'))), receipt.get('at'));
});

test('release queue matches the Go adapter key and ignores object property order', () => {
  const binding = {controller: {identity: {domain: 'catalystsquad.local', account_id: '00000000-0000-4000-8000-000000000001'}, application_id: 'opal-release-controller', instance_id: 'example-1'}, target: 'opal-stateless-canary-v1', candidateId: '00000000-0000-4000-8000-000000000002'};
  assert.equal(releaseQueue(binding), 'release/a323cd04bac1aafdd62a76730df22d760bac93a115caec96903e2e31b04a9c5e');
  assert.equal(releaseQueue(binding), releaseQueue({...binding, controller: {...binding.controller, identity: {account_id: binding.controller.identity.account_id, domain: binding.controller.identity.domain}}}));
});

test('proof refuses changed authority, approval, candidate bytes, or rollback evidence', () => refuse([
  e => {e.row.candidate = Buffer.from('other').toString('base64');},
  e => {e.row.packet = Buffer.from('other').toString('base64');},
  e => {e.expected.controller = {...e.expected.controller, instance_id: 'other'};},
  e => {e.expected.recorder = {...e.expected.recorder, application_id: 'other'};},
  e => {e.expected.target = 'other';}, e => {e.expected.installation = 'other';},
  e => {e.rollback.approvalId = 'other';}, e => {e.rollback.observations = [];},
  e => {e.row.terminal = 0;}, e => {e.row.phase = 'healthy';}, e => {e.link.blocked = 'reconcile_required';},
]));

test('proof requires exact task reservation, completion, and complete audit history', () => refuse([
  e => {e.link.dispatch = {...e.link.dispatch, taskId: e.row.id};},
  e => {e.link.dispatch = {...e.link.dispatch, revision: 3};},
  e => {e.link.completion.revision = 2;}, e => {e.link.completion.installation = 'other';},
  e => {e.observations[0].body.task = {...e.link.dispatch, taskId: e.row.id};},
  e => {e.observations.push(e.observations[0]);}, e => {e.observations[1].at++;},
  e => {e.link.outcome = {...e.link.outcome, auditDigest: 'wrong'};},
  e => {e.row.lastAt++;}, e => {e.link.candidateId = 'other';},
]));

test('proof refuses missing, duplicate, pending, rejected, or misbound journal intents', () => refuse([
  e => {e.journal.intents.pop();}, e => {e.journal.intents[2] = e.journal.intents[1];},
  e => {e.journal.installation = 'other';}, e => {e.journal.intents[0].installation = 'other';},
  e => {e.journal.intents[1].state = 'pending';}, e => {e.journal.intents[1].rejection_code = 7;},
  e => {e.journal.intents[1].method = 'CompleteGuardedTask';}, e => {e.journal.intents[1].request_key = 'other';},
  e => {e.journal.intents[1].provenance = {};}, e => {e.journal.intents[1].attempts = 0;},
]));

test('proof checks payload, guarded revisions, operation IDs, and exact completion receipt', () => refuse([
  e => changeCBOR(e.journal.intents[0], 'request', r => r.set('payload', Buffer.from('{}'))),
  e => changeCBOR(e.journal.intents[0], 'request', r => r.set('guarded', false)),
  e => changeCBOR(e.journal.intents[1], 'request', r => r.set('expected_revision', 2)),
  e => changeCBOR(e.journal.intents[2], 'request', r => r.set('expected_state', 'queued')),
  e => changeCBOR(e.journal.intents[2], 'request', r => r.set('operation_id', 'other')),
  e => changeCBOR(e.journal.intents[2], 'receipt', r => r.set('result_revision', 4)),
  e => changeCBOR(e.journal.intents[2], 'receipt', r => r.set('result_state', 'running')),
  e => changeCBOR(e.journal.intents[2], 'receipt', r => r.set('task_uuid', e.row.id)),
  e => changeCBOR(e.journal.intents[2], 'receipt', r => r.set('at', r.get('at') + 1n)),
  e => changeCBOR(e.journal.intents[2], 'receipt', r => r.set('expires_at', r.get('at'))),
]));

test('proof requires the result at the reserved revision with exact evidence and outcome', () => refuse([
  e => {e.journal.results = [];}, e => {e.journal.results.push(e.journal.results[0]);},
  e => {e.journal.results[0].revision = 3;}, e => {e.journal.results[0].evidence = 'other';},
  e => {e.journal.results[0].result = {binding: e.link.binding, outcome: {...e.link.outcome, phase: 'healthy'}};},
]));

test('proof requires authenticated remote reads with exact response correlation', () => refuse([
  e => {e.remote = [];}, e => {e.remote[0].tls.authorized = false;},
  e => {e.remote[0].tls.server = 'other';}, e => {e.remote[0].operation = 'UpdateGuardedTask';},
  e => {e.remote[0].request.uuid = e.row.id;},
  e => changeCBOR(e.remote[0], 'response', r => r.set('id', 2)),
  e => changeCBOR(e.remote[0], 'response', r => r.set('status', 1)),
  e => changeCBOR(e.remote[0], 'response', r => r.set('variant', 'ServiceError')),
  e => {e.remote[0] = response('GetGuardedTask', e.remote[0].request, {});},
  e => {const r = decode(e.remote[0].response), payload = wire.decode(r.get('payload').value); payload.get('task').set('terminal', false); r.set('payload', {tag: 24, value: wire.encodeValue(payload)}); e.remote[0].response = encode(r);},
  e => {e.remote[1] = response('LookupOperation', e.remote[1].request, {receipt: decode(e.journal.intents[1].receipt)});},
]));

test('operator command refuses an invalid identifier before any cluster access', () => {
  assert.throws(() => execFileSync(process.execPath, ['scripts/check-release-task.mjs', "x';DROP TABLE workflow_intents;--"], {stdio: 'pipe'}), error => error.status === 1 && error.stderr.toString().includes('Use: node scripts/check-release-task.mjs candidate-uuid'));
});
