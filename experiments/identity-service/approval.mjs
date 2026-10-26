import { DatabaseSync } from 'node:sqlite';
import { randomBytes, randomUUID, createHash, sign, verify, createPublicKey, timingSafeEqual } from 'node:crypto';
import { chmodSync } from 'node:fs';
import * as wire from './generated/typescript/codec.gen.ts';
import { sdk, call, keys } from './linkkeys.mjs';

export const hash = bytes => createHash('sha256').update(bytes).digest();
export const equal = (a, b) => a instanceof Uint8Array && b instanceof Uint8Array && a.length === b.length && timingSafeEqual(a, b);
const token = () => randomBytes(32).toString('base64url');
const text = (s, max = 256) => typeof s === 'string' && s.length > 0 && s.length <= max && !/[\x00-\x1f\x7f]/.test(s);
const need = (ok, code) => {if (!ok) throw Error(code);};
export const identityKey = i => JSON.stringify([i.domain, i.accountId]);
export const applicationKey = a => JSON.stringify([a.identity.domain, a.identity.accountId, a.applicationId, a.instanceId]);
const sameIdentity = (a,b) => identityKey(a) === identityKey(b);
const sameApplication = (a,b) => applicationKey(a) === applicationKey(b);
function identity(i) {
  need(i && text(i.domain) && /^[a-z0-9]+(?:[.-][a-z0-9]+)+$/.test(i.domain) && /^[0-9a-f-]{36}$/.test(i.accountId), 'identity');
}
function application(a) {identity(a.identity); need(text(a.applicationId) && text(a.instanceId), 'application');}
function timestamp(d) {need(d instanceof Date && Number.isFinite(+d), 'timestamp');}
function shape(actual, expected) {
  if (actual instanceof Map && expected instanceof Map) {
    need(actual.size === expected.size, 'unknown-field');
    for (const [key, value] of expected) {need(actual.has(key), 'missing-field'); shape(actual.get(key), value);}
  } else if (actual instanceof Uint8Array && expected instanceof Uint8Array) need(equal(actual, expected), 'bytes');
  else if (actual?.tag === 0 && expected?.tag === 0) {
    // This experiment has millisecond clocks. Preserve the original signed
    // timestamp text. Accept both Go and TS encodings of the same exact time.
    need(typeof actual.value === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,3})?Z$/.test(actual.value), 'timestamp-profile');
    const normalized = s => s.replace(/\.([0-9]*?)0+Z$/, (_, fraction) => fraction ? `.${fraction}Z` : 'Z');
    need(normalized(actual.value) === normalized(expected.value), 'timestamp-value');
  } else need(actual === expected, 'typed-value');
}
export function decode(type, bytes) {
  need(bytes instanceof Uint8Array && bytes.length > 0 && bytes.length <= 32768, 'frame-limit');
  const value = wire[`from${type}Cbor`](bytes);
  // Detect duplicate fields and trailing bytes before checking the exact known
  // field shape. Do not normalize bytes used for signatures or approval hashes.
  const tree = wire.decode(bytes);
  need(equal(bytes, wire.encodeValue(tree)), 'noncanonical');
  shape(tree, wire.decode(wire[`to${type}Cbor`](value)));
  if ('version' in value) need(value.version === 1, 'version');
  return value;
}
export function allowance(bytes) {
  const a = decode('Allowance', bytes); identity(a.issuer); application(a.delegate);
  for (const v of [a.allowanceId, a.audience, a.resource, a.action, a.purpose]) need(text(v), 'allowance-field');
  timestamp(a.notBefore);
  if (a.expiresAt !== undefined) {timestamp(a.expiresAt); need(+a.expiresAt > +a.notBefore, 'allowance-window');}
  return a;
}
const message = (type, packet) => wire.toSignatureInputCbor({context: `opal.w02.${type}.v1`, body: packet.body, keyId: packet.keyId, signedAt: packet.signedAt});
export function signed(type, body, signer, at = new Date()) {
  const packet = {body: Buffer.from(body), keyId: signer.keyId, signedAt: at};
  return {...packet, signature: sign(null, message(type, packet), signer.privateKey)};
}
function db(path) {
  const d = new DatabaseSync(path); chmodSync(path, 0o600);
  d.exec('PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;');
  return d;
}
function tx(d, work) { d.exec('BEGIN IMMEDIATE'); try {const value = work(); d.exec('COMMIT'); return value;} catch(e) {d.exec('ROLLBACK'); throw e;} }

