import {requireDeployment} from '../config/deployment.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
// Enroll two distinct Magi applications under a test account. Never sign in as
// the human approver. Keep all private values in the ignored setup directory.
import {readFileSync, writeFileSync, mkdirSync, existsSync, renameSync, lstatSync} from 'node:fs';
import {join, resolve} from 'node:path';
import {homedir} from 'node:os';
import {resolveTxt} from 'node:dns/promises';
import {generateKeyPairSync, createPrivateKey, createHash, sign, randomBytes} from 'node:crypto';
import {readPrivate} from './qa-bootstrap.mjs';
import {sdk, codec, carrier, tuple, call} from '../release/linkkeys.mjs';
import {renewalRequest} from './release-renewal.mjs';
import {setTimeout} from 'node:timers/promises';
import {operatorPolicy} from '../release/configuration.mjs';
const root = resolve(import.meta.dirname, '..'), dir = environmentConfig.releaseProfileDir, path = join(dir, 'enrollment.json');
const origin = ("https://" + environmentConfig.linkkeysHost);
const save = state => {const temp = join(dir, `setup-${randomBytes(8).toString('hex')}.json`); writeFileSync(temp, JSON.stringify(state, null, 2) + '\n', {mode: 0o600, flag: 'wx'}); renameSync(temp, path);};
async function main() {
  requireDeployment();
  if (process.argv.length !== 3 || !['--enroll', '--render', '--renew'].includes(process.argv[2])) throw Error('Use --enroll, --render, or --renew');
  const policy = process.argv[2] === '--renew' ? undefined : operatorPolicy();
  mkdirSync(dir, {recursive: true, mode: 0o700}); const st = lstatSync(dir);
  if (!st.isDirectory() || st.isSymbolicLink() || st.mode & 0o077) throw Error('private-directory');
  const base = JSON.parse(readPrivate((environmentConfig.qaProfileDir + "/bootstrap.json")));
  let state = existsSync(path) ? JSON.parse(readPrivate(path)) : {applications: {}};
  const rpc = new sdk.PinnedRpcTransport({tcpAddress: (environmentConfig.linkkeysHost + ":4987"), fingerprints: base.fingerprints, apiKey: base.rp.apiKey, requestTimeoutMs: 8000});
  let cookie;
  const browser = async (service, op, type, body, responseType) => {
    const r = await fetch(`${origin}/csil/v1/rpc`, {method: 'POST', redirect: 'error', signal: AbortSignal.timeout(10000), headers: {'content-type': 'application/cbor', origin, ...(cookie ? {cookie} : {})}, body: new carrier.RpcRequest(service, op, codec[`to${type}Cbor`](body)).withId(1).encode()});
    if (!r.ok) throw Error('test-account-operation');
    const result = carrier.RpcResponse.decode(new Uint8Array(await r.arrayBuffer()));
    if (result.status !== 0) throw Error('test-account-operation');
    const cookies = r.headers.getSetCookie(); if (cookies.length) cookie = cookies.map(c => c.split(';')[0]).join('; ');
    return codec[`from${responseType}Cbor`](result.payload);
  };
  if (process.argv[2] === '--enroll') {
    const password = readPrivate(environmentConfig.loginCredentials).split('\n').find(l => l.startsWith(environmentConfig.testUsername+': '))?.slice(environmentConfig.testUsername.length+2);
    if (!password) throw Error('test-login-unavailable');
    try {
      const login = await browser('Session', 'login-password', 'SessionPasswordLoginRequest', {username: environmentConfig.testUsername, password}, 'SessionPasswordLoginResponse');
      const identity = {domain: environmentConfig.domain, accountId: login.session.user.id};
      for (const name of ['approval', 'controller']) {
        const entry = state.applications[name] ??= {app: {identity, applicationId: `opal-release-${name}`, instanceId: environmentConfig.applicationInstance}, keys: Array.from({length: 3}, (_, i) => ({keyId: `release-${name}-${i}`, key: generateKeyPairSync('ed25519').privateKey.export({format: 'jwk'})}))};
        save(state); if (entry.enrolled) continue;
        if (entry.attempted) throw Error('An earlier enrollment has an unknown outcome. Inspect the enrolled keys before retry.');
        const additions = [];
        for (const k of entry.keys) {
          const publicKey = Buffer.from(k.key.x, 'base64url');
          const ch = await call(rpc, 'ApplicationKeys', 'start-key-challenge', 'StartApplicationKeyChallengeRequest', {subjectUserId: identity.accountId, applicationId: entry.app.applicationId, instanceId: entry.app.instanceId, purpose: 'add', keyUsage: 'sign', algorithm: 'ed25519', publicKey}, 'StartApplicationKeyChallengeResponse');
          const at = new Date();
          const addition = codec.toApplicationKeyAdditionCbor({subjectUserId: identity.accountId, subjectDomain: identity.domain, applicationId: entry.app.applicationId, instanceId: entry.app.instanceId, keyId: k.keyId, keyUsage: 'sign', algorithm: 'ed25519', publicKey, fingerprint: createHash('sha256').update(publicKey).digest('hex'), requestedKeyLifetimeSeconds: 30 * 86400, challengeId: ch.challengeId, challenge: ch.challenge, requestedAt: at.toISOString(), expiresAt: new Date(+at + 60000).toISOString()});
          additions.push({addition, signatures: [], possessionProof: sign(null, tuple.encodeCborTuple([tuple.text('linkkeys-application-key-possession-v1alpha'), tuple.bytes(addition)]), createPrivateKey({key: k.key, format: 'jwk'}))});
        }
        entry.attempted = new Date().toISOString(); save(state);
        entry.attestations = await browser('Account', 'enroll-application-instance', 'EnrollApplicationInstanceRequest', {applicationId: entry.app.applicationId, instanceId: entry.app.instanceId, keys: additions}, 'EnrollApplicationInstanceResponse');
        entry.enrolled = true; save(state);
        console.log(`Enrolled ${entry.app.applicationId} as a test-account application`);
      }
    } finally {if (cookie) await browser('Session', 'logout', 'EmptyRequest', {}, 'SessionLogoutResponse').catch(() => {});}
  }
  if (!state.applications.approval?.enrolled || !state.applications.controller?.enrolled) throw Error('Enroll both applications first');
  if (process.argv[2] === '--renew') {
    for (const name of ['approval', 'controller']) {
      const entry = state.applications[name];
      for (const key of entry.keys) {
        const publicKey = Buffer.from(key.key.x, 'base64url');
        const challenge = await call(rpc, 'ApplicationKeys', 'start-key-challenge', 'StartApplicationKeyChallengeRequest', {subjectUserId: entry.app.identity.accountId, applicationId: entry.app.applicationId, instanceId: entry.app.instanceId, purpose: 'renew', keyUsage: 'sign', algorithm: 'ed25519', publicKey}, 'StartApplicationKeyChallengeResponse');
        const result = await call(rpc, 'ApplicationKeys', 'renew-attestation', 'RenewApplicationKeyAttestationRequest', {request: renewalRequest(entry, key.keyId, challenge)}, 'RenewApplicationKeyAttestationResponse');
        const a = codec.fromApplicationKeyAttestationCbor(result.attestation.attestation);
        if (a.subjectUserId !== entry.app.identity.accountId || a.subjectDomain !== entry.app.identity.domain || a.applicationId !== entry.app.applicationId || a.instanceId !== entry.app.instanceId || a.keyId !== key.keyId || !Buffer.from(a.publicKey).equals(publicKey)) throw Error('renewal-response-binding');
        (entry.renewed ??= {})[key.keyId] = result.attestation; save(state);
        console.log(JSON.stringify({application: entry.app.applicationId, keyId: key.keyId, signed: result.signed, keyExpiresAt: a.keyExpiresAt, attestationExpiresAt: a.attestationExpiresAt}));
        await setTimeout(1100);
      }
    }
    return; // Do not rewrite runtime profiles or replace any key.
  }
  const domains = [];
  for (const domain of new Set([environmentConfig.domain, policy.approver.domain])) {
    const records = (await resolveTxt(`_linkkeys.${domain}`)).map(p => p.join(''));
    const fingerprints = records.filter(v => v.startsWith('v=lk1 ')).flatMap(v => [...v.matchAll(/(?:^| )fp=([0-9a-f]{64})(?= |$)/g)].map(m => m[1]));
    const apis = (await resolveTxt(`_linkkeys_apis.${domain}`)).map(p => p.join('')).find(v => v.startsWith('v=lk1 '));
    const tcpAddress = apis?.match(/(?:^| )tcp=([^ ]+)/)?.[1], browserHost = apis?.match(/(?:^| )https=([^ ]+)/)?.[1];
    if (fingerprints.length < 2 || !tcpAddress || !browserHost) throw Error('Domain trust discovery incomplete');
    domains.push({domain, fingerprints, tcpAddress, browserHost});
  }
  const app = state.applications.approval.app, controller = state.applications.controller.app;
  const rp = {tcpAddress: (environmentConfig.linkkeysHost + ":4987"), fingerprints: base.fingerprints, apiKey: base.rp.apiKey};
  const approval = {app, controller, origin: ("https://" + environmentConfig.approvalHost), rp, domains, signer: state.applications.approval.keys[0], path: '/data/approval.sqlite', host: '0.0.0.0', port: 8098};
  const operator = {app: controller, recorder: app, controller, rp, domains, signer: state.applications.controller.keys[0], path: join(dir, 'controller.sqlite'), kubeconfig: environmentConfig.kubeconfig, approvalEndpoint: ("https://" + environmentConfig.approvalHost + "/csil/v1/rpc")};
  for (const [name, value] of [['approval.json', approval], ['controller.json', operator]]) writeFileSync(join(dir, name), JSON.stringify({...value, policy}, null, 2) + '\n', {mode: 0o600});
  console.log('Private approval and controller profiles prepared. No human approval was issued.');
}
main().catch(e => {console.error(e.message.startsWith('An earlier') ? e.message : 'Release setup failed. Inspect the private state before retry; no credentials were printed.'); process.exitCode = 1;});
