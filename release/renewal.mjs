import {deployment as environmentConfig} from '../config/deployment.mjs';
// A single bounded renewal run. No HTTP listener, RP token, or deployment API.
import {createPrivateKey, createPublicKey, createHash} from 'node:crypto';
import {setTimeout} from 'node:timers/promises';
import {sdk, codec, call} from './linkkeys.mjs';
import {application, need} from './policy.mjs';
import {renewalRequest} from '../deploy/release-renewal.mjs';

export const renewalEndpoint = (environmentConfig.linkkeysService + "." + environmentConfig.linkkeysNamespace + ".svc.cluster.local:4987");
const exact = (v, keys) => need(v && typeof v === 'object' && !Array.isArray(v) && Object.keys(v).sort().join(',') === keys.sort().join(','), 'renewal-config');
const rawPublic = key => Buffer.from(createPublicKey(createPrivateKey({key, format:'jwk'})).export({format:'jwk'}).x,'base64url');
export const instance = app => ({subjectUserId:app.identity.accountId, subjectDomain:app.identity.domain, applicationId:app.applicationId, instanceId:app.instanceId});
export function renewalConfig(value) {
  exact(value,['version','tcpAddress','fingerprints','applications']);
  need(value.version === 1 && value.tcpAddress === renewalEndpoint, 'renewal-config');
  need(Array.isArray(value.fingerprints) && value.fingerprints.length >= 2 && value.fingerprints.length <= 8 && new Set(value.fingerprints).size === value.fingerprints.length && value.fingerprints.every(f=>/^[a-f0-9]{64}$/.test(f)), 'renewal-config');
  need(Array.isArray(value.applications) && value.applications.length === 2, 'renewal-config');
  const names = new Set(), keys = new Set(), publicKeys = new Set(), owners = new Set();
  for (const entry of value.applications) {
    exact(entry,['app','keys']); exact(entry.app,['identity','applicationId','instanceId']); exact(entry.app.identity,['accountId','domain']); application(entry.app);
    need(entry.app.identity.domain === environmentConfig.domain && ['opal-release-approval','opal-release-controller'].includes(entry.app.applicationId) && entry.app.instanceId === environmentConfig.applicationInstance, 'renewal-config');
    names.add(entry.app.applicationId); owners.add(entry.app.identity.accountId);
    need(Array.isArray(entry.keys) && entry.keys.length === 3, 'renewal-config');
    for (const key of entry.keys) {
      exact(key,['keyId','key']); exact(key.key,['kty','crv','x','d']);
      need(typeof key.keyId === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(key.keyId) && key.key.kty === 'OKP' && key.key.crv === 'Ed25519', 'renewal-config');
      need([key.key.x,key.key.d].every(v=>typeof v==='string' && /^[A-Za-z0-9_-]{43}$/.test(v) && Buffer.from(v,'base64url').toString('base64url')===v), 'renewal-config');
      need(rawPublic(key.key).toString('base64url') === key.key.x, 'renewal-config');
      keys.add(key.keyId); publicKeys.add(key.key.x);
    }
  }
  need(names.size === 2 && owners.size === 1 && keys.size === 6 && publicKeys.size === 6, 'renewal-config');
  return value;
}

// Use the pinned SDK's low-level transport. auth=undefined omits the RPC auth
// field; these operations authenticate with the signed fresh challenge.
export function renewalTransport(config) {
  renewalConfig(config);
  const endpoint = {tcpAddr:config.tcpAddress,fingerprints:config.fingerprints};
  const raw = new sdk.NodeTransport({connectTimeoutMs:5000});
  const transport = {dial(address, options) {need(address === config.tcpAddress,'renewal-endpoint');return raw.dial(address,options);}};
  const rpc = {call(service,operation,bytes) {
    need(service === 'ApplicationKeys' && ['start-key-challenge','renew-attestation','get-application-keys'].includes(operation),'renewal-operation');
    return sdk.call(transport,endpoint,service,operation,bytes,undefined,{timeoutMs:8000});
  }};
  const dns = {async txtLookup(name) {
    if (name === ("_" + environmentConfig.linkkeysHost)) return [`v=lk1 ${config.fingerprints.map(f=>`fp=${f}`).join(' ')}`];
    if (name === ("_linkkeys_apis." + environmentConfig.domain)) return [`v=lk1 tcp=${config.tcpAddress}`];
    throw Error('renewal-endpoint');
  }};
  return {rpc, anchors:()=>sdk.fetchDomainKeys(transport,dns,environmentConfig.domain,{timeoutMs:8000,signal:AbortSignal.timeout(16000)})};
}

