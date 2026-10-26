import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import {need} from '../release/policy.mjs';
import {owned} from '../deploy/release-controller-render.mjs';

export function operator(runtime) {
  const run = (args, input) => {try {return execFileSync('kubectl', ['--kubeconfig', runtime.kubeconfig, '--request-timeout=15s', ...args], {input, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], maxBuffer: 4 * 1024 * 1024});} catch {throw Error('controller-operator-command-failed');}};
  const config = JSON.parse(run(['config', 'view', '--minify', '-o', 'json'])); need(config.clusters?.[0]?.cluster?.server === environmentConfig.clusterServer, 'wrong-cluster');
  return run;
}
export function controllerPod(run) {
  const get = args => JSON.parse(run(['-n', environmentConfig.namespace, 'get', ...args, '-o', 'json']));
  const d = get(['deployment', 'release-controller']), image = JSON.parse(readFileSync(operatorFile('release-controller-image.json'))).image;
  need(owned(d) && d.spec.replicas === 1 && d.spec.template.spec.containers[0].image === image, 'controller-deployment');
  const sets = get(['replicasets', '-l', 'app.kubernetes.io/name=opal-release-controller']).items;
  const ids = new Set(sets.filter(r => owned(r) && r.metadata.ownerReferences?.some(o => o.kind === 'Deployment' && o.uid === d.metadata.uid && o.controller)).map(r => r.metadata.uid));
  const pods = get(['pods', '-l', 'app.kubernetes.io/name=opal-release-controller']).items.filter(p => !p.metadata.deletionTimestamp && owned(p) && p.metadata.ownerReferences?.some(o => o.kind === 'ReplicaSet' && ids.has(o.uid) && o.controller));
  need(pods.length === 1, 'controller-pod-count'); const p = pods[0], s = p.status?.containerStatuses?.find(c => c.name === 'controller');
  need(p.spec.containers.length === 1 && p.spec.containers[0].image === image && s, 'controller-container');
  return {name: p.metadata.name, podUid: p.metadata.uid, deploymentUid: d.metadata.uid, image, containerId: s.containerID, restarts: s.restartCount, ready: s.ready, startedAt: s.state?.running?.startedAt, terminated: s.lastState?.terminated};
}
