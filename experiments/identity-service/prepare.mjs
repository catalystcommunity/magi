import { execFileSync } from 'node:child_process';
import { resolve, join } from 'node:path';
const checkout = resolve(import.meta.dirname, '../../../linkkeys');
const pin = '03fcbd8e700715fb686bce2016136ecd8071f307';
if (execFileSync('git', ['rev-parse', 'HEAD'], {cwd: checkout, encoding: 'utf8'}).trim() !== pin) throw Error('LinkKeys checkout pin differs');
if (execFileSync('git', ['status', '--porcelain', '--', 'sdks/regular-rp/typescript'], {cwd: checkout, encoding: 'utf8'}).trim()) throw Error('LinkKeys SDK has local changes');
const sdk = join(checkout, 'sdks/regular-rp/typescript');
execFileSync(process.execPath, [join(sdk, 'node_modules/typescript/bin/tsc'), '-p', join(sdk, 'tsconfig.build.json'), '--outDir', join(import.meta.dirname, '.sdk')], {stdio: 'inherit'});
