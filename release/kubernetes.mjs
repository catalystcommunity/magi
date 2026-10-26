import {deployment as environmentConfig} from '../config/deployment.mjs';
import {execFile, spawn} from 'node:child_process';
import {promisify} from 'node:util';
import {once} from 'node:events';
import {request as httpRequest} from 'node:http';
import {hash, policy, need, equal} from './policy.mjs';
import {matches} from './controller.mjs';
const execute = promisify(execFile);
const markerKey = 'opal.catalystcommunity.com/release-operation';
const sorted = value => Array.isArray(value) ? value.map(sorted) : value && typeof value === 'object' ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b, 'en')).map(([k, v]) => [k, sorted(v)])) : value;

function probeHTTP(origin, path, body) {
  return new Promise((resolve, reject) => {
    const request = httpRequest(`${origin}${path}`, {method: body ? 'POST' : 'GET', headers: {host: environmentConfig.canaryHost, ...(body ? {'content-type': 'application/cbor', 'content-length': body.length} : {})}, signal: AbortSignal.timeout(3000)}, response => {
      const chunks = []; let length = 0;
      response.on('data', bytes => {
        length += bytes.length;
        if (length > 32768) {response.destroy(); reject(Error('probe-response-limit'));} else chunks.push(bytes);
      });
      response.on('error', reject);
      response.on('end', () => resolve({status: response.statusCode, type: response.headers['content-type'], body: Buffer.concat(chunks)}));
    });
    request.on('error', reject); request.end(body);
  });
}

export function snapshot(deployment, config) {
  const d = deployment, pod = d.spec?.template?.spec, containers = pod?.containers;
  need(d.apiVersion === 'apps/v1' && d.kind === 'Deployment' && d.metadata?.namespace === config.namespace && d.metadata.name === config.deployment, 'wrong-resource');
  need(d.metadata.labels?.['app.kubernetes.io/part-of'] === 'opal' && d.metadata.labels?.['app.kubernetes.io/managed-by'] === 'opal-release', 'resource-owner');
  need(typeof d.metadata.uid === 'string' && typeof d.metadata.resourceVersion === 'string' && Number.isSafeInteger(d.metadata.generation), 'resource-revision');
  need(d.spec.replicas === 1 && containers?.length === 1 && containers[0].name === config.container, 'canary-shape');
  need(pod.automountServiceAccountToken === false && pod.serviceAccountName === 'opal-release-canary' && !pod.hostNetwork && !pod.hostPID && !pod.hostIPC && !pod.initContainers?.length && !pod.volumes?.length, 'canary-isolation');
  const c = containers[0], security = c.securityContext;
  need(!c.envFrom?.length && !(c.env ?? []).some(v => v.valueFrom) && !c.volumeMounts?.length && !security?.privileged && security?.allowPrivilegeEscalation === false && security?.readOnlyRootFilesystem === true && security?.capabilities?.drop?.includes('ALL') && !security?.capabilities?.add?.length && pod.securityContext?.runAsNonRoot === true, 'canary-credentials-or-privilege');
  const configSpec = structuredClone(d.spec); configSpec.template.spec.containers[0].image = '<approved-image>';
  const annotations = {...d.metadata.annotations}; delete annotations[markerKey]; delete annotations['deployment.kubernetes.io/revision'];
  const configuration = hash(JSON.stringify(sorted({spec: configSpec, labels: d.metadata.labels, annotations}))).toString('hex');
  return {uid: d.metadata.uid, generation: d.metadata.generation, version: d.metadata.resourceVersion, image: c.image, configuration, marker: d.metadata.annotations?.[markerKey] ?? ''};
}

