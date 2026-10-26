import {immutableImage} from '../config/deployment.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {namespace,labels} from './release-render.mjs';
import {corndogs} from './qa-render.mjs';
export const taskStore='release-corndogs',taskProfile='release-task-runtime';
export const taskLabels={...labels,'app.kubernetes.io/name':'opal-release-tasks'};
const meta=name=>({name,namespace,labels:taskLabels});
const tcp=port=>({protocol:'TCP',port});
const cdPeer={podSelector:{matchLabels:{'app.kubernetes.io/name':taskStore}}};
const controllerPeer={podSelector:{matchLabels:{'app.kubernetes.io/name':'opal-release-controller'}}};
const checkPeer={podSelector:{matchLabels:{'app.kubernetes.io/name':'opal-release-task-check'}}};
const pgPeer={podSelector:{matchLabels:{'app.kubernetes.io/name':'opal-qa','opal.component':'postgres'}}};
const locked={allowPrivilegeEscalation:false,readOnlyRootFilesystem:true,capabilities:{drop:['ALL','NET_RAW']}};
const resources={requests:{cpu:'25m',memory:'64Mi'},limits:{cpu:'250m',memory:'256Mi'}};
const security={runAsNonRoot:true,runAsUser:65532,runAsGroup:65532,fsGroup:65532,seccompProfile:{type:'RuntimeDefault'}};
const profileVolumes=[{name:'task-secret',secret:{secretName:taskProfile,defaultMode:0o440,items:[{key:'profile.json',path:'profile.json'}]}},{name:'task-profile',emptyDir:{medium:'Memory',sizeLimit:'1Mi'}},{name:'task-trust',configMap:{name:'release-task-trust'}}];
const mounts=[{name:'task-profile',mountPath:'/run/release-tasks',readOnly:true},{name:'task-trust',mountPath:'/trust/release',readOnly:true}];
function initializer(image) {return {name:'task-profile',image,command:['node','-e',"const fs=require('node:fs');const bytes=fs.readFileSync('/task-secret/profile.json');if(bytes.length>16384)throw Error('profile-size');fs.writeFileSync('/run/release-tasks/profile.json',bytes,{mode:384});fs.chmodSync('/run/release-tasks/profile.json',384);"],securityContext:locked,resources,volumeMounts:[{name:'task-secret',mountPath:'/task-secret',readOnly:true},{name:'task-profile',mountPath:'/run/release-tasks'}]};}
export function withTaskRuntime(pod,image) {
  if(!immutableImage(image))throw Error('task-runtime-image');
  if(pod.containers.length!==1 || pod.containers[0].name!=='controller' || pod.initContainers?.length || pod.volumes.some(v=>v.name.startsWith('task-')))throw Error('unexpected-controller-layout');
  const result=structuredClone(pod);result.containers[0].image=image;
  result.containers[0].volumeMounts.push(...structuredClone(mounts));result.volumes.push(...structuredClone(profileVolumes));result.initContainers=[initializer(image)];return result;
}
export function taskStoreManifests(ca) {
  const all=[
    {apiVersion:'v1',kind:'ServiceAccount',metadata:meta(taskStore),automountServiceAccountToken:false},
    {apiVersion:'v1',kind:'ServiceAccount',metadata:meta('release-task-check'),automountServiceAccountToken:false},
    {apiVersion:'v1',kind:'ConfigMap',metadata:meta('release-task-trust'),data:{'ca.crt':ca}},
    {apiVersion:'v1',kind:'PersistentVolumeClaim',metadata:meta(taskStore),spec:{accessModes:['ReadWriteOnce'],storageClassName:environmentConfig.storageClass,resources:{requests:{storage:'1Gi'}}}},
    {apiVersion:'v1',kind:'Service',metadata:meta(taskStore),spec:{selector:{'app.kubernetes.io/name':taskStore},ports:[{name:'rpc',port:5080,targetPort:5080}]}},
    {apiVersion:'apps/v1',kind:'StatefulSet',metadata:meta(taskStore),spec:{serviceName:taskStore,replicas:1,selector:{matchLabels:{'app.kubernetes.io/name':taskStore}},template:{metadata:{labels:{...taskLabels,'app.kubernetes.io/name':taskStore}},spec:{serviceAccountName:taskStore,automountServiceAccountToken:false,enableServiceLinks:false,nodeSelector:{'kubernetes.io/arch':'amd64'},securityContext:security,containers:[{
      name:'corndogs',image:corndogs,securityContext:locked,resources,
      env:Object.entries({STORAGE_BACKEND:'file',CORNDOGS_FILESTORE_DIR:'/data',CORNDOGS_FILESTORE_SYNC:'always',CORNDOGS_SUBMISSION_KEY_POLICY:'required',CORNDOGS_TASK_GUARD_POLICY:'required',CORNDOGS_TLS_CERT_FILE:'/tls/tls.crt',CORNDOGS_TLS_KEY_FILE:'/tls/tls.key',LOGLEVEL:'error'}).map(([name,value])=>({name,value})),
      volumeMounts:[{name:'data',mountPath:'/data'},{name:'tls',mountPath:'/tls',readOnly:true}],readinessProbe:{tcpSocket:{port:5080},periodSeconds:3},livenessProbe:{tcpSocket:{port:5080},initialDelaySeconds:20,periodSeconds:10},
    }],volumes:[{name:'data',persistentVolumeClaim:{claimName:taskStore}},{name:'tls',secret:{secretName:'release-corndogs-tls',defaultMode:0o440}}]}}}},
  ];
  const net=(name,selector,policyTypes,extra)=>({apiVersion:'networking.k8s.io/v1',kind:'NetworkPolicy',metadata:meta(name),spec:{podSelector:selector.podSelector,policyTypes,...extra}});
  all.push(net(taskStore,cdPeer,['Ingress','Egress'],{ingress:[{from:[controllerPeer,checkPeer],ports:[tcp(5080)]}],egress:[]}));
  all.push(net('release-task-postgres',pgPeer,['Ingress'],{ingress:[{from:[controllerPeer,checkPeer],ports:[tcp(5432)]}]}));
  all.push(net('release-controller-tasks',controllerPeer,['Egress'],{egress:[{to:[cdPeer],ports:[tcp(5080)]},{to:[pgPeer],ports:[tcp(5432)]}]}));
  all.push(net('release-task-check',checkPeer,['Ingress','Egress'],{ingress:[],egress:[{to:[cdPeer],ports:[tcp(5080)]},{to:[pgPeer],ports:[tcp(5432)]},{to:[{namespaceSelector:{matchLabels:{'kubernetes.io/metadata.name':'kube-system'}},podSelector:{matchLabels:{'k8s-app':'kube-dns'}}}],ports:[{protocol:'UDP',port:53},tcp(53)]}]}));
  return all;
}
export function taskCheckJob(image,name) {
  const source=withTaskRuntime({containers:[{name:'controller',volumeMounts:[]}],volumes:[]},image);
  return {apiVersion:'batch/v1',kind:'Job',metadata:meta(name),spec:{backoffLimit:0,activeDeadlineSeconds:120,ttlSecondsAfterFinished:86400,template:{metadata:{labels:{...taskLabels,'app.kubernetes.io/name':'opal-release-task-check'}},spec:{serviceAccountName:'release-task-check',automountServiceAccountToken:false,enableServiceLinks:false,imagePullSecrets:[{name:'opal-registry'}],nodeSelector:{'kubernetes.io/arch':'amd64'},securityContext:security,restartPolicy:'Never',initContainers:source.initContainers,volumes:source.volumes,containers:[{name:'check',image,command:['/app/bin/release-tasks','--check','/run/release-tasks/profile.json'],securityContext:locked,resources,volumeMounts:mounts}]}}}};
}
