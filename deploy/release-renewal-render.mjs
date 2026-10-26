import {immutableImage} from '../config/deployment.mjs';
import {withEnvironment} from './environment-render.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {namespace,labels} from './release-render.mjs';
export const renewalName='release-renewal';
export const renewalLabels={...labels,'app.kubernetes.io/name':'opal-release-renewal'};
const metadata=()=>({name:renewalName,namespace,labels:renewalLabels});
export function renewalManifests(image) {
  if(!immutableImage(image))throw Error('renewal-image');
  const spec={serviceAccountName:renewalName,automountServiceAccountToken:false,enableServiceLinks:false,restartPolicy:'Never',
    imagePullSecrets:[{name:'opal-registry'}],nodeSelector:{'kubernetes.io/arch':'amd64'},
    securityContext:{runAsNonRoot:true,runAsUser:65532,runAsGroup:65532,fsGroup:65532,seccompProfile:{type:'RuntimeDefault'}},
    containers:[{name:'renewal',image,imagePullPolicy:'IfNotPresent',command:['node','release/renewal-run.mjs'],
      resources:{requests:{cpu:'25m',memory:'64Mi'},limits:{cpu:'250m',memory:'256Mi'}},
      securityContext:{allowPrivilegeEscalation:false,readOnlyRootFilesystem:true,capabilities:{drop:['ALL','NET_RAW']}},
      volumeMounts:[{name:'private',mountPath:'/private',readOnly:true}]}],
    volumes:[{name:'private',secret:{secretName:renewalName,defaultMode:0o440,items:[{key:'renewal.json',path:'renewal.json'}]}}]};
  return withEnvironment([
    {apiVersion:'v1',kind:'ServiceAccount',metadata:metadata(),automountServiceAccountToken:false},
    {apiVersion:'networking.k8s.io/v1',kind:'NetworkPolicy',metadata:metadata(),spec:{podSelector:{matchLabels:{'app.kubernetes.io/name':'opal-release-renewal'}},policyTypes:['Ingress','Egress'],ingress:[],egress:[
      {to:[{namespaceSelector:{matchLabels:{'kubernetes.io/metadata.name':'kube-system'}},podSelector:{matchLabels:{'k8s-app':'kube-dns'}}}],ports:[{protocol:'UDP',port:53},{protocol:'TCP',port:53}]},
      {to:[{namespaceSelector:{matchLabels:{'kubernetes.io/metadata.name':environmentConfig.linkkeysNamespace}},podSelector:{matchLabels:{'app.kubernetes.io/name':'linkkeys','app.kubernetes.io/instance':environmentConfig.linkkeysInstance}}}],ports:[{protocol:'TCP',port:4987}]},
    ]}},
    {apiVersion:'batch/v1',kind:'CronJob',metadata:metadata(),spec:{schedule:'17 */4 * * *',timeZone:'Etc/UTC',concurrencyPolicy:'Forbid',startingDeadlineSeconds:600,successfulJobsHistoryLimit:2,failedJobsHistoryLimit:2,
      jobTemplate:{metadata:{labels:renewalLabels},spec:{backoffLimit:1,activeDeadlineSeconds:360,ttlSecondsAfterFinished:172800,template:{metadata:{labels:renewalLabels},spec}}}}},
  ]);
}
