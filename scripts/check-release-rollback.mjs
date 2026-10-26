import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {operatorPolicy} from '../release/configuration.mjs';
// Read-only checks of a completed rollback. This does not approve or deploy.
import {readFileSync, writeFileSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import {createPrivateKey} from 'node:crypto';
import {DatabaseSync} from 'node:sqlite';
import {privateConfig} from '../release/main.mjs';
import {linkkeys} from '../release/linkkeys.mjs';
import {ReleaseClient} from '../release/client.mjs';
import {KubernetesCanary} from '../release/kubernetes.mjs';
import {matches} from '../release/controller.mjs';
import {rollbackProof} from '../release/rollback-proof.mjs';
import {decode, need, equal, wire, hash} from '../release/policy.mjs';
import {controllerState} from './release-controller-state.mjs';
import {controllerPod, operator} from './release-controller-inspection.mjs';
import {supervisionProof} from './release-supervision-proof.mjs';

const id = process.argv[2]; need(process.argv.length === 3 && /^[0-9a-f-]{36}$/.test(id), 'Use: node scripts/check-release-rollback.mjs candidate-uuid');
const runtime = privateConfig((environmentConfig.releaseProfileDir + "/controller.json")), config = operatorPolicy();
const state = controllerState(runtime), db = new DatabaseSync(state.path, {readOnly: true});
let row, observations, inbox, crash, supervision;
try {
  row = db.prepare('SELECT * FROM releases WHERE id=?').get(id); need(row, 'release-not-admitted'); row.body = JSON.parse(row.body);
  observations = db.prepare('SELECT sequence,at,kind,body FROM observations WHERE release_id=? ORDER BY sequence').all(id).map(o => ({...o, body: JSON.parse(o.body)}));
  // Match the authenticated event digest, not just an arbitrary inbox entry.
  inbox = db.prepare('SELECT id,digest,disposition,at FROM delivery_inbox').all();
  if (db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name='supervision_faults'").get()) crash = db.prepare('SELECT candidate_id,point,fired_at FROM supervision_faults WHERE candidate_id=?').get(id);
} finally {db.close();}
const value = decode('ReleaseCandidate', row.candidate), preparation = JSON.parse(readFileSync(operatorFile(`release-candidate-${id}.json`)));
const proof = rollbackProof({row, value, observations, preparation});
if (preparation.supervisionDrill) {
  need(state.source === 'supervised-controller' && crash?.point === 'after-restore-effect', 'controller-crash-not-observed');
  need(crash.fired_at >= observations.find(o => o.kind === 'restore-intent').at && crash.fired_at <= observations.find(o => o.kind === 'restore-observed').at, 'controller-crash-boundary');
  need(!observations.some(o => o.kind === 'restore-send-returned'), 'controller-crash-was-not-before-receipt');
  const before = JSON.parse(readFileSync(operatorFile(`release-controller-crash-${id}.json`)));
  need(before.candidateId === id && before.candidateDigest === preparation.candidateDigest, 'controller-crash-candidate-binding');
  supervision = supervisionProof(before.before, controllerPod(operator(runtime)), crash.fired_at);
}
const {resolveKey} = linkkeys(runtime), signer = {keyId: runtime.signer.keyId, privateKey: createPrivateKey({key: runtime.signer.key, format: 'jwk'})};
const client = new ReleaseClient({endpoint: runtime.approvalEndpoint, recorder: runtime.recorder, controller: runtime.controller, signer, resolveKey});
const packet = await client.fetch(row.approval_id), status = await client.status(packet);
need(equal(wire.toSignedRecordCbor(packet), row.packet) && equal(status.approval.candidate, row.candidate), 'rollback-approval-binding');
need(status.approval.human.accountId === config.approver.account_id && status.approval.human.domain === config.approver.domain, 'rollback-approver');
const backend = new KubernetesCanary({config, kubeconfig: runtime.kubeconfig}); await backend.cluster();
// Only select public receipt fields. Do not print the recorder's private profile.
const query = `import {readFileSync} from 'node:fs';import {DatabaseSync} from 'node:sqlite';
const r=JSON.parse(readFileSync('/private/approval.json')),db=new DatabaseSync(r.path,{readOnly:true});
console.log(JSON.stringify(db.prepare('SELECT id,kind,approval_id,candidate_id,at,acknowledged FROM release_events WHERE candidate_id=? ORDER BY sequence').all('${id}')));db.close();`;
const events = JSON.parse(execFileSync('kubectl', ['--kubeconfig', runtime.kubeconfig, '--request-timeout=10s', '-n', config.namespace, 'exec', '-i', 'deployment/release-approval', '--', 'node', '--input-type=module'], {input: query, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe']}));
const event = events.find(e => e.kind === 'approved'); need(event && event.approval_id === row.approval_id && event.acknowledged === 1, 'rollback-event-not-acknowledged');
const receipt = inbox.find(i => i.id === event.id);
const eventBytes = wire.toReleaseEventCbor({eventId: event.id, kind: event.kind, candidateId: event.candidate_id, approvalId: event.approval_id, at: new Date(event.at)});
need(receipt && ['admitted', 'already-admitted'].includes(receipt.disposition) && equal(receipt.digest, hash(eventBytes)), 'rollback-inbox-binding');
const before = await backend.inspect();
need(matches(before, row.body.restored), 'rollback-current-target-changed');
const probe = await backend.probe(before, value.checks, true); need(probe.readiness && probe.operation && probe.bound, 'rollback-live-probe');
need(matches(await backend.inspect(), before), 'rollback-target-changed-during-probe');
execFileSync(process.execPath, ['deploy/release-apply.mjs', '--verify'], {stdio: 'pipe'});
const report = {checkedAt: new Date().toISOString(), ...proof, candidateDigest: preparation.candidateDigest, controllerState: state.source, crash, supervision, human: status.approval.human, approvalWithdrawnAtCheck: status.withdrawn, event, inbox: {eventId: receipt.id, disposition: receipt.disposition, at: receipt.at}, liveTarget: before, probe, existingOpalDeployments: 'unchanged', observations};
const file = `test-results/${report.checkedAt.replaceAll(':', '-')}-release-rollback.json`; writeFileSync(file, JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({file, ...proof, existingOpalDeployments: 'unchanged'}));
