import {DatabaseSync} from 'node:sqlite';
import {chmodSync} from 'node:fs';
import {randomBytes, randomUUID} from 'node:crypto';
import {signed, verified} from '../protocol/profile.mjs';
import {wire, decode, hash, equal, need, same, identity, application, time, validText, policy, candidate, scope} from './policy.mjs';

const token = () => randomBytes(32).toString('base64url');
export function database(path) {
  const db = new DatabaseSync(path); chmodSync(path, 0o600);
  db.exec('PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;');
  return db;
}
export function transaction(db, work) {
  db.exec('BEGIN IMMEDIATE');
  try {const result = work(); db.exec('COMMIT'); return result;}
  catch (error) {db.exec('ROLLBACK'); throw error;}
}

// A current LinkKeys key resolver is mandatory in the network service. Tests
// supply isolated keys. The signature proves the recorder, not human intent.
export async function authenticate(packet, kind, signer, recipient, resolveKey, now, historical = false) {
  const input = decode('SignatureInput', packet.input);
  need(same(input.signer, signer) && same(input.recipient, recipient), 'signature-participants');
  const key = await resolveKey(signer, input.keyId);
  const at = +now();
  for (const date of [key.createdAt, key.expiresAt, input.signedAt]) time(date);
  need(+key.createdAt <= +input.signedAt && +input.signedAt <= at && at < +key.expiresAt && (historical || at - input.signedAt <= 60000), 'signature-time');
  return verified(packet, key.publicKey, scope(kind, signer, recipient)).value;
}

