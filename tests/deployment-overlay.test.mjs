import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync,writeFileSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {execFileSync,spawnSync} from 'node:child_process';
import {validateDeployment,exampleDeployment,requireDeployment} from '../config/deployment.mjs';

test('overlay validation rejects unknown fields, mutable images and inconsistent scope',()=>{
  for(const patch of [{token:'not-a-config-field'},{clusterAddress:'192.0.2.99'},{repository:'other.local/magi'},{corndogsImage:'image:latest'},{podCIDR:'0.0.0.0/0'},{namespace:"bad'namespace"},{qaProfileDir:'relative'},{kind:'production'}])assert.throws(()=>validateDeployment(patch));
  const value=validateDeployment({qaProfileDir:'~/config/qa'},'/tmp/example-home');
  assert.equal(value.qaProfileDir,'/tmp/example-home/config/qa');assert(Object.isFrozen(value));
  assert.throws(()=>requireDeployment(),/select an operator overlay/);
});

test('a non-default overlay binds policy, registry, network and runtime mounts',t=>{
  const dir=mkdtempSync(join(tmpdir(),'magi-overlay-test-'));t.after(()=>rmSync(dir,{recursive:true,force:true}));
  const file=join(dir,'deployment.json');
  const config={...exampleDeployment,kind:'operator',domain:'alternate.local',namespace:'magi-alternate',registry:'registry.alternate.local',repository:'registry.alternate.local/magi',clusterServer:'https://192.0.2.22:6443',clusterAddress:'192.0.2.22',clusterApiCIDR:'192.0.2.22/32',podCIDR:'198.19.0.0/16',gatewayName:'alternate-gateway',ingressNamespace:'alternate-ingress',storageClass:'alternate-storage',canaryHost:'canary.alternate.local',applicationInstance:'alternate-1',linkkeysNamespace:'identity-alternate',linkkeysService:'identity-alternate',linkkeysInstance:'identity-alternate'};
  writeFileSync(file,JSON.stringify(config),{mode:0o600});
  const source=`import assert from 'node:assert/strict';
import {deployment,immutableImage,podAddressAllowed,requireDeployment} from './config/deployment.mjs';
import {manifests} from './deploy/release-controller-render.mjs';
import {policy} from './release/policy.mjs';
requireDeployment();
const image=deployment.repository+'@sha256:'+'a'.repeat(64), all=manifests(image);
assert(immutableImage(image));assert(!immutableImage('registry.catalystsquad.local/magi@sha256:'+'a'.repeat(64)));
assert(podAddressAllowed('198.19.2.3'));assert(!podAddressAllowed('198.18.2.3'));assert(!podAddressAllowed('127.0.0.1'));
const d=all.find(r=>r.kind==='Deployment');assert.equal(d.metadata.namespace,'magi-alternate');
const mount=d.spec.template.spec.containers[0].volumeMounts.find(v=>v.name==='environment');assert.equal(mount.readOnly,true);
const map=all.find(r=>r.kind==='ConfigMap'), runtime=JSON.parse(map.data['deployment.json']);assert.equal(runtime.domain,'alternate.local');assert.equal(runtime.kubeconfig,undefined);assert.equal(runtime.testUsername,undefined);
const admission=all.find(r=>r.kind==='ValidatingAdmissionPolicy');assert(admission.spec.validations.some(v=>v.expression.includes('registry[.]alternate[.]local/magi')));
assert(all.find(r=>r.kind==='NetworkPolicy').spec.egress.some(e=>e.to?.some(t=>t.ipBlock?.cidr==='192.0.2.22/32')));
const p={version:1,schema_changes:'none',approver:{domain:'human.example',account_id:'00000000-0000-4000-8000-000000000001'},target_domain:deployment.domain,namespace:deployment.namespace,deployment:'release-canary',container:'opal',target_id:'opal-stateless-canary-v1',cluster_server:deployment.clusterServer,image_repository:deployment.repository};
policy(p);assert.throws(()=>policy({...p,namespace:'magi-example'}));assert.throws(()=>policy({...p,cluster_server:'https://192.0.2.10:6443'}));
console.log('overlay checks passed');`;
  assert.match(execFileSync(process.execPath,['--input-type=module','-e',source],{cwd:resolve(import.meta.dirname,'..'),env:{...process.env,MAGI_DEPLOYMENT_CONFIG:file},encoding:'utf8'}),/overlay checks passed/);
});

test('a live check refuses example settings before calling kubectl',t=>{
  const dir=mkdtempSync(join(tmpdir(),'magi-overlay-refusal-'));t.after(()=>rmSync(dir,{recursive:true,force:true}));
  const child=spawnSync(process.execPath,['scripts/check-release-live.mjs'],{cwd:resolve(import.meta.dirname,'..'),env:{...process.env,MAGI_DEPLOYMENT_CONFIG:resolve(import.meta.dirname,'../config/deployment.example.json'),MAGI_OPERATOR_STATE_DIR:join(dir,'state')},encoding:'utf8'});
  assert.notEqual(child.status,0);assert.match(child.stderr,/select an operator overlay/);
});