// No shell text or caller-selected resource names. RBAC must additionally limit
// this process to this Deployment and read/port-forward access to its pods.
export class KubernetesCanary {
  constructor({config, kubeconfig, run, forward}) {
    this.config = policy(config); need(typeof kubeconfig === 'string' && kubeconfig.startsWith('/'), 'kubeconfig-path');
    this.prefix = ['--kubeconfig', kubeconfig, '--request-timeout=8s'];
    this.run = run ?? (async args => (await execute('kubectl', [...this.prefix, ...args], {timeout: 10000, maxBuffer: 2 * 1024 * 1024})).stdout);
    this.forward = forward ?? (pod => this.portForward(pod));
  }
  async cluster() {
    const k = JSON.parse(await this.run(['config', 'view', '--minify', '-o', 'json']));
    need(k.clusters?.length === 1 && k.clusters[0].cluster?.server === this.config.cluster_server && !k.clusters[0].cluster?.['insecure-skip-tls-verify'], 'wrong-cluster');
  }
  async resource() {
    await this.cluster();
    return JSON.parse(await this.run(['-n', this.config.namespace, 'get', 'deployment', this.config.deployment, '-o', 'json']));
  }
  async inspect() {return snapshot(await this.resource(), this.config);}
  async replace(before, image, marker) {
    need(image.startsWith(`${this.config.image_repository}@sha256:`) && /^[0-9a-f]{64}$/.test(image.slice(this.config.image_repository.length + 8)), 'immutable-image');
    need(/^[A-Za-z0-9._:-]{1,256}\/(promote|restore)$/.test(marker), 'operation-marker');
    const d = await this.resource(), actual = snapshot(d, this.config);
    need(matches(actual, before) && actual.version === before.version, 'target-cas');
    const annotations = {...d.metadata.annotations, [markerKey]: marker};
    const patch = [
      {op: 'test', path: '/metadata/uid', value: before.uid},
      {op: 'test', path: '/metadata/resourceVersion', value: before.version},
      {op: 'test', path: '/metadata/generation', value: before.generation},
      {op: 'test', path: '/spec/template/spec/containers/0/image', value: before.image},
      {op: 'replace', path: '/spec/template/spec/containers/0/image', value: image},
      {op: 'add', path: '/metadata/annotations', value: annotations},
    ];
    await this.run(['-n', this.config.namespace, 'patch', 'deployment', this.config.deployment, '--type=json', '-p', JSON.stringify(patch), '-o', 'name']);
  }
  async portForward(pod) {
    need(/^[a-z0-9][a-z0-9-]{0,252}$/.test(pod), 'pod-name');
    const child = spawn('kubectl', [...this.prefix, '-n', this.config.namespace, 'port-forward', '--address=127.0.0.1', `pod/${pod}`, ':8080'], {stdio: ['ignore', 'pipe', 'pipe']});
    const closed = once(child, 'exit').catch(() => {});
    const close = async () => {child.kill('SIGTERM'); const timer = setTimeout(() => child.kill('SIGKILL'), 1000); await closed; clearTimeout(timer);};
    try {
      const port = await new Promise((resolve, reject) => {
        let text = '';
        const timer = setTimeout(() => reject(Error('forward-timeout')), 5000);
        const fail = () => {clearTimeout(timer); reject(Error('forward-failed'));};
        child.once('error', fail); child.once('exit', fail);
        child.stderr.on('data', () => {});
        child.stdout.on('data', bytes => {
          text += bytes.toString();
          if (text.length > 4096) return fail();
          const found = /Forwarding from 127\.0\.0\.1:(\d+) -> 8080/.exec(text);
          if (found) {clearTimeout(timer); resolve(Number(found[1]));}
        });
      });
      return {origin: `http://127.0.0.1:${port}`, close};
    } catch (error) {await close(); throw error;}
  }
  async probe(expected, checks, restoring) {
    const d = await this.resource(); if (!matches(snapshot(d, this.config), expected)) return {readiness: false, operation: false, bound: false};
    const selector = d.spec.selector?.matchLabels;
    need(selector?.['app.kubernetes.io/name'] === 'opal-release-canary' && !d.spec.selector.matchExpressions?.length, 'canary-selector');
    const select = Object.entries(selector).sort().map(([key, value]) => `${key}=${value}`).join(',');
    const sets = JSON.parse(await this.run(['-n', this.config.namespace, 'get', 'replicasets', '-l', select, '-o', 'json'])).items;
    const owners = new Set(sets.filter(r => r.metadata.ownerReferences?.some(o => o.uid === expected.uid && o.kind === 'Deployment' && o.controller) && r.spec.template.spec.containers?.length === 1 && r.spec.template.spec.containers[0].image === expected.image).map(r => r.metadata.uid));
    const pods = JSON.parse(await this.run(['-n', this.config.namespace, 'get', 'pods', '-l', select, '-o', 'json'])).items;
    const ready = pods.filter(p => !p.metadata.deletionTimestamp && p.metadata.ownerReferences?.some(o => owners.has(o.uid) && o.kind === 'ReplicaSet' && o.controller) && p.spec.containers?.length === 1 && p.spec.containers[0].image === expected.image && p.status.containerStatuses?.[0]?.state?.running && p.status.conditions?.some(c => c.type === 'Ready' && c.status === 'True'));
    if (ready.length !== 1 || d.status?.observedGeneration !== expected.generation || d.status?.updatedReplicas !== 1 || d.status?.availableReplicas !== 1 || d.status?.replicas !== 1) return {readiness: false, operation: false, bound: true};
    const pod = ready[0], tunnel = await this.forward(pod.metadata.name);
    try {
      const health = await probeHTTP(tunnel.origin, checks.readinessPath);
      const reply = await probeHTTP(tunnel.origin, checks.operationPath, checks.requestBody);
      const currentPod = JSON.parse(await this.run(['-n', this.config.namespace, 'get', 'pod', pod.metadata.name, '-o', 'json']));
      const bound = currentPod.metadata.uid === pod.metadata.uid && !currentPod.metadata.deletionTimestamp && currentPod.spec.containers[0].image === expected.image && matches(await this.inspect(), expected);
      return {readiness: health.status >= 200 && health.status < 300, operation: reply.status === 200 && reply.type?.split(';')[0] === 'application/cbor' && equal(reply.body, restoring ? checks.previousExpectedBody : checks.expectedBody), bound, podUid: pod.metadata.uid, responseDigest: hash(reply.body).toString('hex')};
    } finally {await tunnel.close();}
  }
}
