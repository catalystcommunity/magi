import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {environment} from './environment.mjs';
// This check changes only the existing Magi Deployment when --restart is used.
import {execFileSync,spawn} from 'node:child_process';
import {writeFileSync,mkdirSync,readFileSync} from 'node:fs';
import {once} from 'node:events';
import assert from 'node:assert/strict';
import {DiscoveryAsyncClient} from '../.build/client.async.gen.js';
import {HTTPTransport} from '../protocol/http.mjs';
import {namespace,hostname,labels} from '../deploy/render.mjs';
const context=environment('MAGI_KUBE_CONTEXT')||environmentConfig.kubeContext;
const endpoint=`https://${hostname}/csil/v1/rpc`;
const args=['--context',context,'--request-timeout=15s','-n',namespace];
const deployment=JSON.parse(execFileSync('kubectl',[...args,'get','deployment','opal','-o','json'],{encoding:'utf8'}));
for(const [key,value]of Object.entries(labels))assert.equal(deployment.metadata.labels[key],value);
assert.equal(deployment.spec.replicas,2);
const client=new DiscoveryAsyncClient(new HTTPTransport(endpoint));
let probes=0;const failures=[];
async function probe(){
  const d=await client.describe({versions:['0alpha1'],requiredFeatures:['strict-cbor']});
  assert.deepEqual(d.implementedOperations,['Discovery/describe','Discovery/read-schema']);probes++;
  return d;
}
const start=new Date().toISOString();const d=await probe();
const schema=await client.readSchema({schema:d.schema});
assert.deepEqual(Buffer.from(schema.descriptor),readFileSync(new URL('../generated/go/magi.csil-schema.cbor',import.meta.url)));
await assert.rejects(client.describe({versions:['future'],requiredFeatures:[]}),{code:1002});
const root=await fetch(`https://${hostname}/`,{signal:AbortSignal.timeout(10000)});assert.equal(root.status,200);assert.ok((await root.text()).includes('No login'));
let restarted=false;
if(process.argv.includes('--restart')){
  const cfg=JSON.parse(execFileSync('kubectl',['--context',context,'config','view','--minify','-o','json'],{encoding:'utf8'}));
  assert.equal(cfg.clusters[0].cluster.server,environmentConfig.clusterServer);
  execFileSync('kubectl',[...args,'rollout','restart','deployment/opal'],{stdio:'pipe'});
  const rollout=spawn('kubectl',[...args,'rollout','status','deployment/opal','--timeout=120s'],{stdio:'ignore'});
  let finished=false,code;
  const exit=once(rollout,'exit').then(([c])=>{finished=true;code=c;});
  const until=Date.now()+130000;
  while((!finished||probes<20)&&Date.now()<until){
    try{await probe();}catch{failures.push({at:new Date().toISOString(),reason:'discovery request failed'});}
    await new Promise(resolve=>setTimeout(resolve,250));
  }
  if(!finished){rollout.kill('SIGTERM');throw Error('Magi rollout timed out');}
  await exit;assert.equal(code,0);restarted=true;
}
await probe();
const pods=JSON.parse(execFileSync('kubectl',[...args,'get','pods','-l','app.kubernetes.io/name=opal','-o','json'],{encoding:'utf8'}));
const active=pods.items.filter(p=>!p.metadata.deletionTimestamp);
assert.equal(active.length,2);
for(const p of active)assert.ok(p.status.containerStatuses?.every(s=>s.ready));
const report={start,completed:new Date().toISOString(),endpoint,namespace,restarted,probes,failures,schemaDigest:Buffer.from(d.schema.digest).toString('hex'),pods:active.map(p=>({name:p.metadata.name,node:p.spec.nodeName,image:p.status.containerStatuses[0].imageID,restarts:p.status.containerStatuses[0].restartCount}))};
mkdirSync(new URL('../test-results/',import.meta.url),{recursive:true});
const output=new URL(`../test-results/${start.replaceAll(':','-')}.live.json`,import.meta.url);writeFileSync(output,JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify(report));assert.equal(failures.length,0);
