import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import * as all from '../.build/codec.gen.js';
import * as release from '../.build/releases/codec.gen.js';
import {decode} from '../protocol/profile.mjs';
import {fixture} from './fixtures.mjs';

test('release approval has its own contract and preserves aggregate wire bytes', () => {
  const descriptor = release.decode(readFileSync(new URL('../generated/releases/go/releases.csil-schema.cbor', import.meta.url)));
  const body = descriptor.get('body');
  assert.deepEqual(body.get('services').map(s => s.get('name')).sort(), ['Discovery', 'Releases']);
  assert.deepEqual(Buffer.from(descriptor.get('digest')), createHash('sha256').update(release.encodeValue(body)).digest());
  assert.match(readFileSync(new URL('../generated/releases/schema.go', import.meta.url), 'utf8'), new RegExp(Buffer.from(descriptor.get('digest')).toString('hex')));
  for (const rule of body.get('rules')) {
    const name = rule.get('name');
    if (typeof release[`to${name}Cbor`] !== 'function') continue;
    const raw = fixture(name, true);
    assert.deepEqual(release[`to${name}Cbor`](decode(name, raw, release)), all[`to${name}Cbor`](decode(name, raw)));
  }
});
