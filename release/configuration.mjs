import {readFileSync, lstatSync} from 'node:fs';
import {policy} from './policy.mjs';
import {deployment} from '../config/deployment.mjs';

// A runtime receives its policy through its private profile, never its image.
export function runtimePolicy(runtime) {
  if (!runtime?.policy) throw Error('release-policy-required-in-private-profile');
  return policy(runtime.policy);
}

// Operator commands use a separate mode-600 local file. No public fallback.
export function operatorPolicy(path = deployment.releasePolicy) {
  const s = lstatSync(path);
  if (!s.isFile() || s.isSymbolicLink() || (s.mode & 0o077)) throw Error('release-policy-permissions');
  return policy(JSON.parse(readFileSync(path)));
}
