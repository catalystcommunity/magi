import {deployment as environmentConfig} from '../config/deployment.mjs';
import {createHash, timingSafeEqual} from 'node:crypto';
import {readFileSync} from 'node:fs';
import {isDeepStrictEqual} from 'node:util';
import * as wire from '../.build/releases/codec.gen.js';
import {decode as profileDecode, checkFrame} from '../protocol/profile.mjs';

export {wire};
export const decode = (type, bytes) => profileDecode(type, bytes, wire);
export const hash = bytes => createHash('sha256').update(bytes).digest();
export const equal = (a, b) => a instanceof Uint8Array && b instanceof Uint8Array && a.length === b.length && timingSafeEqual(a, b);
export const need = (condition, code) => {if (!condition) throw Error(code);};
export const same = isDeepStrictEqual;
export const descriptor = readFileSync(new URL('../generated/releases/typescript/releases.csil-schema.cbor', import.meta.url));
export const schema = {name: 'opal-releases', version: '0alpha1', digest: Uint8Array.from(wire.decode(descriptor).get('digest'))};
export const scope = (kind, signer, recipient) => ({context: `opal.release.${kind}.v1`, schema, signer, recipient});
export const validText = (text, max = 256) => typeof text === 'string' && text.length > 0 && text.length <= max && !/[\x00-\x1f\x7f]/.test(text);
export const identity = value => need(value && /^[a-z0-9]+(?:[.-][a-z0-9]+)+$/.test(value.domain) && /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(value.accountId), 'identity');
export const application = value => {identity(value?.identity); need(validText(value.applicationId) && validText(value.instanceId), 'application');};
export const time = date => need(date instanceof Date && Number.isFinite(+date), 'timestamp');

export function policy(config) {
  need(config.version === 1 && config.schema_changes === 'none', 'policy-version');
  identity({domain: config.approver?.domain, accountId: config.approver?.account_id});
  need(config.target_domain === environmentConfig.domain && config.namespace === environmentConfig.namespace && config.deployment === 'release-canary' && config.container === 'opal' && config.target_id === 'opal-stateless-canary-v1', 'test-target-only');
  need(config.cluster_server === environmentConfig.clusterServer && config.image_repository === environmentConfig.repository, 'test-cluster-only');
  // Freeze a copy. Callers cannot change authority while a request is pending.
  return Object.freeze({...config, approver: Object.freeze({...config.approver})});
}

export function candidate(bytes, config, now = new Date(), {allowExpired = false} = {}) {
  const c = decode('ReleaseCandidate', bytes);
  need(/^[A-Za-z0-9._:-]{1,256}$/.test(c.candidateId) && c.target.domain === config.target_domain && c.target.targetId === config.target_id && validText(c.target.resourceUid), 'candidate-target');
  need(Number.isSafeInteger(c.target.generation) && c.target.generation > 0, 'candidate-generation');
  for (const d of [c.sourceDigest, c.recipeDigest, c.configurationDigest]) need(d.length === 32 && d.some(v => v !== 0), 'candidate-digest');
  for (const image of [c.image, c.previousImage]) need(image.startsWith(`${config.image_repository}@sha256:`) && /^[0-9a-f]{64}$/.test(image.slice(config.image_repository.length + 8)), 'immutable-image');
  need(c.image !== c.previousImage && c.schemaChanges === 'none', 'stateless-candidate');
  if (c.explanation !== undefined) {
    const e = c.explanation;
    need(validText(e.title, 160) && validText(e.reason, 2000), 'candidate-explanation');
    for (const items of [e.changes, e.risks]) need(items.length > 0 && items.length <= 16 && items.every(s => validText(s, 2000)), 'candidate-explanation');
  }
  const q = c.checks;
  need(q.readinessPath === '/health/ready' && q.operationPath === '/csil/v1/rpc', 'probe-path');
  need(q.requestBody.length > 0 && q.requestBody.length <= 8192, 'probe-body');
  // Require a real CSIL Discovery call, not an arbitrary URL or script.
  checkFrame(q.requestBody);
  const rpc = wire.decode(q.requestBody);
  need(rpc instanceof Map && rpc.size === 5 && rpc.get('v') === 1 && rpc.get('id') === 1 && rpc.get('service') === 'Discovery' && rpc.get('op') === 'describe' && rpc.get('payload')?.tag === 24, 'probe-operation');
  const request = decode('DescribeRequest', rpc.get('payload').value);
  need(same(request, {versions: ['0alpha1'], requiredFeatures: ['strict-cbor']}), 'probe-request');
  for (const bytes of [q.expectedBody, q.previousExpectedBody]) {
    need(bytes.length > 0 && bytes.length <= 32768, 'probe-body'); checkFrame(bytes);
    const response = wire.decode(bytes);
    need(response instanceof Map && response.size === 5 && response.get('v') === 1 && response.get('id') === 1 && response.get('status') === 0 && response.get('variant') === 'ServiceDescription' && response.get('payload')?.tag === 24, 'probe-response');
    const description = decode('ServiceDescription', response.get('payload').value);
    need(description.protocol === '0alpha1' && description.features.includes('strict-cbor') && description.schema.digest.length === 32, 'probe-description');
  }
  need(Number.isSafeInteger(q.successCount) && q.successCount >= 2 && q.successCount <= 10 && Number.isSafeInteger(q.failureCount) && q.failureCount >= 1 && q.failureCount <= 10, 'probe-count');
  need(Number.isSafeInteger(q.observationSeconds) && q.observationSeconds >= 10 && q.observationSeconds <= 300 && Number.isSafeInteger(q.rollbackSeconds) && q.rollbackSeconds >= 10 && q.rollbackSeconds <= 300, 'probe-deadline');
  need(c.evidence.length > 0 && c.evidence.length <= 32, 'build-evidence');
  for (const e of c.evidence) {identity(e.authority); need(validText(e.kind) && validText(e.recordId), 'evidence-ref');}
  time(c.expiresAt);
  need(allowExpired || +c.expiresAt > +now && +c.expiresAt <= +now + 86400000, 'candidate-expiry');
  return c;
}
