import { execFileSync } from 'node:child_process';
import { mkdirSync,writeFileSync,readFileSync,readdirSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { resolve,join } from 'node:path';
const root=resolve(import.meta.dirname,'..');
const env={...process.env,MAGI_DEPLOYMENT_CONFIG:join(root,'config/deployment.example.json'),GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(cmd,args)=>execFileSync(cmd,args,{cwd:root,env,encoding:'utf8',maxBuffer:32*1024*1024});
run('node',['scripts/generate.mjs','--check']);run('npm',['run','build:client']);
const dir=join(root,'test-results');mkdirSync(dir,{recursive:true});
const stamp=new Date().toISOString().replaceAll(':','-');
const sourceHashes={};
function hashes(path){for(const f of readdirSync(join(root,path),{withFileTypes:true})){if(f.name==='__pycache__')continue;const p=join(path,f.name);if(f.isDirectory())hashes(p);else sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');}}
for(const p of ['csil','generated','protocol','server','eventing','identity','workflow','question','speech','conversation','dialogue','execution','harness','workshop','editor','cmd','tests','scripts','deploy','vendor','config','release'])hashes(p);
hashes('experiments/release-rollback');
hashes('releasework');
for(const p of ['schema.go','go.mod','go.sum','package.json','package-lock.json'])sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');
const reports=[];
function recorded(command,args,path){try{const output=run(command,args);writeFileSync(path,output);return output;}catch(e){writeFileSync(path.replace(/\.(jsonl|tap)$/,'.failed.$1'),e.stdout||'');writeFileSync(path+'.failed.stderr',e.stderr||'');throw Error(`Tests failed; output retained for ${path}`);}}
for(let pass=1;pass<=3;pass++){
  const go=recorded('go',['test','-race','-count=1','-json','./...'],join(dir,`${stamp}-${pass}.go.jsonl`));
  const node=recorded('node',['--test','--test-reporter=tap','tests/rename.test.mjs','tests/conformance.test.mjs','tests/http.test.mjs','tests/deployment.test.mjs','tests/qa-deployment.test.mjs','tests/events-schema.test.mjs','tests/workflows-schema.test.mjs','tests/speech-schema.test.mjs','tests/conversations-schema.test.mjs','tests/releases-schema.test.mjs','tests/workflow-editor.test.mjs'],join(dir,`${stamp}-${pass}.node.tap`));
  const events=go.trim().split('\n').map(l=>JSON.parse(l));const leaves=events.filter(x=>x.Action==='pass'&&x.Test&&!events.some(y=>y.Test?.startsWith(x.Test+'/')));
  const count=key=>Number(node.match(new RegExp(`^# ${key} (\\d+)$`,'m'))?.[1]);
  if(count('fail')||count('skipped')||!count('pass'))throw Error('incomplete Node run');
  reports.push({pass,goLeaves:leaves.length,nodeTests:count('tests'),nodePass:count('pass'),nodeSkipped:count('skipped')});
  console.log(JSON.stringify(reports.at(-1)));
}
run('go',['vet','./...']);run('node',['scripts/check-docs.mjs']);
for(const[p,d]of Object.entries(sourceHashes))if(createHash('sha256').update(readFileSync(join(root,p))).digest('hex')!==d)throw Error(`source drift ${p}`);
const report={started:stamp,go:run('go',['version']).trim(),node:process.version,reports,vet:'pass',sourceHashes};
const file=join(dir,`${stamp}.summary.json`);writeFileSync(file,JSON.stringify(report,null,2)+'\n');console.log(file);
