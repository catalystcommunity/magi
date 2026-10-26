// Operator state is private and is not a test fixture or a build input.
import {mkdirSync, lstatSync} from 'node:fs';
import {resolve} from 'node:path';
import {operatorStateDirectory} from '../release/operator-paths.mjs';
export {operatorFile} from '../release/operator-paths.mjs';
export const operatorDirectory = operatorStateDirectory();
process.umask(0o077);
mkdirSync(operatorDirectory, {recursive: true, mode: 0o700});
const stat = lstatSync(operatorDirectory);
if (!stat.isDirectory() || stat.isSymbolicLink() || (stat.mode & 0o077)) throw Error('Operator state directory must be private');
// Live checks can write a fresh report without any previous test run.
mkdirSync(resolve(import.meta.dirname, '../test-results'), {recursive: true, mode: 0o700});
