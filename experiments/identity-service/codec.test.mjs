import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { generateKeyPairSync } from 'node:crypto';
import * as wire from './generated/typescript/codec.gen.ts';
import { decode, allowance, hash, signed } from './approval.mjs';
const id = {domain: 'primary.opal.test', accountId: '01987fe9-7668-7000-8000-000000000001'};
const app = {identity: id, applicationId: 'opal', instanceId: 'stable'};
const a = {version: 1, allowanceId: 'test', issuer: id, delegate: app, audience: 'secondary.opal.test', resource: 'calendar/private', action: 'update', purpose: 'codec test', notBefore: new Date('2026-01-02T03:04:05.123Z')};
test('Go and TypeScript preserve signed allowance bytes', () => {
  const output = join(mkdtempSync(join(tmpdir(), 'opal-w02-interop-')), 'interop');
  execFileSync('go', ['build', '-o', output, './interop'], {cwd: import.meta.dirname, env: {...process.env, GOCACHE: process.env.GOCACHE || '/tmp/opal-knowledge-go-build-20261011'}});
  const roundtrip = (kind, bytes) => Buffer.from(execFileSync(output, [kind, Buffer.from(bytes).toString('hex')], {encoding: 'utf8'}), 'hex');
  const bytes = wire.toAllowanceCbor(a);
  assert.deepEqual(roundtrip('allowance', bytes), Buffer.from(bytes));
  const p = {version: 1, approvalId: 'test', approvalService: app, human: id, allowance: bytes, allowanceDigest: hash(bytes), approvedAt: a.notBefore, authenticatedAt: a.notBefore, authenticationEvidence: 'test-receipt'};
  const approval = wire.toApprovalCbor(p);
  assert.deepEqual(roundtrip('approval', approval), Buffer.from(approval));
  const proof = wire.toSignedApprovalCbor(signed('approval', approval, {...generateKeyPairSync('ed25519'), keyId: 'key'}, a.notBefore));
  assert.deepEqual(roundtrip('signed', proof), Buffer.from(proof));
  const alternate = roundtrip('time', bytes);
  assert.equal(+allowance(alternate).notBefore, +new Date('2026-01-02T03:04:05.120Z'));
});
test('invalid, lossy, and normalized dates are rejected', () => {
  for (const date of ['2026-02-31T00:00:00Z', '2026-01-02T03:04:05.123456Z', 'bad', '2026-01-02']) {
    const tree = wire.decode(wire.toAllowanceCbor(a)); tree.set('not_before', {tag: 0, value: date});
    assert.throws(() => allowance(wire.encodeValue(tree)));
  }
});
test('unknown nested fields and hostile CBOR fail before approval', () => {
  const tree = wire.decode(wire.toAllowanceCbor(a)); tree.get('issuer').set('approved', true);
  assert.throws(() => allowance(wire.encodeValue(tree)), /unknown-field/);
  for (const hex of ['9b000001000000000000', 'bb000001000000000000', '5bffffffffffffffff', '7bffffffffffffffff', '830102']) assert.throws(() => decode('Allowance', Buffer.from(hex, 'hex')));
  assert.throws(() => allowance(Buffer.alloc(32769)), /frame-limit/);
});
