import { spawn, execFileSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:net';
import { homedir } from 'node:os';
import { collectLines } from './test-events.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const split = process.argv[2] || 'dev';
if (!['dev','test'].includes(split)) throw Error('Use dev or test');
const input = split === 'dev' ? '2026-10-13T00-29-24-846Z-comparison-dev-5f3fe0' : '2026-10-13T00-31-18-636Z-comparison-test-2c424d';
const candidates = join(root,'results',input,'cases.json');
const files = ['support.go','support_test.go','store.go','storage.sql','comparison_test.go','harness_test.go','retrieval/corpus.json','retrieval/questions.json','sufficiency/prompt.txt','sufficiency/protocol.json','sufficiency/worker.py','sufficiency/run.mjs','sufficiency/test-events.mjs'];
const hash = path => createHash('sha256').update(readFileSync(path)).digest('hex');
const sources = Object.fromEntries(files.map(p=>[p,hash(join(root,p))]));
const model = process.env.OPAL_PLANNER_MODEL || join(homedir(),'models/qwen2.5-1.5b-instruct-q4_k_m.gguf');
const binary = process.env.OPAL_LLAMA_SERVER || join(homedir(),'llamacpp/llama.cpp/build/bin/llama-server');
const modelHash = hash(model), binaryHash = hash(binary);
const listener = createServer();
await new Promise((resolve,reject)=>{listener.once('error',reject);listener.listen(0,'127.0.0.1',resolve);});
const port = listener.address().port;
await new Promise(resolve=>listener.close(resolve));
const started = new Date().toISOString();
const resultDir = join(root,'results',`${started.replace(/[:.]/g,'-')}-support-${split}-${randomBytes(3).toString('hex')}`);
mkdirSync(resultDir,{recursive:true,mode:0o700});
const args = ['-m',model,'--host','127.0.0.1','--port',String(port),'-ngl','0','-t','2','-tb','2','-c','4096','-np','1','--no-webui'];
const server = spawn('nice',['-n','10',binary,...args],{cwd:root,env:{PATH:process.env.PATH,LANG:'C.UTF-8',CUDA_VISIBLE_DEVICES:''},stdio:['ignore','pipe','pipe']});
let log='', serverError, serverExited=false, child;
server.stdout.on('data',b=>{log+=b;});server.stderr.on('data',b=>{log+=b;});server.on('error',e=>{serverError=e;});
const serverDone = new Promise(resolve=>server.on('close',code=>{serverExited=true;resolve(code);}));
for (const signal of ['SIGTERM','SIGINT']) process.on(signal,()=>{child?.kill(signal);server.kill('SIGTERM');});
try {
  let ready=false;
  for(let n=0;n<120;n++) {
    if(serverError||serverExited) throw Error('Local model failed to start');
    try {ready=(await fetch(`http://127.0.0.1:${port}/health`,{signal:AbortSignal.timeout(1000)})).ok;} catch {}
    if(ready)break;
    await new Promise(resolve=>setTimeout(resolve,250));
  }
  if(!ready)throw Error('Model startup deadline exceeded');
  console.log(`Local CPU support check: ${split}. Labels stay outside inference.`);
  child=spawn('go',['test','-json','-race','-count=1','-timeout=35m','-run','TestSupport','.'],{cwd:root,env:{...process.env,GOCACHE:process.env.GOCACHE||join(root,'.cache/go'),OPAL_KNOWLEDGE_INTEGRATION:'1',OPAL_SUPPORT_INFERENCE:'1',OPAL_SUPPORT_SPLIT:split,OPAL_SUPPORT_CANDIDATES:candidates,OPAL_PLANNER_PORT:String(port)},stdio:['ignore','pipe','pipe']});
  let output='',pending='',stderr='',parseErrors=0;const cases=[];
  const accept=collectLines(text=>{const marker=text.indexOf('SUPPORT_CASE ');if(marker>=0){const row=JSON.parse(text.slice(marker+13));cases.push(row);console.log(`${row.question}: ${row.validation}, mode ${row.assessment.mode}, agreement ${row.mode_match}`);}});
  child.stdout.on('data',b=>{output+=b;pending+=b;let n;
    while((n=pending.indexOf('\n'))>=0){const line=pending.slice(0,n);pending=pending.slice(n+1);let e;try{e=JSON.parse(line);}catch{continue;}
      try{accept(e);}catch{parseErrors++;stderr+='Invalid support evidence line\n';}
      if(e.Action==='fail')console.error(`Failed: ${e.Test||e.Package}`);
    }
  });
  child.stderr.on('data',b=>{stderr+=b;process.stderr.write(b);});
  const exitCode=await new Promise((resolve,reject)=>{child.once('error',reject);child.once('close',resolve);});
  writeFileSync(join(resultDir,'tests.jsonl'),output,{mode:0o600});
  writeFileSync(join(resultDir,'cases.json'),JSON.stringify(cases,null,2)+'\n',{mode:0o600});
  const values=cases.map(c=>c.elapsed_ms).sort((a,b)=>a-b);
  const drift=files.filter(p=>hash(join(root,p))!==sources[p]);
  const summary={started,finished:new Date().toISOString(),exit_code:exitCode,split,sources,source_drift:drift,parse_errors:parseErrors,
    candidates:{file:candidates,sha256:hash(candidates)},model:{path:model,sha256:modelHash,binary,binary_sha256:binaryHash,args},
    runtime:{node:process.version,go:execFileSync('go',['version'],{encoding:'utf8'}).trim()},
    questions:cases.length,valid:cases.filter(c=>c.validation==='valid').length,mode_matches:cases.filter(c=>c.mode_match).length,
    complete_evidence:cases.filter(c=>c.complete_evidence).length,supported_questions:cases.filter(c=>c.required_count>0).length,
    questions_with_irrelevant_citations:cases.filter(c=>c.irrelevant_citations.length).length,
    runtime_errors:cases.filter(c=>c.error).length,p95_ms:values[Math.ceil(values.length*.95)-1],max_ms:values.at(-1),
    prompt_tokens:cases.reduce((n,c)=>n+(c.timings.prompt_n||0),0),output_tokens:cases.reduce((n,c)=>n+(c.timings.predicted_n||0),0),stderr,
    scope:'Extractive selection, not answer correctness; fixed synthetic inputs; no paid endpoint or public API'};
  writeFileSync(join(resultDir,'summary.json'),JSON.stringify(summary,null,2)+'\n',{mode:0o600});
  console.log(JSON.stringify({evidence:resultDir,questions:summary.questions,valid:summary.valid,mode_matches:summary.mode_matches,complete_evidence:summary.complete_evidence},null,2));
  process.exitCode=exitCode||drift.length||parseErrors||(cases.length!==(split==='dev'?10:20)?1:0);
} finally {
  if(!serverExited)server.kill('SIGTERM');
  const timer=setTimeout(()=>{if(!serverExited)server.kill('SIGKILL');},10000);
  await serverDone;clearTimeout(timer);
  writeFileSync(join(resultDir,'model-server.log'),log,{mode:0o600});
}
