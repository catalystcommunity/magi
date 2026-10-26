import {need} from '../release/policy.mjs';

export function supervisionProof(before, after, faultAt) {
  return restartProof(before, after, faultAt, 137, 10000);
}
export function gracefulRestartProof(before, after, faultAt) {
  return restartProof(before, after, faultAt, 0, 30000);
}
function restartProof(before, after, faultAt, exitCode, delay) {
  need(before?.podUid && before.podUid === after?.podUid && before.deploymentUid === after.deploymentUid && before.image === after.image, 'supervision-target');
  need(Number.isSafeInteger(before.restarts) && after.restarts === before.restarts + 1, 'supervision-restart-count');
  const t = after.terminated;
  need(t?.exitCode === exitCode && t.reason !== 'OOMKilled' && (t.signal === undefined || t.signal === (exitCode === 137 ? 9 : 0)), 'supervision-termination');
  need(t.startedAt === before.startedAt && t.containerID === before.containerId, 'supervision-prior-container');
  const end = Date.parse(t.finishedAt), start = Date.parse(after.startedAt);
  // Kubernetes timestamps have one-second resolution. Keep the actual hook time.
  need(Number.isFinite(faultAt) && Date.parse(before.startedAt) <= faultAt && end + 1000 >= faultAt && end <= faultAt + delay && start + 1000 >= end && after.ready === true && after.containerId !== before.containerId, 'supervision-restart-order');
  return {podUid: after.podUid, deploymentUid: after.deploymentUid, image: after.image, previousContainerId: before.containerId, currentContainerId: after.containerId, restartCountBefore: before.restarts, restartCountAfter: after.restarts, faultAt, termination: t, restartedAt: after.startedAt};
}
