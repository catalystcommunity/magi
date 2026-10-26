import {execFileSync} from 'node:child_process';
import {readFileSync,writeFileSync,readdirSync,mkdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {resolve,join} from 'node:path';
const root=resolve(import.meta.dirname,'..'),env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(cmd,args)=>execFileSync(cmd,args,{cwd:root,env,encoding:'utf8',maxBuffer:8*1024*1024});
run('node',['scripts/prepare-release-sdk.mjs']);run('npm',['run','build:client']);mkdirSync(join(root,'.build'),{recursive:true});
run('go',['build','-o','.build/release-tasks','./cmd/release-tasks']);
const stamp=new Date().toISOString().replaceAll(':','-'),sourceHashes={},reports=[];
function scan(path){for(const f of readdirSync(join(root,path),{withFileTypes:true})){const p=join(path,f.name);if(f.isDirectory())scan(p);else sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');}}
for(const p of ['releasework','release','workflow','cmd/release-tasks'])scan(p);
for(const p of ['tests/release-tasks.test.mjs','tests/release-tasks.integration.mjs','tests/release-fixture.mjs','scripts/test-release-tasks.mjs'])sourceHashes[p]=createHash('sha256').update(readFileSync(join(root,p))).digest('hex');
for(let pass=1;pass<=3;pass++){
  const file=`test-results/${stamp}-release-tasks-${pass}.tap`;let output;
  try{output=run('node',['--test','--test-isolation=none','--test-reporter=tap','tests/release-tasks.test.mjs','tests/release-tasks.integration.mjs']);}
  catch(e){writeFileSync(file+'.failed',e.stdout||'');writeFileSync(file+'.stderr',e.stderr||'');throw Error(`Release task tests failed; see ${file}.failed`);}
  writeFileSync(file,output);const count=n=>Number(output.match(new RegExp(`^# ${n} (\\d+)$`,'m'))?.[1]);
  if(count('tests')!==22||count('pass')!==22||count('fail')||count('skipped'))throw Error('Incomplete release task matrix');
  reports.push({pass,tests:count('tests'),passed:count('pass')});console.log(JSON.stringify(reports.at(-1)));
}
run('go',['test','-race','./releasework','./cmd/release-tasks']);run('go',['vet','./releasework','./cmd/release-tasks']);
for(const [p,h]of Object.entries(sourceHashes))if(createHash('sha256').update(readFileSync(join(root,p))).digest('hex')!==h)throw Error('Source drift: '+p);
const file=`test-results/${stamp}-release-tasks.summary.json`;writeFileSync(file,JSON.stringify({reports,sourceHashes,helperSha256:createHash('sha256').update(readFileSync('.build/release-tasks')).digest('hex'),scope:'Real PostgreSQL and Corndogs file/PostgreSQL. Synthetic human authentication and simulated deployment. No live release or cluster change.'},null,2)+'\n');console.log(file);
