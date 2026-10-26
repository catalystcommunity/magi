import { spawn, execFileSync } from 'node:child_process';
import { readFileSync, readdirSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { createHash, randomBytes } from 'node:crypto';
const root = import.meta.dirname;
const hash = path => createHash('sha256').update(readFileSync(path)).digest('hex');
function files(dir, prefix = '') {
  return readdirSync(dir, {withFileTypes: true}).flatMap(e => e.name === 'results' ? [] : e.isDirectory() ? files(join(dir, e.name), join(prefix, e.name)) : [join(prefix, e.name)]);
}
const run = (program, args) => new Promise((resolve, reject) => {
  const p = spawn(program, args, {cwd: root, env: {...process.env, GOCACHE: process.env.GOCACHE || '/tmp/opal-knowledge-go-build-20261011'}, stdio: ['ignore', 'pipe', 'pipe']});
  let out = '', err = ''; p.stdout.on('data', b => out += b); p.stderr.on('data', b => err += b); p.on('error', reject); p.on('exit', (code, signal) => resolve({code, signal, out, err}));
});
const count = Number(process.argv[2] || 3); if (!Number.isInteger(count) || count < 1 || count > 10) throw Error('count must be 1..10');
for (const script of ['prepare.mjs', 'generate.mjs']) {
  const r = await run(process.execPath, [script, '--check']); if (r.code) throw Error(r.err || r.out);
}
const sourceFiles = files(root), sources = Object.fromEntries(sourceFiles.map(p => [p, hash(join(root, p))]));
const name = `${new Date().toISOString().replaceAll(':', '-')}-${randomBytes(3).toString('hex')}`;
const results = join(root, 'results'); mkdirSync(results, {recursive: true});
const runs = [];
for (let i = 0; i < count; i++) {
  const r = await run(process.execPath, ['--test', '--test-reporter=./reporter.mjs', 'codec.test.mjs', 'integration.test.mjs']);
  writeFileSync(join(results, `${name}-${i + 1}.jsonl`), r.out);
  const events = r.out.trim().split('\n').filter(Boolean).map(s => JSON.parse(s));
  const leaves = events.filter(e => e.name !== 'two independent LinkKeys servers and durable Magi approval');
  runs.push({code: r.code, signal: r.signal, leaf_passes: leaves.filter(e => e.event === 'test:pass').length, failures: events.filter(e => e.event === 'test:fail'), skips: leaves.filter(e => e.skip).length, stderr: r.err});
  console.log(`Run ${i + 1}: ${runs.at(-1).leaf_passes} leaf passes; ${runs.at(-1).failures.length} failures`);
  if (r.code) break;
}
const vet = await run('go', ['vet', './...']);
const drift = sourceFiles.filter(p => hash(join(root, p)) !== sources[p]);
const summary = {date: new Date().toISOString(), node: process.version, go: execFileSync('go', ['version'], {encoding: 'utf8'}).trim(), linkkeys_sdk: '03fcbd8e700715fb686bce2016136ecd8071f307', linkkeys_image: 'sha256:b537c0fa9d85ec035c7bd61285ebb0a6980795cce81cf8a3afa640218b64b567', runs, leaf_passes: runs.reduce((n,r) => n + r.leaf_passes, 0), vet: {code: vet.code, output: vet.out + vet.err}, source_drift: drift, sources};
writeFileSync(join(results, `${name}.summary.json`), JSON.stringify(summary, null, 2) + '\n');
console.log(`Evidence: results/${name}.summary.json`);
process.exitCode = runs.some(r => r.code || r.failures.length || r.skips) || vet.code || drift.length || runs.length !== count ? 1 : 0;
