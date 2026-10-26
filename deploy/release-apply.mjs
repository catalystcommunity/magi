import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from '../scripts/operator-state.mjs';
import {runtimePolicy} from '../release/configuration.mjs';
import {execFileSync} from 'node:child_process';
import {readFileSync, writeFileSync} from 'node:fs';
import {resolve, join} from 'node:path';
import {createHash} from 'node:crypto';
import {readPrivate} from './qa-bootstrap.mjs';
import {manifests, labels, namespace, hostname} from './release-render.mjs';
const root = resolve(import.meta.dirname, '..');
const mode = process.argv[2]; if (!['--check', '--apply', '--verify'].includes(mode) || process.argv.length !== 3) throw Error('Use --check, --apply, or --verify');
const run = args => execFileSync('kubectl', ['--context', environmentConfig.kubeContext, '--request-timeout=15s', ...args], {encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], maxBuffer: 4 * 1024 * 1024});
const get = args => JSON.parse(run(['get', ...args, '-o', 'json']));
const digest = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
const kube = JSON.parse(run(['config', 'view', '--minify', '-o', 'json']));
if (kube.clusters?.[0]?.cluster?.server !== environmentConfig.clusterServer) throw Error('wrong-cluster');
const owned = r => r.metadata.labels?.['app.kubernetes.io/part-of'] === 'opal' && r.metadata.labels?.['app.kubernetes.io/managed-by'] === 'opal-release';
const ns = get(['namespace', namespace]); if (ns.metadata.labels?.['app.kubernetes.io/part-of'] !== 'opal') throw Error('namespace-owner');
const allDeployments = get(['deployments', '-n', namespace]).items;
for (const d of allDeployments) if (d.metadata.name === 'release-controller' && !owned(d)) throw Error('controller-owner');
const deployments = allDeployments.filter(d => !['release-canary', 'release-approval', 'release-controller'].includes(d.metadata.name));
const protectedState = Object.fromEntries(deployments.map(d => [d.metadata.name, {uid: d.metadata.uid, generation: d.metadata.generation, specDigest: digest(d.spec)}]));
const report = operatorFile('release-deployment.json');
if (mode === '--verify') {
  const previous = JSON.parse(readFileSync(report));
  if (JSON.stringify(protectedState) !== JSON.stringify(previous.protectedState)) throw Error('Existing Magi workloads changed; inspect before any further operation');
  console.log('Existing Magi deployment specs and generations are unchanged');
} else {
  const approvalImage = JSON.parse(readFileSync(operatorFile('release-approval-image.json'))).image;
  const baseline = deployments.find(d => d.metadata.name === 'opal')?.spec.template.spec.containers.find(c => c.name === 'opal')?.image;
  const items = manifests(approvalImage, baseline);
  for (const route of get(['httproutes', '-A']).items) if (route.spec.hostnames?.includes(hostname) && (route.metadata.namespace !== namespace || route.metadata.name !== 'release-approval' || !owned(route))) throw Error('hostname-owned-elsewhere');
  for (const route of get(['ingress', '-A']).items) if (route.spec.rules?.some(r => r.host === hostname)) throw Error('hostname-owned-elsewhere');
  for (const r of [...items, {kind: 'Secret', metadata: {name: 'release-approval'}}]) {
    const existing = run(['get', r.kind, r.metadata.name, '-n', namespace, '--ignore-not-found', '-o', 'json']);
    if (existing.trim() && !owned(JSON.parse(existing))) throw Error(`resource-owner:${r.kind}/${r.metadata.name}`);
    // This setup command must not reset an installed canary after promotion.
    if (existing.trim() && r.kind === 'Deployment' && r.metadata.name === 'release-canary') {
      if (JSON.parse(existing).spec.template.spec.containers[0].image !== baseline) throw Error('Existing canary image differs. Use the release controller, not setup.');
    }
  }
  console.log(JSON.stringify({status: 'preflight-passed', namespace, hostname, resources: items.map(r => `${r.kind}/${r.metadata.name}`)}));
  if (mode === '--apply') {
    const privateText = readPrivate((environmentConfig.releaseProfileDir + "/approval.json"));
    runtimePolicy(JSON.parse(privateText));
    const secret = {apiVersion: 'v1', kind: 'Secret', metadata: {name: 'release-approval', namespace, labels}, type: 'Opaque', data: {'approval.json': Buffer.from(privateText).toString('base64')}};
    try {run(['get', 'secret', 'opal-registry', '-n', namespace, '-o', 'name']);} catch {throw Error('Magi registry pull secret missing');}
    writeFileSync(report, JSON.stringify({at: new Date().toISOString(), approvalImage, baseline, protectedState}, null, 2) + '\n');
    const apply = (r, secret = false) => {
      try {
        const result = execFileSync('kubectl', ['--context', environmentConfig.kubeContext, '--request-timeout=15s', 'apply', '--server-side', '--field-manager=opal-release-setup', '-f', '-'], {input: JSON.stringify(r), encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe']});
        console.log(secret ? 'Secret/release-approval applied' : result.trim());
      } catch {throw Error(`Apply failed: ${r.kind}/${r.metadata.name}. Credentials were not printed.`);}
    };
    apply(secret, true);
    for (const r of items) apply(r);
  }
}
