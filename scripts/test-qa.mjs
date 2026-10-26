import {spawn,execFileSync} from 'node:child_process';
import {mkdirSync,readFileSync,readdirSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {join,resolve} from 'node:path';
import {startModel} from './qa-model.mjs';
const root=resolve(import.meta.dirname,'..');
const real=process.argv.includes('--model');
if(process.argv.slice(2).some(a=>a!=='--model'))throw Error('Unknown test option');
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const dir=join(root,'test-results');mkdirSync(dir,{recursive:true});mkdirSync(join(root,'.build'),{recursive:true});
const stamp=new Date().toISOString().replaceAll(':','-');const kind=real?'qa-model':'qa';
function hashes(){const out={};function walk(path){for(const f of readdirSync(join(root,path),{withFileTypes:true})){if(f.name==='__pycache__')continue;const p=join(path,f.name);if(f.isDirectory())walk(p);else out[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');}}for(const path of ['question','workflow','identity','protocol','generated','csil','cmd','scripts','vendor','deploy'])walk(path);for(const path of ['go.mod','go.sum','package.json','schema.go'])out[path]=createHash('sha256').update(readFileSync(join(root,path))).digest('hex');return out;}
function run(command,args){return new Promise((ok,fail)=>{const child=spawn(command,args,{cwd:root,env,stdio:['ignore','pipe','pipe']});let stdout='',stderr='';const timer=setTimeout(()=>child.kill('SIGKILL'),220000);const append=(which,chunk)=>{if(stdout.length+stderr.length>32*1024*1024){child.kill('SIGKILL');return;}if(which==='out')stdout+=chunk;else stderr+=chunk;};child.stdout.on('data',c=>append('out',c));child.stderr.on('data',c=>append('err',c));child.on('error',e=>{clearTimeout(timer);fail(e);});child.on('exit',code=>{clearTimeout(timer);ok({code,stdout,stderr});});});}
const sourceHashes=hashes();const reports=[];let model;
const report={started:stamp,kind,sourceHashes,reports,go:execFileSync('go',['version'],{env,encoding:'utf8'}).trim(),node:process.version};
try{
 if(real){
  const built=await run('go',['build','-o','.build/magi-qa','./cmd/magi-qa']);if(built.code!==0)throw Error(built.stderr);
  model=await startModel();report.model=model.metadata;env.OPAL_QA_TEST_MODEL_URL=model.endpoint;env.OPAL_QA_TEST_CLI=join(root,'.build/magi-qa');
  console.log('Pinned small CPU model is ready. Running the signed Q&A path through the CLI.');
 }
 for(let pass=1;pass<=3;pass++){
  const args=['test','-race','-count=1','-timeout=180s','-json',`-tags=${real?'integration,model':'integration'}`];if(real)args.push('-run','^TestQuestionRealModel$');args.push('./question');
  const result=await run('go',args);writeFileSync(join(dir,`${stamp}-${kind}-${pass}.go.jsonl`),result.stdout);writeFileSync(join(dir,`${stamp}-${kind}-${pass}.stderr`),result.stderr);
  if(result.code!==0)throw Error('Q&A tests failed; inspect the retained raw output');
  const events=result.stdout.trim().split('\n').map(line=>JSON.parse(line));
  const leaves=events.filter(e=>e.Action==='pass'&&e.Test&&!events.some(other=>other.Test?.startsWith(e.Test+'/')));
  if(!leaves.length||events.some(e=>e.Action==='skip'||e.Action==='fail'))throw Error('Incomplete Q&A test run');
  reports.push({pass,goLeaves:leaves.length});console.log(JSON.stringify(reports.at(-1)));
 }
 const vet=await run('go',['vet',`-tags=${real?'integration,model':'integration'}`,'./question','./cmd/magi-qa']);if(vet.code!==0)throw Error(vet.stderr);report.vet='pass';
 if(JSON.stringify(hashes())!==JSON.stringify(sourceHashes))throw Error('Source drift during tests');report.sourceDrift=[];report.outcome='pass';
}catch(e){report.outcome='failed';report.error=e.message;process.exitCode=1;console.error(e.message);}
finally{
 if(model){await model.stop();const logs=model.logs();writeFileSync(join(dir,`${stamp}-${kind}.model.log`),logs.text,{mode:0o600});report.modelLogDropped=logs.dropped;}
 const path=join(dir,`${stamp}-${kind}.summary.json`);writeFileSync(path,JSON.stringify(report,null,2)+'\n');console.log(path);
}
