import {need, hash, equal} from './policy.mjs';
import {matches} from './controller.mjs';

// A terminal label alone is not proof. Require the expected fault, both actual
// image transitions, and successful checks of the exact restored baseline.
export function rollbackProof({row, value, observations, preparation}) {
  const id = value.candidateId, q = value.checks, b = row.body;
  need(row.id === id && row.terminal && b.phase === 'restored' && b.reason === 'candidate-check-failed', 'rollback-not-restored');
  need(preparation.purpose === 'isolated-rollback-drill' && preparation.candidateId === id && preparation.candidateDigest === hash(row.candidate).toString('hex') && preparation.image === value.image, 'rollback-proposal-binding');
  need(equal(q.expectedBody, q.previousExpectedBody), 'rollback-contract-changed');
  const artifact = preparation.observations.find(o => o.role === 'candidate');
  need(artifact?.image === value.image && artifact.expectedFailure && artifact.readinessStatus === 204 && artifact.operationStatus === 503 && /^[0-9a-f]{64}$/.test(artifact.responseDigest), 'rollback-artifact-failure');
  need(preparation.build.image === value.image && preparation.build.failure.responseDigest === artifact.responseDigest && preparation.build.binarySha256 === artifact.binaryDigest && preparation.build.sourceDigest === Buffer.from(value.sourceDigest).toString('hex') && preparation.build.recipeDigest === Buffer.from(value.recipeDigest).toString('hex'), 'rollback-build-binding');
  for (let i = 1; i < observations.length; i++) need(observations[i].sequence > observations[i - 1].sequence && observations[i].at >= observations[i - 1].at, 'rollback-audit-order');
  const one = kind => {const found = observations.filter(o => o.kind === kind); need(found.length === 1, `rollback-count-${kind}`); return found[0];};
  const admission = one('admitted'), promotionIntent = one('promotion-intent'), promotion = one('promotion-observed');
  const restoreIntent = one('restore-intent'), restore = one('restore-observed'), completed = one('restored');
  const ordered = [admission, promotionIntent, promotion, restoreIntent, restore, completed];
  need(ordered.every((o, i) => i === 0 || o.sequence > ordered[i - 1].sequence), 'rollback-transition-order');
  need(restoreIntent.body.reason === 'candidate-check-failed', 'rollback-wrong-cause');
  const baseline = admission.body.baseline, deployed = promotion.body, restored = restore.body;
  for (const s of [baseline, deployed, restored]) need(s.uid === value.target.resourceUid && s.configuration === Buffer.from(value.configurationDigest).toString('hex'), 'rollback-target-binding');
  need(baseline.generation === value.target.generation && baseline.image === value.previousImage, 'rollback-baseline');
  need(deployed.generation === baseline.generation + 1 && deployed.image === value.image && deployed.marker === `${id}/promote`, 'rollback-promotion');
  need(restored.generation === baseline.generation + 2 && restored.image === value.previousImage && restored.marker === `${id}/restore`, 'rollback-restoration');
  need(matches(preparation.baseline, baseline) && matches(b.baseline, baseline) && matches(promotionIntent.body.before, baseline) && matches(restoreIntent.body.before, deployed) && matches(b.restored, restored), 'rollback-state-binding');
  const good = p => p.readiness === true && p.operation === true && p.bound === true;
  need(good(admission.body.probe) && good(preparation.baselineProbe), 'rollback-baseline-probe');
  const failed = observations.filter(o => o.kind === 'probe' && o.sequence > promotion.sequence && o.sequence < restoreIntent.sequence);
  need(failed.length >= q.failureCount && failed.slice(-q.failureCount).every(o => o.body.restoring === false && !good(o.body)), 'rollback-failure-threshold');
  const fault = failed.filter(o => o.body.readiness === true && o.body.operation === false && o.body.bound === true && typeof o.body.podUid === 'string' && o.body.responseDigest === artifact.responseDigest);
  need(fault.length > 0, 'rollback-fault-not-observed');
  const checks = observations.filter(o => o.kind === 'probe' && o.sequence > restore.sequence && o.sequence < completed.sequence);
  const passes = checks.slice(-q.successCount);
  need(passes.length === q.successCount && passes.every(o => o.body.restoring === true && good(o.body) && o.body.responseDigest === hash(q.previousExpectedBody).toString('hex')), 'rollback-restoration-checks');
  need(passes.every((o, i) => !i || o.at - passes[i - 1].at >= 1000), 'rollback-check-spacing');
  need(completed.at < restoreIntent.at + q.rollbackSeconds * 1000, 'rollback-restoration-deadline');
  return {candidateId: id, approvalId: row.approval_id, phase: 'restored', reason: b.reason, faultProbes: fault.length, failedChecks: failed.length, restoredChecks: passes.length, generations: [baseline.generation, deployed.generation, restored.generation], images: {baseline: baseline.image, failed: deployed.image, restored: restored.image}, restoreMilliseconds: completed.at - restoreIntent.at};
}