export class ApprovalRecorder {
  constructor({path, config, app, controller, origin, signer, createLogin, now = () => new Date(), fault = () => {}}) {
    this.config = policy(config); application(app); application(controller);
    need(app.identity.domain === config.target_domain && controller.identity.domain === config.target_domain && !same(app, controller), 'separate-release-applications');
    const url = new URL(origin);
    need(url.protocol === 'https:' && url.origin === origin && url.hostname.endsWith(`.${config.target_domain}`), 'approval-origin');
    need(validText(signer.keyId) && signer.privateKey && typeof createLogin === 'function', 'recorder-dependencies');
    Object.assign(this, {app, controller, origin, signer, now, fault});
    this.db = database(path);
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS binding (id INTEGER PRIMARY KEY CHECK(id=1), body TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS pending (state TEXT PRIMARY KEY, body TEXT NOT NULL, expires INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS authentication (id TEXT PRIMARY KEY, body BLOB NOT NULL);
      CREATE TABLE IF NOT EXISTS sessions (token_hash BLOB PRIMARY KEY, csrf_hash BLOB NOT NULL, human TEXT NOT NULL, authenticated INTEGER NOT NULL, expires INTEGER NOT NULL, evidence TEXT NOT NULL REFERENCES authentication(id));
      CREATE TABLE IF NOT EXISTS reviews (id TEXT PRIMARY KEY, session_hash BLOB NOT NULL, body BLOB NOT NULL, digest BLOB NOT NULL, expires INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS approvals (id TEXT PRIMARY KEY, human TEXT NOT NULL, command_key TEXT NOT NULL, candidate_id TEXT NOT NULL UNIQUE, digest BLOB NOT NULL, packet BLOB NOT NULL, UNIQUE(human,command_key));
      CREATE TABLE IF NOT EXISTS withdrawals (id TEXT PRIMARY KEY REFERENCES approvals(id), at INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS audit (sequence INTEGER PRIMARY KEY, at INTEGER NOT NULL, kind TEXT NOT NULL, reference TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS candidates (id TEXT PRIMARY KEY, body BLOB NOT NULL, digest BLOB NOT NULL, submitted INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS release_events (sequence INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, candidate_id TEXT NOT NULL, approval_id TEXT NOT NULL REFERENCES approvals(id), kind TEXT NOT NULL, at INTEGER NOT NULL, acknowledged INTEGER NOT NULL DEFAULT 0, UNIQUE(approval_id,kind));
    `);
    const binding = JSON.stringify({app, controller, origin, config: this.config});
    transaction(this.db, () => {
      this.db.prepare('INSERT OR IGNORE INTO binding VALUES (1,?)').run(binding);
      need(this.db.prepare('SELECT body FROM binding WHERE id=1').get().body === binding, 'recorder-binding-changed');
    });
    const pendingStore = {
      save: async p => transaction(this.db, () => {
        this.db.prepare('DELETE FROM pending WHERE expires<=?').run(+this.now());
        need(this.db.prepare('SELECT count(*) AS n FROM pending').get().n < 100, 'login-capacity');
        this.db.prepare('INSERT INTO pending VALUES (?,?,?)').run(p.state, JSON.stringify(p), +this.now() + 300000);
      }),
      take: async state => transaction(this.db, () => {
        const row = this.db.prepare('DELETE FROM pending WHERE state=? RETURNING body,expires').get(state);
        return row && row.expires > +this.now() ? JSON.parse(row.body) : undefined;
      }),
    };
    this.login = createLogin({pendingStore, now});
  }
  close() {this.db.close();}
  audit(kind, reference) {this.db.prepare('INSERT INTO audit(at,kind,reference) VALUES (?,?,?)').run(+this.now(), kind, reference);}
  async beginLogin(identityInput, browserBinding) {
    need(identityInput === this.config.login_hint || identityInput === `${this.config.approver.account_id}@${this.config.approver.domain}`, 'login-hint');
    return this.login.beginLogin({identity: identityInput, sessionBinding: browserBinding, callbackUrl: `${this.origin}/auth/callback`, requestedClaims: {required: [], optional: []}, signal: AbortSignal.timeout(10000)});
  }
  async finishLogin(state, encryptedToken, browserBinding) {
    const v = await this.login.completeLogin({state, encryptedToken, sessionBinding: browserBinding, signal: AbortSignal.timeout(10000)});
    const human = {domain: v.domain, accountId: v.userId}; identity(human);
    need(same(human, {domain: this.config.approver.domain, accountId: this.config.approver.account_id}), 'not-release-approver');
    const at = +this.now(), authenticated = Date.parse(v.assertion.issuedAt), expiry = Date.parse(v.assertion.expiresAt);
    need(v.assertion.audience === this.app.identity.domain && Number.isFinite(authenticated) && authenticated <= at && at - authenticated <= 300000 && expiry > at, 'authentication-time-or-audience');
    const session = token(), csrf = token(), evidence = randomUUID();
    // Do not retain the callback, assertion token, or browser credentials.
    const facts = Buffer.from(JSON.stringify({method: 'LinkKeys regular RP', human, audience: v.assertion.audience, assertedAt: authenticated, expiresAt: expiry, verifiedAt: at, assurance: 'verified-assertion; factor-unspecified'}));
    transaction(this.db, () => {
      this.db.prepare('INSERT INTO authentication VALUES (?,?)').run(evidence, facts);
      this.db.prepare('INSERT INTO sessions VALUES (?,?,?,?,?,?)').run(hash(session), hash(csrf), JSON.stringify(human), authenticated, Math.min(at + 300000, authenticated + 300000, expiry), evidence);
      this.audit('login', evidence);
    });
    return {session, csrf, human};
  }
  authenticated({session, csrf, origin}) {
    need(origin === this.origin && validText(session) && validText(csrf), 'csrf');
    const row = this.db.prepare('SELECT * FROM sessions WHERE token_hash=?').get(hash(session)), at = +this.now();
    need(row && equal(row.csrf_hash, hash(csrf)) && row.expires > at && row.authenticated <= at && at - row.authenticated <= 300000, 'session');
    return {...row, human: JSON.parse(row.human)};
  }
  async submit(packet, resolveKey) {
    const input = await authenticate(packet, 'submit', this.controller, this.app, resolveKey, this.now);
    const bytes = decode('ReleaseSubmission', input.body).candidate;
    need(bytes.length <= 65536, 'candidate-size');
    const c = candidate(bytes, this.config, this.now(), {allowExpired: true});
    need(c.explanation !== undefined, 'candidate-explanation-required');
    return transaction(this.db, () => {
      const old = this.db.prepare('SELECT body FROM candidates WHERE id=?').get(c.candidateId);
      if (old) need(equal(old.body, bytes), 'candidate-conflict');
      else {
        candidate(bytes, this.config, this.now());
        need(this.db.prepare('SELECT count(*) AS n FROM candidates').get().n < 1000, 'candidate-capacity');
        this.db.prepare('INSERT INTO candidates VALUES (?,?,?,?)').run(c.candidateId, bytes, hash(bytes), +this.now());
        this.audit('submitted', c.candidateId);
      }
      return {candidateId: c.candidateId, digest: hash(bytes), reviewUrl: `${this.origin}/?candidate=${encodeURIComponent(c.candidateId)}`};
    });
  }
  reviewCandidate(auth, id) {
    this.authenticated(auth); need(validText(id), 'candidate-id');
    const row = this.db.prepare('SELECT body FROM candidates WHERE id=?').get(id);
    need(row, 'unknown-candidate');
    return this.review(auth, row.body);
  }
  event(kind, approvalId, candidateId) {
    this.db.prepare('INSERT OR IGNORE INTO release_events(id,candidate_id,approval_id,kind,at) VALUES (?,?,?,?,?)').run(randomUUID(), candidateId, approvalId, kind, +this.now());
  }
  async pullEvents(packet, resolveKey) {
    const input = await authenticate(packet, 'event-query', this.controller, this.app, resolveKey, this.now);
    const q = decode('ReleaseEventQuery', input.body); need(q.challenge.length === 32, 'event-challenge');
    const rows = this.db.prepare('SELECT * FROM release_events WHERE acknowledged=0 ORDER BY sequence LIMIT 8').all();
    const events = rows.map(r => ({eventId: r.id, candidateId: r.candidate_id, approvalId: r.approval_id, kind: r.kind, at: new Date(r.at)}));
    return signed(scope('events', this.app, this.controller), wire.toReleaseEventBatchCbor({challenge: q.challenge, events}), this.signer.keyId, this.now(), this.signer.privateKey);
  }
  async acknowledgeEvent(packet, resolveKey) {
    const input = await authenticate(packet, 'event-ack', this.controller, this.app, resolveKey, this.now);
    const q = decode('ReleaseEventAck', input.body); need(validText(q.eventId), 'event-id');
    return transaction(this.db, () => {
      need(this.db.prepare('SELECT 1 FROM release_events WHERE id=?').get(q.eventId), 'unknown-event');
      this.db.prepare('UPDATE release_events SET acknowledged=1 WHERE id=?').run(q.eventId);
      return {eventId: q.eventId};
    });
  }
  async fetchApproval(packet, resolveKey) {
    const input = await authenticate(packet, 'fetch-approval', this.controller, this.app, resolveKey, this.now);
    const q = decode('ReleaseApprovalFetch', input.body); need(validText(q.approvalId), 'approval-id');
    const row = this.db.prepare('SELECT packet FROM approvals WHERE id=?').get(q.approvalId);
    need(row, 'unknown-approval'); return decode('SignedRecord', row.packet);
  }
  review(auth, bytes) {
    const session = this.authenticated(auth); candidate(bytes, this.config, this.now());
    return transaction(this.db, () => {
      this.db.prepare('DELETE FROM reviews WHERE expires<=?').run(+this.now() - 300000);
      need(this.db.prepare('SELECT count(*) AS n FROM reviews').get().n < 1000, 'review-capacity');
      const reviewId = randomUUID(), digest = hash(bytes);
      this.db.prepare('INSERT INTO reviews VALUES (?,?,?,?,?)').run(reviewId, session.token_hash, bytes, digest, Math.min(+this.now() + 120000, session.expires));
      this.audit('review', reviewId);
      return {reviewId, candidate: Buffer.from(bytes), digest};
    });
  }
  approve(auth, {reviewId, digest, commandKey}) {
    need(validText(reviewId) && validText(commandKey) && digest.length === 32, 'approval-input');
    const result = transaction(this.db, () => {
      const s = this.authenticated(auth), human = JSON.stringify(s.human);
      const r = this.db.prepare('SELECT * FROM reviews WHERE id=?').get(reviewId);
      need(r && equal(r.session_hash, s.token_hash) && equal(r.digest, digest), 'review-binding');
      const old = this.db.prepare('SELECT * FROM approvals WHERE human=? AND command_key=?').get(human, commandKey);
      if (old) {need(equal(old.digest, r.digest), 'approval-conflict'); return decode('SignedRecord', old.packet);}
      need(r.expires > +this.now(), 'review-expired');
      const c = candidate(r.body, this.config, this.now());
      need(!this.db.prepare('SELECT 1 FROM approvals WHERE candidate_id=?').get(c.candidateId), 'candidate-already-approved');
      const approvalId = randomUUID(), at = this.now();
      const value = {approvalId, candidate: r.body, candidateDigest: r.digest, human: s.human, recorder: this.app, controller: this.controller, approvedAt: at, authenticatedAt: new Date(s.authenticated), authenticationEvidence: {authority: this.app.identity, kind: 'release-authentication', recordId: s.evidence}};
      const packet = signed(scope('approval', this.app, this.controller), wire.toReleaseApprovalCbor(value), this.signer.keyId, at, this.signer.privateKey);
      this.db.prepare('INSERT INTO approvals VALUES (?,?,?,?,?,?)').run(approvalId, human, commandKey, c.candidateId, r.digest, wire.toSignedRecordCbor(packet));
      this.event('approved', approvalId, c.candidateId);
      this.audit('approved', approvalId); this.fault('before-approval-commit');
      return packet;
    });
    this.fault('after-approval-commit');
    return result;
  }
  withdraw(auth, approvalId) {
    return transaction(this.db, () => {
      const s = this.authenticated(auth), row = this.db.prepare('SELECT human,candidate_id FROM approvals WHERE id=?').get(approvalId);
      need(row && row.human === JSON.stringify(s.human), 'withdrawal-owner');
      this.db.prepare('INSERT OR IGNORE INTO withdrawals VALUES (?,?)').run(approvalId, +this.now());
      const at = this.db.prepare('SELECT at FROM withdrawals WHERE id=?').get(approvalId).at;
      this.audit('withdrawn', approvalId);
      this.event('withdrawn', approvalId, row.candidate_id);
      return {approvalId, withdrawnAt: new Date(at)};
    });
  }
  async status(packet, resolveKey) {
    const input = await authenticate(packet, 'status-query', this.controller, this.app, resolveKey, this.now);
    const q = decode('ReleaseApprovalQuery', input.body);
    need(validText(q.approvalId) && q.challenge.length === 32, 'status-query');
    const row = this.db.prepare('SELECT packet FROM approvals WHERE id=?').get(q.approvalId);
    need(row, 'unknown-approval');
    const withdrawn = !!this.db.prepare('SELECT 1 FROM withdrawals WHERE id=?').get(q.approvalId);
    const at = this.now();
    const status = {approval: decode('SignedRecord', row.packet), challenge: q.challenge, withdrawn, checkedAt: at};
    return signed(scope('status', this.app, this.controller), wire.toReleaseApprovalStatusCbor(status), this.signer.keyId, at, this.signer.privateKey);
  }
}

// The resource controller calls this immediately before its first mutation.
// Withdrawal prevents new admission. It is not cancellation of an admitted run.
export async function authorizeRelease({packet, recorder, controller, config, signer, resolveKey, getStatus, now = () => new Date()}) {
  policy(config); application(recorder); application(controller);
  const input = await authenticate(packet, 'approval', recorder, controller, resolveKey, now, true);
  const a = decode('ReleaseApproval', input.body);
  need(same(a.recorder, recorder) && same(a.controller, controller) && same(a.human, {domain: config.approver.domain, accountId: config.approver.account_id}) && equal(hash(a.candidate), a.candidateDigest), 'approval-binding');
  for (const date of [a.approvedAt, a.authenticatedAt]) time(date);
  need(validText(a.approvalId) && same(a.authenticationEvidence.authority, recorder.identity) && a.authenticationEvidence.kind === 'release-authentication' && validText(a.authenticationEvidence.recordId), 'authentication-evidence');
  need(+a.authenticatedAt <= +a.approvedAt && a.approvedAt - a.authenticatedAt <= 300000 && +a.approvedAt <= +input.signedAt && +input.signedAt <= +now(), 'approval-authentication');
  const challenge = randomBytes(32);
  const request = signed(scope('status-query', controller, recorder), wire.toReleaseApprovalQueryCbor({approvalId: a.approvalId, challenge}), signer.keyId, now(), signer.privateKey);
  const statusPacket = await getStatus(request);
  const statusInput = await authenticate(statusPacket, 'status', recorder, controller, resolveKey, now);
  const s = decode('ReleaseApprovalStatus', statusInput.body);
  need(equal(s.challenge, challenge) && equal(s.approval.input, packet.input) && equal(s.approval.signature, packet.signature) && !s.withdrawn && +s.checkedAt <= +now() && +s.checkedAt === +statusInput.signedAt && now() - s.checkedAt <= 5000, 'current-approval-status');
  candidate(a.candidate, config, now());
  return a;
}
