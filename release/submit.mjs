import {readFileSync} from 'node:fs';
import {createPrivateKey} from 'node:crypto';
import {privateConfig} from './main.mjs';
import {linkkeys} from './linkkeys.mjs';
import {ReleaseClient} from './client.mjs';
import {need} from './policy.mjs';

const [profile, file] = process.argv.slice(2);
try {
  need(profile && file && process.argv.length === 4, 'Use: node release/submit.mjs private-controller.json candidate.cbor');
  const runtime = privateConfig(profile), {resolveKey} = linkkeys(runtime);
  const signer = {keyId: runtime.signer.keyId, privateKey: createPrivateKey({key: runtime.signer.key, format: 'jwk'})};
  const client = new ReleaseClient({endpoint: runtime.approvalEndpoint, recorder: runtime.recorder, controller: runtime.controller, signer, resolveKey});
  const receipt = await client.submit(readFileSync(file));
  console.log(JSON.stringify({...receipt, digest: Buffer.from(receipt.digest).toString('hex'), approved: false}));
} catch {console.error('Candidate submission failed. No approval was issued. Check the candidate, policy, and current application key.'); process.exitCode = 1;}
