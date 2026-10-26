import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
// Read-only checks. This command does not approve, apply, or restart anything.
import {execFileSync} from 'node:child_process';
import {writeFileSync, readFileSync, existsSync} from 'node:fs';
import {DatabaseSync} from 'node:sqlite';
import {resolve, join} from 'node:path';
import {privateConfig} from '../release/main.mjs';
import {linkkeys} from '../release/linkkeys.mjs';
import {controllerState} from './release-controller-state.mjs';
const root = resolve(import.meta.dirname, '..');
const supervised = existsSync(new URL((environmentConfig.releaseProfileDir + "/controller-supervised.json"), import.meta.url));
const output = execFileSync('kubectl', ['--context', environmentConfig.kubeContext, '--request-timeout=10s', '-n', environmentConfig.namespace, 'get', 'deployments', 'release-approval', 'release-canary', ...(supervised ? ['release-controller'] : []), '-o', 'json'], {encoding: 'utf8'});
const workloads = JSON.parse(output).items.map(d => ({name: d.metadata.name, uid: d.metadata.uid, generation: d.metadata.generation, desired: d.spec.replicas, ready: d.status?.readyReplicas ?? 0, image: d.spec.template.spec.containers[0].image, conditions: d.status?.conditions ?? []}));
const expected = JSON.parse(readFileSync(operatorFile('release-approval-image.json'))).image;
if (workloads.find(d => d.name === 'release-approval')?.image !== expected) throw Error('Pending approval image differs from the final build');
let https;
try {const r = await fetch(("https://" + environmentConfig.approvalHost + "/health/ready"), {redirect: 'error', signal: AbortSignal.timeout(10000)}); https = {tls: 'verified', status: r.status}; await r.body?.cancel();}
catch (error) {https = {tls: 'unconfirmed', error: error.cause?.code ?? error.name};}
const applications = [];
for (const name of ['approval', 'controller']) {
  const config = privateConfig(((environmentConfig.releaseProfileDir + "/") + (name) + ".json"));
  try {const key = await linkkeys(config).resolveKey(config.app, config.signer.keyId); applications.push({application: config.app.applicationId, currentKey: 'verified', expiresAt: key.expiresAt});}
  catch {applications.push({application: config.app.applicationId, currentKey: 'unavailable'});}
}
execFileSync(process.execPath, ['deploy/release-apply.mjs', '--verify'], {cwd: root, stdio: 'pipe'});
// This check must not claim that no approval exists. Report retained controller
// observations; the recorder owns approvals that have not reached admission.
const runtime = privateConfig((environmentConfig.releaseProfileDir + "/controller.json"));
const state = controllerState(runtime);
let controllerReleases = [];
if (existsSync(state.path)) {
  const db = new DatabaseSync(state.path, {readOnly: true});
  try {controllerReleases = db.prepare('SELECT id,approval_id,terminal,body FROM releases').all().map(row => {
    const body = JSON.parse(row.body);
    return {candidateId: row.id, approvalId: row.approval_id, phase: body.phase, terminal: !!row.terminal, reason: body.reason};
  });} finally {db.close();}
}
const report = {checkedAt: new Date().toISOString(), workloads, https, applications, existingOpalDeployments: 'unchanged', controllerState: state.source, controllerReleases};
const file = join(root, `test-results/${report.checkedAt.replaceAll(':', '-')}-release-live.json`);
writeFileSync(file, JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({file, workloads: workloads.map(d => ({name: d.name, ready: d.ready, desired: d.desired, reasons: d.conditions.map(c => c.reason)})), https, applications, existingOpalDeployments: report.existingOpalDeployments, controllerReleases}));
