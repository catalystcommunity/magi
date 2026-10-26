import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from '../scripts/operator-state.mjs';
import {runtimePolicy} from '../release/configuration.mjs';
// Operator setup only. Never mount this process's kubeconfig into the controller.
import {execFileSync} from 'node:child_process';
import {readFileSync, writeFileSync, chmodSync} from 'node:fs';
import {DatabaseSync, backup} from 'node:sqlite';
import {createHash} from 'node:crypto';
import {setTimeout} from 'node:timers/promises';
import {privateConfig} from '../release/main.mjs';
import {need, decode, hash} from '../release/policy.mjs';
import {manifests, seedPod, fields, owned, name, policyName} from './release-controller-render.mjs';
import {namespace, labels} from './release-render.mjs';

const mode = process.argv[2], drillId = process.argv[3];
need(['--prepare', '--seed', '--start', '--verify', '--clear-crash', '--upgrade-runtime'].includes(mode) && process.argv.length === 3 || mode === '--arm-crash' && process.argv.length === 4 && /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(drillId), 'Use --prepare, --seed, --start, --verify, --upgrade-runtime, --arm-crash candidate-id, or --clear-crash');
const digest = b => createHash('sha256').update(b).digest('hex');
function run(args, input) {
  try {return execFileSync('kubectl', ['--context', environmentConfig.kubeContext, '--request-timeout=15s', ...args], {input, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], maxBuffer: 4 * 1024 * 1024});}
  catch {throw Error(`Kubernetes operator ${args[0]} failed. Credentials were not printed.`);}
}
const get = args => JSON.parse(run(['get', ...args, '-o', 'json']));
const cluster = JSON.parse(run(['config', 'view', '--minify', '-o', 'json']));
need(cluster.clusters?.[0]?.cluster?.server === environmentConfig.clusterServer, 'wrong-cluster');
need(get(['namespace', namespace]).metadata.labels?.['app.kubernetes.io/part-of'] === 'opal', 'namespace-owner');
const runtime = privateConfig((environmentConfig.releaseProfileDir + "/controller.json"));
const image = JSON.parse(readFileSync(operatorFile('release-controller-image.json'))).image;
const items = manifests(image), deployment = items.find(r => r.kind === 'Deployment');
const reportPath = operatorFile('release-controller-deployment.json');
const apply = r => {
  const scope = r.metadata.namespace ? ['-n', r.metadata.namespace] : [];
  const existing = run(['get', r.kind, r.metadata.name, ...scope, '--ignore-not-found', '-o', 'json']);
  if (existing.trim()) need(owned(JSON.parse(existing)), 'resource-owner');
  run(['apply', '--server-side', '--field-manager=opal-release-controller', '-f', '-'], JSON.stringify(r));
  console.log(`${r.kind}/${r.metadata.name} applied`);
};
const noLocalWork = () => {
  const db = new DatabaseSync(runtime.path, {readOnly: true});
  try {need(db.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n === 0, 'local-release-active');}
  finally {db.close();}
  const processes = execFileSync('ps', ['-eo', 'args='], {encoding: 'utf8'});
  need(!processes.split('\n').some(p => /^node release\/run[.]mjs .* --(?:watch|once|admit|resume|retry-unstarted)(?: |$)/.test(p)), 'local-controller-running');
};
const absentController = () => need(!run(['-n', namespace, 'get', 'deployment', name, '--ignore-not-found', '-o', 'name']).trim(), 'controller-already-installed');

