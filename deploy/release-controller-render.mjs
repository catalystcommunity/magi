import {immutableImage} from '../config/deployment.mjs';
import {withEnvironment} from './environment-render.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {labels, namespace} from './release-render.mjs';
export const name = 'release-controller', account = 'opal-release-controller';
export const subject = `system:serviceaccount:${namespace}:${account}`;
export const policyName = 'opal-release-controller-image-only';
export const fields = {
  'io.k8s.api.apps.v1.DeploymentSpec': 'minReadySeconds paused progressDeadlineSeconds replicas revisionHistoryLimit selector strategy template'.split(' '),
  'io.k8s.api.core.v1.PodSpec': 'activeDeadlineSeconds affinity automountServiceAccountToken containers dnsConfig dnsPolicy enableServiceLinks ephemeralContainers hostAliases hostIPC hostNetwork hostPID hostUsers hostname hostnameOverride imagePullSecrets initContainers nodeName nodeSelector os overhead preemptionPolicy priority priorityClassName readinessGates resourceClaims resources restartPolicy runtimeClassName schedulerName schedulingGates schedulingGroup securityContext serviceAccount serviceAccountName setHostnameAsFQDN shareProcessNamespace subdomain terminationGracePeriodSeconds tolerations topologySpreadConstraints volumes'.split(' '),
  'io.k8s.api.core.v1.Container': 'args command env envFrom image imagePullPolicy lifecycle livenessProbe name ports readinessProbe resizePolicy resources restartPolicy restartPolicyRules securityContext startupProbe stdin stdinOnce terminationMessagePath terminationMessagePolicy tty volumeDevices volumeMounts workingDir'.split(' '),
};
export const owned = r => r.metadata?.labels?.['app.kubernetes.io/part-of'] === 'opal' && r.metadata?.labels?.['app.kubernetes.io/managed-by'] === 'opal-release';
const meta = (n = name) => ({name: n, namespace, labels});
const freeze = (path, names) => names.map(f => {
  // Resource.Quantity has a union schema. The API's static CEL type omits this
  // map, so check it dynamically. Live denial tests must cover this field.
  const field = obj => f === 'overhead' ? `dyn(${obj}.${path}).${f}` : `${obj}.${path}.${f}`;
  const a = field('object'), b = field('oldObject');
  return `(has(${a}) == has(${b}) && (!has(${b}) || ${a} == ${b}))`;
}).join(' && ');

export function admission() {
  const marker = 'opal.catalystcommunity.com/release-operation';
  return [
    {apiVersion: 'admissionregistration.k8s.io/v1', kind: 'ValidatingAdmissionPolicy', metadata: {name: policyName, labels}, spec: {
      failurePolicy: 'Fail', matchConstraints: {resourceRules: [{apiGroups: ['apps'], apiVersions: ['v1'], operations: ['UPDATE'], resources: ['deployments'], scope: 'Namespaced'}]},
      matchConditions: [{name: 'only-opal-controller', expression: `request.userInfo.username == '${subject}'`}],
      variables: [{name: 'a', expression: 'object.metadata.?annotations.orValue({})'}, {name: 'b', expression: 'oldObject.metadata.?annotations.orValue({})'}],
      validations: [
        {expression: `request.namespace == '${namespace}' && request.name == 'release-canary' && request.?subResource.orValue('') == ''`, message: 'Magi controller can change only the isolated canary.'},
        {expression: freeze('spec', fields['io.k8s.api.apps.v1.DeploymentSpec'].filter(f => f !== 'template')), message: 'Magi controller cannot change deployment settings.'},
        {expression: 'object.spec.template.metadata == oldObject.spec.template.metadata', message: 'Magi controller cannot change pod metadata.'},
        {expression: freeze('spec.template.spec', fields['io.k8s.api.core.v1.PodSpec'].filter(f => f !== 'containers')), message: 'Magi controller cannot change pod privileges, credentials, volumes, or scheduling.'},
        {expression: 'size(object.spec.template.spec.containers) == 1 && size(oldObject.spec.template.spec.containers) == 1', message: 'Magi controller requires exactly one canary container.'},
        {expression: freeze('spec.template.spec.containers[0]', fields['io.k8s.api.core.v1.Container'].filter(f => f !== 'image')), message: 'Magi controller can change only the canary container image.'},
        {expression: `object.spec.template.spec.containers[0].image.matches('^${environmentConfig.repository.replaceAll('.','[.]')}@sha256:[0-9a-f]{64}$')`, message: 'Magi controller requires an immutable image in the Magi repository.'},
        {expression: freeze('metadata', ['labels', 'ownerReferences', 'finalizers']), message: 'Magi controller cannot change ownership or labels.'},
        {expression: `variables.a.all(k, k == '${marker}' || (k in variables.b && variables.a[k] == variables.b[k])) && variables.b.all(k, k == '${marker}' || (k in variables.a && variables.a[k] == variables.b[k])) && '${marker}' in variables.a && variables.a['${marker}'].matches('^[A-Za-z0-9._:-]{1,256}/(promote|restore)$')`, message: 'Magi controller can change only its release marker annotation.'},
      ],
    }},
    {apiVersion: 'admissionregistration.k8s.io/v1', kind: 'ValidatingAdmissionPolicyBinding', metadata: {name: policyName, labels}, spec: {policyName, validationActions: ['Deny']}},
  ];
}

