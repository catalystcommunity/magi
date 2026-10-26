import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, mkdirSync, copyFileSync, mkdtempSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';
const root = dirname(fileURLToPath(import.meta.url));
const hash = p => createHash('sha256').update(readFileSync(p)).digest('hex');
const compiler = join(homedir(), '.local/bin/csilgen');
const scratch = mkdtempSync(join(tmpdir(), 'opal-approval-csil-'));
mkdirSync(join(scratch, '.generators'));
const pins = {
  csilgen_typescript_generator: '2f6f22e302c0baa140db7a120525ece44298df3559949e1b7d7e512e7c4c767c',
  csilgen_go_generator: '3478752ad48c83963eb303e653cdb664cd28314ff4c9a7fe15c3fe589fdb015a',
};
if (hash(compiler) !== 'c0de42950d4da7cef723dbc9f7c7466c35b4daccdb707af01b0dd8df7ada2cfe') throw Error('compiler pin');
for (const [name, digest] of Object.entries(pins)) {
  const source = join(homedir(), '.csilgen/generators', `${name}.wasm`);
  if (hash(source) !== digest) throw Error('generator pin');
  copyFileSync(source, join(scratch, '.generators', `${name}.wasm`));
}
for (const target of ['typescript', 'go']) {
  const out = join(scratch, target);
  execFileSync(compiler, ['generate', '-i', join(root, 'schema/approval.csil'), '-t', `${target}-client`, '-o', out], {cwd: scratch, stdio: 'inherit'});
  const dest = join(root, 'generated', target);
  mkdirSync(dest, {recursive: true});
  const files = readdirSync(out).sort();
  if (process.argv.includes('--check')) {
    if (JSON.stringify(files) !== JSON.stringify(readdirSync(dest).sort())) throw Error('generated file list');
    for (const name of files) if (hash(join(out, name)) !== hash(join(dest, name))) throw Error(`generated drift: ${name}`);
  } else for (const name of files) copyFileSync(join(out, name), join(dest, name));
}
