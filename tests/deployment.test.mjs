import test from 'node:test';
import assert from 'node:assert/strict';
import {manifests,namespace,hostname} from '../deploy/render.mjs';
const image='registry.catalystsquad.local/magi@sha256:'+'a'.repeat(64);
test('deployment writes only Magi resources and uses the existing gateway',()=>{
 const all=manifests(image);for(const r of all){if(r.kind==='Namespace')assert.equal(r.metadata.name,namespace);else assert.equal(r.metadata.namespace,namespace);assert.equal(r.metadata.labels['app.kubernetes.io/name'],'opal');}
 assert.ok(!all.some(r=>['ClusterRole','ClusterRoleBinding','Gateway','CustomResourceDefinition'].includes(r.kind)));
 const route=all.find(r=>r.kind==='HTTPRoute');assert.deepEqual(route.spec.hostnames,[hostname]);assert.equal(route.spec.parentRefs[0].name,'gateway');
 const d=all.find(r=>r.kind==='Deployment');assert.equal(d.spec.replicas,2);const pod=d.spec.template.spec;
 assert.equal(pod.automountServiceAccountToken,false);assert.equal(pod.enableServiceLinks,false);assert.equal(pod.nodeSelector['kubernetes.io/arch'],'amd64');
 assert.equal(pod.containers[0].securityContext.readOnlyRootFilesystem,true);assert.deepEqual(pod.containers[0].securityContext.capabilities.drop,['ALL']);
 assert.equal(pod.containers[0].image,image);assert.deepEqual(all.find(r=>r.kind==='NetworkPolicy').spec.egress,[]);
});
test('deployment refuses tags and other image paths',()=>{for(const image of ['opal:latest','registry.catalystsquad.local/public/catalystcommunity/opal@sha256:'+'a'.repeat(64)])assert.throws(()=>manifests(image));});