const podLabels = {...labels, 'app.kubernetes.io/name': 'opal-release-controller'};
const security = {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, fsGroup: 65532, seccompProfile: {type: 'RuntimeDefault'}};
const containerSecurity = {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: ['ALL', 'NET_RAW']}};
const resources = {requests: {cpu: '25m', memory: '64Mi'}, limits: {cpu: '250m', memory: '256Mi'}};
export function manifests(image) {
  if (!immutableImage(image)) throw Error('immutable-controller-image');
  return withEnvironment([
    ...admission(),
    {apiVersion: 'v1', kind: 'ServiceAccount', metadata: meta(account), automountServiceAccountToken: false},
    {apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'Role', metadata: meta(), rules: [
      {apiGroups: ['apps'], resources: ['deployments'], resourceNames: ['release-canary'], verbs: ['get', 'patch']},
      {apiGroups: ['apps'], resources: ['replicasets'], verbs: ['get', 'list']},
      {apiGroups: [''], resources: ['pods'], verbs: ['get', 'list']},
    ]},
    {apiVersion: 'rbac.authorization.k8s.io/v1', kind: 'RoleBinding', metadata: meta(), roleRef: {apiGroup: 'rbac.authorization.k8s.io', kind: 'Role', name}, subjects: [{kind: 'ServiceAccount', name: account, namespace}]},
    {apiVersion: 'v1', kind: 'PersistentVolumeClaim', metadata: meta(), spec: {accessModes: ['ReadWriteOnce'], storageClassName: environmentConfig.storageClass, resources: {requests: {storage: '1Gi'}}}},
    {apiVersion: 'networking.k8s.io/v1', kind: 'NetworkPolicy', metadata: meta(), spec: {podSelector: {matchLabels: {'app.kubernetes.io/name': 'opal-release-controller'}}, policyTypes: ['Ingress', 'Egress'], ingress: [], egress: [
      {to: [{namespaceSelector: {matchLabels: {'kubernetes.io/metadata.name': 'kube-system'}}, podSelector: {matchLabels: {'k8s-app': 'kube-dns'}}}], ports: [{protocol: 'UDP', port: 53}, {protocol: 'TCP', port: 53}]},
      {to: [{ipBlock: {cidr: environmentConfig.clusterApiCIDR}}], ports: [{protocol: 'TCP', port: 6443}]},
      {to: [{ipBlock: {cidr: '0.0.0.0/0'}}], ports: [{protocol: 'TCP', port: 443}, {protocol: 'TCP', port: 4987}]},
      {to: [{podSelector: {matchLabels: {'app.kubernetes.io/name': 'opal-release-canary'}}}], ports: [{protocol: 'TCP', port: 8080}]},
    ]}},
    {apiVersion: 'networking.k8s.io/v1', kind: 'NetworkPolicy', metadata: meta('release-canary-controller-probe'), spec: {podSelector: {matchLabels: {'app.kubernetes.io/name': 'opal-release-canary'}}, policyTypes: ['Ingress'], ingress: [{from: [{podSelector: {matchLabels: {'app.kubernetes.io/name': 'opal-release-controller'}}}], ports: [{protocol: 'TCP', port: 8080}]}]}},
    {apiVersion: 'apps/v1', kind: 'Deployment', metadata: meta(), spec: {replicas: 1, revisionHistoryLimit: 2, strategy: {type: 'Recreate'}, selector: {matchLabels: {'app.kubernetes.io/name': 'opal-release-controller'}}, template: {metadata: {labels: podLabels}, spec: {
      serviceAccountName: account, automountServiceAccountToken: false, enableServiceLinks: false, imagePullSecrets: [{name: 'opal-registry'}], nodeSelector: {'kubernetes.io/arch': 'amd64'}, securityContext: security, terminationGracePeriodSeconds: 30,
      containers: [{name: 'controller', image, imagePullPolicy: 'IfNotPresent', command: ['node', 'release/controller-init.mjs'], securityContext: containerSecurity, resources,
        ports: [{name: 'health', containerPort: 8099}], readinessProbe: {httpGet: {path: '/health/ready', port: 'health'}, periodSeconds: 5}, livenessProbe: {httpGet: {path: '/health/live', port: 'health'}, initialDelaySeconds: 10, periodSeconds: 10, failureThreshold: 3},
        volumeMounts: [{name: 'data', mountPath: '/data'}, {name: 'private', mountPath: '/private', readOnly: true}, {name: 'kubernetes', mountPath: '/kubernetes', readOnly: true}]}],
      volumes: [{name: 'data', persistentVolumeClaim: {claimName: name}}, {name: 'private', secret: {secretName: name, defaultMode: 0o440}}, {name: 'kubernetes', projected: {defaultMode: 0o440, sources: [
        {serviceAccountToken: {path: 'token', expirationSeconds: 3600}}, {configMap: {name: 'kube-root-ca.crt', items: [{key: 'ca.crt', path: 'ca.crt'}]}},
      ]}}],
    }}}},
  ]);
}

export function seedPod(image) {
  const deployment = manifests(image).find(r => r.kind === 'Deployment');
  const pod = {apiVersion: 'v1', kind: 'Pod', metadata: {...meta('release-controller-seed'), labels: {...labels, 'app.kubernetes.io/name': 'opal-release-controller-seed'}}, spec: {
    ...deployment.spec.template.spec, restartPolicy: 'Never', serviceAccountName: 'opal-release-canary',
    containers: [{name: 'seed', image, command: ['node', '-e', 'setInterval(()=>{},1000)'], securityContext: containerSecurity, resources, volumeMounts: [{name: 'data', mountPath: '/data'}]}],
    volumes: [{name: 'data', persistentVolumeClaim: {claimName: name}}],
  }};
  return pod;
}
