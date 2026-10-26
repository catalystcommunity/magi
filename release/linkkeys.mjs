import {readFileSync} from 'node:fs';
import {createHash, createPublicKey} from 'node:crypto';
import {need, application} from './policy.mjs';
const base = new URL('../.build/release-sdk/', import.meta.url);
const provenance = JSON.parse(readFileSync(new URL('provenance.json', base)));
need(provenance.pin === '03fcbd8e700715fb686bce2016136ecd8071f307', 'sdk-pin');
for (const [path, digest] of Object.entries(provenance.files)) {
  need(!path.startsWith('/') && !path.split('/').includes('..'), 'sdk-path');
  need(createHash('sha256').update(readFileSync(new URL(path, base))).digest('hex') === digest, 'sdk-content-changed');
}
export const sdk = await import(new URL('src/index.js', base));
export const codec = await import(new URL('generated/codec.gen.js', base));
export const tuple = await import(new URL('src/cborTuple.js', base));
export const carrier = await import(new URL('src/vendor/csilgen-transport/index.js', base));
const {parseLinkkeysApisTxt} = await import(new URL('src/dnsRecords.js', base));
export const call = async (rpc, service, operation, requestType, request, responseType) => codec[`from${responseType}Cbor`](await rpc.call(service, operation, codec[`to${requestType}Cbor`](request)));

// Keep raw evidence, not a usable-key verdict. Recheck signatures and current
// validity on every use. Reuse cannot add time to the RP's one-second budget.
export function keyResolver({fetchEvidence, domains, now = () => new Date(), capacity = 16}) {
  need(Number.isSafeInteger(capacity) && capacity > 0 && capacity <= 128, 'key-cache-capacity');
  const cached = new Map(), pending = new Map();
  let lastAt = -Infinity;
  return async (subject, keyId) => {
    application(subject); need(domains.includes(subject.identity.domain), 'untrusted-domain');
    const instance = {subjectUserId: subject.identity.accountId, subjectDomain: subject.identity.domain, applicationId: subject.applicationId, instanceId: subject.instanceId};
    const id = JSON.stringify(Object.values(instance)), started = +now();
    need(Number.isFinite(started), 'key-clock');
    if (started < lastAt) {cached.clear(); throw Error('key-clock-reversal');}
    lastAt = started;
    let evidence = cached.get(id);
    if (!evidence || started >= evidence.until) {
      cached.delete(id);
      let request = pending.get(id);
      if (!request) {
        need(pending.size < capacity, 'key-lookups-busy');
        request = Promise.resolve().then(() => fetchEvidence(subject, instance)).then(({anchors, response}) => ({anchors, response,
          until: Math.min(started + 1000, Date.parse(response.fetchedAt) + 1000, Date.parse(response.revocationsCheckedAt) + 1000)}));
        pending.set(id, request);
      }
      try {evidence = await request;}
      finally {if (pending.get(id) === request) pending.delete(id);}
    }
    const {anchors, response} = evidence, at = now();
    need(Number.isFinite(+at) && +at >= lastAt && +at >= started, 'key-clock-reversal'); lastAt = +at;
    need(Object.entries(instance).every(([k, v]) => response[k] === v) && ['fresh', 'refreshed'].includes(response.cacheStatus), 'key-subject-or-cache');
    for (const stamp of [response.fetchedAt, response.revocationsCheckedAt]) need(Number.isFinite(Date.parse(stamp)) && Date.parse(stamp) <= +at && +at - Date.parse(stamp) <= 5000, 'key-freshness');
    const verified = sdk.verifyApplicationKeySet(response.applicationKeys, response.applicationKeyRevocations, anchors, instance, at, 0);
    need(verified.rejected.length === 0, 'key-evidence');
    const k = sdk.keyForUse(verified, keyId, 'sign', 'ed25519');
    need(k && Date.parse(k.attestedAt) <= +at && Date.parse(k.keyCreatedAt) <= +at, 'key-validity');
    if (+at < evidence.until) {
      cached.delete(id); cached.set(id, evidence);
      while (cached.size > capacity) cached.delete(cached.keys().next().value);
    }
    return {publicKey: createPublicKey({key: {kty: 'OKP', crv: 'Ed25519', x: Buffer.from(k.publicKey).toString('base64url')}, format: 'jwk'}), createdAt: new Date(k.keyCreatedAt), expiresAt: new Date(k.keyExpiresAt)};
  };
}

export function linkkeys({rp, domains, app}) {
  application(app);
  need(rp.fingerprints?.length >= 2 && domains.length > 0, 'linkkeys-trust');
  const rpc = new sdk.PinnedRpcTransport({...rp, requestTimeoutMs: 8000});
  const dns = {async txtLookup(name) {
    for (const d of domains) {
      if (name === `_linkkeys.${d.domain}`) return [`v=lk1 ${d.fingerprints.map(f => `fp=${f}`).join(' ')}`];
      if (name === `_linkkeys_apis.${d.domain}`) return [`v=lk1 tcp=${d.tcpAddress} https=${d.browserHost}`];
    }
    throw Error('untrusted-domain');
  }};
  // Private addresses are allowed only for these operator-configured endpoints.
  const raw = new sdk.NodeTransport({connectTimeoutMs: 5000});
  const addresses = new Set(domains.map(d => parseLinkkeysApisTxt(`v=lk1 tcp=${d.tcpAddress}`).tcp));
  const socketTransport = {dial(address, options) {need(addresses.has(address), 'untrusted-endpoint'); return raw.dial(address, options);}};
  const resolveKey = keyResolver({domains: domains.map(d => d.domain), fetchEvidence: async (subject, instance) => {
    const anchors = await sdk.fetchDomainKeys(socketTransport, dns, subject.identity.domain, {signal: AbortSignal.timeout(8000)});
    const response = await call(rpc, 'Rp', 'resolve-application-keys', 'RpResolveApplicationKeysRequest', {...instance, maxCacheAgeSeconds: 1}, 'RpResolveApplicationKeysResponse');
    return {anchors, response};
  }});
  return {resolveKey, createLogin: options => new sdk.RegularRpClient({...options, rpDomain: app.identity.domain, rpServer: {...rp, requestTimeoutMs: 8000}, dnsResolver: dns, socketTransport, clockSkewMs: 0})};
}
