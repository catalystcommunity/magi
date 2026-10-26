import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {environment} from '../scripts/environment.mjs';

test('Magi settings accept legacy names but prefer explicit new values', () => {
  assert.equal(environment('MAGI_LISTEN', {OPAL_LISTEN:'old'}), 'old');
  assert.equal(environment('MAGI_LISTEN', {OPAL_LISTEN:'old', MAGI_LISTEN:'new'}), 'new');
  assert.equal(environment('MAGI_LISTEN', {OPAL_LISTEN:'old', MAGI_LISTEN:''}), '');
  assert.equal(environment('MAGI_LISTEN', {}), undefined);
  assert.throws(() => environment('OTHER'), /MAGI_/);
});

test('Magi package and module use the new name', () => {
  assert.equal(JSON.parse(readFileSync(new URL('../package.json',import.meta.url))).name, 'magi-contracts');
  assert.match(readFileSync(new URL('../go.mod',import.meta.url),'utf8'), /^module github.com\/catalystcommunity\/magi\n/);
});
