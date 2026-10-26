import {execFileSync} from 'node:child_process';
import {mkdirSync,writeFileSync,readFileSync,readdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {join,resolve} from 'node:path';
const root=resolve(import.meta.dirname,'..');
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(command,args)=>execFileSync(command,args,{cwd:root,env,encoding:'utf8',maxBuffer:32*1024*1024});
run('node',['scripts/generate.mjs','--check']);run('npm',['run','build:client']);run('node',['scripts/vendor-linkkeys.mjs','--check']);run('node',['experiments/identity-service/prepare.mjs']);
const dir=join(root,'test-results');mkdirSync(dir,{recursive:true});const stamp=new Date().toISOString().replaceAll(':','-');
const sourceHashes={};
function hashes(path){for(const f of readdirSync(join(root,path),{withFileTypes:true})){const p=join(path,f.name);if(f.isDirectory())hashes(p);else sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');}}
for(const p of ['csil','generated','protocol','eventing','identity','cmd/events','tests','scripts','vendor'])hashes(p);
for(const p of ['go.mod','go.sum','package.json','package-lock.json','tsconfig.json','tsconfig.events.json','experiments/identity-service/harness.mjs','experiments/identity-service/linkkeys.mjs','experiments/identity-service/prepare.mjs','experiments/identity-service/dns.mjs'])sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');
const reports=[];
for(let pass=1;pass<=3;pass++){
 const go=run('go',['test','-race','-tags=integration','-count=1','-json','./eventing']);writeFileSync(join(dir,`${stamp}-events-${pass}.go.jsonl`),go);
 const node=run('node',['--test','--test-reporter=tap','tests/events-linkkeys.test.mjs']);writeFileSync(join(dir,`${stamp}-events-${pass}.node.tap`),node);
 const events=go.trim().split('\n').map(l=>JSON.parse(l));const leaves=events.filter(x=>x.Action==='pass'&&x.Test&&x.Test!=='TestCrashProcess'&&!events.some(y=>y.Test?.startsWith(x.Test+'/')));
 const count=key=>Number(node.match(new RegExp(`^# ${key} (\\d+)$`,'m'))?.[1]);
 if(events.some(x=>x.Action==='skip'||x.Action==='fail')||count('fail')||count('skipped')||!count('pass'))throw Error('incomplete event test run');
 reports.push({pass,goLeaves:leaves.length,nodePass:count('pass'),nodeSkipped:count('skipped')});console.log(JSON.stringify(reports.at(-1)));
}
run('go',['vet','./...']);run('go',['vet','-tags=integration','./eventing']);
for(const[p,d]of Object.entries(sourceHashes))if(createHash('sha256').update(readFileSync(join(root,p))).digest('hex')!==d)throw Error(`source drift: ${p}`);
const report={started:stamp,go:run('go',['version']).trim(),node:process.version,reports,vet:'pass',sourceHashes};const path=join(dir,`${stamp}-events.summary.json`);writeFileSync(path,JSON.stringify(report,null,2)+'\n');console.log(path);
