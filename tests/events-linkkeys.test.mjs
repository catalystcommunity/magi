import test from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync,spawn} from 'node:child_process';
import {writeFileSync,mkdtempSync} from 'node:fs';
import {join,resolve} from 'node:path';
import {tmpdir} from 'node:os';
import {createHash,randomUUID} from 'node:crypto';
import {cluster,enroll,revokeFirstKey} from '../experiments/identity-service/harness.mjs';
import {codec as w,transport,signed,DiscoveryClient,EventsClient} from '../eventing/client.mjs';
import {consumerStore} from './events-consumer.mjs';

const root=resolve(import.meta.dirname,'..');
const scratch=mkdtempSync(join(tmpdir(),'events-linkkeys-'));
const binary=join(scratch,'events');
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
execFileSync('go',['build','-o',binary,'./cmd/events'],{cwd:root,env,stdio:'pipe'});
const docker=args=>execFileSync('docker',args,{encoding:'utf8',timeout:45000,stdio:['ignore','pipe','pipe']}).trim();
const wait=ms=>new Promise(r=>setTimeout(r,ms));
const app=a=>({identity:{domain:a.identity.domain,account_id:a.identity.accountId},application_id:a.applicationId,instance_id:a.instanceId});
const identity=i=>({domain:i.domain,account_id:i.accountId});
const pgImage='postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd';

