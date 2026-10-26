import {deployment as environmentConfig} from '../../config/deployment.mjs';
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { randomBytes, generateKeyPairSync, createHash, sign } from 'node:crypto';
import https from 'node:https';
import { codec, carrier, transport, call, tuple, keys } from './linkkeys.mjs';

export const image = environmentConfig.linkkeysImage;
const nodeImage = 'node@sha256:0353e48e0e8a993db87b720c242f54b207059d1bcc0106534896e8a11054c837';
export const digest = bytes => createHash('sha256').update(bytes).digest();
const docker = args => execFileSync('docker', args, {encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 60000}).trim();
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
export async function cluster() {
  const directory = mkdtempSync(join(tmpdir(), 'opal-w02-')); chmodSync(directory, 0o700);
  const name = `opal-w02-${randomBytes(5).toString('hex')}`;
  const label = `opal.w02=${name}`;
  const names = []; let networkCreated = false;
  async function close() {
    for (const n of names.reverse()) {
      if (docker(['inspect', '-f', '{{index .Config.Labels "opal.w02"}}', n]) !== name) throw Error('cleanup label mismatch');
      docker(['rm', '-f', '-v', n]);
    }
    if (networkCreated) {
      if (docker(['network', 'inspect', '-f', '{{index .Labels "opal.w02"}}', name]) !== name) throw Error('network label mismatch');
      docker(['network', 'rm', name]);
    }
  }
  try {
    docker(['network', 'create', '--internal', '--label', label, name]); networkCreated = true;
    writeFileSync(join(directory, 'records.json'), '{}', {mode: 0o600});
    const dns = `${name}-dns`;
    docker(['create', '--name', dns, '--label', label, '--network', name, '--read-only', '--cap-drop', 'ALL', '--user', `${process.getuid()}:${process.getgid()}`, '--memory', '128m', '--cpus', '1', '--pids-limit', '128', '--mount', `type=bind,src=${directory},dst=/fixture,readonly`, '--mount', `type=bind,src=${join(import.meta.dirname, 'dns.mjs')},dst=/dns.mjs,readonly`, nodeImage, 'node', '/dns.mjs']); names.push(dns);
    docker(['start', dns]);
    const ip = n => docker(['inspect', '-f', `{{(index .NetworkSettings.Networks "${name}").IPAddress}}`, n]);
    const records = {}, nodes = [];
    for (const side of ['primary', 'secondary']) {
      const n = `${name}-${side}`, domain = `${side}.opal.test`;
      const passphrase = randomBytes(24).toString('hex');
      docker(['create', '--name', n, '--label', label, '--network', name, '--dns', ip(dns), '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--user', '0', '--memory', '768m', '--cpus', '2', '--pids-limit', '256', '--tmpfs', '/tmp:rw,noexec,nosuid,size=128m', '-e', 'DATABASE_BACKEND=sqlite', '-e', 'DATABASE_URL=/tmp/linkkeys.sqlite', '-e', `DOMAIN_NAME=${domain}`, '-e', `DOMAIN_KEY_PASSPHRASE=${passphrase}`, '-e', 'HTTPS_PORT=8443', '-e', 'TCP_PORT=8444', '-e', 'ALLOW_INVALID_CERTS=true', '-e', 'LOG_LEVEL=error', image, 'sleep', 'infinity']); names.push(n);
      docker(['start', n]);
      docker(['exec', n, 'linkkeys', 'domain', 'init']);
      const service = docker(['exec', n, 'linkkeys', 'user', 'create', 'opal-service', 'Magi W02 service', '--api-key', '--relation', 'api_access']);
      const apiKey = service.match(/API key: (\S+)/)?.[1];
      const accountId = service.match(/User created: id=(\S+)/)?.[1];
      if (!apiKey || !accountId) throw Error('fixture bootstrap failed');
      const password = randomBytes(24).toString('hex');
      const human = docker(['exec', n, 'linkkeys', 'user', 'create', 'human', 'Synthetic W02 human', '--password', password]);
      const humanId = human.match(/User created: id=(\S+)/)?.[1];
      const listing = docker(['exec', n, 'linkkeys', 'domain', 'list-keys']);
      const fingerprints = listing.split('\n').filter(s => /\bsign\s+valid\b/.test(s)).map(s => s.trim().split(/\s+/).at(-1));
      // Older CLI releases call this state "active".
      if (!fingerprints.length) for (const s of listing.split('\n')) if (/\bsign\s+active\b/.test(s)) fingerprints.push(s.trim().split(/\s+/).at(-1));
      if (fingerprints.length !== 3) throw Error(`fixture signing key listing: ${listing}`);
      const tcpPort = 8444, httpsPort = 8443, host = ip(n);
      records[domain] = {ip: ip(n)};
      records[`_linkkeys.${domain}`] = {txt: `v=lk1 ${fingerprints.map(p => `fp=${p}`).join(' ')}`};
      records[`_linkkeys_apis.${domain}`] = {txt: `v=lk1 tcp=${domain}:8444 https=${domain}:8443`};
      nodes.push({name: n, domain, password, humanId, fingerprints, httpsPort, tcpPort, host,
        config: {tcpAddress: `${host}:${tcpPort}`, fingerprints, apiKey},
        application: {identity: {domain, accountId}, applicationId: 'opal-approval', instanceId: `${side}-stable`}});
    }
    writeFileSync(join(directory, 'records.json'), JSON.stringify(records), {mode: 0o600});
    for (const node of nodes) {
      const child = spawn('docker', ['exec', '-e', 'LINKKEYS_ALLOW_PRIVATE_PEERS=true', '-e', 'TCP_HANDSHAKE_RATE_PER_MINUTE=6000', '-e', 'TCP_HANDSHAKE_BURST=200', node.name, 'linkkeys', 'serve'], {stdio: ['ignore', 'pipe', 'pipe']});
      node.serverLogs = '';
      const collect = chunk => {node.serverLogs = (node.serverLogs + chunk.toString()).slice(-8192);};
      child.stdout.on('data', collect); child.stderr.on('data', collect);
      node.transport = transport(node.config);
      // Respect the released server's source bucket (2 API-key checks/s).
      const rawCall = node.transport.call.bind(node.transport); let next = 0;
      node.transport.call = async (...args) => {const wait = Math.max(0, next - Date.now()); next = Math.max(next, Date.now()) + 550; await delay(wait); return rawCall(...args);};
      let ready = false;
      for (let i = 0; i < 100; i++) {
        try { await browserRpc(node, 'Session', 'get-current', 'Empty', {}, 'SessionCurrentResponse'); }
        catch (e) { if (e.rpcStatus !== undefined) { ready = true; break; } }
        await delay(100);
      }
      if (!ready) throw Error('LinkKeys readiness timeout');
    }
    return {nodes, directory, close, stop: node => docker(['stop', '--time', '2', node.name]), start: node => {docker(['start', node.name]); docker(['exec', '-d', node.name, 'linkkeys', 'serve']);}};
  } catch (error) { await close(); throw error; }
}

