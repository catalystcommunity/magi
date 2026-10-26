import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture} from './release-fixture.mjs';
import {ReleaseDelivery, DeliverySchedule} from '../release/delivery.mjs';
import {wire, decode, hash, scope} from '../release/policy.mjs';
import {signed} from '../protocol/profile.mjs';

const setup = t => {const f = fixture(); t.after(() => f.close()); return f;};
const submit = f => f.client.submit(wire.toReleaseCandidateCbor(f.value));
async function approve(f) {
  await submit(f); const auth = await f.login(), review = f.recorder.reviewCandidate(auth, f.value.candidateId);
  const packet = f.recorder.approve(auth, {...review, commandKey:'approve-request'});
  return {auth, packet, approval:decode('ReleaseApproval',decode('SignatureInput',packet.input).body)};
}
test('submission is immutable, idempotent, durable, and not approval', async t => {
  const f=setup(t), receipt=await submit(f); f.restartRecorder();
  assert.deepEqual(await submit(f),receipt);
  assert.equal(new URL(receipt.reviewUrl).searchParams.get('candidate'), f.value.candidateId);
  assert.equal(f.recorder.db.prepare('SELECT count(*) AS n FROM candidates').get().n,1);
  assert.deepEqual(await f.client.events(),[]);
  assert.equal(f.recorder.db.prepare('SELECT count(*) AS n FROM approvals').get().n,0);
  await assert.rejects(f.client.submit(wire.toReleaseCandidateCbor({...f.value,explanation:{...f.value.explanation,title:'Changed claim'}})),/candidate-conflict/);
  f.startController(); const delivery=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});
  assert.deepEqual(await delivery.tick(),[]); assert.equal(f.changes.length,0);
});
test('submission requires the authorized application, fresh proof, and readable explanation', async t => {
  const f=setup(t), bytes=wire.toReleaseCandidateCbor(f.value);
  const packet=signed(scope('submit',f.app,f.app),wire.toReleaseSubmissionCbor({candidate:bytes}),f.recorderKey.keyId,f.now(),f.recorderKey.privateKey);
  await assert.rejects(f.recorder.submit(packet,f.resolveKey),/signature-participants/);
  const old=f.client.packet('submit','ReleaseSubmission',{candidate:bytes});f.advance(60001);
  await assert.rejects(f.recorder.submit(old,f.resolveKey),/signature-time/);
  await assert.rejects(f.client.submit(wire.toReleaseCandidateCbor({...f.value,explanation:undefined})),/explanation-required/);
  await assert.rejects(f.client.submit(wire.toReleaseCandidateCbor({...f.value,explanation:{...f.value.explanation,title:'\nunsafe'}})),/candidate-explanation/);
});
test('review by ID requires human session and returns exactly submitted bytes', async t => {
  const f=setup(t);await submit(f);
  assert.throws(()=>f.recorder.reviewCandidate({},f.value.candidateId),/csrf/);
  const auth=await f.login();assert.throws(()=>f.recorder.reviewCandidate(auth,'missing'),/unknown-candidate/);
  const r=f.recorder.reviewCandidate(auth,f.value.candidateId);
  assert.deepEqual(Buffer.from(r.candidate),Buffer.from(wire.toReleaseCandidateCbor(f.value)));
  assert.deepEqual(Buffer.from(r.digest),hash(r.candidate));
});
test('approval and its durable event commit together and replay once', async t => {
  const f=setup(t);await submit(f);const auth=await f.login(),r=f.recorder.reviewCandidate(auth,f.value.candidateId);
  f.restartRecorder(point=>{if(point==='before-approval-commit')throw Error('crash');});
  assert.throws(()=>f.recorder.approve(auth,{...r,commandKey:'request'}),/crash/);
  assert.deepEqual(await f.client.events(),[]);
  f.restartRecorder(point=>{if(point==='after-approval-commit')throw Error('crash');});
  assert.throws(()=>f.recorder.approve(auth,{...r,commandKey:'request'}),/crash/);
  f.restartRecorder();f.recorder.approve(auth,{...r,commandKey:'request'});
  const events=await f.client.events();assert.equal(events.length,1);assert.equal(events[0].kind,'approved');
  assert.deepEqual(await f.client.events(),events);
  await f.client.ack(events[0].eventId);await f.client.ack(events[0].eventId);
  f.restartRecorder();assert.deepEqual(await f.client.events(),[]);
});
for (const boundary of ['after-admission-before-inbox','after-inbox-before-ack']) test(`delivery resumes after ${boundary} without another admission`, async t=>{
  const f=setup(t);await approve(f);f.startController();
  const events=await f.client.events();
  const delivery=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now,fault:point=>{if(point===boundary)throw Error('crash');}});
  await assert.rejects(delivery.accept(events[0]),/crash/);
  assert.equal(f.execution.db.prepare('SELECT count(*) AS n FROM releases').get().n,1);
  f.startController();f.restartRecorder();const resumed=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});
  await resumed.accept(events[0]);assert.deepEqual(await f.client.events(),[]);
  assert.equal(f.execution.db.prepare('SELECT count(*) AS n FROM releases').get().n,1);
  await f.run();assert.equal(f.changes.length,1);assert.equal(f.execution.read(f.value.candidateId).body.phase,'healthy');
});
test('lost acknowledgement reply is safe across recorder and controller restarts', async t=>{
  const f=setup(t);await approve(f);f.startController();const event=(await f.client.events())[0];
  const ack=f.client.ack.bind(f.client);f.client.ack=async id=>{await ack(id);throw Error('lost-reply');};
  const d=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});await assert.rejects(d.accept(event),/lost-reply/);
  f.client.ack=ack;f.startController();f.restartRecorder();
  await new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now}).accept(event);
  assert.equal(f.execution.db.prepare('SELECT count(*) AS n FROM delivery_inbox').get().n,1);
  assert.deepEqual(await f.client.events(),[]);
});
test('withdrawn and expired approvals are recorded without deployment', async t=>{
  for(const mode of ['withdrawn','expired']){
    const f=setup(t),{auth,approval}=await approve(f);f.startController();
    if(mode==='withdrawn')f.recorder.withdraw(auth,approval.approvalId);else f.advance(3600001);
    const result=await new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now}).tick();
    assert(result.some(r=>r.disposition===`${mode}-before-admission`));
    assert.equal(f.changes.length,0);assert.equal(f.execution.db.prepare('SELECT count(*) AS n FROM releases').get().n,0);
    assert.deepEqual(await f.client.events(),[]);
  }
});
test('transient fetch failure retains the event until a successful retry', async t=>{
  const f=setup(t);await approve(f);f.startController();const fetch=f.client.fetch.bind(f.client);
  f.client.fetch=async()=>{throw Error('unavailable');};const d=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});
  assert.equal((await d.tick())[0].disposition,'pending-retry');assert.equal((await f.client.events()).length,1);
  assert.equal(f.changes.length,0);f.client.fetch=fetch;assert.equal((await d.tick())[0].disposition,'admitted');
});
test('event retrieval requires a current signature and response challenge', async t=>{
  const f=setup(t);await approve(f);
  const old=f.client.packet('event-query','ReleaseEventQuery',{challenge:hash('old')});
  f.advance(60001);await assert.rejects(f.recorder.pullEvents(old,f.resolveKey),/signature-time/);
  const call=f.client.call;let previous;
  f.client.call=async(op,...args)=>{if(op!=='pull-events')return call(op,...args);if(previous)return previous;previous=await call(op,...args);return previous;};
  await f.client.events();await assert.rejects(f.client.events(),/event-response/);
});
test('a conflicting replay event cannot change its recorded meaning', async t=>{
  const f=setup(t);await approve(f);f.startController();const event=(await f.client.events())[0];
  const d=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});await d.accept(event);
  await assert.rejects(d.accept({...event,candidateId:'other'}),/event-conflict/);
});
test('an unavailable event service does not prevent admitted recovery', async t=>{
  const f=setup(t);await approve(f);f.startController();
  const d=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});
  await d.tick();await d.tick();assert.equal(f.changes.length,1);
  f.behavior('bad-protocol');f.client.events=async()=>{throw Error('offline');};
  for(let i=0;i<20&&!f.execution.read(f.value.candidateId).terminal;i++){f.advance(1100);await d.tick();}
  assert.equal(f.execution.read(f.value.candidateId).body.phase,'restored');assert.equal(f.changes.length,2);
});
test('idle polling backs off independently of active controller steps', async t=>{
  const schedule=new DeliverySchedule();assert(schedule.due(0));
  schedule.completed(0,[]);assert(!schedule.due(9999));assert(schedule.due(10000));
  schedule.completed(10000,[{delivery:'unavailable'}]);assert.equal(schedule.nextPull,30000);
  schedule.completed(30000,[{disposition:'pending-retry'}]);assert.equal(schedule.nextPull,70000);
  schedule.completed(70000,[{delivery:'unavailable'}]);assert.equal(schedule.nextPull,130000);
  schedule.completed(130000,[]);assert.equal(schedule.nextPull,140000);
  const f=setup(t);await approve(f);f.startController();
  const d=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});await d.tick();
  f.client.events=async()=>{throw Error('must not pull');};
  await d.tick({pull:false});assert.equal(f.changes.length,1);
});
