import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {operatorPolicy} from '../release/configuration.mjs';
// Test the actual immutable images in credential-free, network-isolated
// containers. The output is review material, never an approval.
import {execFileSync} from 'node:child_process';
import {readFileSync, writeFileSync, mkdirSync, mkdtempSync} from 'node:fs';
import {resolve, join} from 'node:path';
import {randomUUID} from 'node:crypto';
import {tmpdir} from 'node:os';
import {setTimeout} from 'node:timers/promises';
import {wire, decode, hash, candidate, need} from '../release/policy.mjs';
import {KubernetesCanary} from '../release/kubernetes.mjs';
import {privateConfig} from '../release/main.mjs';
const root = resolve(import.meta.dirname, '..'), dir = environmentConfig.releaseProfileDir;
const drill = process.argv[2] === '--rollback-drill';
const supervisionDrill = drill && process.argv[3] === '--controller-crash';
const taskDrill = drill && process.argv[3] === '--tasks';
need(process.argv.length === 2 || process.argv.length === 3 && drill || process.argv.length === 4 && (supervisionDrill || taskDrill), 'Use: node scripts/prepare-release-candidate.mjs [--rollback-drill [--controller-crash | --tasks]]');
const taskDeployment = taskDrill ? JSON.parse(readFileSync(operatorFile('release-task-deployment.json'))) : undefined;
if(taskDrill)need(taskDeployment.proof?.helperReady && taskDeployment.proof?.binding?.installation===taskDeployment.installation,'live-task-adapter-required');
const runtime = privateConfig(join(dir, 'controller.json'));
const config = operatorPolicy();
const image = JSON.parse(readFileSync(operatorFile(drill ? 'release-rollback-image.json' : 'image.json')));
if (drill) need(image.purpose === 'isolated-rollback-drill' && image.failure?.readinessStatus === 204 && image.failure?.operationStatus === 503 && /^[0-9a-f]{64}$/.test(image.failure?.responseDigest), 'rollback-fixture');
const backend = new KubernetesCanary({config, kubeconfig: runtime.kubeconfig});
const before = await backend.inspect();
need(image.image !== before.image && /^[0-9a-f]{64}$/.test(image.sourceDigest) && /^[0-9a-f]{64}$/.test(image.recipeDigest), 'built-candidate');
const payload = wire.toDescribeRequestCbor({versions: ['0alpha1'], requiredFeatures: ['strict-cbor']});
const request = wire.encodeValue(new Map([['v', 1], ['id', 1], ['service', 'Discovery'], ['op', 'describe'], ['payload', {tag: 24, value: payload}]]));
const run = args => execFileSync('docker', args, {encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], maxBuffer: 4 * 1024 * 1024});
const id = randomUUID(), network = `opal-release-check-${id}`, owner = `opal.release-check=${id}`;
const observations = [];
const probeImage = 'node:26.1.0-bookworm-slim@sha256:8c676dbdbfc322815df09c910e80685fc5de08baa21c3b6189627e302b1bf131';
const probeScript = ("\nimport {request as httpRequest} from 'node:http';\nconst origin=process.argv[1],request=Buffer.from(process.argv[2],'base64');\nconst send=(path,body)=>new Promise((resolve,reject)=>{\n const r=httpRequest(origin+path,{method:body?'POST':'GET',headers:{host:'" + environmentConfig.canaryHost + "',...(body?{'content-type':'application/cbor','content-length':body.length}:{})},signal:AbortSignal.timeout(3000)},s=>{\n  const chunks=[];let size=0;s.on('data',b=>{size+=b.length;if(size>32768){s.destroy();reject(Error('size'));}else chunks.push(b);});\n  s.on('error',reject);s.on('end',()=>resolve({status:s.statusCode,type:s.headers['content-type'],body:Buffer.concat(chunks)}));\n });r.on('error',reject);r.end(body);\n});\nconst health=await send('/health/ready'),r=await send('/csil/v1/rpc',request);\nconsole.log(JSON.stringify({readinessStatus:health.status,operationStatus:r.status,type:r.type,body:r.body.toString('base64')}));\n");
run(['network', 'create', '--internal', '--label', owner, network]);
async function check(imageRef, role) {
  need(imageRef.startsWith(`${config.image_repository}@sha256:`), 'image-repository');
  const name = `opal-release-check-${role}-${id}`;
  // The local daemon may need registry access to pull. The container gets none
  // of the daemon's credentials, host paths, or network egress.
  try {
    run(['run', '-d', '--name', name, '--label', owner, '--network', network, '--read-only', '--user', '65532:65532', '--cap-drop=ALL', '--security-opt=no-new-privileges', '--memory=128m', '--cpus=0.25', '--pids-limit=64', '--env', 'OPAL_LISTEN=0.0.0.0:8080', '--env', ("OPAL_ORIGIN=https://" + environmentConfig.canaryHost), imageRef]);
    const detail = JSON.parse(run(['inspect', name]))[0];
    const extracted = join(mkdtempSync(join(tmpdir(), 'opal-artifact-bytes-')), 'opal');
    run(['cp', `${name}:/opal`, extracted]); const binaryDigest = hash(readFileSync(extracted)).toString('hex');
    if (role === 'candidate') need(binaryDigest === image.binarySha256, 'artifact-build-binding');
    const address = detail.NetworkSettings.Networks?.[network]?.IPAddress;
    need(typeof address === 'string' && /^\d+\.\d+\.\d+\.\d+$/.test(address), 'isolated-container-address');
    const origin = `http://${address}:8080`;
    let response, failure, result;
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        const output = run(['run', '--rm', '--name', `${name}-probe`, '--label', owner, '--network', network, '--read-only', '--user', '65532:65532', '--cap-drop=ALL', '--security-opt=no-new-privileges', '--memory=256m', '--cpus=0.5', '--pids-limit=64', probeImage, 'node', '--input-type=module', '-e', probeScript, origin, Buffer.from(request).toString('base64')]);
        result = JSON.parse(output.trim());
        const bytes = Uint8Array.from(Buffer.from(result.body, 'base64')); need(bytes.length <= 32768, 'response-limit');
        need(result.readinessStatus >= 200 && result.readinessStatus < 300, 'readiness-status');
        if (drill && role === 'candidate') {
          need(result.readinessStatus === image.failure.readinessStatus && result.operationStatus === image.failure.operationStatus && hash(bytes).toString('hex') === image.failure.responseDigest, 'expected-fixture-failure');
        } else {
          need(result.operationStatus === 200 && result.type?.split(';')[0] === 'application/cbor', 'protocol-status');
          const decoded = wire.decode(bytes); need(decoded.get('status') === 0 && decoded.get('variant') === 'ServiceDescription', 'protocol-result');
          decode('ServiceDescription', decoded.get('payload').value);
        }
        response = bytes; break;
      } catch (error) {
        failure = String(error.stderr || error.message).slice(0, 8192);
        const current = JSON.parse(run(['inspect', name]))[0];
        if (!current.State.Running) {
          writeFileSync(operatorFile(`release-artifact-${id}-${role}.failed.json`), JSON.stringify({role, image: imageRef, phase: 'startup', exitCode: current.State.ExitCode}) + '\n');
          throw Error('artifact-exited-before-check');
        }
        await setTimeout(250);
      }
    }
    if (!response) {
      const failureFile = operatorFile(`release-artifact-${id}-${role}.failed.json`);
      writeFileSync(failureFile, JSON.stringify({role, image: imageRef, failure}, null, 2) + '\n');
      throw Error(`image-check-failed: ${failureFile}`);
    }
    const file = operatorFile(`release-artifact-${id}-${role}.${drill && role === 'candidate' ? 'response' : 'cbor'}`); writeFileSync(file, response);
    observations.push({role, image: imageRef, containerImage: detail.Image, binaryDigest, readiness: true, readinessStatus: result.readinessStatus, operationStatus: result.operationStatus, expectedFailure: drill && role === 'candidate', requestDigest: hash(request).toString('hex'), responseDigest: hash(response).toString('hex'), responseFile: file, isolation: {network: 'internal', hostMounts: false, credentials: false, readonly: true, uid: 65532}});
    return response;
  } finally {
    const detail = JSON.parse(run(['inspect', name]))[0];
    need(detail.Config.Labels?.['opal.release-check'] === id, 'cleanup-owner');
    run(['rm', '-f', name]);
  }
}
try {
  const previousExpectedBody = await check(before.image, 'baseline'), candidateResponse = await check(image.image, 'candidate');
  // The drill must still satisfy the unchanged healthy protocol contract.
  // Never make its deliberate 503 response an approved success response.
  const expectedBody = drill ? previousExpectedBody : candidateResponse;
  const after = await backend.inspect(); need(before.uid === after.uid && before.generation === after.generation && before.image === after.image && before.configuration === after.configuration, 'target-changed');
  const value = {candidateId: id, target: {domain: config.target_domain, targetId: config.target_id, resourceUid: after.uid, generation: after.generation}, sourceDigest: Buffer.from(image.sourceDigest, 'hex'), recipeDigest: Buffer.from(image.recipeDigest, 'hex'), configurationDigest: Buffer.from(after.configuration, 'hex'), image: image.image, previousImage: after.image, schemaChanges: 'none', evidence: [{authority: runtime.controller.identity, kind: 'release-artifact-check', recordId: id}], expiresAt: new Date(Date.now() + 86400000 - 1000), checks: {readinessPath: '/health/ready', operationPath: '/csil/v1/rpc', requestBody: request, expectedBody, previousExpectedBody, successCount: 3, failureCount: 10, observationSeconds: 300, rollbackSeconds: 300}};
  value.explanation = {
    title: 'Test an isolated Magi discovery deployment',
    reason: 'Verify the authenticated approval, deployment, and recovery path before Magi can deploy its own changes.',
    changes: [("Replace only release-canary in " + environmentConfig.namespace + " with the prepared immutable image."), 'The image provides read-only protocol discovery with an updated schema. It does not enable agent execution.', 'Check readiness and the exact discovery response. Attempt to restore the previous image if checks fail.'],
    risks: ['This is a release-safety test, not the complete Magi demo or an agent-produced feature.', 'The isolated canary can be unavailable during replacement or recovery. Existing Q&A services are not targets.', 'No data migration is permitted. If recovery cannot be confirmed, the controller stops for operator attention.'],
  };
  if (drill) value.explanation = {
    title: 'Prove automatic rollback on the isolated Magi canary',
    reason: 'Install a deliberate protocol failure and prove that the release controller restores the current healthy image without a manual deployment change.',
    changes: [("Change only release-canary in " + environmentConfig.namespace + ". The fixture reports readiness but returns HTTP 503 for the protocol request."), 'Keep the healthy protocol response as the required result. After ten consecutive failed checks, restore the exact current image and require three successful checks.', 'Retain the approval, failure observations, restoration intent, image receipts, and final checks. No data migration or agent execution is included.'],
    risks: ['The isolated canary will be temporarily unusable. Existing Q&A and other Magi deployments are not targets.', 'Allow up to five minutes for candidate observation and five minutes for restoration. The controller stops for operator attention if restoration cannot be confirmed.', 'This is a deliberate failure test, not a feature release. It uses no task secrets, host mounts, or network egress.'],
  };
  if (supervisionDrill) {
    value.explanation.title = 'Prove rollback after a controller crash';
    value.explanation.reason = 'Verify that the supervised controller resumes from durable evidence after it stops during restoration.';
    value.explanation.changes.push('Stop the controller once with SIGKILL after the restoration API call returns but before its receipt is recorded. Let Kubernetes restart it with the same evidence volume.', 'Reconcile the restored target without repeating the image change. Require three successful baseline checks within the original restoration deadline.');
    value.explanation.risks.push('The deliberate controller crash can extend recovery time. If restart or storage recovery fails, operator attention is required. No production workload is involved.');
  }
  if (taskDrill) {
    value.explanation.title = 'Prove the Corndogs release-task path';
    value.explanation.reason = 'Verify that this exact approved candidate enters the dedicated Corndogs store, restores the healthy canary after a deliberate failure, and records one guarded task result.';
    value.explanation.changes.push('Bind the task to the exact candidate and approval digests. Require the original current task revision before the first deployment change.', 'Keep rollback independent of queue availability after deployment intent. Retain the result and retry task completion without repeating the deployment.');
    value.explanation.risks.push('This test does not interrupt the queue or database. Local recovery tests cover those failures. It does not prove distributed execution fencing.');
  }
  const bytes = wire.toReleaseCandidateCbor(value); candidate(bytes, config);
  const baselineProbe = await backend.probe(after, value.checks, true);
  need(baselineProbe.readiness && baselineProbe.operation && baselineProbe.bound, 'live-baseline-not-healthy');
  const report = {candidateId: id, candidateDigest: hash(bytes).toString('hex'), checkedAt: new Date().toISOString(), purpose: drill ? 'isolated-rollback-drill' : 'promotion', supervisionDrill, taskDrill, taskInstallation:taskDeployment?.installation, image: image.image, baseline: after, baselineProbe, build: image, observations, approved: false};
  mkdirSync(dir, {recursive: true, mode: 0o700});
  const file = join(dir, drill ? 'rollback-candidate.cbor' : 'candidate.cbor');
  writeFileSync(file, bytes, {mode: 0o600});
  writeFileSync(join(dir, `candidate-${id}.cbor`), bytes, {mode: 0o600, flag: 'wx'});
  writeFileSync(operatorFile(`release-candidate-${id}.json`), JSON.stringify(report, null, 2) + '\n');
  writeFileSync(operatorFile(drill ? 'release-rollback-candidate.json' : 'release-candidate.json'), JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify({candidateId: id, candidateDigest: report.candidateDigest, file, expiresAt: value.expiresAt, checkedImages: 2, purpose: report.purpose, approved: false}));
} finally {
  const detail = JSON.parse(run(['network', 'inspect', network]))[0]; need(detail.Labels?.['opal.release-check'] === id, 'network-cleanup-owner'); run(['network', 'rm', network]);
}
