import { spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { cpus, platform, arch } from 'node:os';

const root = dirname(fileURLToPath(import.meta.url));
const corpus = process.argv.includes('--corpus');
const countArg = process.argv.indexOf('--count');
const count = countArg < 0 ? 1 : Number(process.argv[countArg + 1]);
if (!Number.isInteger(count) || count < 1 || count > 10) throw Error('count must be 1..10');
const env = {...process.env, GOCACHE: process.env.GOCACHE || join(root, '.cache/go')};
function run(cmd, args, capture = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, {cwd: root, env, stdio: ['ignore', 'pipe', 'pipe']});
    let out = '', err = '';
    child.stdout.on('data', b => { out += b; if (!capture) process.stdout.write(b); });
    child.stderr.on('data', b => { err += b; process.stderr.write(b); });
    child.on('error', reject);
    child.on('close', code => resolve({code, out, err}));
  });
}
async function requirePass(cmd, args) {
  const result = await run(cmd, args);
  if (result.code !== 0) throw Error(`${cmd} failed with ${result.code}`);
}
await requirePass(process.execPath, ['generate.mjs', '--check']);
await requirePass(process.execPath, ['generate.mjs', '--capture', '--check']);
await requirePass('cargo', ['build', '--locked', '--offline', '--manifest-path', 'interop-rust/Cargo.toml']);
env.OPAL_RUST_CODEC = join(root, 'interop-rust/target/debug/opal-codec-check');
env.OPAL_KNOWLEDGE_INTEGRATION = '1';
env.OPAL_KNOWLEDGE_CORPUS = corpus ? '1' : '0';
const args = ['test', '-json', '-race', `-count=${count}`, '-timeout=8m', './...'];
const sourceFiles = ['store.go','storage.sql','storage_test.go','retrieval_test.go','codec_test.go','harness_test.go','support.go','support_test.go','capture/stream.go','capture/stream_test.go','capture/spool.go','capture/spool_test.go','capture/codec_test.go','schema/capture.csil','generate.mjs','run.mjs','fixtures/shared-reply.json','fixtures/recall-questions.json','go.mod','go.sum','interop-rust/Cargo.toml','interop-rust/Cargo.lock','interop-rust/src/main.rs'];
const hash = p => createHash('sha256').update(readFileSync(join(root,p))).digest('hex');
const sources = Object.fromEntries(sourceFiles.map(p=>[p,hash(p)]));
console.log(`Running disposable storage and codec tests${corpus ? ' with retrieval corpus' : ''}, count=${count}`);
const started = new Date().toISOString();
const result = await run('go', args, true);
const name = `${started.replace(/[:.]/g, '-')}-${corpus ? 'corpus' : 'checks'}-${randomBytes(3).toString('hex')}`;
const results = join(root, 'results');
mkdirSync(results, {recursive: true});
writeFileSync(join(results, `${name}.jsonl`), result.out);
const events = result.out.trim().split('\n').filter(Boolean).map(line => JSON.parse(line));
const tests = events.filter(e => e.Test && ['pass','fail','skip'].includes(e.Action));
const parents = new Set();
for (const e of tests) { const parts = e.Test.split('/'); for (let n = 1; n < parts.length; n++) parents.add(parts.slice(0,n).join('/')); }
const leaves = tests.filter(e => !parents.has(e.Test));
const measurements = events.filter(e => e.Output?.includes('RETRIEVAL_RESULT ')).map(e => JSON.parse(e.Output.split('RETRIEVAL_RESULT ')[1]));
const summary = {
  started, finished: new Date().toISOString(), exit_code: result.code,
  command: ['go', ...args], corpus, count,
  passed_test_events: tests.filter(e => e.Action === 'pass').length,
  failed_test_events: tests.filter(e => e.Action === 'fail').length,
  skipped_test_events: tests.filter(e => e.Action === 'skip').length,
  passed_leaf_events: leaves.filter(e => e.Action === 'pass').length,
  skipped_leaf_events: leaves.filter(e => e.Action === 'skip').length,
  failures: tests.filter(e => e.Action === 'fail').map(e => e.Test),
  measurements,
  toolchain: JSON.parse(readFileSync(join(root, 'toolchain.json'))),
  schema_sha256: createHash('sha256').update(readFileSync(join(root, 'schema/knowledge.csil'))).digest('hex'),
  sources, source_drift: sourceFiles.filter(p=>hash(p)!==sources[p]),
  host: {platform: platform(), arch: arch(), cpu: cpus()[0]?.model, logical_cpus: cpus().length},
  stderr: result.err
};
writeFileSync(join(results, `${name}.summary.json`), JSON.stringify(summary, null, 2) + '\n');
console.log(JSON.stringify({...summary, toolchain: undefined, stderr: undefined}, null, 2));
console.log(`Evidence: results/${name}.jsonl`);
process.exitCode = result.code || summary.source_drift.length || 0;
