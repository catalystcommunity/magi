import {execFileSync} from 'node:child_process';
import {mkdirSync,writeFileSync,readFileSync,readdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {join,resolve} from 'node:path';
const root=resolve(import.meta.dirname,'..');
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(command,args)=>execFileSync(command,args,{cwd:root,env,encoding:'utf8',maxBuffer:32*1024*1024});
run('node',['scripts/vendor-linkkeys.mjs','--check']);
const dir=join(root,'test-results');mkdirSync(dir,{recursive:true});const stamp=new Date().toISOString().replaceAll(':','-');
const sourceHashes={};
function hashes(path){for(const f of readdirSync(join(root,path),{withFileTypes:true})){const p=join(path,f.name);if(f.isDirectory())hashes(p);else sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');}}
for(const p of ['workflow','question','identity','eventing','protocol','generated','scripts','vendor','cmd','csil','tests'])hashes(p);
for(const p of ['go.mod','go.sum','package.json','package-lock.json'])sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');
const reports=[];
for(let pass=1;pass<=3;pass++){
 let go;
 try{go=run('go',['test','-timeout=180s','-race','-tags=integration','-count=1','-json','./workflow']);}
 catch(e){writeFileSync(join(dir,`${stamp}-workflows-${pass}.failed.jsonl`),e.stdout||'');writeFileSync(join(dir,`${stamp}-workflows-${pass}.failed.stderr`),e.stderr||'');throw Error('Workflow tests failed; raw output is in test-results');}
 writeFileSync(join(dir,`${stamp}-workflows-${pass}.go.jsonl`),go);
 const events=go.trim().split('\n').map(l=>JSON.parse(l));
 const leaves=events.filter(x=>x.Action==='pass'&&x.Test&&!['TestJournalCrashChild','TestWorkflowRuntimeCrashChild','TestWorkflowCoordinatorCrashChild','TestWorkflowSweepCrashChild'].includes(x.Test)&&!events.some(y=>y.Test?.startsWith(x.Test+'/')));
 if(events.some(x=>x.Action==='skip'||x.Action==='fail')||!leaves.length)throw Error('Incomplete workflow test run');
 reports.push({pass,goLeaves:leaves.length});console.log(JSON.stringify(reports.at(-1)));
}
run('go',['vet','-tags=integration','./workflow']);
for(const[p,d]of Object.entries(sourceHashes))if(createHash('sha256').update(readFileSync(join(root,p))).digest('hex')!==d)throw Error(`Source drift: ${p}`);
const report={started:stamp,go:run('go',['version']).trim(),node:process.version,reports,vet:'pass',sourceHashes};
const path=join(dir,`${stamp}-workflows.summary.json`);writeFileSync(path,JSON.stringify(report,null,2)+'\n');console.log(path);
