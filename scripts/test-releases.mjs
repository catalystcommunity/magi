import {execFileSync} from 'node:child_process';
import {mkdirSync, readFileSync, writeFileSync, readdirSync} from 'node:fs';
import {resolve, join} from 'node:path';
import {createHash} from 'node:crypto';
const root = resolve(import.meta.dirname, '..'), dir = join(root, 'test-results'); mkdirSync(dir, {recursive: true});
const run = (cmd, args) => execFileSync(cmd, args, {cwd: root, env:{...process.env,MAGI_DEPLOYMENT_CONFIG:join(root,'config/deployment.example.json')}, encoding: 'utf8', maxBuffer: 8 * 1024 * 1024});
run('node', ['scripts/prepare-release-sdk.mjs']); run('npm', ['run', 'build:client']);
const hashes = {};
function scan(path) {for (const f of readdirSync(join(root, path), {withFileTypes: true})) {const p = join(path, f.name); if (f.isDirectory()) scan(p); else hashes[p] = createHash('sha256').update(readFileSync(join(root, p))).digest('hex');}}
for (const path of ['release', 'csil', 'generated/releases', 'config', 'experiments/release-rollback']) scan(path);
for (const path of ['tests/release-fixture.mjs', 'tests/release-approval.test.mjs', 'tests/release-recovery.test.mjs', 'tests/release-http.test.mjs', 'tests/release-kubernetes.test.mjs', 'scripts/test-releases.mjs', 'scripts/prepare-release-sdk.mjs', 'deploy/release-render.mjs', 'deploy/release-apply.mjs', 'deploy/release-bootstrap.mjs']) hashes[path] = createHash('sha256').update(readFileSync(join(root, path))).digest('hex');
for (const path of ['tests/release-crash-child.mjs', '.build/release-sdk/provenance.json']) hashes[path] = createHash('sha256').update(readFileSync(join(root, path))).digest('hex');
const stamp = new Date().toISOString().replaceAll(':', '-'), reports = [];
const tests = ['tests/releases-schema.test.mjs', 'tests/release-approval.test.mjs', 'tests/release-recovery.test.mjs', 'tests/release-http.test.mjs', 'tests/release-kubernetes.test.mjs', 'tests/release-delivery.test.mjs', 'tests/release-linkkeys.test.mjs', 'tests/release-rollback-proof.test.mjs'];
tests.push('tests/release-controller-runtime.test.mjs', 'tests/release-renewal.test.mjs', 'tests/release-supervision-proof.test.mjs', 'tests/release-init.test.mjs');
tests.push('tests/release-tasks.test.mjs');
tests.push('tests/release-renewal-service.test.mjs');
tests.push('tests/release-tasks-deployment.test.mjs');
tests.push('tests/release-task-proof.test.mjs');
tests.push('tests/storage-layout.test.mjs');
tests.push('tests/publication.test.mjs');
tests.push('tests/deployment-overlay.test.mjs');
hashes['deploy/environment-render.mjs'] = createHash('sha256').update(readFileSync(join(root,'deploy/environment-render.mjs'))).digest('hex');
for (const path of ['tests/fixtures/release-policy.json', 'scripts/release-image-context.mjs', 'scripts/operator-state.mjs']) hashes[path] = createHash('sha256').update(readFileSync(join(root, path))).digest('hex');
for (const path of ['scripts/check-release-task.mjs', 'scripts/release-task-proof.mjs']) hashes[path] = createHash('sha256').update(readFileSync(join(root,path))).digest('hex');
for (const path of ['deploy/release-tasks.mjs','deploy/release-tasks-render.mjs']) hashes[path] = createHash('sha256').update(readFileSync(join(root,path))).digest('hex');
for (const path of ['deploy/release-renewal-render.mjs','deploy/release-renewal-service.mjs','deploy/release-renewal.Dockerfile','scripts/build-release-renewal-image.mjs']) hashes[path] = createHash('sha256').update(readFileSync(join(root,path))).digest('hex');
hashes['scripts/check-release-renewal-isolation.mjs'] = createHash('sha256').update(readFileSync(join(root,'scripts/check-release-renewal-isolation.mjs'))).digest('hex');
for (const path of tests) hashes[path] = createHash('sha256').update(readFileSync(join(root, path))).digest('hex');
for (const path of ['scripts/release-supervision-proof.mjs', 'scripts/release-controller-inspection.mjs', 'scripts/check-release-controller-runtime.mjs']) hashes[path] = createHash('sha256').update(readFileSync(join(root, path))).digest('hex');
for (const path of ['scripts/build-release-rollback-image.mjs', 'scripts/prepare-release-candidate.mjs', 'scripts/check-release-rollback.mjs', 'deploy/release-rollback.Dockerfile', 'scripts/build-release-image.mjs', 'scripts/check-release-controller-authority.mjs', 'scripts/check-release-live.mjs', 'scripts/release-controller-state.mjs', 'deploy/release-controller.mjs', 'deploy/release-controller-render.mjs', 'deploy/release-renewal.mjs', 'deploy/release.Dockerfile']) hashes[path] = createHash('sha256').update(readFileSync(join(root, path))).digest('hex');
hashes['scripts/release-task-artifact.mjs'] = createHash('sha256').update(readFileSync(join(root, 'scripts/release-task-artifact.mjs'))).digest('hex');
for (let pass = 1; pass <= 3; pass++) {
  const file = join(dir, `${stamp}-release-${pass}.tap`);
  let output;
  try {output = run('node', ['--test', '--test-reporter=tap', ...tests]);}
  catch (e) {writeFileSync(`${file}.failed`, e.stdout || ''); writeFileSync(`${file}.stderr`, e.stderr || ''); throw Error(`Release tests failed; see ${file}.failed`);}
  writeFileSync(file, output);
  const count = name => Number(output.match(new RegExp(`^# ${name} (\\d+)$`, 'm'))?.[1]);
  if (!count('tests') || count('fail') || count('skipped')) throw Error('Incomplete release test run');
  reports.push({pass, tests: count('tests'), passed: count('pass')}); console.log(JSON.stringify(reports.at(-1)));
}
for (const [path, digest] of Object.entries(hashes)) if (createHash('sha256').update(readFileSync(join(root, path))).digest('hex') !== digest) throw Error(`Source changed: ${path}`);
const file = join(dir, `${stamp}-release.summary.json`);
writeFileSync(file, JSON.stringify({at: stamp, reports, sourceHashes: hashes, scope: 'Synthetic RP operations through the pinned SDK; local HTTP; simulated Kubernetes target and fault injection. Not a live human approval or live rollout.'}, null, 2) + '\n'); console.log(file);
