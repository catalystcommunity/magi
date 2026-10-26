import test from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';
import { randomBytes, randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { cluster, passwordLogin, browserRpc, enroll, revokeFirstKey, addShortKey } from './harness.mjs';
import { sdk, socket, rpc, carrier } from './linkkeys.mjs';
import { gateway } from './http.mjs';
import { ApprovalService, Receiver, resolver, identityKey, applicationKey, signed } from './approval.mjs';
import * as wire from './generated/typescript/codec.gen.ts';

test('two independent LinkKeys servers and durable Magi approval', {timeout: 240000}, async t => {
  const fixture = await cluster(); t.after(() => fixture.close());
  const [primary, secondary] = fixture.nodes;
  const dns = {txtLookup: async name => {
    const node = fixture.nodes.find(n => name === `_linkkeys.${n.domain}` || name === `_linkkeys_apis.${n.domain}`);
    if (!node) throw Error('test DNS refuses unknown domain');
    return [name.startsWith('_linkkeys_apis.') ? `v=lk1 tcp=${node.host}:8444 https=${node.domain}:8443` : `v=lk1 ${node.fingerprints.map(f => `fp=${f}`).join(' ')}`];
  }};
  const sock = new socket.NodeTransport({policy: 'permissive'});
  const anchors = new Map();
  for (const n of fixture.nodes) {
    await enroll(n);
    anchors.set(n.domain, await rpc.fetchDomainKeys(sock, dns, n.domain));
  }
  function service(n, suffix = '') {
    const s = new ApprovalService({path: join(fixture.directory, `${n.domain}${suffix}.sqlite`), application: n.application, origin: `https://${n.domain}`, rp: n.config, dns, socketTransport: sock, signer: n.signers[0]});
    return s;
  }
  const services = fixture.nodes.map(n => service(n));
  t.after(() => services.forEach(s => s.close()));
  const [a,b] = services;
  async function login(service, humanNode, binding = randomBytes(32).toString('hex')) {
    const pending = await service.beginLogin(`human@${humanNode.domain}`, binding);
    const browser = await passwordLogin(humanNode);
    const signedRequest = new URL(pending.redirectUrl).searchParams.get('signed_request');
    try { await browserRpc(humanNode, 'BrowserAuthorization', 'inspect', 'BrowserAuthorizationInspectRequest', {signedRequest}, 'BrowserAuthorizationInspectResponse', browser.cookie); }
    catch(e) { for (const n of fixture.nodes) console.error(n.serverLogs.split('\n').filter(line => /ERROR|failed/.test(line)).join('\n').replace(/https:\/\/\S+/g, '[test URL]')); throw e; }
    const finished = await browserRpc(humanNode, 'BrowserAuthorization', 'complete', 'BrowserAuthorizationCompleteRequest', {signedRequest, authorizedClaims: [], claimTypesToSet: [], claimValuesToSet: []}, 'BrowserAuthorizationCompleteResponse', browser.cookie);
    const callback = new URL(finished.body.redirectUrl);
    return {state: pending.state, encryptedToken: callback.searchParams.get('encrypted_token'), binding};
  }
  let authA, authB;
  await t.test('real cross-domain login and canonical human attribution in both directions', async () => {
    const l = await login(a, secondary);
    authA = {...await a.finishLogin(l.state, l.encryptedToken, l.binding), origin: a.origin};
    assert.deepEqual(authA.human, {domain: secondary.domain, accountId: secondary.humanId});
    const r = await login(b, primary);
    authB = {...await b.finishLogin(r.state, r.encryptedToken, r.binding), origin: b.origin};
    assert.deepEqual(authB.human, {domain: primary.domain, accountId: primary.humanId});
    await assert.rejects(a.finishLogin(l.state, l.encryptedToken, l.binding), /missing-state/);
  });
  await t.test('login callback from a different browser is rejected and consumed', async () => {
    const l = await login(a, secondary);
    await assert.rejects(a.finishLogin(l.state, l.encryptedToken, 'wrong-browser'), /session-mismatch/);
    await assert.rejects(a.finishLogin(l.state, l.encryptedToken, l.binding), /missing-state/);
  });
  await t.test('wrong password and cross-origin LinkKeys login are rejected', async () => {
    await assert.rejects(browserRpc(primary, 'Session', 'login-password', 'SessionPasswordLoginRequest', {username: 'human', password: 'not-the-fixture-password'}, 'SessionPasswordLoginResponse'));
    await assert.rejects(browserRpc(primary, 'Session', 'login-password', 'SessionPasswordLoginRequest', {username: 'human', password: primary.password}, 'SessionPasswordLoginResponse', undefined, 'https://attacker.opal.test'));
  });
  function terms(auth, delegate, audience) {
    return {version: 1, allowanceId: randomUUID(), issuer: auth.human, delegate, audience, resource: 'calendar/private', action: 'calendar.update', purpose: 'test exact human approval', notBefore: new Date(Date.now() - 1000)};
  }
  const termsA = terms(authA, secondary.application, secondary.domain), bytes = wire.toAllowanceCbor(termsA);
  let review, approval;
  await t.test('exact review creates one approval and a transactional outbox record', () => {
    review = a.review(authA, bytes);
    assert.deepEqual(review.bytes, Buffer.from(bytes));
    approval = a.approve(authA, review.id, review.digest, 'approve-1');
    assert.deepEqual(a.approve(authA, review.id, review.digest, 'approve-1'), approval);
    assert.equal(a.db.prepare('SELECT count(*) AS n FROM approvals').get().n, 1);
    assert.equal(a.db.prepare('SELECT count(*) AS n FROM outbox').get().n, 1);
  });
  const trust = new Map([[applicationKey(primary.application), new Set([secondary.domain])]]);
  const owners = new Map([['calendar/private', identityKey(authA.human)]]);
  const freshAnchors = domain => {assert(anchors.has(domain)); return rpc.fetchDomainKeys(sock, dns, domain);};
  const r = new Receiver({path: join(fixture.directory, 'receiver.sqlite'), audience: secondary.domain, trustedServices: trust, owners, actions: new Set(['calendar.update']), resolveKey: resolver(secondary.transport, freshAnchors)});
  t.after(() => r.close());
  await t.test('real RP resolves remote application keys before receiver accepts approval', async () => {
    await a.publish(packet => r.accept(packet));
    assert.equal(r.db.prepare('SELECT count(*) AS n FROM accepted').get().n, 1);
  });
  function request(overrides = {}, signer = secondary.signers[0]) {
    return wire.toSignedUseCbor(signed('use', wire.toUseCbor({version: 1, approvalId: approval.approvalId, delegate: secondary.application, audience: secondary.domain, resource: 'calendar/private', action: 'calendar.update', issuedAt: new Date(), nonce: randomBytes(32), ...overrides}), signer));
  }
  const status = (...args) => a.status(...args);
  await t.test('fresh signed request and status permit use and reject replay', async () => {
    const req = request();
    assert.equal((await r.authorize(primary.application, req, status)).decision, 'permit');
    await assert.rejects(r.authorize(primary.application, req, status), /request-replay/);
  });
  await t.test('Magi rejects missing session, wrong origin, and wrong CSRF token', () => {
    for (const auth of [{...authA, session: 'no-session'}, {...authA, origin: 'https://attacker.opal.test'}, {...authA, csrf: 'wrong-token'}]) {
      assert.throws(() => a.review(auth, bytes), /session|csrf/);
      assert.throws(() => a.approve(auth, review.id, review.digest, 'forged'), /session|csrf/);
    }
  });
  await t.test('a review cannot be used by another authenticated human', async () => {
    const l = await login(a, primary);
    const other = {...await a.finishLogin(l.state, l.encryptedToken, l.binding), origin: a.origin};
    assert.throws(() => a.approve(other, review.id, review.digest, 'stolen'), /review-binding/);
    assert.throws(() => a.withdraw(other, approval.approvalId), /withdrawal-owner/);
    assert.throws(() => a.review(authA, wire.toAllowanceCbor({...termsA, issuer: authB.human})), /issuer-not-human/);
  });
  await t.test('changed bytes need another review and cannot reuse the command key', () => {
    assert.throws(() => a.approve(authA, review.id, randomBytes(32), 'altered-digest'), /review-binding/);
    const changed = a.review(authA, wire.toAllowanceCbor({...termsA, purpose: 'different terms'}));
    assert.throws(() => a.approve(authA, changed.id, changed.digest, 'approve-1'), /idempotency-conflict/);
    assert.throws(() => a.approve(authA, review.id, review.digest, 'different-command'), /allowance-already-approved/);
  });
  await t.test('unknown fields, invalid version, and trailing bytes are rejected', () => {
    const fields = wire.decode(bytes); fields.set('extra', 'not displayed');
    for (const bad of [wire.encodeValue(fields), Buffer.concat([bytes, Buffer.from([0])]), wire.toAllowanceCbor({...termsA, version: 2})]) assert.throws(() => a.review(authA, bad));
  });
  await t.test('expired review and expired authenticated session are rejected', () => {
    const fresh = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    const before = a.now;
    try {
      a.now = () => new Date(Date.now() + 130000);
      assert.throws(() => a.approve(authA, fresh.id, fresh.digest, 'old-review'), /review-expired/);
      a.now = () => new Date(Date.now() + 310000);
      assert.throws(() => a.review(authA, bytes), /session/);
    } finally {a.now = before;}
  });
  for (const [name, change] of [['audience', {audience: primary.domain}], ['resource', {resource: 'calendar/other'}], ['action', {action: 'calendar.delete'}], ['delegate', {delegate: primary.application}]]) {
    await t.test(`receiver rejects wrong ${name}`, () => assert.rejects(r.authorize(primary.application, request(change), status), /request-scope/));
  }
  await t.test('receiver applies current local ownership policy', async () => {
    owners.delete('calendar/private');
    try {await assert.rejects(r.authorize(primary.application, request(), status), /receiver-policy/);} finally {owners.set('calendar/private', identityKey(authA.human));}
  });
  await t.test('receiver requires explicit trust in the approval service', async () => {
    trust.clear();
    try {await assert.rejects(r.authorize(primary.application, request(), status), /untrusted-approval-service/);} finally {trust.set(applicationKey(primary.application), new Set([secondary.domain]));}
  });
  await t.test('receiver can deny an approved action by current policy', async () => {
    r.actions.clear();
    try {await assert.rejects(r.authorize(primary.application, request(), status), /receiver-policy/);} finally {r.actions.add('calendar.update');}
  });
  await t.test('receiver rejects changed signature and an old request', async () => {
    const p = wire.fromSignedUseCbor(request()); p.signature[0] ^= 1;
    await assert.rejects(r.authorize(primary.application, wire.toSignedUseCbor(p), status), /signature/);
    await assert.rejects(r.authorize(primary.application, request({issuedAt: new Date(Date.now() - 61000)}), status), /request-time-or-nonce/);
  });
  await t.test('status cannot be replayed for another challenge or approval body', async () => {
    await assert.rejects(r.authorize(primary.application, request(), (id, audience) => a.status(id, audience, randomBytes(32))), /status-binding/);
  });
  await t.test('unavailable approval authority denies use', async () => {
    await assert.rejects(r.authorize(primary.application, request(), () => {throw Error('authority-unavailable');}), /authority-unavailable/);
  });
  await t.test('lost delivery acknowledgement replays the same signed packet once', async () => {
    const fresh = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    const record = a.approve(authA, fresh.id, fresh.digest, 'lost-ack');
    let captured;
    await assert.rejects(a.publish(async packet => {captured = Buffer.from(packet); await r.accept(packet); throw Error('lost-ack');}), /lost-ack/);
    assert.equal(a.db.prepare('SELECT delivered FROM outbox WHERE approval_id=?').get(record.approvalId).delivered, 0);
    await a.publish(async packet => {assert.deepEqual(Buffer.from(packet), captured); await r.accept(packet);});
    assert.equal(r.db.prepare('SELECT count(*) AS n FROM accepted WHERE id=?').get(record.approvalId).n, 1);
  });
  function child(fresh, command, crash) {
    const config = {path: join(fixture.directory, `${primary.domain}.sqlite`), application: primary.application, origin: a.origin, rp: primary.config};
    return new Promise((resolve, reject) => {
      const p = spawn(process.execPath, [join(import.meta.dirname, 'crash-child.mjs')], {stdio: ['pipe', 'pipe', 'pipe']});
      let out = '', error = ''; p.stdout.on('data', c => out += c); p.stderr.on('data', c => error += c);
      p.on('error', reject); p.on('exit', (code, signal) => resolve({code, signal, out, error}));
      p.stdin.end(JSON.stringify({config, auth: authA, review: fresh.id, digest: fresh.digest.toString('hex'), command, crash}));
    });
  }
  await t.test('SIGKILL before commit leaves neither approval nor outbox', async () => {
    const fresh = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    const result = await child(fresh, 'crash-before', 'before-commit'); assert.equal(result.signal, 'SIGKILL', result.error);
    assert.equal(a.db.prepare('SELECT count(*) AS n FROM approvals WHERE command_key=?').get('crash-before').n, 0);
    a.approve(authA, fresh.id, fresh.digest, 'crash-before');
  });
  await t.test('SIGKILL after commit retains exact approval and pending publication', async () => {
    const fresh = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    const result = await child(fresh, 'crash-after', 'after-commit'); assert.equal(result.signal, 'SIGKILL', result.error);
    const record = a.approve(authA, fresh.id, fresh.digest, 'crash-after');
    assert.equal(a.db.prepare('SELECT delivered FROM outbox WHERE approval_id=?').get(record.approvalId).delivered, 0);
    await a.publish(packet => r.accept(packet));
  });
  await t.test('independent processes return one approval for concurrent retries', async () => {
    const fresh = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    const results = await Promise.all(Array.from({length: 6}, () => child(fresh, 'concurrent')));
    for (const value of results) assert.equal(value.code, 0, value.error);
    assert.equal(new Set(results.map(v => v.out)).size, 1);
    assert.equal(a.db.prepare('SELECT count(*) AS n FROM approvals WHERE command_key=?').get('concurrent').n, 1);
  });
  await t.test('indefinite allowance can be withdrawn only by its approving human', async () => {
    assert.equal(termsA.expiresAt, undefined);
    assert.throws(() => a.withdraw({...authA, session: authB.session}, approval.approvalId), /session/);
    a.withdraw(authA, approval.approvalId); a.withdraw(authA, approval.approvalId);
    await assert.rejects(r.authorize(primary.application, request(), status), /withdrawn-or-stale/);
  });
  await t.test('CSIL HTTP login, review, approval, publication, status, and withdrawal', async () => {
    const httpA = await gateway({approval: a, resolveKey: resolver(primary.transport, freshAnchors), statusRequesters: new Map([[secondary.domain, applicationKey(secondary.application)]])});
    const httpR = await gateway({receiver: r});
    try {
      async function post(endpoint, service, op, type, body, cookie, csrf, origin = a.origin) {
        const response = await fetch(endpoint.url + '/csil/v1/rpc', {method: 'POST', headers: {'content-type': 'application/cbor', origin, ...(cookie ? {cookie} : {}), ...(csrf ? {'x-opal-csrf': csrf} : {})}, body: new carrier.RpcRequest(service, op, wire[`to${type}Cbor`](body)).withId(7).encode()});
        const envelope = carrier.RpcResponse.decode(new Uint8Array(await response.arrayBuffer()));
        return {response, envelope};
      }
      const started = await post(httpA, 'ApprovalService', 'begin-login', 'LoginStart', {identity: `human@${secondary.domain}`});
      assert.equal(started.envelope.status, 0);
      assert.match(started.response.headers.get('set-cookie'), /Secure; HttpOnly; SameSite=Lax/);
      const loginCookie = started.response.headers.get('set-cookie').split(';')[0];
      const redirect = new URL(wire.fromLoginStartedCbor(started.envelope.payload).redirectUrl);
      const browser = await passwordLogin(secondary);
      const finished = await browserRpc(secondary, 'BrowserAuthorization', 'complete', 'BrowserAuthorizationCompleteRequest', {signedRequest: redirect.searchParams.get('signed_request'), authorizedClaims: [], claimTypesToSet: [], claimValuesToSet: []}, 'BrowserAuthorizationCompleteResponse', browser.cookie);
      const callback = new URL(finished.body.redirectUrl);
      const response = await fetch(httpA.url + callback.pathname + callback.search, {headers: {cookie: loginCookie}});
      const completed = carrier.RpcResponse.decode(new Uint8Array(await response.arrayBuffer())); assert.equal(completed.status, 0);
      const s = wire.fromLoginSessionCbor(completed.payload);
      assert.deepEqual(s.human, authA.human);
      const sessionCookie = response.headers.getSetCookie().find(v => v.startsWith('__Host-opal-session=')).split(';')[0];
      const changed = wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()});
      const reviewed = await post(httpA, 'ApprovalService', 'review', 'ReviewRequest', {allowance: changed}, sessionCookie, s.csrf);
      assert.equal(reviewed.envelope.status, 0);
      const review = wire.fromReviewResponseCbor(reviewed.envelope.payload);
      const input = {reviewId: review.reviewId, digest: review.digest, commandKey: 'http-approval'};
      const forged = await post(httpA, 'ApprovalService', 'approve', 'ApproveRequest', input, sessionCookie, 'bad-token');
      assert.equal(forged.response.status, 403);
      const approved = await post(httpA, 'ApprovalService', 'approve', 'ApproveRequest', input, sessionCookie, s.csrf);
      assert.equal(approved.envelope.status, 0);
      const a2 = wire.fromApprovalCbor(approved.envelope.payload);
      await a.publish(async packet => {
        const response = await post(httpR, 'ApprovalReceiver', 'accept', 'SignedApproval', wire.fromSignedApprovalCbor(packet));
        assert.equal(response.envelope.status, 0);
      });
      const remoteStatus = async (approvalId, audience, nonce) => {
        const query = wire.toStatusQueryCbor({version: 1, requester: secondary.application, approvalId, audience, nonce, issuedAt: new Date()});
        const response = await post(httpA, 'ApprovalService', 'status', 'SignedStatusQuery', signed('status-query', query, secondary.signers[0]));
        assert.equal(response.envelope.status, 0); return response.envelope.payload;
      };
      assert.equal((await r.authorize(primary.application, request({approvalId: a2.approvalId}), remoteStatus)).decision, 'permit');
      const withdrawal = await post(httpA, 'ApprovalService', 'withdraw', 'WithdrawRequest', {approvalId: a2.approvalId}, sessionCookie, s.csrf);
      assert.equal(withdrawal.envelope.status, 0);
      await assert.rejects(r.authorize(primary.application, request({approvalId: a2.approvalId}), remoteStatus), /withdrawn-or-stale/);
    } finally {await httpA.close(); await httpR.close();}
  });
  await t.test('reverse-domain allowance and resource decision use independent trust', async () => {
    const reverse = new Receiver({path: join(fixture.directory, 'reverse.sqlite'), audience: primary.domain, trustedServices: new Map([[applicationKey(secondary.application), new Set([primary.domain])]]), owners: new Map([['calendar/private', identityKey(authB.human)]]), actions: new Set(['calendar.update']), resolveKey: resolver(primary.transport, freshAnchors)});
    try {
      const v = b.review(authB, wire.toAllowanceCbor(terms(authB, primary.application, primary.domain)));
      const p = b.approve(authB, v.id, v.digest, 'reverse'); await b.publish(packet => reverse.accept(packet));
      const req = wire.toSignedUseCbor(signed('use', wire.toUseCbor({version: 1, approvalId: p.approvalId, delegate: primary.application, audience: primary.domain, resource: 'calendar/private', action: 'calendar.update', issuedAt: new Date(), nonce: randomBytes(32)}), primary.signers[0]));
      assert.equal((await reverse.authorize(secondary.application, req, (...args) => b.status(...args))).decision, 'permit');
    } finally {reverse.close();}
  });
  await t.test('accepted allowance survives actual signing-key expiry', async () => {
    await a.publish(packet => r.accept(packet));
    const key = await addShortKey(primary);
    a.signer = key;
    const v = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    const p = a.approve(authA, v.id, v.digest, 'short-key'); await a.publish(packet => r.accept(packet));
    const delay = Date.parse(key.attestation.keyExpiresAt) - Date.now() + 50;
    assert(delay >= 0 && delay < 5100);
    await new Promise(resolve => setTimeout(resolve, delay));
    a.signer = primary.signers[0];
    assert.equal((await r.authorize(primary.application, request({approvalId: p.approvalId}), status)).decision, 'permit');
  });
  await t.test('real key revocation is separate from accepted allowance withdrawal', async () => {
    const v = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    const approved = a.approve(authA, v.id, v.digest, 'key-retirement'); await a.publish(packet => r.accept(packet));
    await revokeFirstKey(primary);
    await new Promise(resolve => setTimeout(resolve, 1100));
    a.signer = primary.signers[1];
    assert.equal((await r.authorize(primary.application, request({approvalId: approved.approvalId}), status)).decision, 'permit');
    // A new presentation under the revoked key is not accepted.
    const newReview = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    const newer = a.approve(authA, newReview.id, newReview.digest, 'revoked-new-proof');
    const oldProof = wire.toSignedApprovalCbor(signed('approval', wire.toApprovalCbor(newer), primary.signers[0]));
    await assert.rejects(r.accept(oldProof), /key-unusable/);
    // Receiver policy may reject prior evidence after a compromise finding.
    const blocked = JSON.stringify([applicationKey(primary.application), primary.signers[0].keyId]);
    r.blockedHistoricalKeys.add(blocked);
    await assert.rejects(r.authorize(primary.application, request({approvalId: approved.approvalId}), status), /historical-key-policy/);
    r.blockedHistoricalKeys.delete(blocked);
    await revokeFirstKey(secondary); await new Promise(resolve => setTimeout(resolve, 1100));
    await assert.rejects(r.authorize(primary.application, request({approvalId: approved.approvalId}), status), /key-unusable/);
    assert.equal((await r.authorize(primary.application, request({approvalId: approved.approvalId}, secondary.signers[1]), status)).decision, 'permit');
  });
  await t.test('real key origin outage fails closed even when the approval is retained', async () => {
    fixture.stop(primary);
    const live = a.review(authA, wire.toAllowanceCbor({...termsA, allowanceId: randomUUID()}));
    a.approve(authA, live.id, live.digest, 'origin-offline');
    await assert.rejects(a.publish(packet => r.accept(packet)));
    const unfinished = a.db.prepare('SELECT count(*) AS n FROM outbox WHERE delivered=0').get().n;
    assert(unfinished > 0);
  });
});
