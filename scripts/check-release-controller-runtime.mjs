import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
// Operator checks. The two idle modes deliberately stop the controller child.
import {readFileSync, writeFileSync} from 'node:fs';
import {DatabaseSync} from 'node:sqlite';
import {createHash} from 'node:crypto';
import {setTimeout} from 'node:timers/promises';
import {privateConfig} from '../release/main.mjs';
import {need} from '../release/policy.mjs';
import {controllerState} from './release-controller-state.mjs';
import {controllerPod, operator} from './release-controller-inspection.mjs';
import {gracefulRestartProof, supervisionProof} from './release-supervision-proof.mjs';

const mode = process.argv[2], id = process.argv[3];
need(['--check', '--restart-idle', '--crash-idle'].includes(mode) && process.argv.length === 3 || mode === '--baseline' && process.argv.length === 4 && /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(id), 'Use --check, --restart-idle, --crash-idle, or --baseline candidate-id');
const runtime = privateConfig((environmentConfig.releaseProfileDir + "/controller.json")), run = operator(runtime), before = controllerPod(run);
need(before.ready && before.startedAt, 'controller-not-ready');
const state = controllerState(runtime); need(state.source === 'supervised-controller', 'controller-not-installed');
function audit(path) {
  const db = new DatabaseSync(path, {readOnly: true});
  try {
    need(db.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n === 0, 'controller-release-active');
    const rows = {};
    for (const [table, order] of [['releases', 'id'], ['observations', 'release_id,sequence'], ['delivery_inbox', 'id']]) rows[table] = db.prepare(`SELECT * FROM ${table} ORDER BY ${order}`).all();
    return {digest: createHash('sha256').update(JSON.stringify(rows)).digest('hex'), counts: Object.fromEntries(Object.entries(rows).map(([k, v]) => [k, v.length]))};
  } finally {db.close();}
}
const original = audit(runtime.path), copied = audit(state.path);
need(original.digest === copied.digest, 'controller-migrated-audit-differs');
// Use the actual mounted token, restricted API adapter, and pod-network probe.
const probeScript = ("import {readFileSync} from 'node:fs';import {DatabaseSync} from 'node:sqlite';import {KubernetesAPI,apiTransport} from './release/kubernetes-api.mjs';import {decode,need} from './release/policy.mjs';\nconst r=JSON.parse(readFileSync('/private/controller.json')),config=r.policy??JSON.parse(readFileSync('config/release-example.json')),db=new DatabaseSync(r.path,{readOnly:true});\nconst rows=db.prepare('SELECT candidate,body FROM releases WHERE terminal=1').all(),api=new KubernetesAPI({config,...r.kubernetes}),before=await api.inspect();\nconst match=rows.find(row=>{const b=JSON.parse(row.body);return (b.restored??b.deployed)?.generation===before.generation;});need(match,'controller-probe-baseline');\nconst candidate=decode('ReleaseCandidate',match.candidate),probe=await api.probe(before,candidate.checks,true);need(probe.readiness&&probe.operation&&probe.bound,'controller-direct-probe');\nconst call=apiTransport({server:config.cluster_server,...r.kubernetes});let refused=false;try{await call('/api/v1/namespaces/" + environmentConfig.namespace + "/secrets/release-approval');}catch(e){refused=e.message==='kubernetes-status-403';}need(refused,'controller-secret-access');\nconsole.log(JSON.stringify({target:before,probe,secretRead:'denied-403',transport:'projected-token-and-direct-pod-http'}));db.close();");
const activeProbeScript = probeScript.replaceAll("config/release-example.json", environmentConfig.legacyReleasePolicyPath);
const probe = JSON.parse(run(['-n', environmentConfig.namespace, 'exec', '-i', `pod/${before.name}`, '--', 'node', '--input-type=module'], activeProbeScript));
let recovery;
if (['--restart-idle', '--crash-idle'].includes(mode)) {
  const pendingScript = `const db=new(require('node:sqlite').DatabaseSync)('/data/approval.sqlite',{readOnly:true});console.log(JSON.stringify({pending:db.prepare('SELECT count(*) n FROM release_events WHERE acknowledged=0').get().n}));db.close();`;
  const pending = JSON.parse(run(['-n', environmentConfig.namespace, 'exec', 'deployment/release-approval', '--', 'node', '-e', pendingScript])); need(pending.pending === 0, 'approval-delivery-pending');
  const faultAt = Date.now();
  const signal = mode === '--crash-idle' ? 'SIGKILL' : 'SIGTERM';
  // Select the exact controller child of our PID-1 launcher, not another process.
  const killScript = `const fs=require('node:fs'),db=new(require('node:sqlite').DatabaseSync)('/data/controller.sqlite',{readOnly:true});if(db.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n!==0)throw Error('active');db.close();if(!fs.readFileSync('/proc/1/cmdline','utf8').split(String.fromCharCode(0)).includes('release/controller-init.mjs'))throw Error('process-identity');const children=fs.readdirSync('/proc').filter(n=>/^[0-9]+$/.test(n)).filter(n=>{try{return fs.readFileSync('/proc/'+n+'/cmdline','utf8').split(String.fromCharCode(0)).includes('/app/release/controller-cluster.mjs')&&/^PPid:\\s+1$/m.test(fs.readFileSync('/proc/'+n+'/status','utf8'));}catch{return false;}});if(children.length!==1)throw Error('controller-child');process.kill(Number(children[0]),'${signal}');`;
  writeFileSync(operatorFile(`release-controller-restart-attempt-${faultAt}.json`), JSON.stringify({before, requestedAt: faultAt, signal, scope: 'idle restart, not active restoration recovery'}) + '\n');
  // Stopping PID 1 can close the exec stream. Later termination evidence proves it.
  try {run(['-n', environmentConfig.namespace, 'exec', `pod/${before.name}`, '--', 'node', '-e', killScript]);} catch {}
  for (let n = 0; n < 20; n++) {
    await setTimeout(2000); const after = controllerPod(run);
    if (after.ready && after.restarts > before.restarts) {recovery = (mode === '--crash-idle' ? supervisionProof : gracefulRestartProof)(before, after, faultAt); break;}
  }
  need(recovery, 'controller-restart-not-proved');
  need(audit(controllerState(runtime).path).digest === copied.digest, 'controller-restart-audit-changed');
  const after = controllerPod(run);
  const afterProbe = JSON.parse(run(['-n', environmentConfig.namespace, 'exec', '-i', `pod/${after.name}`, '--', 'node', '--input-type=module'], activeProbeScript));
  need(afterProbe.target.generation === probe.target.generation && afterProbe.target.image === probe.target.image, 'idle-crash-changed-canary');
  recovery.afterProbe = afterProbe;
}
const report = {checkedAt: new Date().toISOString(), mode, before, audit: copied, probe, recovery};
if (mode === '--baseline') {
  const preparation = JSON.parse(readFileSync(operatorFile(`release-candidate-${id}.json`))); need(preparation.supervisionDrill && preparation.candidateId === id, 'controller-drill-candidate');
  report.candidateId = id; report.candidateDigest = preparation.candidateDigest;
  const file = operatorFile(`release-controller-crash-${id}.json`); writeFileSync(file, JSON.stringify(report, null, 2) + '\n', {flag: 'wx'}); console.log(JSON.stringify({file, candidateId: id, ready: true}));
} else {
  const file = `test-results/${report.checkedAt.replaceAll(':', '-')}-release-controller-runtime.json`; writeFileSync(file, JSON.stringify(report, null, 2) + '\n'); console.log(JSON.stringify({file, audit: copied.counts, directProbe: 'passed', secretRead: 'denied', restarted: !!recovery}));
}
