import test from 'node:test';
import assert from 'node:assert/strict';
import {supervise} from '../release/controller-init.mjs';

test('launcher returns the child status without a restart', async () => {
  assert.equal(await supervise(process.execPath, ['-e', 'process.exit(7)']), 7);
  assert.equal(await supervise(process.execPath, ['-e', 'process.exit(0)']), 0);
});
test('launcher propagates an actual SIGKILL as exit 137', async () => {
  assert.equal(await supervise(process.execPath, ['-e', 'process.kill(process.pid,"SIGKILL")']), 137);
});
test('launcher failure is nonzero', async () => {assert.equal(await supervise('/no-such-opal-controller', []), 1);});
