import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, mkdirSync, copyFileSync, mkdtempSync, existsSync, cpSync, renameSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { execFileSync } from 'node:child_process';
const root = resolve(import.meta.dirname, '..');
const pins = JSON.parse(readFileSync(join(root, 'csil/toolchain.json')));
const hash = p => createHash('sha256').update(readFileSync(p)).digest('hex');
// Prefer a repository-local pinned cache. A shared install can change while
// another ecosystem project works. Every artifact still needs its exact hash.
const cached = join(root, '.build/csilgen', pins.version);
const compiler = process.env.CSILGEN || (existsSync(join(cached, 'csilgen')) ? join(cached, 'csilgen') : join(homedir(), '.local/bin/csilgen'));
if (hash(compiler) !== pins.compilerSha256) throw Error('compiler checksum does not match csil/toolchain.json');
const scratch = mkdtempSync(join(tmpdir(), 'magi-csil-'));
// The 0alpha1 descriptor includes the input basename in its signed body.
// Keep that wire identity while publishing source and artifacts as Magi.
const compatibility = join(scratch, 'compatibility');
cpSync(join(root, 'csil'), compatibility, {recursive: true});
renameSync(join(compatibility, 'magi.csil'), join(compatibility, 'opal.csil'));
mkdirSync(join(scratch, '.generators'));
for (const [target, digest] of Object.entries(pins.generators)) {
  const name = `csilgen_${target}_generator.wasm`;
  const source = join(process.env.CSILGEN_MODULES || (existsSync(join(cached, 'generators')) ? join(cached, 'generators') : join(homedir(), '.csilgen/generators')), name);
  if (hash(source) !== digest) throw Error(`${target} generator checksum mismatch`);
  copyFileSync(source, join(scratch, '.generators', name));
}
for (const component of ['magi', 'events', 'workflows', 'speech', 'conversations', 'releases']) for (const target of Object.keys(pins.generators)) {
  const output = join(scratch, component, target);
  const input = component === 'magi' ? join(compatibility, 'opal.csil') : join(root, `csil/${component}.csil`);
  execFileSync(compiler, ['generate', '-i', input, '-t', `${target}-client`, '-o', output], {cwd: scratch, stdio: 'inherit'});
  if (component === 'magi') renameSync(join(output, 'opal.csil-schema.cbor'), join(output, 'magi.csil-schema.cbor'));
  const dest = component === 'magi' ? join(root, 'generated', target) : join(root, 'generated', component, target);
  const files = readdirSync(output).sort();
  if (process.argv.includes('--check')) {
    if (!existsSync(dest) || JSON.stringify(files) !== JSON.stringify(readdirSync(dest).sort())) throw Error(`file drift: ${target}`);
    for (const name of files) if (hash(join(output, name)) !== hash(join(dest, name))) throw Error(`content drift: ${target}/${name}`);
  } else {
    mkdirSync(dest, {recursive: true});
    for (const name of files) copyFileSync(join(output, name), join(dest, name));
  }
}
for (const component of ['magi', 'events', 'workflows', 'speech', 'conversations', 'releases']) {
  const base = component === 'magi' ? 'generated' : `generated/${component}`;
  if (hash(join(root, base, `go/${component}.csil-schema.cbor`)) !== hash(join(root, base, `typescript/${component}.csil-schema.cbor`))) throw Error('descriptor mismatch');
}
