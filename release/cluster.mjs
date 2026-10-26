// The Kubernetes projection is a symlink owned by root. Only this entry point
// accepts it. The local operator entry point still requires a mode-600 file.
import {readFileSync} from 'node:fs';
import {serve} from './main.mjs';
try {await serve(JSON.parse(readFileSync('/private/approval.json')));}
catch {console.error('Release approval startup failed. Check mounted configuration and current LinkKeys attestation.'); process.exitCode = 1;}
