import {execFileSync,spawn} from 'node:child_process';
import {existsSync,mkdirSync} from 'node:fs';
import {resolve,join} from 'node:path';
const root=resolve(import.meta.dirname,'..');
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(cmd,args)=>execFileSync(cmd,args,{cwd:root,env,stdio:'inherit'});
// Compose owns only this named local test project. No cleanup runs on exit.
run('docker',['compose','-f','deploy/qa.compose.yaml','up','-d','--wait','--wait-timeout','60']);
mkdirSync(join(root,'.build'),{recursive:true});
run('go',['build','-o','.build/magi-qa','./cmd/magi-qa']);
if(!existsSync(join(root,'.local/qa/server.json'))) run('.build/magi-qa',['init',...process.argv.slice(2)]);
else if(process.argv.length>2) throw Error('Configuration already exists. Edit .local/qa/server.json; existing configuration was not overwritten.');
console.log('Use another terminal: .build/magi-qa ask -run first-question < question.txt');
console.log('Ctrl-C stops the API and worker. Docker volumes and configuration remain.');
const child=spawn(join(root,'.build/magi-qa'),['serve'],{cwd:root,env,stdio:'inherit'});
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,()=>child.kill(signal));
child.on('error',()=>{process.exitCode=1;});
child.on('exit',(code,signal)=>{process.exitCode=code??(signal?1:0);});
