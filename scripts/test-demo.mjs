import {execFileSync,spawnSync} from 'node:child_process';
import {mkdirSync,writeFileSync,readFileSync,readdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {join,resolve} from 'node:path';
const root=resolve(import.meta.dirname,'..'),env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(cmd,args)=>execFileSync(cmd,args,{cwd:root,env,encoding:'utf8',maxBuffer:32*1024*1024});
run('node',['scripts/generate.mjs','--check']);run('npm',['run','build:client']);
const dir=join(root,'test-results');mkdirSync(dir,{recursive:true});const stamp=new Date().toISOString().replaceAll(':','-'),sourceHashes={},reports=[];
function hash(path){for(const f of readdirSync(join(root,path),{withFileTypes:true})){if(f.name==='__pycache__')continue;const p=join(path,f.name);if(f.isDirectory())hash(p);else sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');}}
for(const p of ['conversation','dialogue','question','speech','workflow','identity','protocol','generated','csil','harness','execution','workshop','cmd/magi-demo','cmd/conversations','cmd/magi-task-worker'])hash(p);
for(let pass=1;pass<=3;pass++){
 const file=join(dir,`${stamp}-demo-${pass}.go.jsonl`);let output;
 try{output=run('go',['test','-timeout=150s','-race','-tags=integration','-count=1','-json','./conversation','./dialogue','./execution','./harness','./workshop','./cmd/magi-demo','./cmd/conversations','./cmd/magi-task-worker']);}catch(e){writeFileSync(file+'.failed',e.stdout||'');writeFileSync(file+'.stderr',e.stderr||'');throw Error('Demo integration failed; see test-results');}
 const python=spawnSync('python3',['-B','-m','unittest','discover','-s','harness','-p','test_*.py','-v'],{cwd:root,env,encoding:'utf8'});writeFileSync(join(dir,`${stamp}-demo-${pass}.python.txt`),python.stdout+python.stderr);if(python.status!==0||!/Ran 6 tests/.test(python.stderr))throw Error('Python harness tests failed');
 writeFileSync(file,output);const events=output.trim().split('\n').map(JSON.parse),leaves=events.filter(e=>e.Action==='pass'&&e.Test&&!events.some(other=>other.Test?.startsWith(e.Test+'/')));if(!leaves.length||events.some(e=>e.Action==='fail'||e.Action==='skip'&&e.Test))throw Error('Incomplete test run');reports.push({pass,goLeaves:leaves.length});console.log(JSON.stringify(reports.at(-1)));
}
const node=run('node',['--test','--test-reporter=tap','tests/conversations-schema.test.mjs']);writeFileSync(join(dir,`${stamp}-demo.node.tap`),node);run('go',['vet','-tags=integration','./conversation','./dialogue','./execution','./harness','./workshop','./cmd/magi-demo','./cmd/conversations','./cmd/magi-task-worker']);
for(const [p,h]of Object.entries(sourceHashes))if(createHash('sha256').update(readFileSync(join(root,p))).digest('hex')!==h)throw Error('Source drift: '+p);
const file=join(dir,`${stamp}-demo.summary.json`);writeFileSync(file,JSON.stringify({reports,sourceHashes,vet:'pass',nodeContract:'pass'},null,2)+'\n');console.log(file);
