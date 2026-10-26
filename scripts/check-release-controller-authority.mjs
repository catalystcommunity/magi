import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
// Operator verification with API-server impersonation. Mutations are dry-run.
import {execFileSync} from 'node:child_process';
import {readFileSync, writeFileSync} from 'node:fs';
import {subject, policyName, fields} from '../deploy/release-controller-render.mjs';
import {need} from '../release/policy.mjs';
const base = ['--context', environmentConfig.kubeContext, '--request-timeout=15s'];
const run = args => execFileSync('kubectl', [...base, ...args], {encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe']});
const k = JSON.parse(run(['config', 'view', '--minify', '-o', 'json'])); need(k.clusters[0].cluster.server === environmentConfig.clusterServer, 'wrong-cluster');
const schemas = JSON.parse(run(['get', '--raw', '/openapi/v3/apis/apps/v1'])).components.schemas;
for (const [key, expected] of Object.entries(fields)) need(JSON.stringify(Object.keys(schemas[key].properties).sort()) === JSON.stringify([...expected].sort()), 'authority-schema-drift');
const checks = [];
for (const [verb, resource, ns, allowed] of [
  ['get', 'deployments/release-canary', environmentConfig.namespace, true], ['patch', 'deployments/release-canary', environmentConfig.namespace, true],
  ['get', 'pods', environmentConfig.namespace, true], ['list', 'replicasets', environmentConfig.namespace, true],
  ['get', 'secrets', environmentConfig.namespace, false], ['patch', 'deployments/release-approval', environmentConfig.namespace, false],
  ['patch', 'deployments/release-controller', environmentConfig.namespace, false], ['create', 'pods', environmentConfig.namespace, false],
  ['create', 'pods/exec', environmentConfig.namespace, false], ['create', 'pods/portforward', environmentConfig.namespace, false],
  ['create', 'serviceaccounts/token', environmentConfig.namespace, false], ['create', 'rolebindings', environmentConfig.namespace, false],
  ['patch', 'deployments/release-canary', 'default', false], ['list', 'secrets', 'kube-system', false],
]) {
  const subresource = ['pods/exec', 'pods/portforward', 'serviceaccounts/token'].includes(resource);
  const target = subresource ? [resource.split('/')[0], `--subresource=${resource.split('/')[1]}`] : [resource];
  let out; try {out = run(['--as', subject, '-n', ns, 'auth', 'can-i', verb, ...target]);} catch (e) {out = e.stdout;}
  const result = String(out).trim(); need(result === (allowed ? 'yes' : 'no'), 'unexpected-rbac-result'); checks.push({verb, resource, namespace: ns, allowed});
}
const d = JSON.parse(run(['-n', environmentConfig.namespace, 'get', 'deployment', 'release-canary', '-o', 'json']));
const image = JSON.parse(readFileSync(operatorFile('release-rollback-image.json'))).image;
need(image !== d.spec.template.spec.containers[0].image, 'dry-run-needs-different-image');
const allowed = [{op: 'test', path: '/metadata/resourceVersion', value: d.metadata.resourceVersion}, {op: 'replace', path: '/spec/template/spec/containers/0/image', value: image}, {op: 'add', path: '/metadata/annotations/opal.catalystcommunity.com~1release-operation', value: 'authority-check/promote'}];
const dry = patch => run(['--as', subject, '-n', environmentConfig.namespace, 'patch', 'deployment', 'release-canary', '--type=json', '--dry-run=server', '-p', JSON.stringify(patch), '-o', 'name']);
dry(allowed); checks.push({admission: 'image-and-marker', allowed: true});
const cases = [
  ['service-account', '/spec/template/spec/serviceAccountName', 'opal-release-controller'],
  ['mount-secret', '/spec/template/spec/volumes', [{name: 'forbidden', secret: {secretName: 'release-approval'}}]],
  ['host-network', '/spec/template/spec/hostNetwork', true],
  ['pod-overhead', '/spec/template/spec/overhead', {cpu: '1'}],
  ['resource-limit', '/spec/template/spec/containers/0/resources/limits/cpu', '2'],
  ['command', '/spec/template/spec/containers/0/command', ['sh', '-c', 'true']],
  // Keep the invalid input structurally valid so rejection proves this policy,
  // not the API's privileged/allowPrivilegeEscalation consistency check.
  ['privileged', '/spec/template/spec/containers/0/securityContext', {...d.spec.template.spec.containers[0].securityContext, privileged: true, allowPrivilegeEscalation: true}],
  ['pod-label', '/spec/template/metadata/labels/unauthorized', 'value'],
  ['replicas', '/spec/replicas', 2],
  ['mutable-image', '/spec/template/spec/containers/0/image', (environmentConfig.repository + ":latest")],
  ['other-annotation', '/metadata/annotations/unauthorized', 'value'],
];
for (const [label, path, value] of cases) {
  let rejected = false;
  try {dry([...allowed, {op: 'add', path, value}]);}
  catch (e) {const error = String(e.stderr); rejected = error.includes(policyName) && error.includes('Magi controller');}
  need(rejected, `admission-denial-not-proved-${label}`); checks.push({admission: label, allowed: false});
}
const after = JSON.parse(run(['-n', environmentConfig.namespace, 'get', 'deployment', 'release-canary', '-o', 'json']));
need(after.metadata.generation === d.metadata.generation && JSON.stringify(after.spec) === JSON.stringify(d.spec), 'dry-run-changed-target');
const report = {checkedAt: new Date().toISOString(), subject, policy: policyName, checks, generation: d.metadata.generation, canaryChanged: false};
const file = `test-results/${report.checkedAt.replaceAll(':', '-')}-release-controller-authority.json`; writeFileSync(file, JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({file, checks: checks.length, policy: 'enforced', canaryChanged: false}));
