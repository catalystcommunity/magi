import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from '../scripts/operator-state.mjs';
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import {resolve,join} from 'node:path';
import {createHash} from 'node:crypto';
import {readPrivate} from './qa-bootstrap.mjs';
import {manifests,bootstrapJob,route,namespace,hostname,labels} from './qa-render.mjs';
const root=resolve(import.meta.dirname,'..'),dir=environmentConfig.qaProfileDir;
const mode=process.argv[2]||'--check';
if(!['--check','--apply','--expose'].includes(mode)||process.argv.length>3)throw Error('Use --check, --apply, or --expose');
const run=args=>execFileSync('kubectl',['--context', environmentConfig.kubeContext,'--request-timeout=30s',...args],{encoding:'utf8',stdio:['pipe','pipe','pipe'],maxBuffer:4*1024*1024});
const get=args=>JSON.parse(run(['get',...args,'-o','json']));
const config=JSON.parse(readPrivate(join(dir,'workflows.json')));
const images=JSON.parse(readFileSync(operatorFile('qa-images.json'))).images;
const all=manifests(images,config,readFileSync(join(dir,'ca.crt'),'utf8'));
const jobs=bootstrapJob(images.workflows);
jobs.find(r=>r.kind==='Job').metadata.name='qa-bootstrap-'+createHash('sha256').update(JSON.stringify({jobs,config})).digest('hex').slice(0,16);
const kube=JSON.parse(execFileSync('kubectl',['--context', environmentConfig.kubeContext,'config','view','--minify','-o','json'],{encoding:'utf8'}));
if(kube.clusters?.[0]?.cluster?.server!==environmentConfig.clusterServer)throw Error('Unexpected cluster');
const owned=r=>r.metadata.labels?.['app.kubernetes.io/part-of']==='opal'&&r.metadata.labels?.['app.kubernetes.io/managed-by']==='opal-deploy';
if(!owned(get(['namespace',namespace])))throw Error('Magi namespace ownership differs');
for(const r of get(['httproutes','-A']).items)if(r.spec.hostnames?.includes(hostname)&&(r.metadata.namespace!==namespace||r.metadata.name!=='qa-api'||!owned(r)))throw Error('Hostname belongs to another route');
for(const r of get(['ingress','-A']).items)if(r.spec.rules?.some(rule=>rule.host===hostname))throw Error('Hostname belongs to an ingress');
for(const r of [...all,...jobs,route(),...['qa-rp','qa-db-admin','qa-db-runtime','qa-postgres-tls','qa-corndogs-tls'].map(name=>({kind:'Secret',metadata:{name}}))]) {
  const existing=run(['get',r.kind,r.metadata.name,'-n',namespace,'--ignore-not-found','-o','json']);
  if(existing.trim()&&!owned(JSON.parse(existing)))throw Error(`Resource ownership differs: ${r.kind}/${r.metadata.name}`);
}
function apply(object,secret=false) {
  try {
    const output=execFileSync('kubectl',['--context', environmentConfig.kubeContext,'--request-timeout=30s','apply',...(secret?['--server-side','--field-manager=opal-qa-deploy']:[]),'-f','-'],{input:JSON.stringify(object),encoding:'utf8',stdio:['pipe','pipe','pipe']});
    console.log(secret?`Secret/${object.metadata.name} applied`:output.trim());
  } catch(e) {if(secret)throw Error('Secret apply failed; no credential output');throw Error(`Apply failed: ${e.stderr}`);}
}
function wait(kind,name) {
  for(let i=0;i<8;i++) {
    try {console.log(run(kind==='job'?['wait',`job/${name}`,'-n',namespace,'--for=condition=complete','--timeout=50s']:['rollout','status',`${kind}/${name}`,'-n',namespace,'--timeout=50s']).trim());return;}
    catch {
      if(kind==='job'&&get(['job',name,'-n',namespace]).status?.conditions?.some(v=>v.type==='Failed'&&v.status==='True'))throw Error(`Job/${name} failed; inspect its logs before retry`);
      console.log(`Waiting for ${kind}/${name}`);
    }
  }
  throw Error(`${kind}/${name} did not become ready; inspect before retry`);
}
if(mode==='--check')console.log(JSON.stringify({namespace,hostname,resources:[...all,...jobs,route()].map(r=>r.kind+'/'+r.metadata.name),status:'preflight passed'}));
if(mode==='--apply') {
  apply(all.find(r=>r.kind==='ResourceQuota'));
  const state=JSON.parse(readPrivate(join(dir,'bootstrap.json')));
  const url=(user,password)=>`postgres://${user}:${password}@qa-postgres:5432/postgres?sslmode=verify-full&sslrootcert=/trust/ca.crt`;
  const secrets=[['qa-rp',{'api-key':state.rp.apiKey}],['qa-db-admin',{password:state.database.admin,url:url('postgres',state.database.admin)}],['qa-db-runtime',{password:state.database.runtime,url:url('workflow_runtime',state.database.runtime)}],...['qa-postgres','qa-corndogs'].map(name=>[name+'-tls',{'tls.crt':readFileSync(join(dir,name+'.crt'),'utf8'),'tls.key':readPrivate(join(dir,name+'.key'))}])];
  for(const [name,values] of secrets)apply({apiVersion:'v1',kind:'Secret',metadata:{name,namespace,labels},type:'Opaque',data:Object.fromEntries(Object.entries(values).map(([k,v])=>[k,Buffer.from(v).toString('base64')]))},true);
  for(const r of all)if(!['ResourceQuota','Deployment','PodDisruptionBudget'].includes(r.kind))apply(r);
  wait('statefulset','qa-postgres');wait('statefulset','qa-corndogs');
  for(const r of jobs)apply(r);wait('job',jobs.find(r=>r.kind==='Job').metadata.name);
  for(const r of all)if(['Deployment','PodDisruptionBudget'].includes(r.kind))apply(r);
  for(const component of ['api','coordinator','sweeper','worker'])wait('deployment','qa-'+component);
  console.log('Private runtime is ready. Check it before --expose.');
}
if(mode==='--expose') {
  for(const component of ['api','coordinator','sweeper','worker'])wait('deployment','qa-'+component);
  apply(route());console.log(`Public endpoint: https://${hostname}/csil/v1/rpc`);
}
