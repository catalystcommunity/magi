// Compile an immutable SDK snapshot. Never modify or reset the sibling checkout.
import {execFileSync} from 'node:child_process';
import {mkdtempSync, mkdirSync, writeFileSync, readFileSync, readdirSync, cpSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';
import {createHash} from 'node:crypto';
const root = resolve(import.meta.dirname, '..');
const checkout = resolve(root, '../linkkeys');
const pin = '03fcbd8e700715fb686bce2016136ecd8071f307';
const prefix = 'sdks/regular-rp/typescript';
const scratch = mkdtempSync(join(tmpdir(), 'opal-release-sdk-'));
const archive = execFileSync('git', ['archive', pin, prefix], {cwd: checkout, maxBuffer: 16 * 1024 * 1024});
const tar = join(scratch, 'sdk.tar'); writeFileSync(tar, archive);
execFileSync('tar', ['-xf', tar, '-C', scratch]);
const output = join(root, '.build/release-sdk'); mkdirSync(output, {recursive: true});
execFileSync(process.execPath, [join(root, 'node_modules/typescript/bin/tsc'), '-p', join(scratch, prefix, 'tsconfig.build.json'), '--typeRoots', join(checkout, prefix, 'node_modules/@types'), '--outDir', output], {stdio: 'inherit'});
cpSync(join(scratch, prefix, 'package.json'), join(output, 'package.json'));
cpSync(join(scratch, prefix, 'LICENSE'), join(output, 'LICENSE'));
const hashes = {};
function scan(relative = '') {
  for (const file of readdirSync(join(output, relative), {withFileTypes: true})) {
    const path = join(relative, file.name);
    if (file.isDirectory()) scan(path);
    else if (path !== 'provenance.json') hashes[path] = createHash('sha256').update(readFileSync(join(output, path))).digest('hex');
  }
}
scan();
writeFileSync(join(output, 'provenance.json'), JSON.stringify({pin, archive_sha256: createHash('sha256').update(archive).digest('hex'), compiler: 'typescript-5.9.3', files: hashes}, null, 2) + '\n');
console.log(`Prepared LinkKeys regular-RP SDK ${pin}`);
