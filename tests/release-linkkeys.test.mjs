import test from 'node:test';
import assert from 'node:assert/strict';
import {generateKeyPairSync, createHash, sign} from 'node:crypto';
import {keyResolver, codec, sdk} from '../release/linkkeys.mjs';

function fixture({capacity = 16} = {}) {
  let at = Date.parse('2026-10-18T20:00:00Z'), calls = 0, edit = () => {}, fail = false;
  const subject = {identity: {accountId: '00000000-0000-4000-8000-000000000001', domain: 'catalystsquad.local'}, applicationId: 'controller', instanceId: 'one'};
  const domain = generateKeyPairSync('ed25519'), app = generateKeyPairSync('ed25519');
  const raw = key => Buffer.from(key.export({format: 'jwk'}).x, 'base64url');
  const key = raw(app.publicKey), fingerprint = createHash('sha256').update(key).digest('hex');
  const iso = offset => new Date(at + offset).toISOString();
  const resolve = keyResolver({capacity, domains: ['catalystsquad.local', 'partner.local'], now: () => new Date(at), fetchEvidence: async (_subject, instance) => {
    calls++; if (fail) throw Error('upstream-offline');
    const a = {...instance, keyId: 'app-key', keyUsage: 'sign', algorithm: 'ed25519', publicKey: key, fingerprint,
      keyCreatedAt: iso(-60000), keyExpiresAt: iso(60000), attestedAt: iso(-1000), attestationExpiresAt: iso(60000)};
    const anchors = [{keyId: 'domain-key', keyUsage: 'sign', algorithm: 'ed25519', publicKey: raw(domain.publicKey), createdAt: iso(-60000), expiresAt: iso(60000)}];
    const response = {...instance, cacheStatus: 'refreshed', fetchedAt: iso(0), revocationsCheckedAt: iso(0), applicationKeyRevocations: []};
    edit({a, anchors, response});
    const attestation = codec.toApplicationKeyAttestationCbor(a);
    response.applicationKeys = [{attestation, signatures: [{domain: instance.subjectDomain, signedByKeyId: 'domain-key', signature: sign(null, sdk.attestationSignatureInput(attestation), domain.privateKey)}]}];
    return {anchors, response};
  }});
  return {subject, resolve, get calls() {return calls;}, advance(ms) {at += ms;}, edit(fn) {edit = fn;}, fail() {fail = true;}};
}

test('key evidence coalesces concurrent reads and reuses the remaining one-second budget', async () => {
  const f = fixture();
  await Promise.all(Array.from({length: 10}, () => f.resolve(f.subject, 'app-key')));
  f.advance(999); await f.resolve(f.subject, 'app-key'); assert.equal(f.calls, 1);
  f.advance(1); await f.resolve(f.subject, 'app-key'); assert.equal(f.calls, 2);
});

test('raw evidence is verified again for app and attestation expiry and domain key expiry', async () => {
  for (const field of ['keyExpiresAt', 'attestationExpiresAt', 'domain']) {
    const f = fixture();
    f.edit(({a, anchors, response}) => {const expires = new Date(Date.parse(response.fetchedAt) + 100).toISOString(); if (field === 'domain') anchors[0].expiresAt = expires; else a[field] = expires;});
    await f.resolve(f.subject, 'app-key'); f.advance(101);
    await assert.rejects(f.resolve(f.subject, 'app-key'), /key-validity|key-evidence/); assert.equal(f.calls, 1);
  }
});

test('cache keys bind account, domain, application, and instance; the cache is bounded', async () => {
  const f = fixture({capacity: 2}); await f.resolve(f.subject, 'app-key');
  for (const variant of [
    {...f.subject, identity: {...f.subject.identity, accountId: '00000000-0000-4000-8000-000000000002'}},
    {...f.subject, identity: {...f.subject.identity, domain: 'partner.local'}},
    {...f.subject, applicationId: 'other'}, {...f.subject, instanceId: 'other'},
  ]) await f.resolve(variant, 'app-key');
  assert.equal(f.calls, 5); await f.resolve(f.subject, 'app-key'); assert.equal(f.calls, 6);
  await assert.rejects(f.resolve(f.subject, 'wrong-key'), /key-validity/);
  await assert.rejects(f.resolve({...f.subject, identity: {...f.subject.identity, domain: 'untrusted.test'}}, 'app-key'), /untrusted-domain/);
});

test('upstream timestamps reduce reuse; failed refresh never uses stale evidence', async () => {
  const f = fixture(); f.edit(({response}) => {response.revocationsCheckedAt = new Date(Date.parse(response.fetchedAt) - 800).toISOString();});
  await f.resolve(f.subject, 'app-key'); f.advance(199); await f.resolve(f.subject, 'app-key'); assert.equal(f.calls, 1);
  f.advance(1); f.fail(); await assert.rejects(f.resolve(f.subject, 'app-key'), /upstream-offline/); assert.equal(f.calls, 2);
  await assert.rejects(f.resolve(f.subject, 'app-key'), /upstream-offline/); assert.equal(f.calls, 3);
});

test('refresh detects revoked domain keys; no earlier verdict grants continued use', async () => {
  const f = fixture(); await f.resolve(f.subject, 'app-key'); f.advance(1000);
  f.edit(({anchors, response}) => {anchors[0].revokedAt = response.fetchedAt;});
  await assert.rejects(f.resolve(f.subject, 'app-key'), /key-evidence/); assert.equal(f.calls, 2);
});

test('stale, future, invalid, and wrong-subject evidence is refused and not cached', async () => {
  for (const edit of [
    ({response}) => {response.cacheStatus = 'stale';},
    ({response}) => {response.fetchedAt = new Date(Date.parse(response.fetchedAt) + 1).toISOString();},
    ({response}) => {response.revocationsCheckedAt = new Date(Date.parse(response.fetchedAt) - 5001).toISOString();},
    ({response}) => {response.revocationsCheckedAt = 'invalid';},
    ({response}) => {response.instanceId = 'wrong';},
    ({a}) => {a.instanceId = 'wrong';},
    ({a}) => {a.attestedAt = new Date(Date.parse(a.attestedAt) + 2000).toISOString();},
  ]) {
    const f = fixture(); f.edit(edit);
    await assert.rejects(f.resolve(f.subject, 'app-key'), /key-/);
    await assert.rejects(f.resolve(f.subject, 'app-key'), /key-/); assert.equal(f.calls, 2);
  }
});

test('clock reversal refuses reuse; concurrent lookup count is bounded', async () => {
  const f = fixture({capacity: 1});
  const first = f.resolve(f.subject, 'app-key');
  await assert.rejects(f.resolve({...f.subject, instanceId: 'other'}, 'app-key'), /key-lookups-busy/);
  await first; f.advance(-1); await assert.rejects(f.resolve(f.subject, 'app-key'), /key-clock-reversal/);
  f.advance(1); await f.resolve(f.subject, 'app-key'); assert.equal(f.calls, 2);
});
