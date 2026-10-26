import {readFileSync, existsSync} from 'node:fs';
import {createServer} from 'node:http';
import {runController} from './run.mjs';
import {need} from './policy.mjs';
import {crashDrill} from './crash-drill.mjs';

async function main() {
  const runtime = JSON.parse(readFileSync('/private/controller.json'));
  need(runtime.path === '/data/controller.sqlite' && !runtime.kubeconfig && runtime.kubernetes?.caFile === '/kubernetes/ca.crt' && runtime.kubernetes?.tokenFile === '/kubernetes/token', 'controller-runtime-boundary');
  need(existsSync(runtime.path), 'controller-seed-required');
  let lastTick = Date.now();
  const health = createServer((req, res) => {
    if (!['/health/live', '/health/ready'].includes(req.url)) {res.writeHead(404); res.end(); return;}
    res.writeHead(Date.now() - lastTick < 120000 ? 204 : 503); res.end();
  });
  health.listen(8099, '0.0.0.0');
  try {await runController(runtime, '--watch', undefined, {onTick: () => {lastTick = Date.now();}, fault: crashDrill(runtime.crashDrill)});}
  finally {health.close(); health.closeAllConnections();}
}
main().catch(() => {console.error('Controller startup failed. Check the retained state and restricted runtime profile.'); process.exitCode = 1;});
