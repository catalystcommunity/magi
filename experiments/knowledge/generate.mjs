import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, mkdirSync, copyFileSync, mkdtempSync, existsSync } from 'node:fs';
import { tmpdir, homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';

// Copy verified generators into a private working directory. Never change the
// shared generator installation or hand-written files beside generated output.
const root = dirname(fileURLToPath(import.meta.url));
const pin = JSON.parse(readFileSync(join(root, 'toolchain.json')));
const compiler = process.env.OPAL_CSILGEN || join(homedir(), '.local/bin/csilgen');
const generators = process.env.OPAL_CSIL_GENERATORS || join(homedir(), '.csilgen/generators');
const hash = p => createHash('sha256').update(readFileSync(p)).digest('hex');
if (execFileSync(compiler, ['--version'], {encoding: 'utf8'}).trim() !== `csilgen ${pin.csilgen_version}`) throw Error('Wrong CSIL compiler version');
if (process.platform === 'linux' && process.arch === 'x64' && hash(compiler) !== pin.csilgen_linux_x86_64_sha256) throw Error('Wrong CSIL compiler digest');
const scratch = mkdtempSync(join(tmpdir(), 'opal-csil-'));
mkdirSync(join(scratch, '.generators'));
for (const [name, digest] of Object.entries(pin.generators)) {
  const source = join(generators, name);
  if (hash(source) !== digest) throw Error(`Wrong generator digest: ${name}`);
  copyFileSync(source, join(scratch, '.generators', name));
}
const capture = process.argv.includes('--capture');
const input = join(root, capture ? 'schema/capture.csil' : 'schema/knowledge.csil');
const generatedDir = capture ? 'generated-capture' : 'generated';
execFileSync(compiler, ['validate', '-i', input], {cwd: scratch, stdio: 'inherit'});
function files(dir, prefix = '') {
  return readdirSync(dir, {withFileTypes: true}).flatMap(e => e.isDirectory() ? files(join(dir, e.name), join(prefix, e.name)) : [join(prefix, e.name)]);
}
const check = process.argv.includes('--check');
for (const lang of ['go', 'rust']) {
  const output = join(scratch, lang);
  execFileSync(compiler, ['generate', '-i', input, '-t', `${lang}-client`, '-o', output], {cwd: scratch, stdio: 'inherit'});
  if (check && JSON.stringify(files(output).sort()) !== JSON.stringify(files(join(root, generatedDir, lang)).sort())) throw Error(`Unexpected or missing generated files: ${lang}`);
  for (const name of files(output)) {
    const source = join(output, name);
    const target = resolve(root, generatedDir, lang, name);
    if (check) {
      if (!existsSync(target) || hash(source) !== hash(target)) throw Error(`Generated drift: ${lang}/${name}`);
    } else {
      mkdirSync(dirname(target), {recursive: true});
      copyFileSync(source, target);
    }
  }
}
console.log(`Verified toolchain and generated ${check ? 'check' : 'output'}; scratch: ${scratch}`);
