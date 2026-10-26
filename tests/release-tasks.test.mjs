import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture} from './release-fixture.mjs';
import {ReleaseTasks, taskCommand} from '../release/tasks.mjs';
import {ReleaseDelivery} from '../release/delivery.mjs';
import {wire} from '../release/policy.mjs';

const dispatch = {installation:'test-releases', queue:`release/${'a'.repeat(64)}`, taskId:'00000000-0000-4000-8000-000000000002', revision:2};
async function setup(t, call) {
  const f=fixture();t.after(()=>f.close());
  const tasks=new ReleaseTasks({installation:dispatch.installation,call});
  f.startController(undefined,tasks);
  await f.client.submit(wire.toReleaseCandidateCbor(f.value));
  const auth=await f.login(),review=f.recorder.reviewCandidate(auth,f.value.candidateId);
  f.recorder.approve(auth,{...review,commandKey:'approve-task-test'});
  const delivery=new ReleaseDelivery({controller:f.execution,client:f.client,now:f.now});
  return {f,tasks,delivery};
}
test('a queue outage prevents deployment, without renewing the admission deadline',async t=>{
  const {f,delivery}=await setup(t,async()=>{throw Error('down');});
  await delivery.tick();const deadline=f.execution.read(f.value.candidateId).body.deadline;
  for(let i=0;i<3;i++){f.advance(1000);await delivery.tick();}
  assert.equal(f.changes.length,0);assert.equal(f.execution.read(f.value.candidateId).body.deadline,deadline);
  f.advance(31000);await delivery.tick();
  const row=f.execution.read(f.value.candidateId);
  assert.equal(row.body.phase,'recovery_required');assert.equal(row.body.reason,'task-dispatch-deadline');assert.equal(f.changes.length,0);
});
test('a delayed start reply cannot cause even a same-image deployment after the deadline',async t=>{
  let f;const setupResult=await setup(t,async r=>{if(r.operation==='start'){f.advance(31000);return dispatch;}throw Error('offline');});f=setupResult.f;
  await setupResult.delivery.tick();assert.equal(f.changes.length,0);assert.equal(f.execution.read(f.value.candidateId).body.reason,'task-dispatch-deadline');
});
test('the queue is not called during active recovery; completion waits for an idle controller',async t=>{
  const calls=[];
  const {f,tasks,delivery}=await setup(t,async r=>{calls.push(r);return {...dispatch,revision:r.operation==='start'?2:3};});
  await delivery.tick();assert.equal(f.execution.read(f.value.candidateId).body.phase,'applying');
  assert.deepEqual(await tasks.flush(),[]);assert.equal(calls.length,1);
  f.behavior('bad-protocol');
  for(let i=0;i<20&&!f.execution.read(f.value.candidateId).terminal;i++){f.advance(1100);await delivery.tick();}
  assert.equal(f.execution.read(f.value.candidateId).body.phase,'restored');assert.equal(f.changes.length,2);
  assert.deepEqual(calls.map(c=>c.operation),['start','finish']);assert.equal(calls[1].outcome.phase,'restored');
  assert.equal(f.execution.evidence(f.value.candidateId).find(e=>e.kind==='promotion-intent').body.task.taskId,dispatch.taskId);
  assert.deepEqual(await tasks.flush(),[]);
});
test('restart retries the exact retained result after a lost completion reply',async t=>{
  const calls=[];let lost=true;
  const call=async r=>{calls.push(r);if(r.operation==='finish'&&lost){lost=false;throw Error('reply lost');}return {...dispatch,revision:r.operation==='start'?2:3};};
  const {f,delivery}=await setup(t,call);
  await delivery.tick();await f.run();
  assert.equal((await f.execution.tasks.flush())[0].task,'completion-pending');
  f.startController(undefined,new ReleaseTasks({installation:dispatch.installation,call}));
  assert.equal((await f.execution.tasks.flush())[0].task,'completed');
  assert.deepEqual(calls[1],calls[2]);assert.equal(f.changes.length,1);
});
test('enabled queue binding cannot be removed or changed on restart',async t=>{
  const {f}=await setup(t,async()=>dispatch);
  // Constructor failure closes its database. Do not reuse the fixture's old handle.
  assert.throws(()=>f.startController(),/task-adapter-required/);
  assert.throws(()=>f.startController(undefined,new ReleaseTasks({installation:'another',call:async()=>dispatch})),/task-installation-changed/);
  f.startController(undefined,new ReleaseTasks({installation:dispatch.installation,call:async()=>dispatch}));
});
test('enabling queue dispatch on an active legacy release is refused',async t=>{
  const f=fixture();t.after(()=>f.close());f.startController();
  await f.execution.admit(f.approve(await f.login()));
  assert.throws(()=>f.startController(undefined,new ReleaseTasks({installation:dispatch.installation,call:async()=>dispatch})),/task-enable-requires-idle-controller/);
  f.startController();await f.run();assert.equal(f.changes.length,1);
});
test('malformed or wrong-installation task receipts never permit a deployment',async t=>{
  for(const bad of [{...dispatch,installation:'other'},{...dispatch,revision:0},{...dispatch,taskId:'bad'},{...dispatch,extra:true}]){
    const {f,delivery}=await setup(t,async()=>bad);await delivery.tick();assert.equal(f.changes.length,0);assert.equal(f.execution.read(f.value.candidateId).body.phase,'prepared');
  }
});
test('helper paths are operator-installed absolute paths, never task commands',()=>{
  assert.throws(()=>taskCommand({binary:'sh',profile:'/tmp/profile'}),/task-command-paths/);
  assert.throws(()=>taskCommand({binary:'/bin/false',profile:'profile'}),/task-command-paths/);
});
