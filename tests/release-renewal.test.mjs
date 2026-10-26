import test from 'node:test';
import assert from 'node:assert/strict';
import {generateKeyPairSync, createPublicKey, verify} from 'node:crypto';
import {renewalRequest} from '../deploy/release-renewal.mjs';
import {codec, tuple} from '../release/linkkeys.mjs';
const entry = () => ({app: {identity: {accountId: '00000000-0000-4000-8000-000000000001', domain: 'catalystsquad.local'}, applicationId: 'opal-release-controller', instanceId: 'example-1'}, keys: Array.from({length: 3}, (_, i) => ({keyId: `key-${i}`, key: generateKeyPairSync('ed25519').privateKey.export({format: 'jwk'})}))});
const challenge = {challengeId: 'challenge-id', challenge: new Uint8Array(32).fill(7)};
test('attestation renewal binds identity, challenge, deadline, possession, and two sibling signatures', () => {
  const e = entry(), at = new Date('2026-10-19T23:00:00Z'), r = renewalRequest(e, 'key-0', challenge, at);
  const decoded = codec.fromApplicationKeyRenewalCbor(r.renewal);
  assert.equal(decoded.subjectUserId, e.app.identity.accountId); assert.equal(decoded.subjectDomain, 'catalystsquad.local');
  assert.equal(decoded.applicationId, e.app.applicationId); assert.equal(decoded.instanceId, e.app.instanceId);
  assert.equal(decoded.keyId, 'key-0'); assert.equal(decoded.challengeId, challenge.challengeId); assert.deepEqual(decoded.challenge, challenge.challenge);
  assert.equal(Date.parse(decoded.expiresAt) - Date.parse(decoded.requestedAt), 60000);
  const pub = k => createPublicKey({key: k.key, format: 'jwk'});
  const envelope = tag => tuple.encodeCborTuple([tuple.text(tag), tuple.bytes(r.renewal)]);
  assert(verify(null, envelope('linkkeys-application-key-possession-v1alpha'), pub(e.keys[0]), r.possessionProof));
  assert.deepEqual(r.signatures.map(s => s.signedByKeyId), ['key-1', 'key-2']);
  for (const s of r.signatures) {
    assert(verify(null, envelope('linkkeys-application-key-renewal-v1alpha'), pub(e.keys.find(k => k.keyId === s.signedByKeyId)), s.signature));
    assert(!verify(null, envelope('linkkeys-application-key-possession-v1alpha'), pub(e.keys.find(k => k.keyId === s.signedByKeyId)), s.signature));
  }
});
test('renewal refuses other scopes, unknown target keys, missing challenges, and duplicate siblings', () => {
  const e = entry();
  assert.throws(() => renewalRequest({...e, app: {...e.app, applicationId: 'other-app'}}, 'key-0', challenge), /renewal-scope/);
  assert.throws(() => renewalRequest(e, 'unknown', challenge), /renewal-key/);
  assert.throws(() => renewalRequest(e, 'key-0', {}), /renewal-challenge/);
  assert.throws(() => renewalRequest({...e, keys: [e.keys[0], e.keys[0], e.keys[2]]}, 'key-0', challenge), /renewal-siblings/);
});
