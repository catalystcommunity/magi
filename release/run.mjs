import {deployment as environmentConfig} from '../config/deployment.mjs';
import {runtimePolicy} from './configuration.mjs';
import {readFileSync, existsSync} from 'node:fs';
import {createPrivateKey} from 'node:crypto';
import {setTimeout} from 'node:timers/promises';
import {privateConfig} from './main.mjs';
import {linkkeys} from './linkkeys.mjs';
import {ReleaseController} from './controller.mjs';
import {KubernetesCanary} from './kubernetes.mjs';
import {KubernetesAPI} from './kubernetes-api.mjs';
import {HTTPTransport} from '../protocol/http.mjs';
import {ReleaseClient} from './client.mjs';
import {ReleaseDelivery, DeliverySchedule} from './delivery.mjs';
import {wire, decode, need} from './policy.mjs';
import {ReleaseTasks} from './tasks.mjs';

export async function runController(runtime, mode, value, {onTick = () => {}, fault = () => {}} = {}) {
  const deliveryMode = ['--watch', '--once'].includes(mode);
  const config = runtimePolicy(runtime);
  const {resolveKey} = linkkeys(runtime);
  const transport = new HTTPTransport(runtime.approvalEndpoint, {codec: wire, responses: {'Releases/status': 'SignedRecord'}});
  const signer = {keyId: runtime.signer.keyId, privateKey: createPrivateKey({key: runtime.signer.key, format: 'jwk'})};
  const authorization = {recorder: runtime.recorder, controller: runtime.controller, signer, resolveKey, getStatus: async request => decode('SignedRecord', await transport.call('Releases', 'status', wire.toSignedRecordCbor(request)))};
  const backend = runtime.kubernetes ? new KubernetesAPI({config, ...runtime.kubernetes}) : new KubernetesCanary({config, kubeconfig: runtime.kubeconfig});
  const tasks = runtime.tasks ? new ReleaseTasks(runtime.tasks) : undefined;
  const controller = new ReleaseController({path: runtime.path, config, authorization, backend, tasks, fault: point => fault(point, controller)});
  try {
    if (deliveryMode) {
      const client = new ReleaseClient({endpoint: runtime.approvalEndpoint, recorder: runtime.recorder, controller: runtime.controller, signer, resolveKey});
      const delivery = new ReleaseDelivery({controller, client});
      const schedule = new DeliverySchedule();
      let stopped = false;
      const stop = () => {stopped = true;};
      process.once('SIGTERM', stop); process.once('SIGINT', stop);
      console.log(JSON.stringify({listener: 'approval-events', target: config.target_id}));
      try {
        do {
          try {
            const pull = schedule.due(Date.now()), results = await delivery.tick({pull});
            if (pull) schedule.completed(Date.now(), results);
            for (const result of results) console.log(JSON.stringify(result));
            onTick(results);
            if (mode === '--once' && results.some(r => r.delivery === 'unavailable' || r.disposition === 'pending-retry' || r.task && r.task !== 'completed' || ['step-interrupted-retry', 'recovery_required', 'restored'].includes(r.phase))) process.exitCode = 1;
          }
          catch {console.log(JSON.stringify({delivery: 'unavailable', action: 'retry-without-new-authority'})); if (mode === '--once') process.exitCode = 1;}
          if (mode === '--once' || stopped) break;
          await setTimeout(1000);
        } while (!stopped);
      } finally {process.removeListener('SIGTERM', stop); process.removeListener('SIGINT', stop);}
      return;
    }
    if (mode === '--evidence') {console.log(JSON.stringify(controller.evidence(value), null, 2)); return;}
    let row = mode === '--admit' ? await controller.admit(decode('SignedRecord', readFileSync(value))) : mode === '--retry-unstarted' ? await controller.retryUnstarted(value) : controller.read(value);
    let phase;
    while (true) {
      if (row.body.phase !== phase) console.log(JSON.stringify({candidateId: row.id, phase: row.body.phase, reason: row.body.reason}));
      phase = row.body.phase; if (row.terminal) {
        if (phase !== 'healthy') process.exitCode = 2;
        if (tasks) for (const result of await tasks.flush()) {console.log(JSON.stringify(result)); if (result.task !== 'completed') process.exitCode = 2;}
        break;
      }
      await setTimeout(1000); row = await controller.step(row.id);
    }
  } finally {controller.close();}
}
async function main() {
  const [path, mode, value] = process.argv.slice(2);
  const deliveryMode = ['--watch', '--once'].includes(mode);
  need(path && (deliveryMode ? !value && process.argv.length === 4 : ['--admit', '--resume', '--retry-unstarted', '--evidence'].includes(mode) && value && process.argv.length === 5), 'Use: node release/run.mjs private-config.json --watch | --once | --admit approval.cbor | --resume candidate-id | --retry-unstarted candidate-id | --evidence candidate-id');
  const runtime = privateConfig(path), marker = new URL((environmentConfig.releaseProfileDir + "/controller-supervised.json"), import.meta.url);
  if (mode !== '--evidence' && existsSync(marker)) need(JSON.parse(readFileSync(marker)).localPath !== runtime.path, 'controller-moved-to-supervised-runtime');
  await runController(runtime, mode, value);
}
if (process.argv[1] === import.meta.filename) main().catch(() => {console.error('Release operation refused or interrupted. Inspect retained evidence before resuming.'); process.exitCode = 1;});
