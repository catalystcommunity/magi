import {requireDeployment} from '../config/deployment.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {operatorPolicy} from '../release/configuration.mjs';
// Operator-only, read-only checks across the release controller and task stores.
import {readFileSync, writeFileSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import {DatabaseSync} from 'node:sqlite';
import {privateConfig} from '../release/main.mjs';
import {need} from '../release/policy.mjs';
import {controllerState} from './release-controller-state.mjs';
import {operator, controllerPod} from './release-controller-inspection.mjs';
import {releaseTaskProof, taskUUID, taskQueue, taskOperation} from './release-task-proof.mjs';

const id = process.argv[2]; need(process.argv.length === 3 && taskUUID.test(id), 'Use: node scripts/check-release-task.mjs candidate-uuid');
requireDeployment();
// This also checks the signed approval, event receipt, and current live target.
const rollbackCheck = JSON.parse(execFileSync(process.execPath, ['scripts/check-release-rollback.mjs', id], {encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe']}));
const rollback = JSON.parse(readFileSync(rollbackCheck.file));
const runtime = privateConfig((environmentConfig.releaseProfileDir + "/controller.json")), run = operator(runtime), pod = controllerPod(run);
need(pod.ready, 'task-proof-controller-not-ready');
const state = controllerState(runtime); need(state.source === 'supervised-controller', 'task-proof-controller-source');
const db = new DatabaseSync(state.path, {readOnly: true});
let row, link, observations, installation;
try {
  const r = db.prepare('SELECT id,target,approval_id,candidate,packet,terminal,body FROM releases WHERE id=?').get(id);
  need(r, 'task-proof-no-release'); const body = JSON.parse(r.body);
  row = {id: r.id, target: r.target, approvalId: r.approval_id, candidate: Buffer.from(r.candidate).toString('base64'), packet: Buffer.from(r.packet).toString('base64'), terminal: r.terminal, phase: body.phase, reason: body.reason, lastAt: body.lastAt};
  const l = db.prepare('SELECT * FROM release_task_links WHERE candidate_id=?').get(id); need(l, 'task-proof-no-link');
  link = {candidateId: l.candidate_id, binding: JSON.parse(l.body), dispatch: JSON.parse(l.dispatch), outcome: JSON.parse(l.outcome), completion: JSON.parse(l.completion), blocked: l.blocked};
  installation = db.prepare('SELECT installation FROM release_task_binding WHERE id=1').get()?.installation;
  observations = db.prepare('SELECT sequence,at,kind,body FROM observations WHERE release_id=? ORDER BY sequence').all(id).map(o => ({...o, body: JSON.parse(o.body)}));
} finally {db.close();}
const queue = link.dispatch?.queue, taskId = link.dispatch?.taskId;
need(taskQueue.test(queue) && taskUUID.test(taskId) && link.completion, 'task-proof-not-completed');
// Fixed database and namespace. Validated IDs cannot change these SQL queries.
const sql = `BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT json_build_object(
  'installation',(SELECT installation FROM workflow_journal_binding WHERE singleton=true),
  'intents',COALESCE((SELECT json_agg(json_build_object('installation',installation,'id',id,'method',method,'queue',queue,'request_key',request_key,'request',encode(request,'base64'),'receipt',encode(receipt,'base64'),'provenance',convert_from(provenance,'UTF8')::json,'state',state,'attempts',attempts,'rejection_code',rejection_code) ORDER BY id) FROM workflow_intents WHERE queue='${queue}'),'[]'::json),
  'results',COALESCE((SELECT json_agg(json_build_object('installation',installation,'task_uuid',task_uuid,'revision',revision,'result',convert_from(result,'UTF8')::json,'evidence',evidence)) FROM workflow_results WHERE task_uuid='${taskId}'),'[]'::json));
ROLLBACK;`;
const journal = JSON.parse(run(['-n', environmentConfig.namespace, 'exec', '-i', 'statefulset/qa-postgres', '--', 'psql', '-X', '-qAt', '-U', 'postgres', '-d', 'release_journal', '-v', 'ON_ERROR_STOP=1'], sql));
const operationId = journal.intents.find(i => i.id === `${queue}/finish`)?.request_key;
need(taskOperation.test(operationId), 'task-proof-operation-id');
// Use the controller's allowed network path, not its task mutation helper.
// These two fixed operations do not reserve, update, complete, or enqueue work.
const query = `import {connect} from 'node:tls';import {readFileSync} from 'node:fs';import * as wire from '/app/.build/releases/codec.gen.js';
const input=JSON.parse(process.argv[2]);
async function read(operation,request){return new Promise((resolve,reject)=>{
 const socket=connect({host:'release-corndogs',port:5080,servername:'release-corndogs',ca:readFileSync('/trust/release/ca.crt'),rejectUnauthorized:true,minVersion:'TLSv1.2'});
 let data=Buffer.alloc(0),finished=false;const timer=setTimeout(()=>done(Error('task-proof-rpc-timeout')),5000);
 function done(error,result){if(finished)return;finished=true;clearTimeout(timer);socket.destroy();error?reject(error):resolve(result);}
 socket.once('error',()=>done(Error('task-proof-rpc-tls-or-transport')));
 socket.once('end',()=>done(Error('task-proof-rpc-truncated')));
 socket.once('close',()=>done(Error('task-proof-rpc-closed')));
 socket.once('secureConnect',()=>{
  const payload=wire.encodeValue(new Map(Object.entries(request)));
  const body=Buffer.from(wire.encodeValue(new Map(Object.entries({v:1,id:1,service:'CorndogsService',op:operation,payload:{tag:24,value:payload}}))));
  const header=Buffer.alloc(4);header.writeUInt32BE(body.length);socket.write(Buffer.concat([header,body]));
 });
 socket.on('data',chunk=>{
  data=Buffer.concat([data,chunk]);if(data.length>65540)return done(Error('task-proof-rpc-size'));
  if(data.length<4)return;const size=data.readUInt32BE();if(size===0||size>65536)return done(Error('task-proof-rpc-size'));
  if(data.length<4+size)return;if(data.length!==4+size)return done(Error('task-proof-rpc-trailing'));
  done(null,{operation,request,response:data.subarray(4).toString('base64'),tls:{authorized:socket.authorized,server:'release-corndogs',protocol:socket.getProtocol()}});
 });
});}
const results=[await read('GetGuardedTask',{queue:input.queue,uuid:input.taskId}),await read('LookupOperation',{operation_id:input.operationId})];
console.log(JSON.stringify(results));`;
const remote = JSON.parse(run(['-n', environmentConfig.namespace, 'exec', '-i', pod.name, '-c', 'controller', '--', 'node', '--input-type=module', '-', JSON.stringify({queue, taskId, operationId})], query));
const app = a => ({identity: {domain: a.identity.domain, account_id: a.identity.accountId}, application_id: a.applicationId, instance_id: a.instanceId});
const config = operatorPolicy();
const expected = {controller: app(runtime.controller), recorder: app(runtime.recorder), target: config.target_id, installation: JSON.parse(readFileSync(operatorFile('release-task-deployment.json'))).installation};
const evidence = {expected, rollback, row, observations, link, installation, journal, remote};
const proof = releaseTaskProof(evidence);
const report = {checkedAt: new Date().toISOString(), ...proof, rollbackReport: rollbackCheck.file, controller: pod, evidence, limits: 'Read-only, sequential snapshots of a terminal release. No live queue outage, worker failover, or distributed fence was tested.'};
const file = `test-results/${report.checkedAt.replaceAll(':', '-')}-release-task-proof.json`;
writeFileSync(file, JSON.stringify(report, null, 2) + '\n', {flag: 'wx', mode: 0o600});
console.log(JSON.stringify({file, ...proof}));
