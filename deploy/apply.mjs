import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {environment} from '../scripts/environment.mjs';
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import {homedir} from 'node:os';
import {join} from 'node:path';
import {manifests,namespace,hostname,labels} from './render.mjs';

const context=environment('MAGI_KUBE_CONTEXT')||environmentConfig.kubeContext;
const image=process.argv[2];const resources=manifests(image);
const run=(args,input)=>execFileSync('kubectl',['--context',context,'--request-timeout=30s',...args],{input,encoding:'utf8',maxBuffer:4*1024*1024,stdio:['pipe','pipe','pipe']});
const get=(args)=>JSON.parse(run(['get',...args,'-o','json']));
const owned=resource=>Object.entries(labels).every(([k,v])=>resource.metadata.labels?.[k]===v);
const cfg=JSON.parse(execFileSync('kubectl',['--context',context,'config','view','--minify','-o','json'],{encoding:'utf8'}));
if(cfg.clusters?.[0]?.cluster?.server!==environmentConfig.clusterServer)throw Error('Unexpected cluster API address');
// Read only structural resources. Never print Kubernetes Secrets or kubeconfig.
for(const r of get(['httproutes','-A']).items){if(r.spec.hostnames?.includes(hostname)&&(r.metadata.namespace!==namespace||!owned(r)))throw Error('Hostname belongs to another route');}
for(const r of get(['ingress','-A']).items){if(r.spec.rules?.some(rule=>rule.host===hostname))throw Error('Hostname belongs to an ingress');}
const namespaces=get(['namespaces']);const existing=namespaces.items.find(r=>r.metadata.name===namespace);
if(existing&&!owned(existing))throw Error('Namespace is not owned by Magi');
if(existing){
  for(const r of resources.slice(1)){
    const result=run(['get',r.kind,r.metadata.name,'-n',namespace,'--ignore-not-found','-o','json']);
    if(result.trim()){
      const current=JSON.parse(result);
      if(!owned(current))throw Error(`Resource is not owned by Magi: ${r.kind}/${r.metadata.name}`);
      // The quota is shared by Magi components. A discovery rollout must not
      // reduce the capacity already assigned to the authenticated Q&A roles.
      if(r.kind==='ResourceQuota')r.spec=current.spec;
    }
  }
  const result=run(['get','secret','opal-registry','-n',namespace,'--ignore-not-found','-o','json']);
  if(result.trim()&&!owned(JSON.parse(result)))throw Error('Registry Secret is not owned by Magi');
}
if(!process.argv.includes('--apply')){
  console.log(JSON.stringify({context,namespace,hostname,image,resources:resources.map(r=>`${r.kind}/${r.metadata.name}`),status:'preflight passed; add --apply to deploy'}));
  process.exit(0);
}
// Use only the existing local credential for this registry. Do not copy other
// registry credentials, grant a new registry role, or alter another namespace.
const docker=JSON.parse(readFileSync(join(homedir(),'.docker/config.json')));
const auth=docker.auths?.[environmentConfig.registry];
if(!auth?.auth)throw Error('A local registry login is required');
const pull={apiVersion:'v1',kind:'Secret',metadata:{name:'opal-registry',namespace,labels},type:'kubernetes.io/dockerconfigjson',data:{'.dockerconfigjson':Buffer.from(JSON.stringify({auths:{[environmentConfig.registry]:auth}})).toString('base64')}};
console.log(run(['apply','-f','-'],JSON.stringify(resources[0])).trim());
// Do not use kubectl apply for credentials: avoid a second copy in the
// last-applied annotation. Server-side apply writes only Magi's own Secret.
try {run(['apply','--server-side','--field-manager=opal-deploy','-f','-'],JSON.stringify(pull));}
catch {throw Error('The Magi registry Secret could not be applied');}
console.log(run(['apply','-f','-'],JSON.stringify({apiVersion:'v1',kind:'List',items:resources.slice(1)})).trim());
console.log(run(['rollout','status','deployment/opal','-n',namespace,'--timeout=180s']).trim());
