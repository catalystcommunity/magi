import {execFileSync,spawn} from 'node:child_process';
import {mkdtempSync,readFileSync,writeFileSync,mkdirSync,rmSync} from 'node:fs';
import {randomBytes,createHash} from 'node:crypto';
import {createServer} from 'node:net';
import {join,resolve} from 'node:path';
import {tmpdir} from 'node:os';
const root=resolve(import.meta.dirname,'..');
const id='opal-qa-smoke-'+randomBytes(6).toString('hex');
const state=mkdtempSync(join(tmpdir(),'opal-qa-smoke-'));
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(command,args,input)=>execFileSync(command,args,{cwd:root,env,encoding:'utf8',input,timeout:70000,stdio:['pipe','pipe','pipe']});
const compose=(...args)=>run('docker',['compose','-p',id,'-f','deploy/qa.compose.yaml',...args]);
const report={project:id,mode:'deterministic-compose-smoke',steps:[],composeHash:createHash('sha256').update(readFileSync(join(root,'deploy/qa.compose.yaml'))).digest('hex')};
let server,closed,started=false,logs='';
async function freePort(){const s=createServer();await new Promise((ok,fail)=>{s.once('error',fail);s.listen(0,'127.0.0.1',ok)});const port=s.address().port;await new Promise(ok=>s.close(ok));return port;}
async function stop(){if(server){server.kill('SIGTERM');const timer=setTimeout(()=>server?.kill('SIGKILL'),15000);await closed;clearTimeout(timer);server=undefined;}}
async function serve(config){server=spawn(join(root,'.build/magi-qa'),['serve','-config',config],{cwd:root,env,stdio:['ignore','ignore','pipe']});logs='';server.stderr.on('data',chunk=>{if(logs.length<65536)logs+=chunk});closed=new Promise(ok=>{server.once('exit',ok);server.once('error',ok)});for(let i=0;i<200;i++){if(logs.includes('API ready'))return;if(server.exitCode!==null)throw Error('smoke API stopped: '+logs);await new Promise(ok=>setTimeout(ok,100));}throw Error('smoke API readiness timeout');}
try{
 mkdirSync(join(root,'.build'),{recursive:true});run('go',['build','-o','.build/magi-qa','./cmd/magi-qa']);
 if(run('docker',['ps','-aq','--filter','label=com.docker.compose.project='+id]).trim()||run('docker',['volume','ls','-q','--filter','label=com.docker.compose.project='+id]).trim())throw Error('test project exists');
 started=true;compose('up','-d','--pull','never','--wait','--wait-timeout','60');report.steps.push('compose-start');
 const configDir=join(state,'config');const port=await freePort();
 run('.build/magi-qa',['init','-dir',configDir,'-listen',`127.0.0.1:${port}`,'-model-url','fixture']);
 const config=join(configDir,'server.json'),profile=join(configDir,'client.json');
 await serve(config);
 const args=['ask','-profile',profile,'-run','manual-smoke'];const answer=run('.build/magi-qa',args,'How does this test path work?\n').trim();
 if(!answer.includes('Test responder'))throw Error('unexpected fixture answer');
 const first=JSON.parse(run('.build/magi-qa',['evidence','-profile',profile,'-run','manual-smoke']));
 if(first.metadata.outcome!=='returned')throw Error('missing evidence');report.steps.push('cli-answer-and-evidence');
 await stop();compose('up','-d','--pull','never','--force-recreate','--wait','--wait-timeout','60');
 await serve(config);const replay=run('.build/magi-qa',args,'How does this test path work?\n').trim();
 const second=JSON.parse(run('.build/magi-qa',['evidence','-profile',profile,'-run','manual-smoke']));
 if(replay!==answer||JSON.stringify(first)!==JSON.stringify(second))throw Error('recreate changed retained answer or evidence');report.steps.push('container-recreation-preserves-evidence');
 report.outcome='pass';
}catch(e){report.outcome='failed';report.error=String(e.message);process.exitCode=1;}
finally{
 await stop();
 if(started){
  if(report.outcome!=='pass')report.containerLogs=compose('logs','--no-color','--tail','40');
  const services=compose('ps','-aq').trim().split('\n').filter(Boolean);
  for(const service of services)if(run('docker',['inspect','-f','{{index .Config.Labels "com.docker.compose.project"}}',service]).trim()!==id)throw Error('cleanup ownership mismatch');
  compose('down','--volumes','--remove-orphans');report.cleanup='removed only unique smoke project and its volumes';
 }
 rmSync(state,{recursive:true});
 mkdirSync(join(root,'test-results'),{recursive:true});
 const path=join(root,'test-results',new Date().toISOString().replaceAll(':','-')+'-qa-compose-smoke.json');writeFileSync(path,JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report));console.log(path);
}