if (mode === '--prepare') {
  noLocalWork(); absentController();
  execFileSync(process.execPath, ['deploy/release-apply.mjs', '--verify'], {stdio: 'pipe'});
  const schemas = JSON.parse(run(['get', '--raw', '/openapi/v3/apis/apps/v1'])).components.schemas;
  for (const [key, names] of Object.entries(fields)) need(JSON.stringify(Object.keys(schemas[key].properties).sort()) === JSON.stringify([...names].sort()), 'admission-schema-drift');
  const q = get(['resourcequota', 'opal', '-n', namespace]); need(q.metadata.labels?.['app.kubernetes.io/part-of'] === 'opal', 'quota-owner');
  if (q.spec.hard.persistentvolumeclaims === '3') run(['-n', namespace, 'patch', 'resourcequota', 'opal', '--type=json', '-p', JSON.stringify([
    {op: 'test', path: '/metadata/resourceVersion', value: q.metadata.resourceVersion}, {op: 'test', path: '/spec/hard/persistentvolumeclaims', value: '3'}, {op: 'replace', path: '/spec/hard/persistentvolumeclaims', value: '4'},
  ])]); else need(q.spec.hard.persistentvolumeclaims === '4', 'unexpected-opal-quota');
  for (const r of items.filter(r => r.kind !== 'Deployment')) apply(r);
  for (let i = 0; i < 20; i++) {
    const p = get(['validatingadmissionpolicy', policyName]);
    if (p.status?.observedGeneration === p.metadata.generation) {
      // Kubernetes omits the empty typeChecking object after a successful check.
      need(!p.status.typeChecking?.expressionWarnings?.length, 'admission-expression-warnings'); break;
    }
    need(i < 19, 'admission-typecheck-pending'); await setTimeout(500);
  }
  const {rp, domains, app, recorder, controller, signer, approvalEndpoint} = runtime;
  const profile = {rp, domains, app, recorder, controller, signer, approvalEndpoint, policy: runtimePolicy(runtime), path: '/data/controller.sqlite', kubernetes: {caFile: '/kubernetes/ca.crt', tokenFile: '/kubernetes/token'}};
  apply({apiVersion: 'v1', kind: 'Secret', metadata: {name, namespace, labels}, type: 'Opaque', data: {'controller.json': Buffer.from(JSON.stringify(profile)).toString('base64')}});
  const baseline = get(['deployment', 'release-canary', '-n', namespace]);
  writeFileSync(reportPath, JSON.stringify({preparedAt: new Date().toISOString(), image, baseline: {uid: baseline.metadata.uid, generation: baseline.metadata.generation, image: baseline.spec.template.spec.containers[0].image}, admissionPolicy: policyName, quotaChange: {opalPVCs: '3 -> 4', storageLimit: '8Gi unchanged'}}, null, 2) + '\n');
  apply(seedPod(image));
}
if (mode === '--seed') {
  noLocalWork(); absentController();
  const pod = get(['pod', 'release-controller-seed', '-n', namespace]); need(owned(pod) && pod.spec.containers[0].image === image && pod.status.phase === 'Running', 'seed-pod');
  const file = ((environmentConfig.releaseProfileDir + "/controller-seed-") + (Date.now()) + ".sqlite");
  const db = new DatabaseSync(runtime.path, {readOnly: true});
  try {await backup(db, file); chmodSync(file, 0o600);} finally {db.close();}
  const bytes = readFileSync(file), sha = digest(bytes);
  const script = `const fs=require('node:fs'),crypto=require('node:crypto');let chunks=[];process.stdin.on('data',b=>chunks.push(b));process.stdin.on('end',()=>{const b=Buffer.concat(chunks),p='/data/controller.sqlite';if(crypto.createHash('sha256').update(b).digest('hex')!=='${sha}')throw Error('seed-digest');if(fs.existsSync(p)){if(crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')!=='${sha}')throw Error('seed-exists-and-differs');}else fs.writeFileSync(p,b,{mode:384,flag:'wx'});const db=new(require('node:sqlite').DatabaseSync)(p,{readOnly:true});if(db.prepare('PRAGMA integrity_check').get().integrity_check!=='ok'||db.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n)throw Error('seed-check');console.log(JSON.stringify({seed:'verified',releases:db.prepare('SELECT count(*) n FROM releases').get().n,observations:db.prepare('SELECT count(*) n FROM observations').get().n}));db.close();});`;
  const result = JSON.parse(run(['-n', namespace, 'exec', '-i', 'pod/release-controller-seed', '--', 'node', '-e', script], bytes));
  const report = JSON.parse(readFileSync(reportPath)); Object.assign(report, {seed: {...result, digest: sha, localBackup: file, podUid: pod.metadata.uid}}); writeFileSync(reportPath, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(result));
}
if (mode === '--start') {
  noLocalWork(); absentController();
  execFileSync(process.execPath, ['scripts/check-release-controller-authority.mjs'], {stdio: 'pipe'});
  const report = JSON.parse(readFileSync(reportPath)); need(report.seed?.seed === 'verified' && report.image === image, 'seed-required');
  const current = get(['deployment', 'release-canary', '-n', namespace]);
  need(current.metadata.uid === report.baseline.uid && current.metadata.generation === report.baseline.generation && current.spec.template.spec.containers[0].image === report.baseline.image, 'canary-changed');
  const pod = get(['pod', 'release-controller-seed', '-n', namespace]); need(owned(pod) && pod.metadata.uid === report.seed.podUid, 'seed-owner');
  // The API checks the UID again when it deletes this exact temporary importer.
  run(['delete', '--raw', `/api/v1/namespaces/${namespace}/pods/release-controller-seed`, '-f', '-'], JSON.stringify({apiVersion: 'v1', kind: 'DeleteOptions', preconditions: {uid: pod.metadata.uid}, gracePeriodSeconds: 1}));
  apply(deployment);
  writeFileSync((environmentConfig.releaseProfileDir + "/controller-supervised.json"), JSON.stringify({namespace, deployment: name, localPath: runtime.path, startedAt: new Date().toISOString()}, null, 2) + '\n', {mode: 0o600});
}
if (mode === '--upgrade-runtime') {
  const d = get(['deployment', name, '-n', namespace]), report = JSON.parse(readFileSync(reportPath));
  need(owned(d) && d.spec.template.spec.containers[0].image === report.image && image !== report.image, 'controller-upgrade-binding');
  const active = JSON.parse(run(['-n', namespace, 'exec', 'deployment/release-controller', '--', 'node', '-e', "const db=new(require('node:sqlite').DatabaseSync)('/data/controller.sqlite',{readOnly:true});console.log(JSON.stringify({active:db.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n}));db.close();"]));
  need(active.active === 0, 'controller-release-active');
  run(['-n', namespace, 'patch', 'deployment', name, '--type=json', '-p', JSON.stringify([
    {op: 'test', path: '/metadata/uid', value: d.metadata.uid}, {op: 'test', path: '/metadata/resourceVersion', value: d.metadata.resourceVersion},
    {op: 'replace', path: '/spec/template/spec/containers/0/image', value: image}, {op: 'replace', path: '/spec/template/spec/containers/0/command', value: deployment.spec.template.spec.containers[0].command},
  ])]);
  (report.runtimeChanges ??= []).push({at: new Date().toISOString(), previousImage: report.image, image}); report.image = image;
  writeFileSync(reportPath, JSON.stringify(report, null, 2) + '\n'); console.log(JSON.stringify({controller: 'idle-runtime-upgrade-requested', image}));
}
if (mode === '--verify') {
  const d = get(['deployment', name, '-n', namespace]); need(owned(d) && d.spec.template.spec.containers[0].image === image && d.status.readyReplicas === 1 && d.status.observedGeneration === d.metadata.generation, 'controller-not-ready');
  execFileSync(process.execPath, ['deploy/release-apply.mjs', '--verify'], {stdio: 'pipe'});
  console.log(JSON.stringify({controller: 'ready', uid: d.metadata.uid, generation: d.metadata.generation, image, protectedDeployments: 'unchanged'}));
}
if (mode === '--arm-crash' || mode === '--clear-crash') {
  const d = get(['deployment', name, '-n', namespace]); need(owned(d) && d.spec.template.spec.containers[0].image === image, 'controller-owner-or-image');
  const active = JSON.parse(run(['-n', namespace, 'exec', 'deployment/release-controller', '--', 'node', '-e', "const d=new(require('node:sqlite').DatabaseSync)('/data/controller.sqlite',{readOnly:true});console.log(JSON.stringify({active:d.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n}));d.close();"]));
  need(active.active === 0, 'controller-release-active');
  if (mode === '--arm-crash') {
    const report = JSON.parse(readFileSync(operatorFile(`release-candidate-${drillId}.json`))), bytes = readFileSync(((environmentConfig.releaseProfileDir + "/candidate-") + (drillId) + ".cbor")), c = decode('ReleaseCandidate', bytes);
    need(report.supervisionDrill && report.purpose === 'isolated-rollback-drill' && report.candidateDigest === hash(bytes).toString('hex') && c.candidateId === drillId && +c.expiresAt > Date.now(), 'crash-drill-proposal');
    need(c.explanation?.changes.some(s => s.includes('SIGKILL')), 'crash-drill-explanation');
    const canary = get(['deployment', 'release-canary', '-n', namespace]);
    need(canary.metadata.uid === c.target.resourceUid && canary.metadata.generation === c.target.generation && canary.spec.template.spec.containers[0].image === c.previousImage, 'crash-drill-target-changed');
  }
  const secret = get(['secret', name, '-n', namespace]); need(owned(secret), 'controller-secret-owner');
  const profile = JSON.parse(Buffer.from(secret.data['controller.json'], 'base64'));
  if (mode === '--arm-crash') profile.crashDrill = {candidateId: drillId, point: 'after-restore-effect'}; else delete profile.crashDrill;
  apply({apiVersion: 'v1', kind: 'Secret', metadata: {name, namespace, labels}, type: 'Opaque', data: {'controller.json': Buffer.from(JSON.stringify(profile)).toString('base64')}});
  const annotations = {...d.spec.template.metadata.annotations, 'opal.catalystcommunity.com/controller-config': `${mode}:${drillId ?? 'none'}:${Date.now()}`};
  run(['-n', namespace, 'patch', 'deployment', name, '--type=json', '-p', JSON.stringify([
    {op: 'test', path: '/metadata/uid', value: d.metadata.uid}, {op: 'test', path: '/metadata/resourceVersion', value: d.metadata.resourceVersion}, {op: 'add', path: '/spec/template/metadata/annotations', value: annotations},
  ])]);
  console.log(JSON.stringify({controller: 'configuration-restart-requested', crashDrill: mode === '--arm-crash' ? drillId : 'disabled', humanApproval: 'still-required-before-deployment'}));
}
