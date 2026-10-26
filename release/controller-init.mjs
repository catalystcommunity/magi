// PID 1 forwards shutdown and propagates the child's exit to Kubernetes.
// It never retries the child: Kubernetes owns process restart and backoff.
import {spawn} from 'node:child_process';
import {constants} from 'node:os';

export function supervise(command, args) {
  return new Promise(resolve => {
    const child = spawn(command, args, {stdio: 'inherit'});
    const term = () => child.kill('SIGTERM'), interrupt = () => child.kill('SIGINT');
    process.on('SIGTERM', term); process.on('SIGINT', interrupt);
    const cleanup = () => {process.removeListener('SIGTERM', term); process.removeListener('SIGINT', interrupt);};
    child.once('error', () => {cleanup(); resolve(1);});
    child.once('exit', (code, signal) => {cleanup(); resolve(code ?? 128 + (constants.signals[signal] ?? 1));});
  });
}
if (process.argv[1] === import.meta.filename) {
  if (process.argv.length !== 2) process.exitCode = 1;
  else process.exitCode = await supervise(process.execPath, [new URL('./controller-cluster.mjs', import.meta.url).pathname]);
}
