import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, mkdirSync, writeFileSync, readFileSync, chmodSync, symlinkSync, existsSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {execFileSync, spawnSync} from 'node:child_process';
import {runtimePolicy, operatorPolicy} from '../release/configuration.mjs';
import {copyReleaseImage, releaseImagePaths} from '../scripts/release-image-context.mjs';
import {operatorStateDirectory, operatorFile} from '../release/operator-paths.mjs';

test('operator paths default outside the checkout and allow an absolute override', () => {
  assert.equal(operatorStateDirectory({}, '/test-home'), '/test-home/data/magi-evidence/deployments/default');
  assert.equal(operatorStateDirectory({MAGI_OPERATOR_STATE_DIR:'/test-state'}), '/test-state');
  assert.throws(() => operatorStateDirectory({MAGI_OPERATOR_STATE_DIR:''}), /absolute/);
  assert.throws(() => operatorStateDirectory({MAGI_OPERATOR_STATE_DIR:'.local'}), /absolute/);
  for (const name of ['../escape','/absolute','a/b','a\\b','.','..','']) assert.throws(() => operatorFile(name), /operator-file-name/);
});

const fixture = JSON.parse(readFileSync(new URL('./fixtures/release-policy.json', import.meta.url)));
function temporary(t) {
  const dir = mkdtempSync(join(tmpdir(), 'magi-storage-test-'));
  t.after(() => rmSync(dir, {recursive: true, force: true}));
  return dir;
}

test('runtime requires a valid explicit policy without a public fallback', () => {
  assert.throws(() => runtimePolicy({}), /release-policy-required/);
  assert.deepEqual(runtimePolicy({policy: fixture}), fixture);
  assert.throws(() => runtimePolicy({policy: {...fixture, target_domain: 'example.test'}}), /test-target-only/);
  assert.ok(Object.isFrozen(runtimePolicy({policy: fixture}).approver));
});

test('operator policy must be a private regular file', t => {
  const dir = temporary(t), file = join(dir, 'policy.json');
  writeFileSync(file, JSON.stringify(fixture), {mode: 0o600});
  assert.deepEqual(operatorPolicy(file), fixture);
  chmodSync(file, 0o644);
  assert.throws(() => operatorPolicy(file), /release-policy-permissions/);
  chmodSync(file, 0o600);
  const link = join(dir, 'link.json'); symlinkSync(file, link);
  assert.throws(() => operatorPolicy(link), /release-policy-permissions/);
  assert.throws(() => operatorPolicy(dir), /release-policy-permissions/);
  assert.throws(() => operatorPolicy(join(dir, 'missing')), /ENOENT/);
});

test('private outputs are ignored but public JSON inputs remain eligible', t => {
  const dir = temporary(t);
  execFileSync('git', ['init', '--quiet', dir]);
  writeFileSync(join(dir, '.gitignore'), readFileSync(new URL('../.gitignore', import.meta.url)));
  const ignored = path => spawnSync('git', ['check-ignore', '--no-index', '--quiet', path], {cwd: dir}).status;
  for (const path of ['test-results/report.json', 'experiments/speech/results/review.json', '.local/deployments/example/release-policy.json', 'config/real.json', 'runtime.private.json', 'runtime.local.json', '.env', '.env.production']) assert.equal(ignored(path), 0, path);
  for (const path of ['config/release-policy.example.json', 'package.json', 'package-lock.json', 'tsconfig.json', 'speech/model-pins.json', 'tests/fixtures/release-policy.json', '.env.example']) assert.equal(ignored(path), 1, path);
});

test('release image copy excludes private configuration and historical reports', t => {
  const dir = temporary(t), source = join(dir, 'source'), app = join(dir, 'app');
  mkdirSync(source); mkdirSync(app);
  for (const path of releaseImagePaths) {
    const target = join(source, path);
    if (/\.(?:m?js|json|cbor)$/.test(path)) {
      mkdirSync(join(target, '..'), {recursive: true}); writeFileSync(target, 'public fixture');
    } else {mkdirSync(target, {recursive: true}); writeFileSync(join(target, 'fixture'), 'public fixture');}
  }
  for (const path of ['.local', 'config', 'test-results']) {
    mkdirSync(join(source, path), {recursive:true}); writeFileSync(join(source, path, 'private.json'), 'PRIVATE SENTINEL');
  }
  copyReleaseImage(source, app);
  for (const path of releaseImagePaths) assert.ok(existsSync(join(app, path)), path);
  for (const path of ['.local', 'config/private.json', 'test-results']) assert.equal(existsSync(join(app, path)), false, path);
});
