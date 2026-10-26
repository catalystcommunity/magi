import {environment} from './environment.mjs';
import {execFileSync,spawn} from 'node:child_process';
import {existsSync,mkdirSync} from 'node:fs';
import {resolve,join} from 'node:path';
import {startModel} from './qa-model.mjs';
const root=resolve(import.meta.dirname,'..'),env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(cmd,args)=>execFileSync(cmd,args,{cwd:root,env,stdio:'inherit'});
const children=[];let model,stopping=false,failed=false;
async function stop(){if(stopping)return;stopping=true;for(const child of children)child.kill('SIGTERM');if(model)await model.stop();}
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,stop);
function child(cmd,args,extra={}){const c=spawn(cmd,args,{cwd:root,env,stdio:'inherit',...extra});children.push(c);c.once('error',()=>{failed=true;stop();});c.once('exit',code=>{if(!stopping){failed=code!==0;stop();}});return c;}
async function ready(port){try{const r=await fetch(`http://127.0.0.1:${port}/health/ready`,{signal:AbortSignal.timeout(1000)});await r.arrayBuffer();return r.ok;}catch{return false;}}
async function component(port,script){if(await ready(port)){console.log(`Using the existing local component on ${port}; it will not be stopped on exit.`);return;}child(process.execPath,[script]);const until=Date.now()+60000;while(Date.now()<until&&!stopping){if(await ready(port))return;await new Promise(resolve=>setTimeout(resolve,200));}throw Error(`Component ${port} did not become ready. Check its configuration and port.`);}
try{
 await component(8094,'scripts/dev-speech.mjs');await component(8095,'scripts/dev-conversations.mjs');
 run('docker',['compose','-f','deploy/dialogue.compose.yaml','up','-d','--wait']);
 mkdirSync(join(root,'.build'),{recursive:true});run('go',['build','-o','.build/magi-demo','./cmd/magi-demo']);run('go',['build','-o','.build/magi-qa','./cmd/magi-qa']);run('go',['build','-o','.build/magi-task-worker','./cmd/magi-task-worker']);
 if(!existsSync(join(root,'.local/dialogue/server.json')))run('.build/magi-qa',['init','-dir','.local/dialogue','-listen','127.0.0.1:8096','-database','postgres://postgres:local-dialogue-only@127.0.0.1:25436/postgres?sslmode=disable','-corndogs','127.0.0.1:25082']);
 let endpoint=environment('MAGI_DEMO_MODEL_ENDPOINT');
 if(!endpoint){model=await startModel(0);endpoint=model.endpoint;console.log('Started the pinned local CPU model on its own port.');model.completion.then(()=>{if(!stopping){failed=true;stop();}});}
 const c=child(join(root,'.build/magi-demo'),[],{env:{...env,OPAL_DEMO_MODEL_ENDPOINT:endpoint}});
 const until=Date.now()+30000;while(!stopping&&Date.now()<until&&!await ready(8096))await new Promise(resolve=>setTimeout(resolve,200));
 if(stopping||!await ready(8096))throw Error('Demo workflow API did not become ready');
 child(join(root,'.build/magi-task-worker'),[],{env:{...env,OPAL_DEMO_MODEL_ENDPOINT:endpoint}});
 await new Promise(resolve=>c.once('exit',resolve));
}catch(e){failed=true;console.error(e.message);}finally{await stop();if(failed)process.exitCode=1;}
