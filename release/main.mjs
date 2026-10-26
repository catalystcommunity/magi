import {runtimePolicy} from './configuration.mjs';
import {readFileSync, lstatSync} from 'node:fs';
import {createPrivateKey} from 'node:crypto';
import {ApprovalRecorder} from './approval.mjs';
import {releaseHTTP} from './http.mjs';
import {need} from './policy.mjs';
import {linkkeys} from './linkkeys.mjs';

export function privateConfig(path) {
  const stat = lstatSync(path); need(stat.isFile() && !stat.isSymbolicLink() && !(stat.mode & 0o077), 'private-config-permissions');
  return JSON.parse(readFileSync(path));
}
export async function serve(runtime) {
  const config = runtimePolicy(runtime);
  const {resolveKey, createLogin} = linkkeys(runtime);
  const signer = {keyId: runtime.signer.keyId, privateKey: createPrivateKey({key: runtime.signer.key, format: 'jwk'})};
  // Refuse an expired or unattested key before accepting human approvals.
  await resolveKey(runtime.app, signer.keyId);
  const recorder = new ApprovalRecorder({...runtime, config, signer, createLogin});
  const server = releaseHTTP({recorder, resolveKey});
  server.listen(runtime.port ?? 8098, runtime.host ?? '127.0.0.1', () => console.log('Magi release approval listener ready'));
  const stop = () => {server.close(() => {recorder.close(); process.exit(0);}); server.closeIdleConnections(); setTimeout(() => {server.closeAllConnections();}, 5000).unref();};
  process.once('SIGINT', stop); process.once('SIGTERM', stop);
}
async function main() {
  need(process.argv.length === 3, 'Use: node release/main.mjs /absolute/private-config.json');
  await serve(privateConfig(process.argv[2]));
}
if (process.argv[1] === import.meta.filename) main().catch(() => {console.error('Release approval startup failed. Check private configuration and current LinkKeys attestation.'); process.exitCode = 1;});
