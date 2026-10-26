import test from 'node:test';
import assert from 'node:assert/strict';
import {manifests,bootstrapJob,route,namespace} from '../deploy/qa-render.mjs';
const image='registry.catalystsquad.local/magi@sha256:'+'a'.repeat(64);
const config={application:{identity:{domain:'catalystsquad.local'}},question_worker:{actor:{identity:{domain:'catalystsquad.local'}}}};
test('QA deployment isolates its stores, credentials and public API',()=>{
  const all=manifests({workflows:image,model:image},config,'TEST CA');
  for(const r of [...all,...bootstrapJob(image),route()]) {
    assert.equal(r.metadata.namespace,namespace);
    assert.ok(r.metadata.name.startsWith('qa-')||['qa','opal'].includes(r.metadata.name));
    assert.ok(!['ClusterRole','ClusterRoleBinding','Gateway','Namespace'].includes(r.kind));
  }
  assert.equal(all.filter(r=>r.kind==='StatefulSet').length,2);
  assert.equal(all.filter(r=>r.kind==='PersistentVolumeClaim').length,2);
  assert.equal(all.filter(r=>r.kind==='Deployment').length,4);
  for(const r of all.filter(r=>['Deployment','StatefulSet'].includes(r.kind))) {
    const p=r.spec.template.spec;
    assert.equal(p.automountServiceAccountToken,false);
    assert.equal(p.securityContext.runAsNonRoot,true);
    for(const c of p.containers) {
      assert.equal(c.securityContext.readOnlyRootFilesystem,true);
      assert.deepEqual(c.securityContext.capabilities.drop,['ALL']);
      for(const env of c.env||[])if(env.name==='WORKFLOWS_DATABASE_URL')assert.equal(env.valueFrom.secretKeyRef.name,'qa-db-runtime');
      if(c.name!=='api')assert.ok(!(c.env||[]).some(e=>e.name==='WORKFLOWS_RP_API_KEY'));
    }
  }
  const worker=all.find(r=>r.kind==='Deployment'&&r.metadata.name==='qa-worker');
  assert.equal(worker.spec.strategy.type,'Recreate');
  assert.equal(worker.spec.template.spec.containers[1].args[3],'127.0.0.1');
  assert.ok(!all.some(r=>r.kind==='Service'&&r.spec.ports.some(p=>p.port===8088)));
  assert.ok(!all.some(r=>r.kind==='HTTPRoute'));
  const apiNet=all.find(r=>r.kind==='NetworkPolicy'&&r.metadata.name==='qa-api');
  assert.equal(apiNet.spec.ingress[0].from[0].namespaceSelector.matchLabels['kubernetes.io/metadata.name'],'gateway-system');
  for(const r of all.filter(r=>r.kind==='NetworkPolicy'))assert.ok(!JSON.stringify(r).includes('0.0.0.0/0'));
  assert.equal(route().spec.rules[0].backendRefs[0].name,'qa-api');
});
test('QA images and domain must match the isolated deployment',()=>{
  assert.throws(()=>manifests({workflows:'untrusted:latest',model:image},config,''));
  assert.throws(()=>manifests({workflows:image,model:image},{...config,application:{identity:{domain:'identity.example'}}},''));
});
test('bootstrap has an administrator only for migration and grant setup',()=>{
  const job=bootstrapJob(image).find(r=>r.kind==='Job');
  assert.equal(job.spec.backoffLimit,1);
  const p=job.spec.template.spec;
  assert.equal(p.automountServiceAccountToken,false);
  assert.equal(p.initContainers[0].name,'wait-stores');
  assert.deepEqual(p.initContainers.slice(1).map(c=>c.args.at(-1)),['-migrate','-apply-policy']);
  assert.ok(bootstrapJob(image)[0].data['grants.sql'].includes('GRANT SELECT ON workflow_service, workflow_grants, workflow_journal_binding'));
  assert.ok(!bootstrapJob(image)[0].data['grants.sql'].includes('GRANT ALL'));
});
