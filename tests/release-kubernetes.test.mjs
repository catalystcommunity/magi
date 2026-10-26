import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {fixture} from './release-fixture.mjs';
import {KubernetesCanary, snapshot} from '../release/kubernetes.mjs';

function deployment(f) {
  return {apiVersion: 'apps/v1', kind: 'Deployment', metadata: {name: f.config.deployment, namespace: f.config.namespace, uid: 'deployment-uid', resourceVersion: '10', generation: 1, labels: {'app.kubernetes.io/part-of': 'opal', 'app.kubernetes.io/managed-by': 'opal-release'}, annotations: {}}, spec: {replicas: 1, selector: {matchLabels: {'app.kubernetes.io/name': 'opal-release-canary'}}, template: {metadata: {labels: {'app.kubernetes.io/name': 'opal-release-canary'}}, spec: {serviceAccountName: 'opal-release-canary', automountServiceAccountToken: false, securityContext: {runAsNonRoot: true}, containers: [{name: 'opal', image: f.value.previousImage, securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ['ALL']}}}]}}}, status: {observedGeneration: 1, updatedReplicas: 1, availableReplicas: 1, replicas: 1}};
}
test('Kubernetes adapter allows only the named isolated deployment and exact CAS patch', async t => {
  const f = fixture(); t.after(() => f.close()); const d = deployment(f), calls = [];
  const backend = new KubernetesCanary({config: f.config, kubeconfig: '/tmp/test-kubeconfig', run: async args => {
    calls.push(args);
    if (args[0] === 'config') return JSON.stringify({clusters: [{cluster: {server: f.config.cluster_server}}]});
    if (args[2] === 'get') return JSON.stringify(d);
    if (args[2] === 'patch') return 'deployment.apps/release-canary';
    throw Error('unexpected command');
  }});
  const before = await backend.inspect(); await backend.replace(before, f.value.image, `${f.value.candidateId}/promote`);
  const command = calls.find(args => args[2] === 'patch'), patch = JSON.parse(command[command.indexOf('-p') + 1]);
  assert.deepEqual(command.slice(0, 6), ['-n', 'magi-example', 'patch', 'deployment', 'release-canary', '--type=json']);
  assert.deepEqual(patch.slice(0, 4).map(p => p.op), ['test', 'test', 'test', 'test']);
  assert.deepEqual(patch.filter(p => p.op !== 'test').map(p => p.path), ['/spec/template/spec/containers/0/image', '/metadata/annotations']);
  d.metadata.resourceVersion = '11'; await assert.rejects(backend.replace(before, f.value.image, `${f.value.candidateId}/promote`), /target-cas/);
  const changed = snapshot(d, f.config); assert.equal(changed.configuration, before.configuration);
  d.spec.template.spec.containers[0].envFrom = [{secretRef: {name: 'do-not-mount'}}];
  assert.throws(() => snapshot(d, f.config), /canary-credentials-or-privilege/);
});
test('wrong cluster and pre-existing workload names fail closed', async t => {
  const f = fixture(); t.after(() => f.close());
  const backend = new KubernetesCanary({config: f.config, kubeconfig: '/tmp/test-kubeconfig', run: async () => JSON.stringify({clusters: [{cluster: {server: 'https://production.invalid'}}]})});
  await assert.rejects(backend.inspect(), /wrong-cluster/);
  assert.throws(() => new KubernetesCanary({config: {...f.config, deployment: 'qa-api'}, kubeconfig: '/tmp/test-kubeconfig'}), /test-target-only/);
});
test('probe binds CSIL response to a ready pod owned by the expected deployment', async t => {
  const f = fixture(); t.after(() => f.close()); const d = deployment(f);
  let body = f.value.checks.previousExpectedBody, podUid = 'pod-uid', closed = 0;
  const server = createServer((req, res) => {
    assert.equal(req.headers.host, 'canary.catalystsquad.local');
    if (req.url === '/health/ready') {res.statusCode = 204; return res.end();}
    res.setHeader('content-type', 'application/cbor'); res.end(body);
  });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(async () => {server.closeAllConnections(); await new Promise(resolve => server.close(resolve));});
  const pod = () => ({metadata: {name: 'release-canary-pod', uid: podUid, ownerReferences: [{uid: 'replicaset-uid', kind: 'ReplicaSet', controller: true}]}, spec: {containers: [{image: f.value.previousImage}]}, status: {containerStatuses: [{state: {running: {}}}], conditions: [{type: 'Ready', status: 'True'}]}});
  const backend = new KubernetesCanary({config: f.config, kubeconfig: '/tmp/test-kubeconfig', forward: async () => ({origin: `http://127.0.0.1:${server.address().port}`, close: async () => {closed++;}}), run: async args => {
    if (args[0] === 'config') return JSON.stringify({clusters: [{cluster: {server: f.config.cluster_server}}]});
    const kind = args[3];
    if (kind === 'deployment') return JSON.stringify(d);
    if (kind === 'replicasets') return JSON.stringify({items: [{metadata: {uid: 'replicaset-uid', ownerReferences: [{uid: d.metadata.uid, kind: 'Deployment', controller: true}]}, spec: d.spec}]});
    if (kind === 'pods') return JSON.stringify({items: [pod()]});
    if (kind === 'pod') return JSON.stringify(pod());
    throw Error('unexpected command');
  }});
  const expected = await backend.inspect(), good = await backend.probe(expected, f.value.checks, true);
  assert(good.operation && good.readiness && good.bound); assert.equal(good.podUid, 'pod-uid'); assert.equal(closed, 1);
  body = f.value.checks.expectedBody; assert.equal((await backend.probe(expected, f.value.checks, true)).operation, false);
  d.metadata.resourceVersion = 'status-update'; assert.equal((await backend.probe(expected, f.value.checks, false)).bound, true);
  d.metadata.generation++; assert.equal((await backend.probe(expected, f.value.checks, false)).bound, false);
});