export class ApprovalService {
  constructor({path, application: app, origin, rp, dns, socketTransport, signer, now = () => new Date(), fault = () => {}}) {
    application(app); this.app = app; this.origin = origin; this.now = now; this.signer = signer; this.fault = fault;
    this.db = db(path);
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS pending(state TEXT PRIMARY KEY, body TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS sessions(token_hash BLOB PRIMARY KEY, csrf_hash BLOB NOT NULL, human TEXT NOT NULL, authenticated INTEGER NOT NULL, expires INTEGER NOT NULL, evidence TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS authentication(id TEXT PRIMARY KEY, body BLOB NOT NULL);
      CREATE TABLE IF NOT EXISTS reviews(id TEXT PRIMARY KEY, session_hash BLOB NOT NULL, body BLOB NOT NULL, digest BLOB NOT NULL, expires INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS approvals(id TEXT PRIMARY KEY, human TEXT NOT NULL, command_key TEXT NOT NULL, digest BLOB NOT NULL, body BLOB NOT NULL, allowance_id TEXT NOT NULL, UNIQUE(human, command_key), UNIQUE(human, allowance_id));
      CREATE TABLE IF NOT EXISTS outbox(approval_id TEXT PRIMARY KEY REFERENCES approvals(id), packet BLOB, delivered INTEGER NOT NULL DEFAULT 0);
      CREATE TABLE IF NOT EXISTS withdrawals(approval_id TEXT PRIMARY KEY REFERENCES approvals(id), human TEXT NOT NULL, withdrawn_at INTEGER NOT NULL);
    `);
    const pendingStore = {
      save: async p => {this.db.prepare('INSERT INTO pending VALUES (?,?)').run(p.state, JSON.stringify(p));},
      take: async state => tx(this.db, () => {
        const row = this.db.prepare('DELETE FROM pending WHERE state=? RETURNING body').get(state);
        return row ? JSON.parse(row.body) : undefined;
      }),
    };
    this.login = new sdk.RegularRpClient({rpDomain: app.identity.domain, rpServer: rp, pendingStore, dnsResolver: dns, socketTransport, now});
  }
  close() {this.db.close();}
  beginLogin(identityInput, browserBinding) {
    return this.login.beginLogin({identity: identityInput, sessionBinding: browserBinding, callbackUrl: `${this.origin}/auth/callback`});
  }
  async finishLogin(state, encryptedToken, browserBinding) {
    const verified = await this.login.completeLogin({state, encryptedToken, sessionBinding: browserBinding});
    const human = {domain: verified.domain, accountId: verified.userId}; identity(human);
    const at = +this.now(), authenticated = Date.parse(verified.assertion.issuedAt), expiry = Date.parse(verified.assertion.expiresAt);
    need(Number.isFinite(authenticated) && authenticated <= at && expiry > at, 'authentication-time');
    const session = token(), csrf = token(), evidence = randomUUID();
    // Preserve the verified assertion's typed facts, never a replayable callback,
    // password, browser binding, API key, or session token.
    const body = Buffer.from(JSON.stringify({method: 'LinkKeys regular RP', human, audience: verified.assertion.audience, authenticatedAt: authenticated, expiresAt: expiry, verifiedAt: at}));
    tx(this.db, () => {
      this.db.prepare('INSERT INTO authentication VALUES (?,?)').run(evidence, body);
      this.db.prepare('INSERT INTO sessions VALUES (?,?,?,?,?,?)').run(hash(session), hash(csrf), JSON.stringify(human), authenticated, Math.min(at + 300000, expiry), evidence);
    });
    return {session, csrf, human};
  }
  authenticated({session, csrf, origin}) {
    need(origin === this.origin && text(session) && text(csrf), 'csrf');
    const row = this.db.prepare('SELECT * FROM sessions WHERE token_hash=?').get(hash(session));
    const now = +this.now();
    need(row && equal(row.csrf_hash, hash(csrf)) && row.expires > now && row.authenticated <= now && now - row.authenticated <= 300000, 'session');
    return {...row, human: JSON.parse(row.human)};
  }
  review(auth, bytes) {
    const s = this.authenticated(auth), a = allowance(bytes);
    need(sameIdentity(s.human, a.issuer), 'issuer-not-human');
    const id = randomUUID();
    this.db.prepare('INSERT INTO reviews VALUES (?,?,?,?,?)').run(id, s.token_hash, bytes, hash(bytes), Math.min(+this.now() + 120000, s.expires));
    return {id, digest: hash(bytes), bytes: Buffer.from(bytes), meaning: a};
  }
  approve(auth, reviewId, expectedDigest, commandKey) {
    need(text(reviewId) && text(commandKey), 'approval-input');
    return tx(this.db, () => {
      const s = this.authenticated(auth);
      const r = this.db.prepare('SELECT * FROM reviews WHERE id=?').get(reviewId);
      need(r && equal(r.session_hash, s.token_hash) && equal(r.digest, expectedDigest), 'review-binding');
      const existing = this.db.prepare('SELECT * FROM approvals WHERE human=? AND command_key=?').get(JSON.stringify(s.human), commandKey);
      if (existing) {need(equal(existing.digest, r.digest), 'idempotency-conflict'); return decode('Approval', existing.body);}
      need(r.expires > +this.now(), 'review-expired');
      const a = allowance(r.body); need(sameIdentity(a.issuer, s.human), 'issuer-not-human');
      need(!this.db.prepare('SELECT 1 FROM approvals WHERE human=? AND allowance_id=?').get(JSON.stringify(s.human), a.allowanceId), 'allowance-already-approved');
      const approval = {version: 1, approvalId: randomUUID(), approvalService: this.app, human: s.human, allowance: r.body, allowanceDigest: r.digest, approvedAt: this.now(), authenticatedAt: new Date(s.authenticated), authenticationEvidence: s.evidence};
      const bytes = wire.toApprovalCbor(approval);
      this.db.prepare('INSERT INTO approvals VALUES (?,?,?,?,?,?)').run(approval.approvalId, JSON.stringify(s.human), commandKey, r.digest, bytes, a.allowanceId);
      this.db.prepare('INSERT INTO outbox(approval_id) VALUES (?)').run(approval.approvalId);
      this.fault('before-commit');
      return approval;
    });
  }
  async publish(deliver) {
    for (const item of this.db.prepare('SELECT approval_id FROM outbox WHERE delivered=0 ORDER BY approval_id').all()) {
      const packet = tx(this.db, () => {
        const row = this.db.prepare('SELECT a.body,o.packet FROM approvals a JOIN outbox o ON o.approval_id=a.id WHERE a.id=?').get(item.approval_id);
        if (row.packet) return row.packet;
        const bytes = wire.toSignedApprovalCbor(signed('approval', row.body, this.signer, this.now()));
        this.db.prepare('UPDATE outbox SET packet=? WHERE approval_id=?').run(bytes, item.approval_id); return bytes;
      });
      this.fault('before-delivery');
      await deliver(packet);
      this.fault('after-delivery');
      this.db.prepare('UPDATE outbox SET delivered=1 WHERE approval_id=?').run(item.approval_id);
    }
  }
  withdraw(auth, approvalId) {
    tx(this.db, () => {
      const s = this.authenticated(auth), a = this.db.prepare('SELECT human FROM approvals WHERE id=?').get(approvalId);
      need(a && sameIdentity(s.human, JSON.parse(a.human)), 'withdrawal-owner');
      this.db.prepare('INSERT OR IGNORE INTO withdrawals VALUES (?,?,?)').run(approvalId, JSON.stringify(s.human), +this.now());
    });
  }
  status(approvalId, audience, nonce) {
    need(text(approvalId) && text(audience) && nonce instanceof Uint8Array && nonce.length === 32, 'status-input');
    const row = this.db.prepare('SELECT body FROM approvals WHERE id=?').get(approvalId); need(row, 'unknown-approval');
    const approval = decode('Approval', row.body), a = allowance(approval.allowance); need(a.audience === audience, 'status-audience');
    const withdrawn = !!this.db.prepare('SELECT 1 FROM withdrawals WHERE approval_id=?').get(approvalId);
    return wire.toSignedStatusCbor(signed('status', wire.toStatusCbor({version: 1, approvalId, approvalDigest: hash(row.body), approvalService: this.app, audience, nonce, withdrawn, checkedAt: this.now()}), this.signer, this.now()));
  }
  async authenticatedStatus(bytes, resolveKey, requesters) {
    const packet = decode('SignedStatusQuery', bytes), q = decode('StatusQuery', packet.body);
    application(q.requester); timestamp(q.issuedAt);
    need(requesters.get(q.audience) === applicationKey(q.requester), 'status-requester');
    need(+q.issuedAt <= +this.now() && +this.now() - +q.issuedAt <= 60000, 'status-request-time');
    await verifyPacket('status-query', packet, q.requester, resolveKey, this.now);
    need(+q.issuedAt <= +packet.signedAt, 'status-request-after-signature');
    return this.status(q.approvalId, q.audience, q.nonce);
  }
}

// Domain anchors are independent inputs. Do not trust a key merely because an
// RP included it in its response. Both freshness timestamps are mandatory.
export function resolver(rp, domainKeys, now = () => new Date()) {
  return async (app, keyId) => {
    application(app);
    const anchors = await domainKeys(app.identity.domain);
    const r = await call(rp, 'Rp', 'resolve-application-keys', 'RpResolveApplicationKeysRequest', {subjectUserId: app.identity.accountId, subjectDomain: app.identity.domain, applicationId: app.applicationId, instanceId: app.instanceId, maxCacheAgeSeconds: 1}, 'RpResolveApplicationKeysResponse');
    need(r.subjectUserId === app.identity.accountId && r.subjectDomain === app.identity.domain && r.applicationId === app.applicationId && r.instanceId === app.instanceId, 'resolver-identity');
    need(r.cacheStatus === 'fresh' || r.cacheStatus === 'refreshed', 'stale-keys');
    const at = now();
    for (const t of [r.fetchedAt, r.revocationsCheckedAt]) {const n = Date.parse(t); need(Number.isFinite(n) && n <= +at && +at - n <= 5000, 'key-freshness');}
    const verified = keys.verifyApplicationKeySet(r.applicationKeys, r.applicationKeyRevocations, anchors, {subjectUserId: app.identity.accountId, subjectDomain: app.identity.domain, applicationId: app.applicationId, instanceId: app.instanceId}, at, 0);
    need(verified.rejected.length === 0, 'key-evidence-rejected');
    const key = keys.keyForUse(verified, keyId, 'sign', 'ed25519'); need(key, 'key-unusable');
    need(Date.parse(key.attestedAt) <= +at && Date.parse(key.keyCreatedAt) <= +at, 'future-key');
    return key;
  };
}
async function verifyPacket(type, packet, app, resolveKey, now) {
  const k = await resolveKey(app, packet.keyId);
  timestamp(packet.signedAt);
  need(+packet.signedAt <= +now() && Date.parse(k.keyCreatedAt) <= +packet.signedAt && +packet.signedAt < Date.parse(k.keyExpiresAt), 'signature-time');
  need(packet.signature.length === 64 && verify(null, message(type, packet), createPublicKey({key: {kty: 'OKP', crv: 'Ed25519', x: Buffer.from(k.publicKey).toString('base64url')}, format: 'jwk'}), packet.signature), 'signature');
  return k;
}
export class Receiver {
  constructor({path, audience, trustedServices, owners, resolveKey, actions = new Set(), blockedHistoricalKeys = new Set(), now = () => new Date()}) {
    this.db = db(path); this.audience = audience; this.trustedServices = trustedServices; this.owners = owners; this.resolveKey = resolveKey; this.now = now; this.blockedHistoricalKeys = blockedHistoricalKeys; this.actions = actions;
    this.db.exec('CREATE TABLE IF NOT EXISTS accepted(origin TEXT NOT NULL, id TEXT NOT NULL, body BLOB NOT NULL, key_id TEXT NOT NULL, packet BLOB NOT NULL, accepted_at INTEGER NOT NULL, PRIMARY KEY(origin,id)); CREATE TABLE IF NOT EXISTS used_nonces(delegate TEXT NOT NULL, nonce BLOB NOT NULL, PRIMARY KEY(delegate,nonce));');
  }
  close() {this.db.close();}
  trust(approval) {
    const domains = this.trustedServices.get(applicationKey(approval.approvalService));
    need(domains?.has(approval.human.domain), 'untrusted-approval-service');
  }
  async accept(bytes) {
    const packet = decode('SignedApproval', bytes), a = decode('Approval', packet.body);
    identity(a.human); application(a.approvalService); this.trust(a);
    const terms = allowance(a.allowance);
    need(sameIdentity(terms.issuer, a.human) && equal(hash(a.allowance), a.allowanceDigest) && terms.audience === this.audience, 'approval-binding');
    timestamp(a.approvedAt); timestamp(a.authenticatedAt);
    need(text(a.approvalId) && text(a.authenticationEvidence) && +a.approvedAt <= +this.now() && +a.authenticatedAt <= +a.approvedAt && +a.approvedAt - +a.authenticatedAt <= 300000, 'approval-authentication');
    const origin = applicationKey(a.approvalService);
    const existing = this.db.prepare('SELECT body FROM accepted WHERE origin=? AND id=?').get(origin, a.approvalId);
    if (existing) {need(equal(existing.body, packet.body), 'approval-conflict'); return a.approvalId;}
    await verifyPacket('approval', packet, a.approvalService, this.resolveKey, this.now);
    need(+a.approvedAt <= +packet.signedAt, 'approval-after-signature');
    tx(this.db, () => {
      const r = this.db.prepare('SELECT body FROM accepted WHERE origin=? AND id=?').get(origin, a.approvalId);
      if (r) need(equal(r.body, packet.body), 'approval-conflict');
      else this.db.prepare('INSERT INTO accepted VALUES (?,?,?,?,?,?)').run(origin, a.approvalId, packet.body, packet.keyId, bytes, +this.now());
    });
    return a.approvalId;
  }
  async authorize(service, requestBytes, getStatus) {
    const p = decode('SignedUse', requestBytes), request = decode('Use', p.body);
    application(request.delegate); timestamp(request.issuedAt);
    need(request.nonce.length === 32 && text(request.approvalId) && +request.issuedAt <= +this.now() && +this.now() - +request.issuedAt <= 60000, 'request-time-or-nonce');
    const row = this.db.prepare('SELECT body,key_id FROM accepted WHERE origin=? AND id=?').get(applicationKey(service), request.approvalId); need(row, 'approval-not-accepted');
    const a = decode('Approval', row.body), terms = allowance(a.allowance); this.trust(a);
    need(sameApplication(request.delegate, terms.delegate) && request.audience === this.audience && request.resource === terms.resource && request.action === terms.action, 'request-scope');
    await verifyPacket('use', p, request.delegate, this.resolveKey, this.now);
    need(+request.issuedAt <= +p.signedAt, 'request-after-signature');
    const nonce = randomBytes(32), response = await getStatus(a.approvalId, this.audience, nonce);
    const sp = decode('SignedStatus', response), s = decode('Status', sp.body);
    need(sameApplication(s.approvalService, a.approvalService) && s.approvalId === a.approvalId && equal(s.approvalDigest, hash(row.body)) && equal(s.nonce, nonce) && s.audience === this.audience, 'status-binding');
    await verifyPacket('status', sp, a.approvalService, this.resolveKey, this.now);
    timestamp(s.checkedAt); need(+s.checkedAt <= +this.now() && +this.now() - +s.checkedAt <= 5000 && !s.withdrawn, 'withdrawn-or-stale');
    // Recheck current local policy and time after network I/O. An allowance is
    // evidence of intent, not authority over a resource owned by this receiver.
    this.trust(a);
    need(!this.blockedHistoricalKeys.has(JSON.stringify([applicationKey(service), row.key_id])), 'historical-key-policy');
    need(this.owners.get(request.resource) === identityKey(a.human) && this.actions.has(request.action), 'receiver-policy');
    need(+terms.notBefore <= +this.now() && (!terms.expiresAt || +terms.expiresAt > +this.now()) && +this.now() - +request.issuedAt <= 60000, 'allowance-or-request-expired');
    tx(this.db, () => {
      need(!this.db.prepare('SELECT 1 FROM used_nonces WHERE delegate=? AND nonce=?').get(applicationKey(request.delegate), request.nonce), 'request-replay');
      this.db.prepare('INSERT INTO used_nonces VALUES (?,?)').run(applicationKey(request.delegate), request.nonce);
    });
    return {decision: 'permit', approvalId: a.approvalId, resource: request.resource, action: request.action};
  }
}
