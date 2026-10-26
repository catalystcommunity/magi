import {deployment as environmentConfig} from '../config/deployment.mjs';
// Run only through scripts/test-release-tasks.mjs. Uses disposable services,
// synthetic approvals, and a simulated deployment, never the live cluster.
import test from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync, execFile} from 'node:child_process';
import {randomUUID} from 'node:crypto';
import {mkdtempSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';
import {createServer, connect} from 'node:net';
import {setTimeout} from 'node:timers/promises';
import {fixture} from './release-fixture.mjs';
import {wire} from '../release/policy.mjs';
import {ReleaseTasks, taskCommand} from '../release/tasks.mjs';
import {ReleaseDelivery} from '../release/delivery.mjs';

const binary=resolve('.build/release-tasks'), images={pg:'postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd',cd:environmentConfig.corndogsImage};
const docker=(...args)=>execFileSync('docker',args,{encoding:'utf8',timeout:45000,stdio:['pipe','pipe','pipe']}).trim();
const address=name=>docker('port',name,name.endsWith('-pg')?'5432/tcp':'5080/tcp');
const toApp=a=>({identity:{domain:a.identity.domain,account_id:a.identity.accountId},application_id:a.applicationId,instance_id:a.instanceId});
async function until(check){const end=Date.now()+30000;while(true){try{if(await check())return;}catch{}if(Date.now()>=end)throw Error('fixture readiness timeout');await setTimeout(100);}}
async function freeAddress(){const s=createServer();await new Promise(r=>s.listen(0,'127.0.0.1',r));const a=`127.0.0.1:${s.address().port}`;await new Promise(r=>s.close(r));return a;}
function frame(body){const h=Buffer.alloc(4);h.writeUInt32BE(body.length);return Buffer.concat([h,body]);}
async function rpc(address,op,payload){
  const [host,port]=address.split(':');
  return new Promise((resolve,reject)=>{
    const s=connect({host,port:Number(port)});let reply=Buffer.alloc(0);
    s.setTimeout(5000,()=>s.destroy(Error('fixture timeout')));s.on('error',reject);
    s.on('connect',()=>s.write(frame(wire.encodeValue(new Map([['v',1],['id',1],['service','CorndogsService'],['op',op],['payload',{tag:24,value:wire.encodeValue(payload)}]])))));
    s.on('data',b=>{reply=Buffer.concat([reply,b]);if(reply.length>=4&&reply.length>=4+reply.readUInt32BE()){s.end();resolve(wire.decode(reply.subarray(4)));}});
  });
}
async function proxy(destination){
  const [host,port]=destination.split(':'), sockets=new Set(), counts=new Map();let fault;
  const server=createServer(client=>{
    sockets.add(client);const upstream=connect({host,port:Number(port)});sockets.add(upstream);
    let request=Buffer.alloc(0),reply=Buffer.alloc(0),operation;
    const close=()=>{client.destroy();upstream.destroy();sockets.delete(client);sockets.delete(upstream);};
    client.on('error',close);upstream.on('error',close);client.on('close',close);
    client.on('data',b=>{request=Buffer.concat([request,b]);if(!operation&&request.length>=4&&request.length>=4+request.readUInt32BE()){
      operation=wire.decode(request.subarray(4)).get('op');counts.set(operation,(counts.get(operation)??0)+1);upstream.write(request);
    }});
    upstream.on('data',b=>{reply=Buffer.concat([reply,b]);if(reply.length>=4&&reply.length>=4+reply.readUInt32BE()){
      if(fault?.operation===operation){const f=fault;fault=undefined;f.action?.();close();}else client.end(reply);
    }});
  });
  await new Promise(r=>server.listen(0,'127.0.0.1',r));
  return {address:`127.0.0.1:${server.address().port}`,counts,arm(operation,action){fault={operation,action};},async close(){for(const s of sockets)s.destroy();await new Promise(r=>server.close(r));}};
}
async function services(t,mode){
  const id='opal-release-task-'+randomUUID(), names=[], pg=id+'-pg', cd=id+'-cd';
  docker('network','create','--label','opal.release-task='+id,id);
  t.after(()=>{
    for(const name of names.reverse()){
      assert.equal(docker('inspect','-f','{{index .Config.Labels "opal.release-task"}}',name),id);
      docker('rm','-f','-v',name);
    }
    assert.equal(docker('network','inspect','-f','{{index .Labels "opal.release-task"}}',id),id);docker('network','rm',id);
  });
  const run=(name,image,args)=>{docker('run','--pull=never','-d','--name',name,'--network',id,'--label','opal.release-task='+id,'--memory','512m','--cpus','2','--pids-limit','256',...args,image);names.push(name);};
  run(pg,images.pg,['-p',`${await freeAddress()}:5432`,'-e','POSTGRES_PASSWORD=synthetic-only']);
  await until(()=>docker('exec',pg,'pg_isready','-h','127.0.0.1','-U','postgres').includes('accepting connections'));
  const sql=q=>docker('exec',pg,'psql','-U','postgres','-d','postgres','-At','-v','ON_ERROR_STOP=1','-c',q);
  if(mode==='postgres')sql('CREATE DATABASE corndogs');
  const args=['-p',`${await freeAddress()}:5080`,'-e','STORAGE_BACKEND='+mode,'-e','CORNDOGS_FILESTORE_DIR=/tmp/release-test','-e','CORNDOGS_FILESTORE_SYNC=always','-e','CORNDOGS_SUBMISSION_KEY_POLICY=required','-e','CORNDOGS_TASK_GUARD_POLICY=required','-e','LOGLEVEL=error'];
  if(mode==='postgres')args.push('-e','DATABASE_HOST='+pg,'-e','DATABASE_NAME=corndogs','-e','DATABASE_USER=postgres','-e','DATABASE_PASSWORD=synthetic-only');
  run(cd,images.cd,args);const pgAddress=address(pg),cdAddress=address(cd),forward=await proxy(cdAddress);
  t.after(()=>forward.close());
  const sample=fixture();const config={database:`postgres://postgres:synthetic-only@${pgAddress}/postgres?sslmode=disable`,installation:id,corndogs_seeds:[forward.address],controller:toApp(sample.controller),recorder:toApp(sample.app),target:sample.config.target_id};sample.close();
  const dir=mkdtempSync(join(tmpdir(),'opal-release-tasks-')),profile=join(dir,'private.json');writeFileSync(profile,JSON.stringify(config),{mode:0o600});
  // The proxy runs on this event loop. Use async child calls, not execFileSync.
  const migrate=()=>new Promise((resolve,reject)=>execFile(binary,['--migrate',profile],{timeout:12000},e=>e?reject(e):resolve(true)));
  await until(migrate);
  sql("CREATE ROLE release_runtime LOGIN PASSWORD 'runtime-test-only' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT; REVOKE CREATE ON SCHEMA public FROM PUBLIC; GRANT USAGE ON SCHEMA public TO release_runtime; GRANT SELECT ON workflow_journal_binding TO release_runtime; GRANT SELECT,INSERT,UPDATE ON workflow_intents TO release_runtime; GRANT SELECT,INSERT ON workflow_results TO release_runtime;");
  config.database=`postgres://release_runtime:runtime-test-only@${pgAddress}/postgres?sslmode=disable`;
  writeFileSync(profile,JSON.stringify(config),{mode:0o600});
  return {id,profile,sql,pg,cd,cdAddress,forward,call:taskCommand({binary,profile}),config};
}
async function approved(t,s,call=s.call){
  const f=fixture();t.after(()=>f.close());const tasks=new ReleaseTasks({installation:s.id,call});f.startController(undefined,tasks);
  await f.client.submit(wire.toReleaseCandidateCbor(f.value));const auth=await f.login(),review=f.recorder.reviewCandidate(auth,f.value.candidateId);
  f.recorder.approve(auth,{...review,commandKey:'integration-approval'});
  return {f,tasks,delivery:new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now})};
}
for(const mode of ['file','postgres'])test(`release tasks with real ${mode} Corndogs`,{timeout:180000},async t=>{
  const s=await services(t,mode);
  await t.test('restricted runtime can check both stores but cannot migrate the journal',async()=>{
    const invoke=flag=>new Promise((resolve,reject)=>execFile(binary,[flag,s.profile],{timeout:12000},(e,out)=>e?reject(e):resolve(JSON.parse(out))));
    assert.deepEqual(await invoke('--check'),{ready:true});await assert.rejects(invoke('--migrate'));
  });
  await t.test('lost submission response resolves the original task without a resend',async t=>{
    const {f,delivery}=await approved(t,s);const before=s.forward.counts.get('SubmitKeyedTask')??0;
    s.forward.arm('SubmitKeyedTask');await delivery.tick();assert.equal(f.changes.length,0);assert.equal(f.execution.read(f.value.candidateId).body.phase,'prepared');
    await delivery.tick();assert.equal(f.execution.read(f.value.candidateId).body.phase,'applying');assert.equal(s.forward.counts.get('SubmitKeyedTask')-before,1);
    await f.run();assert.equal((await f.execution.tasks.flush())[0].task,'completed');
    const dispatch=JSON.parse(f.execution.db.prepare('SELECT dispatch FROM release_task_links').get().dispatch);
    await assert.rejects(s.call({operation:'start',binding:f.execution.tasks.binding(f.execution.read(f.value.candidateId))}));
    assert.equal(f.changes.length,1);assert.equal(dispatch.revision,2);
  });
  await t.test('helper SIGKILL after remote reservation recovers the original operation',async t=>{
    let killed=false;
    const call=request=>new Promise((resolve,reject)=>{
      const child=execFile(binary,['--request',s.profile],{timeout:12000},(error,stdout)=>error?reject(error):resolve(JSON.parse(stdout)));
      if(!killed){s.forward.arm('UpdateGuardedTask',()=>{killed=true;child.kill('SIGKILL');});}
      child.stdin.end(JSON.stringify(request));
    });
    const {f,delivery}=await approved(t,s,call);const before=s.forward.counts.get('UpdateGuardedTask')??0;
    await delivery.tick();assert(killed);assert.equal(f.changes.length,0);
    f.startController(undefined,new ReleaseTasks({installation:s.id,call:s.call}));
    const resumed=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});await resumed.tick();
    assert.equal(s.forward.counts.get('UpdateGuardedTask')-before,1);await f.run();assert.equal((await f.execution.tasks.flush())[0].task,'completed');
  });
  await t.test('same-state revision change cannot start or accept the old result',async t=>{
    const {f,delivery}=await approved(t,s);await delivery.tick();
    const link=f.execution.db.prepare('SELECT * FROM release_task_links').get(),r=JSON.parse(link.dispatch);
    const response=await rpc(s.cdAddress,'UpdateGuardedTask',new Map([['queue',r.queue],['uuid',r.taskId],['operation_id',randomUUID()],['expected_revision',r.revision],['expected_state','running'],['new_state','running'],['auto_target_state','running'],['timeout',-1]]));
    assert.equal(Number(response.get('status')),0);assert.equal(response.get('variant'),'UpdateGuardedTaskResponse');
    await assert.rejects(s.call({operation:'start',binding:JSON.parse(link.body)}));
    await f.run();assert.equal((await f.execution.tasks.flush())[0].task,'completion-requires-reconciliation');
    assert.equal(f.execution.db.prepare('SELECT blocked FROM release_task_links').get().blocked,'reconcile_required');
    assert.deepEqual(await f.execution.tasks.flush(),[]);
    assert.equal(f.execution.db.prepare('SELECT completion FROM release_task_links').get().completion,null);
    assert.equal(s.sql("SELECT count(*) FROM workflow_intents WHERE state='rejected'"),'1');
  });
  await t.test('queue and database loss do not block rollback; result survives lost completion reply',async t=>{
    const {f,delivery}=await approved(t,s);await delivery.tick();await delivery.tick();assert.equal(f.changes.length,1);
    docker('kill','--signal=KILL',s.cd);docker('kill','--signal=KILL',s.pg);f.behavior('bad-protocol');
    for(let i=0;i<20&&!f.execution.read(f.value.candidateId).terminal;i++){f.advance(1100);await delivery.tick();}
    assert.equal(f.execution.read(f.value.candidateId).body.phase,'restored');assert.equal(f.changes.length,2);
    const retained=f.execution.db.prepare('SELECT outcome,completion FROM release_task_links').get();assert(retained.outcome);assert.equal(retained.completion,null);
    docker('start',s.pg);await until(()=>docker('exec',s.pg,'pg_isready','-h','127.0.0.1','-U','postgres').includes('accepting connections'));
    docker('start',s.cd);await until(async()=>{const reply=await rpc(s.cdAddress,'GetServerInfo',new Map());return Number(reply.get('status'))===0;});
    s.forward.arm('CompleteGuardedTask');assert.equal((await f.execution.tasks.flush())[0].task,'completion-pending');
    f.startController(undefined,new ReleaseTasks({installation:s.id,call:s.call}));
    assert.equal((await f.execution.tasks.flush())[0].task,'completed');assert.equal(f.changes.length,2);
    const result=f.execution.db.prepare('SELECT outcome,completion FROM release_task_links').get();assert.equal(result.outcome,retained.outcome);assert(result.completion);
    assert.equal(s.sql("SELECT count(*) FROM workflow_results WHERE convert_from(result,'UTF8')::jsonb->'outcome'->>'phase'='restored'"),'1');
  });
  await t.test('changed approval bytes conflict instead of creating another task',async t=>{
    const {f,delivery}=await approved(t,s);await delivery.tick();const binding=f.execution.tasks.binding(f.execution.read(f.value.candidateId));
    await assert.rejects(s.call({operation:'start',binding:{...binding,approvalDigest:'f'.repeat(64)}}));
    await f.run();assert.equal((await f.execution.tasks.flush())[0].task,'completed');
  });
  t.diagnostic(JSON.stringify({mode,intents:Number(s.sql('SELECT count(*) FROM workflow_intents')),results:Number(s.sql('SELECT count(*) FROM workflow_results')),scope:'real queue and journal; synthetic approval; simulated deployment'}));
});
