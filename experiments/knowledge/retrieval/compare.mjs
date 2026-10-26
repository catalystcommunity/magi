import { spawn, execFileSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:net';
import { cpus, homedir } from 'node:os';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const split = process.argv[2] || 'dev';
if (!['dev', 'test', 'all'].includes(split)) throw Error('split must be dev, test, or all');
const model = process.env.OPAL_PLANNER_MODEL || join(homedir(), 'models/qwen2.5-1.5b-instruct-q4_k_m.gguf');
const binary = process.env.OPAL_LLAMA_SERVER || join(homedir(), 'llamacpp/llama.cpp/build/bin/llama-server');
const python = join(root, '.cache/retrieval-venv/bin/python');
const sourceFiles = ['store.go','storage.sql','harness_test.go','comparison_test.go','retrieval/compare.mjs','retrieval/worker.py','retrieval/planner.txt','retrieval/corpus.json','retrieval/questions.json','retrieval/protocol.json','retrieval/model.json','retrieval/requirements.lock'];
const hash = path => createHash('sha256').update(readFileSync(path)).digest('hex');
const sources = Object.fromEntries(sourceFiles.map(p => [p, hash(join(root, p))]));
const modelHash = hash(model), binaryHash = hash(binary);
const listener = createServer();
await new Promise((resolve, reject) => { listener.once('error', reject); listener.listen(0, '127.0.0.1', resolve); });
const port = listener.address().port;
await new Promise(resolve => listener.close(resolve));
const started = new Date().toISOString();
const resultDir = join(root, 'results', `${started.replace(/[:.]/g, '-')}-comparison-${split}-${randomBytes(3).toString('hex')}`);
mkdirSync(resultDir, {recursive: true});
const args = ['-m',model,'--host','127.0.0.1','--port',String(port),'-ngl','0','-t','2','-tb','2','-c','2048','-np','1','--no-webui'];
const server = spawn('nice', ['-n','10',binary,...args], {cwd:root,env:{PATH:process.env.PATH,LANG:'C.UTF-8',CUDA_VISIBLE_DEVICES:''},stdio:['ignore','pipe','pipe']});
let log='', serverError;
server.stdout.on('data',b=>{ log+=b; }); server.stderr.on('data',b=>{ log+=b; });
server.on('error',e=>{serverError=e;});
let serverExited=false;
const serverDone=new Promise(resolve=>server.on('close',code=>{serverExited=true;resolve(code);}));
async function stopServer() {
  if (!serverExited) server.kill('SIGTERM');
  const timer=setTimeout(()=>{if(!serverExited) server.kill('SIGKILL');},10000);
  await serverDone;clearTimeout(timer);
  writeFileSync(join(resultDir,'model-server.log'),log);
}
let child;
process.on('SIGINT',()=>{child?.kill('SIGINT');server.kill('SIGTERM');});
process.on('SIGTERM',()=>{child?.kill('SIGTERM');server.kill('SIGTERM');});
try {
  let healthy=false;
  for(let n=0;n<120;n++) {
    if(serverError||serverExited) throw Error(`Local model failed: ${serverError||log.slice(-2000)}`);
    try { healthy=(await fetch(`http://127.0.0.1:${port}/health`,{signal:AbortSignal.timeout(1000)})).ok; } catch {}
    if(healthy) break;
    await new Promise(resolve=>setTimeout(resolve,250));
  }
  if(!healthy) throw Error('Local model startup deadline exceeded');
  console.log(`Local CPU model ready. Split: ${split}. Test labels are not sent to models.`);
  const env={...process.env,GOCACHE:process.env.GOCACHE||join(root,'.cache/go'),OPAL_KNOWLEDGE_INTEGRATION:'1',OPAL_RETRIEVAL_COMPARISON:'1',OPAL_RETRIEVAL_SPLIT:split,OPAL_PLANNER_PORT:String(port),OPAL_RETRIEVAL_PYTHON:python};
  const goArgs=['test','-json','-race','-count=1','-timeout=8m','-run','TestComparisonFixtures|TestRetrievalComparison','.'];
  child=spawn('go',goArgs,{cwd:root,env,stdio:['ignore','pipe','pipe']});
  let output='',stderr='',pending=''; const rows=[],indices=[];
  child.stdout.on('data',b=>{
    output+=b; pending+=b;
    let next;
    while((next=pending.indexOf('\n'))>=0) {
      const line=pending.slice(0,next);pending=pending.slice(next+1);
      let e;try{e=JSON.parse(line);}catch{continue;}
      for(const [marker,collection] of [['COMPARISON_CASE ',rows],['COMPARISON_INDEX ',indices]]) {
        if(e.Output?.includes(marker)) collection.push(JSON.parse(e.Output.split(marker)[1]));
      }
      if(e.Output?.includes('COMPARISON_CASE ')&&rows.at(-1)?.method==='hybrid_links') console.log(`Completed ${rows.at(-1).question}`);
      if(e.Action==='fail') console.error(`Test failed: ${e.Test||e.Package}`);
    }
  });
  child.stderr.on('data',b=>{stderr+=b;process.stderr.write(b);});
  const exitCode=await new Promise((resolve,reject)=>{child.on('error',reject);child.on('close',resolve);});
  writeFileSync(join(resultDir,'tests.jsonl'),output);
  writeFileSync(join(resultDir,'cases.json'),JSON.stringify(rows,null,2)+'\n');
  const percentile=(values,p)=>{const sorted=[...values].sort((a,b)=>a-b);return sorted[Math.ceil(sorted.length*p)-1]??null;};
  const groups=[];
  for(const groupSplit of ['dev','test']) for(const method of ['text_all','text_any','model_text','embedding','hybrid','hybrid_links']) {
    const selected=rows.filter(r=>r.split===groupSplit&&r.method===method);
    if(!selected.length)continue;
    const positives=selected.filter(r=>r.relevant_count>0),negatives=selected.filter(r=>r.relevant_count===0);
    groups.push({split:groupSplit,method,queries:selected.length,errors:selected.filter(r=>r.error).length,
      positive_questions:positives.length,hit_at_10:positives.filter(r=>r.hits>0).length,
      complete_evidence_at_10:positives.filter(r=>r.complete_evidence).length,
      macro_recall_at_10:positives.reduce((s,r)=>s+r.hits/r.relevant_count,0)/positives.length,
      precision_at_10:positives.reduce((s,r)=>s+r.precision_at_10,0)/positives.length,
      mrr:positives.reduce((s,r)=>s+r.mrr,0)/positives.length,
      no_evidence_questions:negatives.length,no_evidence_with_candidates:negatives.filter(r=>r.retrieved.length).length,
      p50_ms:percentile(selected.map(r=>r.elapsed_ms),.5),p95_ms:percentile(selected.map(r=>r.elapsed_ms),.95),max_ms:percentile(selected.map(r=>r.elapsed_ms),1),
      model_p95_ms:percentile(selected.map(r=>r.model_ms),.95),
      planner_prompt_tokens:selected.reduce((s,r)=>s+(r.planner_timings?.prompt_n||0),0),planner_generated_tokens:selected.reduce((s,r)=>s+(r.planner_timings?.predicted_n||0),0)});
  }
  const drift=sourceFiles.filter(p=>hash(join(root,p))!==sources[p]);
  const events=output.trim().split('\n').filter(Boolean).map(s=>JSON.parse(s));
  const summary={started,finished:new Date().toISOString(),exit_code:exitCode,split,
    tests:events.filter(e=>e.Test&&['pass','fail','skip'].includes(e.Action)).map(e=>({test:e.Test,result:e.Action})),
    sources,source_drift:drift,embedding_model:JSON.parse(readFileSync(join(root,'retrieval/model.json'))),
    planner:{model,model_sha256:modelHash,binary,binary_sha256:binaryHash,args,temperature:0,seed:1,cache_prompt:false,max_output_tokens:128},
    runtime:{node:process.version,go:execFileSync('go',['version'],{encoding:'utf8'}).trim(),python:execFileSync(python,['--version'],{encoding:'utf8'}).trim(),cpu:cpus()[0]?.model},
    indices,groups,stderr,scope:'Small synthetic quality experiment; exact vectors; sequential requests; no answer generation or public RPC'};
  writeFileSync(join(resultDir,'summary.json'),JSON.stringify(summary,null,2)+'\n');
  console.log(JSON.stringify(groups,null,2));console.log(`Evidence: ${resultDir}`);
  process.exitCode=exitCode||drift.length||0;
} finally {await stopServer();}
