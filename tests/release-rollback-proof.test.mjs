import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture} from './release-fixture.mjs';
import {hash} from '../release/policy.mjs';
import {rollbackProof} from '../release/rollback-proof.mjs';

async function evidence(t) {
  const f = fixture(); t.after(() => f.close());
  f.value.checks.expectedBody = f.value.checks.previousExpectedBody;
  const auth = await f.login(), packet = f.approve(auth); f.startController();
  const original = f.backend.probe;
  const faultDigest = hash('deliberate protocol failure').toString('hex');
  f.backend.probe = async (expected, checks, restoring) => ({...await original(expected, checks, restoring), podUid: 'fixture-pod', responseDigest: restoring ? hash(checks.previousExpectedBody).toString('hex') : faultDigest});
  const admitted = await f.execution.admit(packet); f.behavior('bad-protocol');
  const row = await f.run();
  const preparation = {purpose: 'isolated-rollback-drill', candidateId: row.id, candidateDigest: hash(row.candidate).toString('hex'), image: f.value.image, baseline: admitted.body.baseline, baselineProbe: {readiness: true, operation: true, bound: true},
    build: {image: f.value.image, binarySha256: 'test-binary', sourceDigest: Buffer.from(f.value.sourceDigest).toString('hex'), recipeDigest: Buffer.from(f.value.recipeDigest).toString('hex'), failure: {responseDigest: faultDigest}},
    observations: [{role: 'candidate', image: f.value.image, expectedFailure: true, readinessStatus: 204, operationStatus: 503, responseDigest: faultDigest, binaryDigest: 'test-binary'}]};
  return {row, value: f.value, observations: f.execution.evidence(row.id), preparation};
}

test('rollback proof requires a real fault observation and verified baseline restoration', async t => {
  const e = await evidence(t), result = rollbackProof(e);
  assert.equal(result.phase, 'restored'); assert.equal(result.reason, 'candidate-check-failed');
  assert.equal(result.faultProbes, 2); assert.equal(result.restoredChecks, 2); assert.deepEqual(result.generations, [1, 2, 3]);
  assert.equal(result.images.restored, result.images.baseline); assert.notEqual(result.images.failed, result.images.baseline);
});

test('terminal state, image changes, or unready pods alone do not prove this drill', async t => {
  const source = await evidence(t);
  for (const change of [
    e => {e.row.body.phase = 'healthy';},
    e => {e.observations = e.observations.filter(o => o.kind !== 'promotion-observed');},
    e => {e.observations = e.observations.filter(o => o.kind !== 'restore-observed');},
    e => {e.observations = e.observations.filter(o => o.kind !== 'probe');},
    e => {for (const o of e.observations.filter(o => o.kind === 'probe' && !o.body.restoring)) o.body.readiness = false;},
    e => {for (const o of e.observations.filter(o => o.kind === 'probe' && !o.body.restoring)) o.body.responseDigest = hash('different fault').toString('hex');},
    e => {e.observations.find(o => o.kind === 'restore-intent').body.reason = 'observation-deadline';},
  ]) {const e = structuredClone(source); change(e); assert.throws(() => rollbackProof(e), /rollback-/);}
});

test('rollback proof refuses changed build, proposal, target, or protocol expectations', async t => {
  const source = await evidence(t);
  for (const change of [
    e => {e.preparation.candidateDigest = 'changed';},
    e => {e.preparation.build.sourceDigest = 'changed';},
    e => {e.preparation.build.binarySha256 = 'changed';},
    e => {e.value.checks.expectedBody = hash('changed');},
    e => {e.observations.find(o => o.kind === 'restore-observed').body.image = e.value.image;},
    e => {e.observations.find(o => o.kind === 'restore-observed').body.generation++;},
    e => {e.observations.find(o => o.kind === 'promotion-observed').body.uid = 'other';},
    e => {e.row.body.restored.marker = 'wrong';},
  ]) {const e = structuredClone(source); change(e); assert.throws(() => rollbackProof(e), /rollback-/);}
});

test('rollback proof refuses missing, late, repeated, or wrong restoration checks', async t => {
  const source = await evidence(t);
  for (const change of [
    e => {e.observations.splice(e.observations.findIndex(o => o.kind === 'probe' && o.body.restoring), 1);},
    e => {e.observations.find(o => o.kind === 'probe' && o.body.restoring).body.responseDigest = 'wrong';},
    e => {const checks = e.observations.filter(o => o.kind === 'probe' && o.body.restoring); checks[1].at = checks[0].at;},
    e => {e.observations.find(o => o.kind === 'restored').at = e.observations.find(o => o.kind === 'restore-intent').at + e.value.checks.rollbackSeconds * 1000;},
    e => {e.observations.push({...e.observations.at(-1)});},
    e => {e.value.checks.failureCount++;},
  ]) {const e = structuredClone(source); change(e); assert.throws(() => rollbackProof(e), /rollback-/);}
});
