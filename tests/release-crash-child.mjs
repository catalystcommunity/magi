// Test-only process. The parent supplies synthetic keys and an isolated DB.
import {readFileSync} from 'node:fs';
import {createPrivateKey} from 'node:crypto';
import {ApprovalRecorder} from '../release/approval.mjs';
const fixture = JSON.parse(readFileSync(process.argv[2]));
const recorder = new ApprovalRecorder({...fixture.options, now: () => new Date(fixture.at), createLogin: () => ({}), signer: {keyId: fixture.keyId, privateKey: createPrivateKey({key: fixture.key, format: 'jwk'})}, fault(point) {if (point === fixture.boundary) process.kill(process.pid, 'SIGKILL');}});
recorder.approve(fixture.auth, {...fixture.review, digest: Buffer.from(fixture.review.digest, 'hex')});
throw Error('Crash boundary was not reached');
