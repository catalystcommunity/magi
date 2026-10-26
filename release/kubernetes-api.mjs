import {deployment as environmentConfig} from '../config/deployment.mjs';
import {podAddressAllowed} from '../config/deployment.mjs';
import {request as httpsRequest} from 'node:https';
import {readFileSync} from 'node:fs';
import {KubernetesCanary} from './kubernetes.mjs';
import {need} from './policy.mjs';

// The in-cluster adapter uses the projected, rotating token, not a kubeconfig.
// The response body and authorization header never enter errors or logs.
export function apiTransport({server, caFile, tokenFile, request = httpsRequest}) {
  need(server === environmentConfig.clusterServer, 'wrong-cluster');
  const ca = readFileSync(caFile);
  return (path, method = 'GET', body) => new Promise((resolve, reject) => {
    let token;
    try {token = readFileSync(tokenFile, 'utf8').trim(); need(token.length > 0 && !/[\r\n]/.test(token), 'kubernetes-token');}
    catch {reject(Error('kubernetes-token-unavailable')); return;}
    const bytes = body === undefined ? undefined : Buffer.from(JSON.stringify(body));
    const r = request(`${server}${path}`, {method, ca, rejectUnauthorized: true,
      headers: {authorization: `Bearer ${token}`, accept: 'application/json', ...(bytes ? {'content-type': 'application/json-patch+json', 'content-length': bytes.length} : {})},
      signal: AbortSignal.timeout(10000)}, response => {
      const chunks = []; let size = 0;
      response.on('error', () => reject(Error('kubernetes-response-interrupted')));
      response.on('data', b => {size += b.length; if (size > 2 * 1024 * 1024) {response.destroy(); reject(Error('kubernetes-response-limit'));} else chunks.push(b);});
      response.on('end', () => {
        if (response.statusCode < 200 || response.statusCode >= 300) return reject(Error(`kubernetes-status-${response.statusCode}`));
        resolve(Buffer.concat(chunks).toString('utf8'));
      });
    });
    r.on('error', () => reject(Error('kubernetes-transport-unavailable'))); r.end(bytes);
  });
}

export function apiCommands(config, call) {
  const base = `/apis/apps/v1/namespaces/${config.namespace}`, pods = `/api/v1/namespaces/${config.namespace}/pods`;
  const name = s => {need(typeof s === 'string' && /^[a-z0-9][a-z0-9-]{0,252}$/.test(s), 'kubernetes-name'); return s;};
  return async args => {
    if (JSON.stringify(args) === JSON.stringify(['config', 'view', '--minify', '-o', 'json'])) return JSON.stringify({clusters: [{cluster: {server: config.cluster_server}}]});
    need(args[0] === '-n' && args[1] === config.namespace, 'kubernetes-namespace');
    const [op, kind, target] = args.slice(2);
    if (op === 'get' && kind === 'deployment' && target === config.deployment && args.length === 7 && args[5] === '-o' && args[6] === 'json') return call(`${base}/deployments/${target}`);
    if (op === 'get' && kind === 'pod' && args.length === 7 && args[5] === '-o' && args[6] === 'json') return call(`${pods}/${name(target)}`);
    if (op === 'get' && ['pods', 'replicasets'].includes(kind) && target === '-l' && args.length === 8 && args[6] === '-o' && args[7] === 'json') {
      need(args[5] === 'app.kubernetes.io/name=opal-release-canary', 'kubernetes-selector');
      return call(`${kind === 'pods' ? pods : `${base}/replicasets`}?labelSelector=${encodeURIComponent(args[5])}`);
    }
    if (op === 'patch' && kind === 'deployment' && target === config.deployment && args.length === 10 && args[5] === '--type=json' && args[6] === '-p' && args[8] === '-o' && args[9] === 'name') {
      await call(`${base}/deployments/${target}`, 'PATCH', JSON.parse(args[7])); return 'deployment.apps/release-canary';
    }
    throw Error('unsupported-kubernetes-operation');
  };
}

export class KubernetesAPI extends KubernetesCanary {
  constructor({config, caFile, tokenFile, call}) {
    const request = call ?? apiTransport({server: config.cluster_server, caFile, tokenFile});
    const run = apiCommands(config, request);
    super({config, kubeconfig: '/projected-service-account', run, forward: async name => {
      const pod = JSON.parse(await run(['-n', config.namespace, 'get', 'pod', name, '-o', 'json']));
      const ip = pod.status?.podIP;
      need(podAddressAllowed(ip) && !pod.spec?.hostNetwork, 'canary-pod-address');
      return {origin: `http://${ip}:8080`, close: async () => {}};
    }});
  }
}