// Test-only browser driver. The production SDK uses pinned TLS for backend RPC.
// These synthetic browser calls accept the isolated server's self-signed HTTPS
// certificate. They never use live accounts or a public network.
export async function browserRpc(node, service, op, type, body, responseType, cookie, origin = `https://${node.domain}:8443`) {
  const payload = type === 'Empty' ? Buffer.from([0xa0]) : codec[`to${type}Cbor`](body);
  const envelope = new carrier.RpcRequest(service, op, payload).withId(1).encode();
  return new Promise((resolve, reject) => {
    const req = https.request({host: node.host, port: node.httpsPort, path: '/csil/v1/rpc', method: 'POST', rejectUnauthorized: false, headers: {'content-type': 'application/cbor', host: `${node.domain}:8443`, origin, ...(cookie ? {cookie} : {})}, timeout: 8000}, res => {
      const chunks = []; let size = 0;
      res.on('data', c => {size += c.length; if (size > 1024 * 1024) req.destroy(Error('response limit')); else chunks.push(c);});
      res.on('end', () => {try {
        const result = carrier.RpcResponse.decode(Buffer.concat(chunks));
        if (result.status !== 0) {const e = Error(`LinkKeys rejected ${service}/${op}: ${result.status} ${result.error}`); e.rpcStatus = result.status; throw e;}
        resolve({body: codec[`from${responseType}Cbor`](result.payload), cookie: res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie});
      } catch(e) {reject(e);}});
    });
    req.on('error', reject); req.on('timeout', () => req.destroy(Error(`browser RPC timeout: ${service}/${op}`))); req.end(envelope);
  });
}
export async function passwordLogin(node) {
  return browserRpc(node, 'Session', 'login-password', 'SessionPasswordLoginRequest', {username: 'human', password: node.password}, 'SessionPasswordLoginResponse');
}
export async function enroll(node) {
  node.signers = [];
  const additions = [];
  for (let i = 0; i < 3; i++) {
    const pair = generateKeyPairSync('ed25519');
    const publicKey = Buffer.from(pair.publicKey.export({format: 'jwk'}).x, 'base64url');
    const challenge = await call(node.transport, 'ApplicationKeys', 'start-key-challenge', 'StartApplicationKeyChallengeRequest', {subjectUserId: node.application.identity.accountId, applicationId: node.application.applicationId, instanceId: node.application.instanceId, purpose: 'add', keyUsage: 'sign', algorithm: 'ed25519', publicKey}, 'StartApplicationKeyChallengeResponse');
    const keyId = `key-${i}`, now = new Date();
    const addition = codec.toApplicationKeyAdditionCbor({subjectUserId: node.application.identity.accountId, subjectDomain: node.domain, applicationId: node.application.applicationId, instanceId: node.application.instanceId, keyId, keyUsage: 'sign', algorithm: 'ed25519', publicKey, fingerprint: digest(publicKey).toString('hex'), requestedKeyLifetimeSeconds: 86400, challengeId: challenge.challengeId, challenge: challenge.challenge, requestedAt: now.toISOString(), expiresAt: new Date(+now + 60000).toISOString()});
    const input = tuple.encodeCborTuple([tuple.text('linkkeys-application-key-possession-v1alpha'), tuple.bytes(addition)]);
    additions.push({addition, signatures: [], possessionProof: sign(null, input, pair.privateKey)});
    node.signers.push({...pair, keyId});
  }
  return call(node.transport, 'Account', 'enroll-application-instance', 'EnrollApplicationInstanceRequest', {applicationId: node.application.applicationId, instanceId: node.application.instanceId, keys: additions}, 'EnrollApplicationInstanceResponse');
}
export async function revokeFirstKey(node) {
  const first = node.signers[0];
  const fingerprint = digest(Buffer.from(first.publicKey.export({format: 'jwk'}).x, 'base64url')).toString('hex');
  const revokedAt = new Date().toISOString();
  const a = node.application;
  const input = keys.revocationPayload(a.identity.accountId, a.identity.domain, a.applicationId, a.instanceId, first.keyId, fingerprint, revokedAt);
  const revocation = {subjectUserId: a.identity.accountId, subjectDomain: a.identity.domain, applicationId: a.applicationId, instanceId: a.instanceId, targetKeyId: first.keyId, targetFingerprint: fingerprint, revokedAt, signatures: node.signers.slice(1).map(s => ({signedByKeyId: s.keyId, signature: sign(null, input, s.privateKey)}))};
  return call(node.transport, 'ApplicationKeys', 'revoke-key', 'RevokeApplicationKeyRequest', {revocation}, 'RevokeApplicationKeyResponse');
}
export async function addShortKey(node) {
  const pair = generateKeyPairSync('ed25519'), keyId = 'short-expiry-test';
  const publicKey = Buffer.from(pair.publicKey.export({format: 'jwk'}).x, 'base64url'), a = node.application;
  const challenge = await call(node.transport, 'ApplicationKeys', 'start-key-challenge', 'StartApplicationKeyChallengeRequest', {subjectUserId: a.identity.accountId, applicationId: a.applicationId, instanceId: a.instanceId, purpose: 'add', keyUsage: 'sign', algorithm: 'ed25519', publicKey}, 'StartApplicationKeyChallengeResponse');
  const now = new Date();
  const addition = codec.toApplicationKeyAdditionCbor({subjectUserId: a.identity.accountId, subjectDomain: a.identity.domain, applicationId: a.applicationId, instanceId: a.instanceId, keyId, keyUsage: 'sign', algorithm: 'ed25519', publicKey, fingerprint: digest(publicKey).toString('hex'), requestedKeyLifetimeSeconds: 5, challengeId: challenge.challengeId, challenge: challenge.challenge, requestedAt: now.toISOString(), expiresAt: new Date(+now + 60000).toISOString()});
  const envelope = tag => tuple.encodeCborTuple([tuple.text(tag), tuple.bytes(addition)]);
  const result = await call(node.transport, 'ApplicationKeys', 'add-key', 'AddApplicationKeyRequest', {request: {addition, possessionProof: sign(null, envelope('linkkeys-application-key-possession-v1alpha'), pair.privateKey), signatures: node.signers.slice(0, 2).map(s => ({signedByKeyId: s.keyId, signature: sign(null, envelope('linkkeys-application-key-addition-v1alpha'), s.privateKey)}))}}, 'AddApplicationKeyResponse');
  return {...pair, keyId, attestation: codec.fromApplicationKeyAttestationCbor(result.attestation.attestation)};
}