function checkAttestation(signed, anchors, entry, key, now) {
  const a = sdk.verifyAttestationSignature(signed,anchors,environmentConfig.domain,now);
  need(a && Object.entries(instance(entry.app)).every(([k,v])=>a[k] === v) && a.keyId === key.keyId && a.keyUsage === 'sign' && a.algorithm === 'ed25519' && Buffer.from(a.publicKey).equals(rawPublic(key.key)), 'renewal-response-binding');
  const stamps = ['keyCreatedAt','keyExpiresAt','attestedAt','attestationExpiresAt'].map(k=>Date.parse(a[k]));
  need(stamps.every(Number.isFinite) && stamps[0]<=+now && stamps[2]<=+now && stamps[1]>+now && stamps[3]>+now+4*3600000, 'renewal-validity');
  return a;
}
export async function renewApplications(config, {rpc, anchors, now=()=>new Date(), pause=()=>setTimeout(1100), emit=()=>{}} = renewalTransport(config)) {
  renewalConfig(config);
  let last = -Infinity;
  const clock = () => {const at=now();need(Number.isFinite(+at) && +at>=last,'renewal-clock');last=+at;return at;};
  const startedAt = clock().toISOString(), receipts=[];
  for (const entry of config.applications) {
    const trust = await anchors(), subject = instance(entry.app), renewed=[];
    for (const key of entry.keys) {
      const publicKey = rawPublic(key.key);
      const challenge = await call(rpc,'ApplicationKeys','start-key-challenge','StartApplicationKeyChallengeRequest',{subjectUserId:subject.subjectUserId,applicationId:subject.applicationId,instanceId:subject.instanceId,purpose:'renew',keyUsage:'sign',algorithm:'ed25519',publicKey},'StartApplicationKeyChallengeResponse');
      const response = await call(rpc,'ApplicationKeys','renew-attestation','RenewApplicationKeyAttestationRequest',{request:renewalRequest(entry,key.keyId,challenge,clock())},'RenewApplicationKeyAttestationResponse');
      const a = checkAttestation(response.attestation,trust,entry,key,clock());
      const receipt = {...subject,keyId:key.keyId,signed:response.signed,keyExpiresAt:a.keyExpiresAt,attestationExpiresAt:a.attestationExpiresAt,
        attestationSha256:createHash('sha256').update(response.attestation.attestation).digest('hex'),rotationRequiredSoon:Date.parse(a.keyExpiresAt)-+clock()<7*86400000};
      renewed.push({key,receipt}); await pause();
    }
    // Do not treat a signed reply alone as proof of current, unrevoked status.
    const {subjectDomain:_,...request} = subject;
    const state = await call(rpc,'ApplicationKeys','get-application-keys','GetApplicationKeysRequest',request,'GetApplicationKeysResponse');
    need(Object.entries(subject).every(([k,v])=>state[k]===v),'renewal-response-binding');
    const at=clock(), verified=sdk.verifyApplicationKeySet(state.keys,state.revocations,await anchors(),subject,at,0);
    need(verified.rejected.length === 0,'renewal-key-evidence');
    for (const {key,receipt} of renewed) {
      const current=sdk.keyForUse(verified,key.keyId,'sign','ed25519');
      need(current && Buffer.from(current.publicKey).equals(rawPublic(key.key)) && current.attestationExpiresAt === receipt.attestationExpiresAt,'renewal-current-key');
      receipts.push(receipt);emit({event:'renewal-key-verified',...receipt});
    }
  }
  return {event:'renewal-complete',startedAt,completedAt:clock().toISOString(),keys:receipts};
}
