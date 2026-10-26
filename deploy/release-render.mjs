import {immutableImage} from '../config/deployment.mjs';
import {withEnvironment} from './environment-render.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
export const namespace = environmentConfig.namespace;
export const hostname = environmentConfig.approvalHost;
export const labels = {'app.kubernetes.io/part-of': 'opal', 'app.kubernetes.io/managed-by': 'opal-release'};
const meta = (name, extra = {}) => ({name, namespace, labels: {...labels, ...extra}});
const imagePattern = {test:immutableImage};
const security = {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, fsGroup: 65532, seccompProfile: {type: 'RuntimeDefault'}};
const containerSecurity = {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ['ALL', 'NET_RAW']}};
export function manifests(approvalImage, baselineImage) {
  if (!imagePattern.test(approvalImage) || !imagePattern.test(baselineImage)) throw Error('Immutable private Magi images are required');
  const nameLabel = name => ({...labels, 'app.kubernetes.io/name': name});
  const resources = [];
  for (const name of ['opal-release-canary', 'opal-release-approval']) resources.push({apiVersion: 'v1', kind: 'ServiceAccount', metadata: meta(name), automountServiceAccountToken: false});
  resources.push({apiVersion: 'v1', kind: 'PersistentVolumeClaim', metadata: meta('release-approval'), spec: {accessModes: ['ReadWriteOnce'], storageClassName: environmentConfig.storageClass, resources: {requests: {storage: '1Gi'}}}});
  for (const [name, image, port] of [['release-canary', baselineImage, 8080], ['release-approval', approvalImage, 8098]]) {
    const canary = name === 'release-canary', podLabels = nameLabel(`opal-${name}`);
    const container = {name: canary ? 'opal' : 'approval', image, imagePullPolicy: 'IfNotPresent', ports: [{name: 'http', containerPort: port}], resources: {requests: {cpu: '25m', memory: canary ? '32Mi' : '64Mi'}, limits: {cpu: '250m', memory: canary ? '128Mi' : '256Mi'}}, securityContext: containerSecurity, readinessProbe: {httpGet: {path: '/health/ready', port: 'http'}, periodSeconds: 5}};
    if (canary) container.env = [{name: 'OPAL_LISTEN', value: '0.0.0.0:8080'}, {name: 'OPAL_ORIGIN', value: ("https://" + environmentConfig.canaryHost)}, {name: 'GOMEMLIMIT', value: '64MiB'}];
    else container.volumeMounts = [{name: 'data', mountPath: '/data'}, {name: 'private', mountPath: '/private', readOnly: true}];
    const spec = {serviceAccountName: `opal-${name}`, automountServiceAccountToken: false, enableServiceLinks: false, imagePullSecrets: [{name: 'opal-registry'}], nodeSelector: {'kubernetes.io/arch': 'amd64'}, securityContext: security, containers: [container]};
    if (!canary) spec.volumes = [{name: 'data', persistentVolumeClaim: {claimName: 'release-approval'}}, {name: 'private', secret: {secretName: 'release-approval', defaultMode: 0o440}}];
    resources.push({apiVersion: 'apps/v1', kind: 'Deployment', metadata: meta(name), spec: {replicas: 1, revisionHistoryLimit: 2, strategy: {type: 'Recreate'}, selector: {matchLabels: {'app.kubernetes.io/name': `opal-${name}`}}, template: {metadata: {labels: podLabels}, spec}}});
    resources.push({apiVersion: 'networking.k8s.io/v1', kind: 'NetworkPolicy', metadata: meta(name), spec: {podSelector: {matchLabels: {'app.kubernetes.io/name': `opal-${name}`}}, policyTypes: ['Ingress', 'Egress'], ingress: canary ? [] : [{from: [{namespaceSelector: {matchLabels: {'kubernetes.io/metadata.name': environmentConfig.ingressNamespace}}}], ports: [{protocol: 'TCP', port}]}], egress: canary ? [] : [
      {to: [{namespaceSelector: {matchLabels: {'kubernetes.io/metadata.name': 'kube-system'}}, podSelector: {'matchLabels': {'k8s-app': 'kube-dns'}}}], ports: [{protocol: 'UDP', port: 53}, {protocol: 'TCP', port: 53}]},
      {to: [{ipBlock: {cidr: '0.0.0.0/0'}}], ports: [{protocol: 'TCP', port: 4987}]},
    ]}});
  }
  resources.push({apiVersion: 'v1', kind: 'Service', metadata: meta('release-approval'), spec: {selector: {'app.kubernetes.io/name': 'opal-release-approval'}, ports: [{name: 'http', port: 80, targetPort: 'http'}]}});
  resources.push({apiVersion: 'gateway.networking.k8s.io/v1', kind: 'HTTPRoute', metadata: meta('release-approval'), spec: {parentRefs: [{name: environmentConfig.gatewayName, namespace: environmentConfig.ingressNamespace, sectionName: environmentConfig.gatewaySection}], hostnames: [hostname], rules: [{matches: [{path: {type: 'PathPrefix', value: '/'}}], backendRefs: [{name: 'release-approval', port: 80}]}]}});
  return withEnvironment(resources);
}
