import {randomBytes} from 'node:crypto';
import {HTTPTransport} from '../protocol/http.mjs';
import {signed} from '../protocol/profile.mjs';
import {authenticate} from './approval.mjs';
import {wire, decode, scope, equal, need, hash} from './policy.mjs';

// This client has submission and delivery authority, never human approval.
export class ReleaseClient {
  constructor({endpoint, recorder, controller, signer, resolveKey, now = () => new Date(), call}) {
    Object.assign(this, {endpoint, recorder, controller, signer, resolveKey, now});
    const transport = new HTTPTransport(endpoint, {codec: wire, responses: {'Releases/submit': 'ReleaseSubmitted', 'Releases/pull-events': 'SignedRecord', 'Releases/fetch-approval': 'SignedRecord', 'Releases/acknowledge-event': 'ReleaseAcknowledged', 'Releases/status': 'SignedRecord'}});
    this.call = call ?? (async (op, packet, type) => decode(type, await transport.call('Releases', op, wire.toSignedRecordCbor(packet))));
  }
  packet(kind, type, value) {return signed(scope(kind, this.controller, this.recorder), wire[`to${type}Cbor`](value), this.signer.keyId, this.now(), this.signer.privateKey);}
  async submit(candidate) {
    const result = await this.call('submit', this.packet('submit', 'ReleaseSubmission', {candidate}), 'ReleaseSubmitted');
    need(result.candidateId === decode('ReleaseCandidate', candidate).candidateId && equal(result.digest, hash(candidate)), 'submission-receipt');
    const url = new URL(result.reviewUrl), endpoint = new URL(this.endpoint ?? result.reviewUrl);
    need(url.protocol === 'https:' && url.origin === endpoint.origin, 'review-url');
    return result;
  }
  async events() {
    const challenge = randomBytes(32);
    const packet = await this.call('pull-events', this.packet('event-query', 'ReleaseEventQuery', {challenge}), 'SignedRecord');
    const input = await authenticate(packet, 'events', this.recorder, this.controller, this.resolveKey, this.now);
    const batch = decode('ReleaseEventBatch', input.body);
    need(equal(batch.challenge, challenge) && batch.events.length <= 8, 'event-response');
    return batch.events;
  }
  fetch(approvalId) {return this.call('fetch-approval', this.packet('fetch-approval', 'ReleaseApprovalFetch', {approvalId}), 'SignedRecord');}
  async status(packet) {
    const input = await authenticate(packet, 'approval', this.recorder, this.controller, this.resolveKey, this.now, true);
    const approval = decode('ReleaseApproval', input.body), challenge = randomBytes(32);
    const response = await this.call('status', this.packet('status-query', 'ReleaseApprovalQuery', {approvalId: approval.approvalId, challenge}), 'SignedRecord');
    const statusInput = await authenticate(response, 'status', this.recorder, this.controller, this.resolveKey, this.now);
    const status = decode('ReleaseApprovalStatus', statusInput.body);
    need(equal(status.challenge, challenge) && equal(status.approval.input, packet.input) && equal(status.approval.signature, packet.signature) && +status.checkedAt === +statusInput.signedAt && this.now() - status.checkedAt <= 5000, 'current-approval-status');
    return {approval, withdrawn: status.withdrawn};
  }
  async ack(eventId) {
    const receipt = await this.call('acknowledge-event', this.packet('event-ack', 'ReleaseEventAck', {eventId}), 'ReleaseAcknowledged');
    need(receipt.eventId === eventId, 'event-ack-receipt');
  }
}
