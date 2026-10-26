import test from 'node:test';
import assert from 'node:assert/strict';
import {supervisionProof, gracefulRestartProof} from '../scripts/release-supervision-proof.mjs';
const before = {podUid: 'pod', deploymentUid: 'deployment', image: 'image', restarts: 0, containerId: 'container-a', startedAt: '2026-10-19T23:00:00Z', ready: true};
const after = {...before, restarts: 1, containerId: 'container-b', startedAt: '2026-10-19T23:01:02Z', terminated: {containerID: 'container-a', startedAt: before.startedAt, finishedAt: '2026-10-19T23:01:00Z', exitCode: 137, reason: 'Error'}};
const faultAt = Date.parse('2026-10-19T23:01:00.500Z');
test('supervision proof binds the killed container and exactly one ready restart', () => {assert.equal(supervisionProof(before, after, faultAt).restartCountAfter, 1);});
test('graceful restart is distinct from crash recovery', () => {
  const clean = structuredClone(after); clean.terminated.exitCode = 0; clean.terminated.reason = 'Completed';
  assert.equal(gracefulRestartProof(before, clean, faultAt).restartCountAfter, 1);
  assert.throws(() => supervisionProof(before, clean, faultAt), /supervision-termination/);
  assert.throws(() => gracefulRestartProof(before, after, faultAt), /supervision-termination/);
});
test('supervision proof rejects unrelated kills, OOM, repeated restarts, and unready recovery', () => {
  for (const change of [a => {a.podUid = 'other';}, a => {a.image = 'other';}, a => {a.restarts = 2;}, a => {a.ready = false;}, a => {a.terminated.reason = 'OOMKilled';}, a => {a.terminated.exitCode = 0;}, a => {a.terminated.signal = 15;}, a => {a.terminated.containerID = 'other';}, a => {a.terminated.finishedAt = '2026-10-19T23:00:40Z';}]) {const a = structuredClone(after); change(a); assert.throws(() => supervisionProof(before, a, faultAt), /supervision-/);}
});
