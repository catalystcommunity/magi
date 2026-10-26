import {openSync,fstatSync,readFileSync,closeSync} from 'node:fs';
import {renewApplications,renewalTransport} from './renewal.mjs';

// Kubernetes Secret projection uses symlinks. Check the opened target and bound
// its size, rather than rejecting the projected link or printing input errors.
const timer=setTimeout(()=>{console.error(JSON.stringify({event:'renewal-failed',code:'deadline'}));process.exit(1);},150000);
try {
  if(process.argv.length!==2)throw Error('arguments');
  const fd=openSync('/private/renewal.json','r');let config;
  try {const s=fstatSync(fd);if(!s.isFile() || s.size>16384 || s.size<1 || (s.mode & 0o007))throw Error('profile');config=JSON.parse(readFileSync(fd));}finally{closeSync(fd);}
  const result=await renewApplications(config, {...renewalTransport(config),emit:r=>console.log(JSON.stringify(r))});
  console.log(JSON.stringify(result));
} catch {
  // Upstream errors can contain untrusted text. Never serialize the exception.
  console.error(JSON.stringify({event:'renewal-failed',code:'renewal-or-verification-failed'}));process.exitCode=1;
} finally {clearTimeout(timer);}
