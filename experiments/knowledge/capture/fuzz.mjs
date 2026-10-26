import { spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
const hash=p=>createHash('sha256').update(readFileSync(join(root,p))).digest('hex');
const files=['capture/stream.go','capture/stream_test.go','capture/fuzz.mjs'];
const sources=Object.fromEntries(files.map(p=>[p,hash(p)]));
const started=new Date().toISOString();
const name=`${started.replace(/[:.]/g,'-')}-capture-fuzz-${randomBytes(3).toString('hex')}`;
const args=['test','-json','-fuzz','FuzzChunkInvariant','-fuzztime=20s','-run','^$','./capture'];
const child=spawn('go',args,{cwd:root,env:{...process.env,GOMAXPROCS:'2',GOCACHE:process.env.GOCACHE||join(root,'.cache/go')},stdio:['ignore','pipe','pipe']});
let output='',stderr='';
child.stdout.on('data',b=>{output+=b;});child.stderr.on('data',b=>{stderr+=b;process.stderr.write(b);});
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,()=>child.kill(signal));
const exitCode=await new Promise((resolve,reject)=>{child.on('error',reject);child.on('close',resolve);});
const events=output.trim().split('\n').filter(Boolean).map(line=>JSON.parse(line));
let executions=0;
for(const event of events){const match=event.Output?.match(/execs: (\d+)/);if(match)executions=Math.max(executions,Number(match[1]));}
const summary={started,finished:new Date().toISOString(),exit_code:exitCode,command:['go',...args],workers:2,executions,sources,source_drift:files.filter(p=>hash(p)!==sources[p]),stderr,scope:'Chunk partition invariant with fixed synthetic registered patterns; not unknown-secret detection'};
mkdirSync(join(root,'results'),{recursive:true});
writeFileSync(join(root,'results',`${name}.jsonl`),output);
writeFileSync(join(root,'results',`${name}.summary.json`),JSON.stringify(summary,null,2)+'\n');
console.log(JSON.stringify({...summary,evidence:`results/${name}.summary.json`},null,2));
process.exitCode=exitCode||summary.source_drift.length||0;
