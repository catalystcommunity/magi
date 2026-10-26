import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture} from './release-fixture.mjs';
import {decode} from '../release/policy.mjs';

async function setup(t) {
  const f = fixture(); t.after(() => f.close());
  f.auth = await f.login(); f.packet = f.approve(f.auth); f.startController();
  await f.execution.admit(f.packet); return f;
}
test('healthy release is admitted once and needs two separate protocol checks', async t => {
  const f = await setup(t); await f.execution.admit(f.packet);
  const final = await f.run(); assert.equal(final.body.phase, 'healthy'); assert.equal(f.changes.length, 1);
  assert.equal(f.execution.evidence(f.value.candidateId).filter(x => x.kind === 'probe').length, 2);
  f.startController(); assert.equal((await f.run()).body.phase, 'healthy'); assert.equal(f.changes.length, 1);
});
for (const behavior of ['unready', 'bad-protocol']) test(`candidate ${behavior} restores the old image and checks its protocol`, async t => {
  const f = await setup(t); f.behavior(behavior); const final = await f.run();
  assert.equal(final.body.phase, 'restored'); assert.equal(f.changes.length, 2);
  assert.equal(f.changes[1].image, f.value.previousImage);
  assert.equal(f.execution.evidence(f.value.candidateId).filter(x => x.kind === 'probe' && x.body.restoring).length, 2);
});
for (const boundary of ['after-promotion-effect', 'after-restore-effect']) test(`restart ${boundary} reconciles without repeating the change`, async t => {
  const f = await setup(t); f.behavior('bad-protocol');
  f.startController(point => {if (point === boundary) throw Error('process-crash');});
  await assert.rejects(f.run(), /process-crash/);
  f.startController(); assert.equal((await f.run()).body.phase, 'restored'); assert.equal(f.changes.length, 2);
});
test('lost replies for both effects resolve through target receipts', async t => {
  const f = await setup(t); f.loseReplies(); f.behavior('bad-protocol');
  assert.equal((await f.run()).body.phase, 'restored'); assert.equal(f.changes.length, 2);
  assert.equal(f.execution.evidence(f.value.candidateId).filter(x => x.kind.endsWith('outcome-unknown')).length, 2);
});
test('rollback failure stops without alternating releases', async t => {
  const f = await setup(t); f.behavior('broken-restore');
  assert.equal((await f.run()).body.phase, 'recovery_required'); assert.equal(f.changes.length, 2);
  await f.run(); assert.equal(f.changes.length, 2);
});
test('external target mutation is never overwritten', async t => {
  const f = await setup(t); await f.execution.step(f.value.candidateId);
  f.mutate({version: 'external', generation: 2});
  assert.equal((await f.run()).body.phase, 'recovery_required'); assert.equal(f.changes.length, 0);
});
test('withdrawal before dispatch blocks promotion', async t => {
  const f = await setup(t);
  const a = decode('ReleaseApproval', decode('SignatureInput', f.packet.input).body);
  f.recorder.withdraw(f.auth, a.approvalId);
  assert.equal((await f.run()).body.phase, 'recovery_required'); assert.equal(f.changes.length, 0);
});
test('late successful probe cannot extend observation deadline', async t => {
  const f = await setup(t);
  for (let i = 0; i < 3; i++) await f.execution.step(f.value.candidateId);
  f.delay(31000); assert.equal((await f.execution.step(f.value.candidateId)).body.phase, 'restoring');
  f.delay(0); assert.equal((await f.run()).body.phase, 'restored');
});
test('pending promotion is fenced at its deadline even if it has not arrived', async t => {
  const f = await setup(t), id = f.value.candidateId;
  await f.execution.step(id); const before = await f.backend.inspect(); f.advance(31000);
  assert.equal((await f.execution.step(id)).body.phase, 'restoring');
  assert.equal((await f.run()).body.phase, 'restored');
  await assert.rejects(f.backend.replace(before, f.value.image, `${id}/promote`), /CAS/);
  assert.equal(f.changes.length, 1); assert.equal(f.changes[0].image, f.value.previousImage);
});
test('clock reversal and unavailable target at deadline stop without blind replay', async t => {
  const f = await setup(t); f.advance(-1000);
  assert.equal((await f.run()).body.reason, 'clock-reversal'); assert.equal(f.changes.length, 0);
});
test('two controllers converge on one effect under duplicate dispatch', async t => {
  const f = await setup(t), id = f.value.candidateId;
  await Promise.all([f.execution.step(id), f.execution.step(id)]);
  await Promise.all([f.execution.step(id), f.execution.step(id)]);
  assert.equal((await f.run()).body.phase, 'healthy'); assert.equal(f.changes.length, 1);
});

