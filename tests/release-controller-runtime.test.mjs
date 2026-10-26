import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, writeFileSync, readFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {EventEmitter} from 'node:events';
import {PassThrough} from 'node:stream';
import {apiTransport, apiCommands, KubernetesAPI} from '../release/kubernetes-api.mjs';
import {manifests, fields, subject, policyName, seedPod} from '../deploy/release-controller-render.mjs';
import {crashDrill} from '../release/crash-drill.mjs';
import {fixture} from './release-fixture.mjs';

const config = JSON.parse(readFileSync(new URL('./fixtures/release-policy.json', import.meta.url)));
test('native API rereads rotated tokens, verifies TLS, and does not expose rejection bodies', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'opal-api-test-')), tokenFile = join(dir, 'token'), caFile = join(dir, 'ca');
  writeFileSync(tokenFile, 'first'); writeFileSync(caFile, 'test-ca');
  const requests = []; let status = 200;
  const request = (url, options, callback) => {
    requests.push({url, options}); const r = new EventEmitter();
    r.end = () => {const response = new PassThrough(); response.statusCode = status; callback(response); response.end(status === 200 ? '{}' : 'SECRET-UPSTREAM-DETAIL');};
    return r;
  };
  const api = apiTransport({server: config.cluster_server, tokenFile, caFile, request});
  assert.equal(await api('/first'), '{}'); writeFileSync(tokenFile, 'rotated'); await api('/second');
  assert.equal(requests[0].options.headers.authorization, 'Bearer first'); assert.equal(requests[1].options.headers.authorization, 'Bearer rotated');
  assert.equal(requests[0].options.rejectUnauthorized, true); assert.equal(requests[0].options.ca.toString(), 'test-ca');
  status = 403; await assert.rejects(api('/denied'), {message: 'kubernetes-status-403'});
  assert.throws(() => apiTransport({server: 'https://elsewhere', tokenFile, caFile, request}), /wrong-cluster/);
});
test('native API bridge has no exec, forward, secret, or other-target operation', async () => {
  const calls = [], run = apiCommands(config, async (...a) => {calls.push(a); return '{}';});
  await run(['-n', config.namespace, 'get', 'deployment', 'release-canary', '-o', 'json']);
  await run(['-n', config.namespace, 'get', 'pods', '-l', 'app.kubernetes.io/name=opal-release-canary', '-o', 'json']);
  await run(['-n', config.namespace, 'patch', 'deployment', 'release-canary', '--type=json', '-p', '[]', '-o', 'name']);
  assert.equal(calls[2][1], 'PATCH');
  for (const args of [
    ['-n', 'other', 'get', 'pods'], ['-n', config.namespace, 'get', 'secret', 'release-approval', '-o', 'json'],
    ['-n', config.namespace, 'get', 'deployment', 'release-approval', '-o', 'json'], ['-n', config.namespace, 'exec', 'pod'],
    ['-n', config.namespace, 'get', 'pods', '-l', 'other=value', '-o', 'json'],
  ]) await assert.rejects(run(args));
});
test('direct probe connections are limited to non-host-network canary pod addresses', async () => {
  let pod = {spec: {}, status: {podIP: '198.18.7.218'}};
  const backend = new KubernetesAPI({config, call: async () => JSON.stringify(pod)});
  assert.equal((await backend.forward('release-canary-test')).origin, 'http://198.18.7.218:8080');
  for (const ip of ['127.0.0.1', '169.254.169.254', '192.0.2.10', '198.18.999.1', '198.18.1.1:80']) {
    pod.status.podIP = ip; await assert.rejects(backend.forward('release-canary-test'), /canary-pod-address/);
  }
  pod = {spec: {hostNetwork: true}, status: {podIP: '198.18.1.1'}}; await assert.rejects(backend.forward('release-canary-test'));
});
test('controller manifests separate authority, storage, health, and scoped admission', () => {
  const image = `${config.image_repository}@sha256:${'a'.repeat(64)}`, resources = manifests(image);
  const d = resources.find(r => r.kind === 'Deployment'), pod = d.spec.template.spec;
  assert.equal(d.metadata.name, 'release-controller'); assert.equal(d.spec.strategy.type, 'Recreate'); assert.equal(d.spec.replicas, 1);
  assert.equal(pod.automountServiceAccountToken, false); assert.equal(pod.serviceAccountName, 'opal-release-controller');
  assert.equal(pod.volumes.find(v => v.name === 'kubernetes').projected.sources[0].serviceAccountToken.expirationSeconds, 3600);
  assert.equal(pod.containers[0].securityContext.readOnlyRootFilesystem, true);
  assert.deepEqual(resources.find(r => r.kind === 'Role').rules[0], {apiGroups: ['apps'], resources: ['deployments'], resourceNames: ['release-canary'], verbs: ['get', 'patch']});
  assert(!JSON.stringify(resources.find(r => r.kind === 'Role')).includes('secrets'));
  const policy = resources.find(r => r.kind === 'ValidatingAdmissionPolicy');
  assert.equal(policy.metadata.name, policyName); assert.equal(policy.spec.failurePolicy, 'Fail'); assert(policy.spec.matchConditions[0].expression.includes(subject));
  assert.deepEqual(resources.find(r => r.kind === 'ValidatingAdmissionPolicyBinding').spec.validationActions, ['Deny']);
  for (const f of fields['io.k8s.api.core.v1.PodSpec'].filter(f => f !== 'containers')) assert(policy.spec.validations.some(v => v.expression.includes(f === 'overhead' ? 'dyn(object.spec.template.spec).overhead' : `spec.${f}`)));
  const seed = seedPod(image); assert.equal(seed.spec.automountServiceAccountToken, false); assert.deepEqual(seed.spec.volumes.map(v => v.name), ['data']);
});
test('durable crash drill stops only the selected restore effect once across restart', async t => {
  const f = fixture(); t.after(() => f.close()); const auth = await f.login(), packet = f.approve(auth); f.startController(); await f.execution.admit(packet);
  let stops = 0; const stop = () => {stops++; throw Error('simulated-SIGKILL');};
  const hook = crashDrill({candidateId: f.value.candidateId, point: 'after-restore-effect'}, stop);
  f.execution.fault = p => hook(p, f.execution); f.behavior('bad-protocol');
  await assert.rejects(f.run(), /simulated-SIGKILL/); assert.equal(stops, 1);
  assert.equal(f.execution.read(f.value.candidateId).body.phase, 'restoring');
  f.startController(); f.execution.fault = p => hook(p, f.execution);
  assert.equal((await f.run()).body.phase, 'restored'); assert.equal(stops, 1); assert.equal(f.changes.length, 2);
  assert.equal(f.execution.db.prepare('SELECT count(*) n FROM supervision_faults').get().n, 1);
  assert.throws(() => crashDrill({candidateId: f.value.candidateId, point: 'arbitrary'}), /crash-drill-scope/);
});
