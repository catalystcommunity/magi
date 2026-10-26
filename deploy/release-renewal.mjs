import {deployment as environmentConfig} from '../config/deployment.mjs';
// Operator renewal of existing application attestations, not enrollment or approval.
import {createPrivateKey, sign} from 'node:crypto';
import {codec, tuple} from '../release/linkkeys.mjs';
import {application, need} from '../release/policy.mjs';

export function renewalRequest(entry, keyId, challenge, now = new Date()) {
  application(entry.app);
  need(entry.app.identity.domain === environmentConfig.domain && ['opal-release-approval', 'opal-release-controller'].includes(entry.app.applicationId) && entry.app.instanceId === environmentConfig.applicationInstance, 'renewal-scope');
  need(entry.keys.length === 3 && new Set(entry.keys.map(k => k.keyId)).size === 3, 'renewal-siblings');
  const key = entry.keys.find(k => k.keyId === keyId); need(key, 'renewal-key');
  need(challenge.challengeId && challenge.challenge?.length && Number.isFinite(+now), 'renewal-challenge');
  const renewal = codec.toApplicationKeyRenewalCbor({
    subjectUserId: entry.app.identity.accountId, subjectDomain: entry.app.identity.domain,
    applicationId: entry.app.applicationId, instanceId: entry.app.instanceId, keyId,
    challengeId: challenge.challengeId, challenge: challenge.challenge,
    requestedAt: now.toISOString(), expiresAt: new Date(+now + 60000).toISOString(),
  });
  const signed = (key, tag) => sign(null, tuple.encodeCborTuple([tuple.text(tag), tuple.bytes(renewal)]), createPrivateKey({key: key.key, format: 'jwk'}));
  return {renewal, possessionProof: signed(key, 'linkkeys-application-key-possession-v1alpha'),
    signatures: entry.keys.filter(k => k.keyId !== keyId).map(k => ({signedByKeyId: k.keyId, signature: signed(k, 'linkkeys-application-key-renewal-v1alpha')}))};
}
