import {immutableImage} from '../config/deployment.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
export const namespace=environmentConfig.namespace;
export const hostname=environmentConfig.discoveryHost;
export const labels={'app.kubernetes.io/name':'opal','app.kubernetes.io/part-of':'opal','app.kubernetes.io/managed-by':'opal-deploy'};
export function manifests(image){
  if(!immutableImage(image))throw Error('Use the private Magi image with a digest');
  const meta=name=>({name,namespace,labels});
  const selector={matchLabels:{'app.kubernetes.io/name':'opal'}};
  return [
    {apiVersion:'v1',kind:'Namespace',metadata:{name:namespace,labels:{...labels,'pod-security.kubernetes.io/enforce':'restricted','pod-security.kubernetes.io/enforce-version':'latest'}}},
    {apiVersion:'v1',kind:'ServiceAccount',metadata:meta('opal'),automountServiceAccountToken:false},
    {apiVersion:'v1',kind:'ResourceQuota',metadata:meta('opal'),spec:{hard:{pods:'5','requests.cpu':'250m','requests.memory':'256Mi','limits.cpu':'1250m','limits.memory':'640Mi','count/secrets':'2','count/services':'2'}}},
    {apiVersion:'apps/v1',kind:'Deployment',metadata:meta('opal'),spec:{replicas:2,revisionHistoryLimit:2,selector,
      strategy:{type:'RollingUpdate',rollingUpdate:{maxSurge:1,maxUnavailable:0}},template:{metadata:{labels},spec:{
        serviceAccountName:'opal',automountServiceAccountToken:false,enableServiceLinks:false,terminationGracePeriodSeconds:15,
        nodeSelector:{'kubernetes.io/arch':'amd64'},imagePullSecrets:[{name:'opal-registry'}],
        securityContext:{runAsNonRoot:true,runAsUser:65532,runAsGroup:65532,seccompProfile:{type:'RuntimeDefault'}},
        affinity:{podAntiAffinity:{preferredDuringSchedulingIgnoredDuringExecution:[{weight:100,podAffinityTerm:{labelSelector:selector,topologyKey:'kubernetes.io/hostname'}}]}},
        containers:[{name:'opal',image,imagePullPolicy:'IfNotPresent',ports:[{name:'http',containerPort:8080}],
          env:[{name:'OPAL_LISTEN',value:'0.0.0.0:8080'},{name:'OPAL_ORIGIN',value:`https://${hostname}`},{name:'GOMEMLIMIT',value:'64MiB'}],
          resources:{requests:{cpu:'25m',memory:'32Mi'},limits:{cpu:'250m',memory:'128Mi'}},
          securityContext:{allowPrivilegeEscalation:false,readOnlyRootFilesystem:true,capabilities:{drop:['ALL']}},
          readinessProbe:{httpGet:{path:'/health/ready',port:'http'},periodSeconds:3},
          livenessProbe:{httpGet:{path:'/health/live',port:'http'},periodSeconds:10}
        }]
      }}}},
    {apiVersion:'v1',kind:'Service',metadata:meta('opal'),spec:{selector:selector.matchLabels,ports:[{name:'http',port:80,targetPort:'http'}]}},
    {apiVersion:'policy/v1',kind:'PodDisruptionBudget',metadata:meta('opal'),spec:{minAvailable:1,selector}},
    {apiVersion:'networking.k8s.io/v1',kind:'NetworkPolicy',metadata:meta('opal'),spec:{podSelector:selector,policyTypes:['Ingress','Egress'],egress:[],ingress:[{from:[{namespaceSelector:{matchLabels:{'kubernetes.io/metadata.name':environmentConfig.ingressNamespace}}}],ports:[{protocol:'TCP',port:8080}]}]}},
    {apiVersion:'gateway.networking.k8s.io/v1',kind:'HTTPRoute',metadata:meta('opal'),spec:{parentRefs:[{name:environmentConfig.gatewayName,namespace:environmentConfig.ingressNamespace,sectionName:environmentConfig.gatewaySection}],hostnames:[hostname],rules:[{matches:[{path:{type:'PathPrefix',value:'/'}}],backendRefs:[{name:'opal',port:80}]}]}}
  ];
}
if(process.argv[1]===import.meta.filename)console.log(JSON.stringify({apiVersion:'v1',kind:'List',items:manifests(process.argv[2])},null,2));
