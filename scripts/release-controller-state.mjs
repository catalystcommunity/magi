import {deployment as environmentConfig} from '../config/deployment.mjs';
// Operator evidence export. The service never receives this kubeconfig.
import {readFileSync, writeFileSync, existsSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import {randomUUID} from 'node:crypto';
import {need} from '../release/policy.mjs';
import {owned} from '../deploy/release-controller-render.mjs';

export function controllerState(runtime) {
  const marker = new URL((environmentConfig.releaseProfileDir + "/controller-supervised.json"), import.meta.url);
  if (!existsSync(marker)) return {path: runtime.path, source: 'local-operator'};
  const installation = JSON.parse(readFileSync(marker));
  need(installation.localPath === runtime.path && installation.namespace === environmentConfig.namespace && installation.deployment === 'release-controller', 'controller-installation-binding');
  const args = ['--kubeconfig', runtime.kubeconfig, '--request-timeout=15s'];
  const run = (a, input) => {try {return execFileSync('kubectl', [...args, ...a], {input, stdio: ['pipe', 'pipe', 'pipe'], maxBuffer: 16 * 1024 * 1024});} catch {throw Error('controller-evidence-export-unavailable');}};
  const config = JSON.parse(run(['config', 'view', '--minify', '-o', 'json'])); need(config.clusters?.[0]?.cluster?.server === environmentConfig.clusterServer, 'wrong-cluster');
  const deployment = JSON.parse(run(['-n', installation.namespace, 'get', 'deployment', installation.deployment, '-o', 'json'])); need(owned(deployment), 'controller-owner');
  const script = `import {DatabaseSync,backup} from 'node:sqlite';import {readFileSync,unlinkSync} from 'node:fs';import {randomUUID} from 'node:crypto';
const path='/data/evidence-copy-'+randomUUID()+'.sqlite',db=new DatabaseSync('/data/controller.sqlite',{readOnly:true});
try {await backup(db,path);process.stdout.write(readFileSync(path));}finally{db.close();try{unlinkSync(path);}catch{}}
`;
  const bytes = run(['-n', installation.namespace, 'exec', '-i', 'deployment/release-controller', '--', 'node', '--input-type=module'], script);
  need(bytes.subarray(0, 16).equals(Buffer.from('SQLite format 3\0')), 'controller-evidence-format');
  const path = new URL(((environmentConfig.releaseProfileDir + "/controller-snapshot-") + (randomUUID()) + ".sqlite"), import.meta.url);
  writeFileSync(path, bytes, {flag: 'wx', mode: 0o600});
  return {path: path.pathname, source: 'supervised-controller', deploymentUid: deployment.metadata.uid, collectedAt: new Date().toISOString()};
}
