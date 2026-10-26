import {readFileSync, mkdtempSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {generateKeyPairSync, randomUUID} from 'node:crypto';
import {RegularRpClient} from '../.build/release-sdk/src/index.js';
import {wire, hash, decode, same} from '../release/policy.mjs';
import {ApprovalRecorder} from '../release/approval.mjs';
import {ReleaseController} from '../release/controller.mjs';
import {ReleaseClient} from '../release/client.mjs';

export function fixture() {
  const dir = mkdtempSync(join(tmpdir(), 'opal-release-test-'));
  const config = JSON.parse(readFileSync(new URL('./fixtures/release-policy.json', import.meta.url)));
  let at = Date.parse('2026-10-18T03:00:00Z');
  const now = () => new Date(at);
  const identity = {domain: 'catalystsquad.local', accountId: '00000000-0000-4000-8000-000000000001'};
  const app = {identity, applicationId: 'opal-release-approval', instanceId: 'test'};
  const controller = {...app, applicationId: 'opal-release-controller'};
  const recorderKey = {...generateKeyPairSync('ed25519'), keyId: 'recorder-key'};
  const controllerKey = {...generateKeyPairSync('ed25519'), keyId: 'controller-key'};
  let account = config.approver.account_id, assertionVerified = true, assertionOverrides = {};
  const replies = new Map();
  const createLogin = ({pendingStore, now}) => new RegularRpClient({rpDomain: 'catalystsquad.local', pendingStore, now, clockSkewMs: 0, dnsResolver: {txtLookup: async () => []}, rpOperations: {
    async signRequest(r) {replies.set(r.nonce, r); return {signedRequest: 'fixture-signed-request'};},
    async decryptToken(r) {return {signedAssertion: r.encryptedToken};},
    async verifyAssertion(r) {return {verified: assertionVerified, assertion: {userId: account, domain: config.approver.domain, audience: 'catalystsquad.local', nonce: r.signedAssertion, issuedAt: now().toISOString(), expiresAt: new Date(+now() + 300000).toISOString(), authorizedClaims: [], ...assertionOverrides}};},
    async userinfoFetch() {return {userId: account, domain: config.approver.domain, displayName: 'Synthetic approver', claims: []};},
  }});
  const origin = 'https://approval.catalystsquad.local';
  const options = {path: join(dir, 'approval.sqlite'), config, app, controller, origin, signer: recorderKey, createLogin, now};
  let recorder = new ApprovalRecorder(options);
  const resolveKey = async (who, id) => {
    const key = same(who, app) && id === recorderKey.keyId ? recorderKey : same(who, controller) && id === controllerKey.keyId ? controllerKey : undefined;
    if (!key) throw Error('key-refused');
    return {publicKey: key.publicKey, createdAt: new Date(+now() - 86400000), expiresAt: new Date(+now() + 86400000)};
  };
  const description = {protocol: '0alpha1', schema: {name: 'opal', version: '0alpha1', digest: hash('schema')}, implementedOperations: ['Discovery/describe'], features: ['strict-cbor'], maxFrameBytes: 262144, maxCollectionItems: 256, maxDepth: 24, requestTimeoutMs: 10000, authenticatedOperations: []};
  const envelope = (variant, body) => wire.encodeValue(new Map([['v', 1], ['id', 1], ['status', 0], ['variant', variant], ['payload', {tag: 24, value: body}]]));
  const expected = envelope('ServiceDescription', wire.toServiceDescriptionCbor(description));
  const previous = envelope('ServiceDescription', wire.toServiceDescriptionCbor({...description, schema: {...description.schema, digest: hash('previous-schema')}}));
  const value = {candidateId: randomUUID(), target: {domain: config.target_domain, targetId: config.target_id, resourceUid: 'isolated-resource-uid', generation: 1}, sourceDigest: hash('source'), recipeDigest: hash('recipe'), configurationDigest: hash('config'), image: `${config.image_repository}@sha256:${'a'.repeat(64)}`, previousImage: `${config.image_repository}@sha256:${'b'.repeat(64)}`, schemaChanges: 'none', evidence: [{authority: identity, kind: 'artifact-check', recordId: 'fixture-check'}], expiresAt: new Date(+now() + 3600000), checks: {readinessPath: '/health/ready', operationPath: '/csil/v1/rpc', requestBody: wire.encodeValue(new Map([['v', 1], ['id', 1], ['service', 'Discovery'], ['op', 'describe'], ['payload', {tag: 24, value: wire.toDescribeRequestCbor({versions: ['0alpha1'], requiredFeatures: ['strict-cbor']})}]])), expectedBody: expected, previousExpectedBody: previous, successCount: 2, failureCount: 2, observationSeconds: 30, rollbackSeconds: 30}};
  let snapshot = {uid: value.target.resourceUid, generation: 1, version: 'v1', image: value.previousImage, configuration: hash('config').toString('hex'), marker: ''};
  let behavior = 'good', changes = [], delay = 0, loseReply = false;
  const backend = {
    async inspect() {return structuredClone(snapshot);},
    async replace(before, image, marker) {
      if (!same(before, snapshot)) throw Error('CAS');
      const updated = {...snapshot, generation: before.generation + 1, version: `v${changes.length + 2}`, image, marker};
      changes.push({before: structuredClone(before), image, marker}); snapshot = updated;
      if (loseReply) throw Error('lost-reply');
    },
    async probe(expected, checks, restoring) {
      at += delay;
      return {readiness: restoring || behavior !== 'unready', operation: restoring ? behavior !== 'broken-restore' : !['bad-protocol', 'broken-restore'].includes(behavior), bound: same(snapshot, expected)};
    },
  };
  const authorization = {recorder: app, controller, signer: controllerKey, resolveKey, getStatus: request => recorder.status(request, resolveKey)};
  value.explanation = {title: 'Test discovery release', reason: 'Check the isolated release path.', changes: ['Update the isolated discovery image.'], risks: ['The canary can be unavailable during recovery.']};
  const client = new ReleaseClient({endpoint: `${origin}/csil/v1/rpc`, recorder: app, controller, signer: controllerKey, resolveKey, now,
    call: (op, packet) => recorder[{'submit':'submit', 'pull-events':'pullEvents', 'acknowledge-event':'acknowledgeEvent', 'fetch-approval':'fetchApproval', 'status':'status'}[op]](packet, resolveKey)});
  let execution;
  const f = {
    dir, config, origin, app, controller, recorderKey, controllerKey, value, now, resolveKey, authorization, backend, options, client,
    get recorder() {return recorder;}, get execution() {return execution;}, get changes() {return changes;},
    advance(ms) {at += ms;}, setAccount(v) {account = v;}, setAssertion(v) {assertionOverrides = v;}, rejectAssertion() {assertionVerified = false;},
    behavior(v) {behavior = v;}, delay(ms) {delay = ms;}, loseReplies() {loseReply = true;}, mutate(v) {snapshot = {...snapshot, ...v};},
    async begin() {const binding = 'synthetic-browser-binding'; const r = await recorder.beginLogin(config.login_hint, binding); return {...r, binding, token: new URL(r.redirectUrl).searchParams.get('nonce')};},
    async login() {const r = await f.begin(); return {...await recorder.finishLogin(r.state, r.token, r.binding), origin};},
    approve(auth, bytes = wire.toReleaseCandidateCbor(value), commandKey = randomUUID()) {const r = recorder.review(auth, bytes); return recorder.approve(auth, {reviewId: r.reviewId, digest: r.digest, commandKey});},
    restartRecorder(fault = () => {}) {recorder.close(); recorder = new ApprovalRecorder({...options, fault});},
    startController(fault = () => {}, tasks) {execution?.close(); execution = undefined; execution = new ReleaseController({path: join(dir, 'controller.sqlite'), config, backend, authorization, now, fault, tasks}); return execution;},
    async run(max = 25) {for (let i = 0; i < max; i++) {const r = await execution.step(value.candidateId); if (r.terminal) return r; f.advance(1100);} throw Error('controller-did-not-stop');},
    close() {recorder.close(); execution?.close();},
  };
  return f;
}
