import {requireDeployment} from '../config/deployment.mjs';
if(process.argv.includes('--push'))requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {execFileSync} from 'node:child_process';
import {mkdtempSync,mkdirSync,cpSync,readFileSync,writeFileSync,readdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {createHash} from 'node:crypto';
if(process.argv.slice(2).some(v=>v!=='--push'))throw Error('Use: node scripts/build-release-renewal-image.mjs [--push]');
const root=resolve(import.meta.dirname,'..'),context=mkdtempSync(join(tmpdir(),'opal-renewal-image-')),app=join(context,'app');mkdirSync(app);
const hash=b=>createHash('sha256').update(b).digest('hex');
// Copy code and verified public SDK artifacts only. No task runner, controller,
// approval server, compiler, private profile, or operator credentials.
for(const path of ['config/deployment.mjs','config/deployment.example.json','release/renewal.mjs','release/renewal-run.mjs','release/linkkeys.mjs','release/policy.mjs','deploy/release-renewal.mjs','protocol/profile.mjs','generated/releases/typescript/releases.csil-schema.cbor','.build/codec.gen.js','.build/types.gen.js','.build/releases','.build/release-sdk']){
  mkdirSync(join(app,path,'..'),{recursive:true});cpSync(join(root,path),join(app,path),{recursive:true});
}
cpSync(join(root,'deploy/release-renewal.Dockerfile'),join(context,'Dockerfile'));
const files={};function scan(path=''){for(const f of readdirSync(join(app,path),{withFileTypes:true})){const p=join(path,f.name);if(f.isDirectory())scan(p);else files[p]=hash(readFileSync(join(app,p)));}}scan();
const sourceDigest=hash(JSON.stringify(files)),recipeDigest=hash(readFileSync(join(context,'Dockerfile')));
const repository=environmentConfig.repository,tag=`${repository}:release-renewal-${sourceDigest.slice(0,16)}`;
execFileSync('docker',['build','--platform=linux/amd64','-t',tag,context],{stdio:'inherit'});
execFileSync('docker',['run','--rm','--network=none','--read-only','--cap-drop=ALL','--entrypoint=node',tag,'--input-type=module','-e',"await import('./release/renewal.mjs');console.log('renewal-image-import: pass')"],{stdio:'inherit'});
if(process.argv.includes('--push')){
  execFileSync('docker',['push',tag],{stdio:'inherit'});
  const digests=JSON.parse(execFileSync('docker',['image','inspect',tag,'--format','{{json .RepoDigests}}'],{encoding:'utf8'}));
  const image=digests.find(v=>v.startsWith(`${repository}@sha256:`));if(!image)throw Error('image-digest');
  writeFileSync(operatorFile('release-renewal-image.json'),JSON.stringify({image,sourceDigest,recipeDigest,files,builtAt:new Date().toISOString()},null,2)+'\n');console.log(image);
}else console.log(`Built ${tag}`);
