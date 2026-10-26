import test from 'node:test';
import assert from 'node:assert/strict';
import {taskStoreManifests,taskCheckJob,withTaskRuntime} from '../deploy/release-tasks-render.mjs';
import {manifests} from '../deploy/release-controller-render.mjs';
const image='registry.catalystsquad.local/magi@sha256:'+'a'.repeat(64);
test('release task stores use private TLS, required resilience policies, and no shared workflow queues',()=>{
  const resources=taskStoreManifests('PUBLIC-CA'),store=resources.find(r=>r.kind==='StatefulSet');
  assert.equal(store.metadata.name,'release-corndogs');assert.equal(store.spec.template.spec.automountServiceAccountToken,false);
  const env=Object.fromEntries(store.spec.template.spec.containers[0].env.map(e=>[e.name,e.value]));
  assert.equal(env.CORNDOGS_SUBMISSION_KEY_POLICY,'required');assert.equal(env.CORNDOGS_TASK_GUARD_POLICY,'required');assert.equal(env.CORNDOGS_FILESTORE_SYNC,'always');assert(env.CORNDOGS_TLS_KEY_FILE);
  assert(!resources.some(r=>['Role','RoleBinding','ClusterRole','Deployment'].includes(r.kind)));
  const ingress=resources.find(r=>r.kind==='NetworkPolicy'&&r.metadata.name==='release-corndogs').spec.ingress[0];
  assert.deepEqual(ingress.from.map(p=>p.podSelector.matchLabels['app.kubernetes.io/name']),['opal-release-controller','opal-release-task-check']);
});
test('controller task mounts expose only the runtime profile and public trust; init has no application or Kubernetes key',()=>{
  const original=manifests(image).find(r=>r.kind==='Deployment').spec.template.spec;
  const changed=withTaskRuntime(original,image);assert.equal(original.initContainers,undefined);
  const init=changed.initContainers[0];assert.deepEqual(init.volumeMounts.map(m=>m.name),['task-secret','task-profile']);assert(init.command[2].includes('mode:384'));
  assert(!changed.containers[0].volumeMounts.some(m=>m.name==='task-secret'));
  assert.deepEqual(changed.volumes.find(v=>v.name==='task-secret').secret.items,[{key:'profile.json',path:'profile.json'}]);
  assert.throws(()=>withTaskRuntime(changed,image));assert.throws(()=>withTaskRuntime(original,'image:latest'));
});
test('task readiness Job has no release keys, controller PVC, or service-account token',()=>{
  const job=taskCheckJob(image,'release-task-check-test'),pod=job.spec.template.spec;
  assert.equal(pod.automountServiceAccountToken,false);assert(!pod.volumes.some(v=>v.projected||v.persistentVolumeClaim));
  assert.deepEqual(pod.containers[0].command,['/app/bin/release-tasks','--check','/run/release-tasks/profile.json']);
  assert.equal(pod.volumes.filter(v=>v.secret).length,1);assert.equal(pod.volumes.find(v=>v.secret).secret.secretName,'release-task-runtime');
});