async function stoppedBeforeEffect(t) {
  const f = await setup(t), status = f.authorization.getStatus;
  f.authorization.getStatus = async () => {throw Error('temporary-outage');};
  assert.equal((await f.execution.step(f.value.candidateId)).body.reason, 'approval-unavailable');
  f.authorization.getStatus = status;
  return f;
}
test('explicit unstarted retry preserves the failed admission and rechecks the same approval', async t => {
  const f = await stoppedBeforeEffect(t); f.advance(31000);
  const before = f.execution.evidence(f.value.candidateId);
  assert.equal((await f.execution.retryUnstarted(f.value.candidateId)).body.phase, 'prepared');
  assert.deepEqual(f.execution.evidence(f.value.candidateId).slice(0, before.length), before);
  assert.equal(f.execution.evidence(f.value.candidateId).at(-1).kind, 'unstarted-retry');
  assert.equal(f.changes.length, 0);
  assert.equal((await f.run()).body.phase, 'healthy'); assert.equal(f.changes.length, 1);
  await assert.rejects(f.execution.retryUnstarted(f.value.candidateId), /retry-started-or-wrong-reason/);
});
test('unstarted retry refuses withdrawal, expiry, and unavailable authorization', async t => {
  for (const reason of ['withdrawal', 'expiry', 'offline']) {
    const f = await stoppedBeforeEffect(t);
    if (reason === 'withdrawal') f.recorder.withdraw(f.auth, decode('ReleaseApproval', decode('SignatureInput', f.packet.input).body).approvalId);
    if (reason === 'expiry') f.advance(3600001);
    if (reason === 'offline') f.authorization.getStatus = async () => {throw Error('offline');};
    const before = f.execution.evidence(f.value.candidateId);
    await assert.rejects(f.execution.retryUnstarted(f.value.candidateId));
    assert.deepEqual(f.execution.evidence(f.value.candidateId), before); assert.equal(f.changes.length, 0);
  }
});
test('unstarted retry refuses target drift and failed baseline checks', async t => {
  for (const change of [{generation: 2}, {uid: 'replacement'}, {configuration: 'changed'}, {image: 'other'}, {marker: 'external'}]) {
    const f = await stoppedBeforeEffect(t); f.mutate(change);
    await assert.rejects(f.execution.retryUnstarted(f.value.candidateId), /retry-target-changed/); assert.equal(f.changes.length, 0);
  }
  const f = await stoppedBeforeEffect(t); f.behavior('broken-restore');
  await assert.rejects(f.execution.retryUnstarted(f.value.candidateId), /baseline-not-healthy/); assert.equal(f.changes.length, 0);
});
test('an earlier promotion intent prevents unstarted retry even with no recorded effect', async t => {
  const f = await setup(t), id = f.value.candidateId;
  await f.execution.step(id);
  f.execution.stop(f.execution.read(id), 'approval-unavailable');
  await assert.rejects(f.execution.retryUnstarted(id), /retry-effect-history/); assert.equal(f.changes.length, 0);
});
test('concurrent unstarted retries converge on one prepared state', async t => {
  const f = await stoppedBeforeEffect(t), id = f.value.candidateId;
  await Promise.all([f.execution.retryUnstarted(id), f.execution.retryUnstarted(id)]);
  assert.equal(f.execution.evidence(id).filter(e => e.kind === 'unstarted-retry').length, 1);
  assert.equal((await f.run()).body.phase, 'healthy'); assert.equal(f.changes.length, 1);
});