test('standalone events across two real LinkKeys authorities',{timeout:240000},async t=>{
 const fixture=await cluster();t.after(()=>fixture.close());
 for(const [index,node]of fixture.nodes.entries()){node.application.applicationId=index===0?'external-calendar':'external-report';await enroll(node);}
 const endpoints=[];
 for(const [index,node]of fixture.nodes.entries()){
  const foreign=fixture.nodes[1-index];
  const id=docker(['run','--pull=never','-d','--label','opal.fixture=event-linkkeys','--memory','512m','--cpus','2','-p','127.0.0.1::5432','-e','POSTGRES_PASSWORD=synthetic-only',pgImage]);
  assert.match(id,/^[0-9a-f]{64}$/);
  t.after(()=>{assert.equal(docker(['inspect','-f','{{index .Config.Labels "opal.fixture"}}',id]),'event-linkkeys');docker(['rm','-f','-v',id]);});
  let ready=false;for(let n=0;n<200;n++){try{docker(['exec',id,'pg_isready','-h','127.0.0.1','-U','postgres']);ready=true;break;}catch{await wait(50);}}assert(ready);
  docker(['exec',id,'psql','-U','postgres','-v','ON_ERROR_STOP=1','-c',"CREATE ROLE fixture LOGIN PASSWORD 'synthetic-runtime' NOSUPERUSER NOBYPASSRLS",'-c','CREATE DATABASE events OWNER fixture']);
  const address=docker(['port',id,'5432/tcp']);assert.match(address,/^127\.0\.0\.1:\d+$/);
  const service={identity:node.application.identity,applicationId:'standalone-events',instanceId:'stable'};
  const conf={application:app(service),domains:fixture.nodes.map(n=>({domain:n.domain,tcp_address:n.config.tcpAddress,fingerprints:n.fingerprints})),rp:{tcp_address:node.config.tcpAddress,fingerprints:node.fingerprints},grants:[
   {application:app(node.application),producer:identity(node.application.identity),event_type:'external.changed',publish:true,receive:false,restrictions:{requirements:[]}},
   {application:app(foreign.application),producer:identity(node.application.identity),event_type:'external.changed',publish:false,receive:true,restrictions:{requirements:[]}},
  ]};
  const config=join(scratch,`${index}.json`);writeFileSync(config,JSON.stringify(conf),{mode:0o600});
  const vars={...env,EVENTS_DATABASE_URL:`postgres://fixture:synthetic-runtime@${address}/events?sslmode=disable`,EVENTS_RP_API_KEY:node.config.apiKey,EVENTS_LISTEN:'127.0.0.1:0'};
  for(const mode of ['--migrate','--apply-policy'])execFileSync(binary,['--config',config,mode],{env:vars,stdio:'pipe'});
  // Remove DDL ownership from the serving role after migrations and grants.
  docker(['exec',id,'psql','-U','postgres','-d','events','-v','ON_ERROR_STOP=1','-c',"CREATE ROLE runtime LOGIN PASSWORD 'synthetic-runtime-readwrite' NOSUPERUSER NOBYPASSRLS",'-c','GRANT CONNECT ON DATABASE events TO runtime','-c','GRANT USAGE ON SCHEMA public TO runtime','-c','GRANT SELECT,INSERT,UPDATE ON event_topics,event_subscriptions,event_publications,event_deliveries TO runtime','-c','GRANT SELECT,UPDATE ON event_usage TO runtime','-c','GRANT SELECT ON event_grants,event_service,event_limits,event_recovery TO runtime']);
  vars.EVENTS_DATABASE_URL=`postgres://runtime:synthetic-runtime-readwrite@${address}/events?sslmode=disable`;
  const endpoint={node,foreign,service,config,vars,id};endpoints.push(endpoint);
  async function start(){
   const child=spawn(binary,['--config',config],{env:vars,stdio:['ignore','pipe','pipe']});endpoint.child=child;
   endpoint.exit=new Promise(resolve=>child.once('exit',(code,signal)=>resolve({code,signal})));
   let logs='';child.stderr.on('data',b=>{logs+=b.toString();});
   let origin;for(let n=0;n<200;n++){origin=logs.match(/ready at (http:\/\/\S+)/)?.[1];if(origin)break;if(child.exitCode!==null)throw Error(`event service exited: ${logs}`);await wait(25);}assert(origin,'event service readiness');
   endpoint.transport=transport(`${origin}/csil/v1/rpc`);endpoint.client=new EventsClient(endpoint.transport);endpoint.description=await new DiscoveryClient(endpoint.transport).describe({versions:['0alpha1'],requiredFeatures:['durable-pull']});
  }
  endpoint.start=start;t.after(async()=>{if(endpoint.child?.exitCode===null&&endpoint.child?.signalCode===null)endpoint.child.kill('SIGTERM');if(endpoint.exit)await endpoint.exit;});await start();
 }
 function event(n,recipient,id){return {ref:{authority:n.application.identity,kind:'event',recordId:id},producer:n.application.identity,recipients:[recipient.application.identity],eventType:'external.changed',occurredAt:new Date(),recordedAt:new Date(),payload:{schema:{name:'external-fixture',version:'1',digest:new Uint8Array(32)},data:Buffer.from('not an Magi task')},causes:[],restrictions:{requirements:[]}};}
 async function call(endpoint,node,op,body,overrides={}){
  // LinkKeys 0.16.2 checks API keys at 2/s per source. This is not a load test.
  await wait(600);
  const packet=signed(op,body,{application:node.application,recipient:endpoint.service,schema:endpoint.description.schema,keyId:node.signers[0].keyId,privateKey:node.signers[0].privateKey,...overrides});
  return endpoint.client[{'read-publication':'readPublication'}[op]??op](packet);
 }
 for(const [index,endpoint]of endpoints.entries()){
  const {node,foreign}=endpoint;const sub={subscriptionId:`foreign-${index}`,producer:node.application.identity,eventType:'external.changed'};
  let publication,body,delivery,receipt;let inbox=consumerStore(join(scratch,`consumer-${index}.sqlite`),foreign.application);t.after(()=>inbox.close());
  await t.test(`${index} component discovery has only event operations`,()=>{assert.deepEqual(endpoint.description.authenticatedOperations.sort(),['Events/acknowledge','Events/publish','Events/pull','Events/read-publication','Events/subscribe']);assert.equal(endpoint.description.schema.name,'catalyst.events');});
  await t.test(`${index} foreign authenticated consumer subscribes without Magi context`,async()=>{const r=await call(endpoint,foreign,'subscribe',w.toEventSubscriptionCbor(sub));assert.deepEqual(r.consumer,foreign.application);});
  await t.test(`${index} producer commits publication and returns the same receipt on retry`,async()=>{body=w.toEventCbor(event(node,foreign,'same-logical-id'));publication=await call(endpoint,node,'publish',body);assert.deepEqual(publication,await call(endpoint,node,'publish',body));assert.deepEqual(Buffer.from(publication.digest),createHash('sha256').update(body).digest());});
  await t.test(`${index} publication key rejects altered input`,async()=>{const v=w.fromEventCbor(body);v.payload.data=Buffer.from('different');await assert.rejects(call(endpoint,node,'publish',w.toEventCbor(v)),e=>e.code===1409);});
  await t.test(`${index} cross-domain pull and independent SQLite acceptance`,async()=>{const batch=await call(endpoint,foreign,'pull',w.toPullEventsCbor({subscriptionId:sub.subscriptionId,limit:8}));assert.equal(batch.deliveries.length,1);delivery=batch.deliveries[0];assert.equal(w.fromEventCbor(delivery.event).sessionId,undefined);receipt=inbox.accept(endpoint.service,delivery);inbox.close();inbox=consumerStore(join(scratch,`consumer-${index}.sqlite`),foreign.application);assert.deepEqual(inbox.accept(endpoint.service,delivery),receipt);assert.equal(inbox.apply(endpoint.service,delivery),1);assert.equal(inbox.apply(endpoint.service,delivery),0);});
  await t.test(`${index} acceptance and publication survive service SIGKILL`,async()=>{endpoint.child.kill('SIGKILL');assert.equal((await endpoint.exit).signal,'SIGKILL');await endpoint.start();assert.deepEqual(await call(endpoint,node,'read-publication',w.toReadPublicationCbor({event:publication.event})),publication);const batch=await call(endpoint,foreign,'pull',w.toPullEventsCbor({subscriptionId:sub.subscriptionId,limit:8}));assert.deepEqual(batch.deliveries,[delivery]);});
  await t.test(`${index} lost acknowledgement retries the saved consumer receipt`,async()=>{const raw=w.toAcknowledgeDeliveryCbor({subscriptionId:sub.subscriptionId,receipt});assert.deepEqual(await call(endpoint,foreign,'acknowledge',raw),receipt);assert.deepEqual(await call(endpoint,foreign,'acknowledge',raw),receipt);assert.equal((await call(endpoint,foreign,'pull',w.toPullEventsCbor({subscriptionId:sub.subscriptionId,limit:8}))).deliveries.length,0);});
  await t.test(`${index} valid LinkKeys identity has no implicit publication authority`,async()=>{await assert.rejects(call(endpoint,foreign,'publish',body),e=>e.code===1403);});
  await t.test(`${index} receiver and stale proof checks fail closed`,async()=>{await assert.rejects(call(endpoint,node,'publish',body,{recipient:endpoints[1-index].service}),e=>e.code===1403);await assert.rejects(call(endpoint,node,'publish',body,{at:new Date(Date.now()-3600000)}),e=>e.code===1403);});
  await t.test(`${index} old event uses a fresh authenticated attempt`,async()=>{const v=event(node,foreign,'old-event');v.occurredAt=new Date(Date.now()-7200000);v.recordedAt=v.occurredAt;await call(endpoint,node,'publish',w.toEventCbor(v));});
 }
 await t.test('real LinkKeys revocation denies the previously accepted signing key',async()=>{const e=endpoints[0];await revokeFirstKey(e.node);await wait(1500);const v=event(e.node,e.foreign,randomUUID());await assert.rejects(call(e,e.node,'publish',w.toEventCbor(v)),error=>error.code===1403);});
 await t.test('an unavailable LinkKeys domain fails closed with a retryable error',async()=>{const e=endpoints[1];await fixture.stop(e.node);await assert.rejects(call(e,e.node,'publish',w.toEventCbor(event(e.node,e.foreign,randomUUID()))),error=>error.code===1500&&error.retryable);});
});
