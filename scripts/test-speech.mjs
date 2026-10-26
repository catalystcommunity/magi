import {execFileSync} from 'node:child_process';
import {mkdirSync,writeFileSync,readFileSync,readdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {join,resolve} from 'node:path';
const root=resolve(import.meta.dirname,'..');
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(command,args)=>execFileSync(command,args,{cwd:root,env,encoding:'utf8',maxBuffer:32*1024*1024});
const dir=join(root,'test-results');mkdirSync(dir,{recursive:true});const stamp=new Date().toISOString().replaceAll(':','-');
const sourceHashes={};
function hashes(path){for(const f of readdirSync(join(root,path),{withFileTypes:true})){const p=join(path,f.name);if(f.isDirectory())hashes(p);else sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');}}
for(const p of ['speech','identity','protocol','csil','generated','cmd/speech'])hashes(p);
run('node',['scripts/generate.mjs','--check']);run('npm',['run','build:client']);
const reports=[];
for(let pass=1;pass<=3;pass++){
 const file=join(dir,`${stamp}-speech-${pass}.go.jsonl`);
 let output;
 try{output=run('go',['test','-timeout=120s','-race','-tags=integration','-count=1','-json','./speech','./cmd/speech']);}
 catch(e){writeFileSync(file+'.failed',e.stdout||'');writeFileSync(file+'.stderr',e.stderr||'');throw Error('Speech tests failed; see test-results');}
 writeFileSync(file,output);
 const events=output.trim().split('\n').map(line=>JSON.parse(line));
 const leaves=events.filter(e=>e.Action==='pass'&&e.Test&&!events.some(other=>other.Test?.startsWith(e.Test+'/')));
 if(!leaves.length||events.some(e=>e.Action==='skip'||e.Action==='fail'))throw Error('Incomplete speech test run');
 reports.push({pass,goLeaves:leaves.length});console.log(JSON.stringify(reports.at(-1)));
}
const node=run('node',['--test','--test-reporter=tap','tests/speech-schema.test.mjs']);writeFileSync(join(dir,`${stamp}-speech.node.tap`),node);
run('go',['vet','-tags=integration','./speech','./cmd/speech']);
for(const [path,hash] of Object.entries(sourceHashes))if(createHash('sha256').update(readFileSync(join(root,path))).digest('hex')!==hash)throw Error(`Source drift: ${path}`);
const file=join(dir,`${stamp}-speech.summary.json`);writeFileSync(file,JSON.stringify({reports,sourceHashes,vet:'pass',nodeContract:'pass'},null,2)+'\n');console.log(file);
