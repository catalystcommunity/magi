import {transaction} from './approval.mjs';
import {wire, decode, hash, equal, need} from './policy.mjs';

// The recorder outbox is committed with approval. This inbox is independent.
// Acknowledgement follows durable admission or a durable refusal. A process
// stop between admission and inbox commit is resolved from the release record.
export class ReleaseDelivery {
  constructor({controller, client, now = () => new Date(), fault = () => {}}) {
    Object.assign(this, {controller, client, now, fault});
    controller.db.exec('CREATE TABLE IF NOT EXISTS delivery_inbox (id TEXT PRIMARY KEY, digest BLOB NOT NULL, disposition TEXT NOT NULL, at INTEGER NOT NULL)');
  }
  async accept(event) {
    const db = this.controller.db, digest = hash(wire.toReleaseEventCbor(event));
    const old = db.prepare('SELECT digest FROM delivery_inbox WHERE id=?').get(event.eventId);
    if (old) {need(equal(old.digest, digest), 'event-conflict'); await this.client.ack(event.eventId); return 'duplicate';}
    let disposition;
    if (event.kind === 'withdrawn') disposition = 'withdrawal-recorded';
    else {
      const packet = await this.client.fetch(event.approvalId);
      const {approval, withdrawn} = await this.client.status(packet);
      const candidate = decode('ReleaseCandidate', approval.candidate);
      need(approval.approvalId === event.approvalId && candidate.candidateId === event.candidateId, 'event-approval-binding');
      const existing = db.prepare('SELECT packet FROM releases WHERE id=?').get(event.candidateId);
      if (existing) {
        need(equal(existing.packet, wire.toSignedRecordCbor(packet)), 'admitted-approval-conflict');
        disposition = 'already-admitted';
      } else if (withdrawn) disposition = 'withdrawn-before-admission';
      else if (+candidate.expiresAt <= +this.now()) disposition = 'expired-before-admission';
      else {await this.controller.admit(packet); disposition = 'admitted';}
    }
    this.fault('after-admission-before-inbox');
    transaction(db, () => {
      db.prepare('INSERT OR IGNORE INTO delivery_inbox VALUES (?,?,?,?)').run(event.eventId, digest, disposition, +this.now());
      need(equal(db.prepare('SELECT digest FROM delivery_inbox WHERE id=?').get(event.eventId).digest, digest), 'event-conflict');
    });
    this.fault('after-inbox-before-ack');
    await this.client.ack(event.eventId);
    return disposition;
  }
  async tick({pull = true} = {}) {
    const results = [];
    let events = [];
    try {if (pull) events = await this.client.events();}
    catch {results.push({delivery: 'unavailable', action: 'continue-admitted-recovery'});}
    for (const event of events) {
      try {results.push({eventId: event.eventId, disposition: await this.accept(event)});}
      catch {results.push({eventId: event.eventId, disposition: 'pending-retry'});}
    }
    for (const row of this.controller.db.prepare('SELECT id FROM releases WHERE terminal=0').all()) {
      try {
        const current = await this.controller.step(row.id);
        results.push({candidateId: row.id, phase: current.body.phase});
      } catch {results.push({candidateId: row.id, phase: 'step-interrupted-retry'});}
    }
    if (this.controller.tasks) {
      try {results.push(...await this.controller.tasks.flush());}
      catch {results.push({task: 'completion-requires-reconciliation'});}
    }
    return results;
  }
}

// Idle discovery can tolerate seconds of delay. Recovery cannot. Keep their
// clocks separate and back off failed event reads instead of exhausting the
// identity provider's per-source TLS handshake budget.
export class DeliverySchedule {
  constructor() {this.nextPull = 0; this.backoff = 10000;}
  due(at) {return at >= this.nextPull;}
  completed(at, results) {
    const failed = results.some(r => r.delivery === 'unavailable' || r.disposition === 'pending-retry');
    this.backoff = failed ? Math.min(this.backoff * 2, 60000) : 10000;
    this.nextPull = at + this.backoff;
  }
}
