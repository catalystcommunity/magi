import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {execFileSync} from 'node:child_process';
import {readFileSync,writeFileSync} from 'node:fs';
import {resolve,join} from 'node:path';
import {createHash} from 'node:crypto';
const root=resolve(import.meta.dirname,'..');
const stamp=new Date().toISOString().replaceAll(':','-');
const profile=(environmentConfig.qaProfileDir + "/client.json");
const run=(cmd,args)=>execFileSync(cmd,args,{cwd:root,encoding:'utf8',stdio:['ignore','pipe','pipe'],timeout:200000,maxBuffer:1024*1024});
const sha=raw=>createHash('sha256').update(raw).digest('hex');
const file=join(root,`test-results/${stamp}-qa-live.json`);
const report={startedAt:new Date().toISOString(),endpoint:("https://" + environmentConfig.workflowHost + "/csil/v1/rpc"),images:JSON.parse(readFileSync(operatorFile('qa-images.json'))).images,checks:[],status:'running'};
const save=()=>writeFileSync(file,JSON.stringify(report,null,2)+'\n');
try {
  for(let pass=1;pass<=3;pass++) {
    const id=`live-${stamp.replaceAll(/[^0-9]/g,'')}-${pass}`;
    const output=run('.build/magi-qa-check',['-profile',profile,'-run',id]);
    const evidence=run('.build/magi-qa',['evidence','-profile',profile,'-run',id]);
    const parsed=JSON.parse(evidence);
    report.checks.push({pass,id,output,evidenceSha256:sha(evidence),modelResponse:parsed.response,startedAt:parsed.metadata.started_at,finishedAt:parsed.metadata.finished_at});save();
    console.log(output.trim());
    // Allow the shared test LinkKeys source bucket to recover between passes.
    await new Promise(ok=>setTimeout(ok,10000));
  }
  if(process.argv.includes('--restart')) {
    const last=report.checks.at(-1);
    run('kubectl',['--context', environmentConfig.kubeContext,'--request-timeout=30s','rollout','restart','deployment/qa-api','-n',environmentConfig.namespace]);
    run('kubectl',['--context', environmentConfig.kubeContext,'--request-timeout=30s','rollout','status','deployment/qa-api','-n',environmentConfig.namespace,'--timeout=50s']);
    const after=run('.build/magi-qa',['evidence','-profile',profile,'-run',last.id]);
    if(sha(after)!==last.evidenceSha256)throw Error('Evidence changed after API replacement');
    report.restart={resource:'deployment/qa-api',evidenceUnchanged:true};
  }
  report.status='pass';
} catch(e) {
  report.status='failed';report.failure={message:e.message,stdout:e.stdout?.toString(),stderr:e.stderr?.toString()};
  throw Error(`Live check failed; see ${file}`);
} finally {report.finishedAt=new Date().toISOString();save();console.log(file);}
