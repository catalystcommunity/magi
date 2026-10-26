import {environment} from './environment.mjs';
import {spawn,spawnSync} from 'node:child_process';
import {readFileSync,createReadStream} from 'node:fs';
import {createHash} from 'node:crypto';
import {createServer} from 'node:net';
import {homedir} from 'node:os';
import {join,resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
const root=resolve(import.meta.dirname,'..');
export const pins=JSON.parse(readFileSync(join(root,'question/model-pin.json')));
export async function hashFile(path){const h=createHash('sha256');for await(const chunk of createReadStream(path))h.update(chunk);return h.digest('hex');}
export async function startModel(port=0){
 const model=environment('MAGI_QA_GGUF')||join(homedir(),'models',pins.file);
 const binary=environment('MAGI_QA_LLAMA_SERVER')||join(homedir(),'llamacpp/llama.cpp/build/bin/llama-server');
 const modelHash=await hashFile(model);if(modelHash!==pins.sha256)throw Error('Small model hash differs from question/model-pin.json');
 const binaryHash=await hashFile(binary);
 const versionRun=spawnSync(binary,['--version'],{encoding:'utf8',timeout:10000,maxBuffer:1024*1024,stdio:['ignore','pipe','pipe']});
 if(versionRun.status!==0)throw Error('Cannot read llama.cpp build version');
 const version=(versionRun.stdout+versionRun.stderr).trim();
 if(!version.includes(pins.llama_cpp_version))throw Error('Build llama.cpp at the commit in question/model-pin.json');
 const reservation=createServer();await new Promise((ok,fail)=>{reservation.once('error',fail);reservation.listen(port,'127.0.0.1',ok);});
 port=reservation.address().port;await new Promise(ok=>reservation.close(ok));
 const args=['-m',model,'--host','127.0.0.1','--port',String(port),'-ngl','0','-t','2','-tb','2','-c','4096','-np','1','--no-webui','--alias',pins.alias,'--seed','1'];
 const child=spawn(binary,args,{env:{PATH:process.env.PATH,LANG:'C.UTF-8'},stdio:['ignore','pipe','pipe']});
 let log='',dropped=0,ended=false,startError;
 const append=chunk=>{const value=chunk.toString();if(log.length+value.length<=4*1024*1024)log+=value;else dropped+=value.length;};
 child.stdout.on('data',append);child.stderr.on('data',append);
 const completion=new Promise(ok=>{child.once('error',e=>{startError=e;ended=true;ok();});child.once('exit',()=>{ended=true;ok();});});
 const stop=async()=>{if(!ended){child.kill('SIGTERM');const timer=setTimeout(()=>child.kill('SIGKILL'),5000);await completion;clearTimeout(timer);}};
 try{
  const until=Date.now()+60000;let ready=false;
  while(Date.now()<until){
   if(ended||startError)throw Error('Local model process failed to start');
   await new Promise(ok=>setTimeout(ok,100));
   try{const response=await fetch(`http://127.0.0.1:${port}/health`,{signal:AbortSignal.timeout(1000)});ready=response.ok;await response.arrayBuffer();if(ready&&!ended)break;}catch{}
  }
  if(!ready)throw Error('Local model readiness timed out');
  return {endpoint:`http://127.0.0.1:${port}/v1/chat/completions`,metadata:{model,modelHash,binary,binaryHash,version,args},logs:()=>({text:log,dropped}),stop,completion};
 }catch(e){await stop();throw e;}
}
if(process.argv[1]&&import.meta.url===pathToFileURL(resolve(process.argv[1])).href){
 const port=Number(environment('MAGI_QA_MODEL_PORT')||8088);if(!Number.isInteger(port)||port<1||port>65535)throw Error('Invalid MAGI_QA_MODEL_PORT');
 const server=await startModel(port);
 console.log(`Small local CPU model ready: ${server.endpoint}`);
 console.log('Use only test questions. Ctrl-C stops this model process.');
 let stopping=false;for(const signal of ['SIGTERM','SIGINT'])process.on(signal,()=>{stopping=true;server.stop();});
 await server.completion;if(!stopping)process.exitCode=1;
}
