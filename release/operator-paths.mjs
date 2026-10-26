import {homedir} from 'node:os';
import {isAbsolute, join} from 'node:path';
import {deployment} from '../config/deployment.mjs';

// Path selection has no filesystem side effects. Runtime services do not use it.
export function operatorStateDirectory(env = process.env, home = homedir()) {
  const path = env.MAGI_OPERATOR_STATE_DIR ?? (home === homedir() ? deployment.operatorStateDir : join(home, 'data/magi-evidence/deployments/default'));
  if (!isAbsolute(path)) throw Error('MAGI_OPERATOR_STATE_DIR must be an absolute path');
  return path;
}

export function operatorFile(name) {
  if (!name || name === '.' || name === '..' || /[/\\\x00]/.test(name)) throw Error('operator-file-name');
  return join(operatorStateDirectory(), name);
}
