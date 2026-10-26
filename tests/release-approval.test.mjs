import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture} from './release-fixture.mjs';
import {wire, decode, candidate, hash, scope} from '../release/policy.mjs';
import {authorizeRelease} from '../release/approval.mjs';
import {signed} from '../protocol/profile.mjs';
import {spawnSync} from 'node:child_process';
import {writeFileSync} from 'node:fs';
import {join} from 'node:path';

const setup = t => {const f = fixture(); t.after(() => f.close()); return f;};
test('only the configured canonical human can enter the release session', async t => {
  const f = setup(t); f.setAccount('00000000-0000-4000-8000-000000000099');
  await assert.rejects(f.login(), /not-release-approver/);
});
test('actual pinned SDK consumes state once and binds it to the browser', async t => {
  const f = setup(t), start = await f.begin();
  await assert.rejects(f.recorder.finishLogin(start.state, start.token, 'wrong-browser'), /session-mismatch/);
  await assert.rejects(f.recorder.finishLogin(start.state, start.token, start.binding), /missing-state/);
  const again = await f.begin(); f.restartRecorder();
  const auth = await f.recorder.finishLogin(again.state, again.token, again.binding);
  assert.equal(auth.human.accountId, f.config.approver.account_id);
  await assert.rejects(f.recorder.finishLogin(again.state, again.token, again.binding), /missing-state/);
});
test('unverified assertions, wrong nonce, audience, and future issue time fail', async t => {
  const f = setup(t);
  for (const fields of [{nonce: 'wrong'}, {audience: 'elsewhere.test'}, {issuedAt: new Date(+f.now() + 1000).toISOString()}]) {
    f.setAssertion(fields); await assert.rejects(f.login());
  }
  f.setAssertion({}); f.rejectAssertion(); await assert.rejects(f.login(), /assertion-unverified/);
});
test('approval requires session, origin, CSRF, exact review, and fresh review', async t => {
  const f = setup(t), auth = await f.login(), bytes = wire.toReleaseCandidateCbor(f.value);
  for (const bad of [{...auth, origin: 'https://attacker.test'}, {...auth, csrf: 'wrong'}, {...auth, session: 'wrong'}]) assert.throws(() => f.recorder.review(bad, bytes));
  const r = f.recorder.review(auth, bytes), other = await f.login();
  assert.throws(() => f.recorder.approve(other, {...r, commandKey: 'x'}), /review-binding/);
  assert.throws(() => f.recorder.approve(auth, {...r, digest: hash('wrong'), commandKey: 'x'}), /review-binding/);
  f.advance(120001); assert.throws(() => f.recorder.approve(auth, {...r, commandKey: 'x'}), /review-expired/);
  f.advance(180000); assert.throws(() => f.recorder.review(auth, bytes), /session/);
});
test('exact signed approval survives restart and lost response; conflicting replay fails', async t => {
  const f = setup(t), auth = await f.login(), bytes = wire.toReleaseCandidateCbor(f.value), r = f.recorder.review(auth, bytes);
  f.restartRecorder(point => {if (point === 'after-approval-commit') throw Error('crash');});
  assert.throws(() => f.recorder.approve(auth, {...r, commandKey: 'request-1'}), /crash/);
  const stored = f.recorder.db.prepare('SELECT packet FROM approvals').get().packet;
  f.restartRecorder(); const replay = f.recorder.approve(auth, {...r, commandKey: 'request-1'});
  assert.deepEqual(Buffer.from(wire.toSignedRecordCbor(replay)), Buffer.from(stored));
  const changed = f.recorder.review(auth, wire.toReleaseCandidateCbor({...f.value, candidateId: 'changed'}));
  assert.throws(() => f.recorder.approve(auth, {...changed, commandKey: 'request-1'}), /approval-conflict/);
  assert.equal(f.recorder.db.prepare('SELECT count(*) AS n FROM approvals').get().n, 1);
});
test('failure before commit does not publish or retain an approval', async t => {
  const f = setup(t), auth = await f.login(); f.restartRecorder(point => {if (point === 'before-approval-commit') throw Error('crash');});
  assert.throws(() => f.approve(auth), /crash/);
  assert.equal(f.recorder.db.prepare('SELECT count(*) AS n FROM approvals').get().n, 0);
});
for (const boundary of ['before-approval-commit', 'after-approval-commit']) test(`SIGKILL ${boundary} preserves the transaction boundary across processes`, async t => {
  const f = setup(t), auth = await f.login(), r = f.recorder.review(auth, wire.toReleaseCandidateCbor(f.value));
  const path = join(f.dir, 'crash-fixture.json');
  writeFileSync(path, JSON.stringify({options: {path: f.options.path, config: f.config, app: f.app, controller: f.controller, origin: f.origin}, at: +f.now(), auth, keyId: f.recorderKey.keyId, key: f.recorderKey.privateKey.export({format: 'jwk'}), review: {reviewId: r.reviewId, digest: Buffer.from(r.digest).toString('hex'), commandKey: 'kill-command'}, boundary}), {mode: 0o600});
  const result = spawnSync(process.execPath, [new URL('./release-crash-child.mjs', import.meta.url).pathname, path], {encoding: 'utf8'});
  assert.equal(result.signal, 'SIGKILL'); f.restartRecorder();
  assert.equal(f.recorder.db.prepare('SELECT count(*) AS n FROM approvals').get().n, boundary === 'after-approval-commit' ? 1 : 0);
  const first = f.recorder.approve(auth, {...r, commandKey: 'kill-command'});
  f.restartRecorder();
  const replay = f.recorder.approve(auth, {...r, commandKey: 'kill-command'});
  assert(Buffer.from(wire.toSignedRecordCbor(replay)).equals(Buffer.from(wire.toSignedRecordCbor(first))), 'Signed response bytes changed');
});
test('receiver checks current status and withdrawal, not merely recorder signature', async t => {
  const f = setup(t), auth = await f.login(), packet = f.approve(auth);
  const args = {...f.authorization, packet, config: f.config, now: f.now};
  const a = await authorizeRelease(args); assert.deepEqual(a.human, auth.human);
  const withdrawal = f.recorder.withdraw(auth, a.approvalId); f.advance(1000);
  assert.deepEqual(f.recorder.withdraw(auth, a.approvalId), withdrawal);
  f.restartRecorder(); await assert.rejects(authorizeRelease(args), /current-approval-status/);
});
test('application-only claim and substituted candidate fail at receiver', async t => {
  const f = setup(t), auth = await f.login(), packet = f.approve(auth);
  const input = decode('SignatureInput', packet.input), a = decode('ReleaseApproval', input.body);
  const args = {...f.authorization, packet, config: f.config, now: f.now};
  const resign = approval => signed(scope('approval', f.app, f.controller), wire.toReleaseApprovalCbor(approval), f.recorderKey.keyId, f.now(), f.recorderKey.privateKey);
  await assert.rejects(authorizeRelease({...args, packet: resign({...a, human: f.app.identity})}), /approval-binding/);
  await assert.rejects(authorizeRelease({...args, packet: resign({...a, candidate: wire.toReleaseCandidateCbor({...f.value, candidateId: 'substitution'})})}), /approval-binding/);
  await assert.rejects(authorizeRelease({...args, packet: resign({...a, authenticationEvidence: {...a.authenticationEvidence, kind: 'app-key'}})}), /authentication-evidence/);
  await assert.rejects(authorizeRelease({...args, getStatus: async request => {
    const p = await f.recorder.status(request, f.resolveKey), i = decode('SignatureInput', p.input), s = decode('ReleaseApprovalStatus', i.body);
    return signed(scope('status', f.app, f.controller), wire.toReleaseApprovalStatusCbor({...s, challenge: hash('replay')}), f.recorderKey.keyId, f.now(), f.recorderKey.privateKey);
  }}), /current-approval-status/);
});
test('policy rejects other targets, tags, migration, arbitrary probes, and missing evidence', t => {
  const f = setup(t);
  const changes = [
    {target: {...f.value.target, domain: 'identity.example'}}, {image: `${f.config.image_repository}:latest`},
    {target: {...f.value.target, targetId: 'qa-api'}}, {sourceDigest: new Uint8Array(32)}, {evidence: []},
    {checks: {...f.value.checks, readinessPath: '/admin'}}, {checks: {...f.value.checks, requestBody: Uint8Array.of(0xa0)}},
    {checks: {...f.value.checks, expectedBody: Uint8Array.of(0xa0)}}, {expiresAt: f.now()},
  ];
  for (const change of changes) assert.throws(() => candidate(wire.toReleaseCandidateCbor({...f.value, ...change}), f.config, f.now()));
});
