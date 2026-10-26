import test from 'node:test';
import assert from 'node:assert/strict';
import {generateKeyPairSync,createHash,sign,createPrivateKey,createPublicKey,verify} from 'node:crypto';
import {renewalConfig,renewApplications,renewalEndpoint,renewalTransport,instance} from '../release/renewal.mjs';
import {renewalManifests} from '../deploy/release-renewal-render.mjs';
import {codec,sdk,tuple} from '../release/linkkeys.mjs';
const hash=b=>createHash('sha256').update(b).digest('hex');
function fixture(edit=()=>{}) {
  const at=new Date('2026-10-20T00:00:00Z'),iso=offset=>new Date(+at+offset).toISOString();
  const domain=generateKeyPairSync('ed25519'),raw=key=>Buffer.from(key.export({format:'jwk'}).x,'base64url');
  const anchors=[{keyId:'domain',keyUsage:'sign',algorithm:'ed25519',publicKey:raw(domain.publicKey),createdAt:iso(-86400000),expiresAt:iso(86400000)}];
  const config={version:1,tcpAddress:renewalEndpoint,fingerprints:['a'.repeat(64),'b'.repeat(64)],applications:['approval','controller'].map(name=>({app:{identity:{accountId:'00000000-0000-4000-8000-000000000001',domain:'catalystsquad.local'},applicationId:`opal-release-${name}`,instanceId:'example-1'},keys:Array.from({length:3},(_,i)=>({keyId:`${name}-${i}`,key:generateKeyPairSync('ed25519').privateKey.export({format:'jwk'})}))}))};
  const stored=new Map(),events=[],calls=[],challenges=new Set();
  const rpc={async call(service,op,bytes){
    calls.push(op);assert.equal(service,'ApplicationKeys');
    if(op==='start-key-challenge') {
      const req=codec.fromStartApplicationKeyChallengeRequestCbor(bytes);assert.equal(req.purpose,'renew');
      const challengeId=`challenge-${calls.length}`;challenges.add(challengeId);
      return codec.toStartApplicationKeyChallengeResponseCbor({challengeId,challenge:Buffer.alloc(32,7),expiresAt:iso(60000)});
    }
    if(op==='renew-attestation') {
      const r=codec.fromRenewApplicationKeyAttestationRequestCbor(bytes).request,a=codec.fromApplicationKeyRenewalCbor(r.renewal);
      assert(challenges.delete(a.challengeId));assert.equal(a.expiresAt,iso(60000));
      const entry=config.applications.find(e=>e.app.applicationId===a.applicationId),key=entry.keys.find(k=>k.keyId===a.keyId);
      const message=tag=>tuple.encodeCborTuple([tuple.text(tag),tuple.bytes(r.renewal)]);
      assert(verify(null,message('linkkeys-application-key-possession-v1alpha'),createPublicKey({key:key.key,format:'jwk'}),r.possessionProof));
      assert.equal(new Set(r.signatures.map(s=>s.signedByKeyId)).size,2);
      for(const s of r.signatures)assert(verify(null,message('linkkeys-application-key-renewal-v1alpha'),createPublicKey({key:entry.keys.find(k=>k.keyId===s.signedByKeyId).key,format:'jwk'}),s.signature));
      const publicKey=Buffer.from(key.key.x,'base64url');
      const claim={...instance(entry.app),keyId:key.keyId,keyUsage:'sign',algorithm:'ed25519',publicKey,fingerprint:hash(publicKey),keyCreatedAt:iso(-60000),keyExpiresAt:iso(20*86400000),attestedAt:iso(0),attestationExpiresAt:iso(86400000)};
      edit({claim,anchors});
      const attestation=codec.toApplicationKeyAttestationCbor(claim),signed={attestation,signatures:[{domain:'catalystsquad.local',signedByKeyId:'domain',signature:sign(null,sdk.attestationSignatureInput(attestation),domain.privateKey)}]};
      edit({signed});stored.set(key.keyId,signed);
      return codec.toRenewApplicationKeyAttestationResponseCbor({attestation:signed,signed:false});
    }
    assert.equal(op,'get-application-keys');
    const req=codec.fromGetApplicationKeysRequestCbor(bytes),entry=config.applications.find(e=>e.app.applicationId===req.applicationId);
    const state={...instance(entry.app),keys:entry.keys.map(k=>stored.get(k.keyId)),revocations:[]};edit({state,entry,at});
    return codec.toGetApplicationKeysResponseCbor(state);
  }};
  return {config,events,calls,run:()=>renewApplications(config,{rpc,anchors:async()=>anchors,now:()=>at,pause:async()=>{},emit:e=>events.push(e)})};
}
test('renewal service verifies six fresh quorum requests and current domain-signed keys; false signed flag is success',async()=>{
  const f=fixture(),r=await f.run();assert.equal(r.keys.length,6);assert.equal(f.events.length,6);
  assert.equal(f.calls.filter(c=>c==='renew-attestation').length,6);assert(r.keys.every(k=>k.signed===false && !k.rotationRequiredSoon));
  const text=JSON.stringify(r);for(const e of f.config.applications)for(const k of e.keys)assert(!text.includes(k.key.d));
});
test('renewal config rejects other domains, arbitrary endpoints, extra credentials, duplicates, and incomplete quorum',()=>{
  const base=fixture().config;
  for(const change of [c=>{c.apiKey='secret';},c=>{c.tcpAddress='evil.test:4987';},c=>{c.applications[0].app.identity.domain='identity.example';},c=>{c.applications[0].keys.pop();},c=>{c.applications[1]=c.applications[0];},c=>{c.applications[0].keys[1]=c.applications[0].keys[0];},c=>{c.applications[0].keys[0].key.extra='secret';}]) {
    const c=structuredClone(base);change(c);assert.throws(()=>renewalConfig(c));
  }
});
test('renewal refuses wrong subjects, key bindings, invalid signatures, and insufficient remaining validity',async()=>{
  for(const change of [({claim})=>{if(claim)claim.subjectUserId='other';},({claim})=>{if(claim)claim.keyId='other';},({claim})=>{if(claim)claim.attestationExpiresAt='2026-10-20T01:00:00Z';},({claim})=>{if(claim)claim.attestedAt='2026-10-21T00:00:00Z';},({signed})=>{if(signed)signed.signatures[0].signature.fill(0);},({anchors})=>{if(anchors)anchors[0].revokedAt='2026-10-19T00:00:00Z';}]) {
    const f=fixture(change);await assert.rejects(f.run());assert.equal(f.events.length,0);
  }
});
test('renewal verifies the current public key set instead of trusting the renewal reply alone',async()=>{
  for(const edit of [({state})=>{if(state)state.subjectUserId='other';},({state})=>{if(state)state.keys=[];},({state})=>{if(state)state.keys[0].signatures[0].signature.fill(0);}]) {
    const f=fixture(edit);await assert.rejects(f.run());assert.equal(f.events.length,0);
  }
});
test('renewal reports rotation warning without trying to extend key lifetime',async()=>{
  const f=fixture(({claim})=>{if(claim)claim.keyExpiresAt='2026-10-22T00:00:00Z';});
  const r=await f.run();assert(r.keys.every(k=>k.rotationRequiredSoon));assert(!f.calls.includes('add-key'));
});
test('renewal refuses a currently revoked key even when the renewal reply has a valid domain signature',async()=>{
  const f=fixture(({state,entry,at})=>{
    if(!state)return;
    const target=entry.keys[0],r={...instance(entry.app),targetKeyId:target.keyId,targetFingerprint:hash(Buffer.from(target.key.x,'base64url')),revokedAt:at.toISOString()};
    const bytes=sdk.revocationPayload(r.subjectUserId,r.subjectDomain,r.applicationId,r.instanceId,r.targetKeyId,r.targetFingerprint,r.revokedAt);
    r.signatures=entry.keys.slice(1).map(k=>({signedByKeyId:k.keyId,signature:sign(null,bytes,createPrivateKey({key:k.key,format:'jwk'}))}));state.revocations=[r];
  });
  await assert.rejects(f.run(),/renewal-current-key/);assert.equal(f.events.length,0);
});
test('public renewal transport refuses session, RP, enrollment, and revocation operations before dialing',()=>{
  const {rpc}=renewalTransport(fixture().config);
  for(const [service,op] of [['Session','login-password'],['Rp','resolve-application-keys'],['ApplicationKeys','add-key'],['ApplicationKeys','revoke-key']])assert.throws(()=>rpc.call(service,op,new Uint8Array()),/renewal-operation/);
});
test('renewal CronJob has no Kubernetes token, roles, API route, controller mounts, or persistent storage',()=>{
  const all=renewalManifests('registry.catalystsquad.local/magi@sha256:'+'a'.repeat(64));
  assert.deepEqual(all.map(r=>r.kind),['ServiceAccount','NetworkPolicy','ConfigMap','CronJob']);
  const cron=all.find(r=>r.kind==='CronJob'),pod=cron.spec.jobTemplate.spec.template.spec;
  assert.equal(cron.spec.concurrencyPolicy,'Forbid');assert.equal(pod.automountServiceAccountToken,false);
  assert.deepEqual(pod.volumes.filter(v=>v.secret).map(v=>v.secret.secretName),['release-renewal']);
  assert.equal(pod.volumes.find(v=>v.name==='environment').configMap.name,'release-renewal-environment');
  assert(pod.containers[0].securityContext.readOnlyRootFilesystem);assert.equal(pod.containers[0].securityContext.allowPrivilegeEscalation,false);
  const policy=all[1].spec;assert.deepEqual(policy.ingress,[]);
  assert.deepEqual(policy.egress.flatMap(e=>e.ports.map(p=>p.port)),[53,53,4987]);
  assert(policy.egress.every(e=>e.to.every(t=>!t.ipBlock)));
  assert.throws(()=>renewalManifests('some-image:latest'));
});
