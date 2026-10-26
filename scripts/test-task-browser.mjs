import {environment} from './environment.mjs';
import {readFileSync,writeFileSync,mkdirSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {resolve,join} from 'node:path';
import {pathToFileURL} from 'node:url';
const root=resolve(import.meta.dirname,'..'),stamp=new Date().toISOString().replaceAll(':','-');
if(!environment('MAGI_PLAYWRIGHT_MODULE')||!environment('MAGI_DEMO_BROWSER_URL'))throw Error('Set OPAL_PLAYWRIGHT_MODULE and OPAL_DEMO_BROWSER_URL');
const {firefox}=await import(pathToFileURL(environment('MAGI_PLAYWRIGHT_MODULE')));const browser=await firefox.launch({headless:true}),page=await browser.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
const sourcePath=join(root,'harness/formatting.py'),original=readFileSync(sourcePath);let run,record;
const check=(v,m)=>{if(!v)throw Error(m);};
async function api(path,body){return page.evaluate(async({path,body})=>{const options={headers:{'X-Opal-Demo-Token':sessionStorage.getItem('opal-demo-token')}};if(body!==undefined){options.method='POST';options.headers['Content-Type']='application/json';options.body=JSON.stringify(body);}const r=await fetch('/api/'+path,options);if(!r.ok)throw Error('API '+path+' returned '+r.status);return r.json();},{path,body});}
const control=action=>api('development/control',{id:action+'-'+crypto.randomUUID().replaceAll('-',''),run,action,digest:'',note:''});
try{
 await page.goto(environment('MAGI_DEMO_BROWSER_URL'));await page.locator('#start-task').click();run=await page.locator('#task-run').inputValue();check(run,'No task ID');await page.waitForFunction(()=>document.getElementById('status').textContent.startsWith('Task admitted.'),{},{timeout:15000});
 let held=false,joined=false,ready=false;const until=Date.now()+180000;
 while(Date.now()<until){const state=await api('development/'+run);record=state.evidence;if(record?.container?.ID&&!held){check(/^[a-f0-9]{64}$/.test(record.container.ID),'Container ID');const receipt=await control('hold');check(receipt.shell,'Hold did not provide a local shell hint');held=true;const output=execFileSync('docker',['exec','--user','65532:65532',record.container.ID,'python','-I','-B','-c','print("inspection joined live")'],{encoding:'utf8',timeout:5000});check(output.trim()==='inspection joined live','Live join failed');joined=true;}
  if(state.workflow.steps.some(s=>s.step_id==='review'&&s.phase==='waiting_human')){ready=true;break;}
  if(record?.state==='unknown'||state.workflow.steps.some(s=>s.phase==='reconcile_required'))throw Error('Task needs investigation; inspect '+run);
  await new Promise(r=>setTimeout(r,500));
 }
 check(ready,'Task did not reach human review');check(record.state==='candidate_ready','Final checks did not pass');check(record.held,'Hold was lost');check(record.digest===createHash('sha256').update(record.candidate).digest('hex'),'Candidate digest mismatch');
 check(record.events.some(e=>e.operation==='final-check'&&JSON.parse(e.response?JSON.stringify(e.response):'{}').exit_code===0),'No final successful check');
 const modelCalls=record.events.filter(e=>e.kind==='model').length,tools=record.events.filter(e=>e.kind==='tool').length;check(modelCalls>=2&&tools>=4,'Missing model or tool evidence');
 await page.locator('#inspect-task').click();await page.waitForFunction(()=>document.getElementById('candidate-digest').value.length===64);await page.locator('#review-note').fill('Automated public-fixture review: inspected the exact digest, final checks, and receipts. No deployment is authorized.');await page.locator('#review-task').click();
 let completed=false;for(let n=0;n<60;n++){const state=await api('development/'+run);if(state.workflow.result){completed=true;break;}await new Promise(r=>setTimeout(r,500));}check(completed,'Review did not finish the workflow');
 await control('release');const removed=await control('cleanup');check(!removed.error,'Environment cleanup failed');const retained=await api('development/'+run);check(retained.evidence.state==='closed'&&retained.evidence.candidate===record.candidate,'Evidence did not survive environment deletion');check(original.equals(readFileSync(sourcePath)),'Source checkout was modified');check(!errors.length,'Browser script errors');
 const binaryHashes=Object.fromEntries(['magi-demo','magi-task-worker','magi-qa'].map(name=>[name,createHash('sha256').update(readFileSync(join(root,'.build',name))).digest('hex')]));
 const report={run,modelCalls,tools,liveJoin:joined,holdDidNotPause:true,candidateDigest:record.digest,finalState:retained.evidence.state,sourceUnchanged:true,pageErrors:errors,binaryHashes,scope:'local test identities; real pinned small model; offline Python container; no deployment'};mkdirSync(join(root,'test-results'),{recursive:true});writeFileSync(join(root,'test-results',stamp+'-task-browser.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({report:'test-results/'+stamp+'-task-browser.json',...report}));
}catch(e){mkdirSync(join(root,'test-results'),{recursive:true});writeFileSync(join(root,'test-results',stamp+'-task-browser.failed.json'),JSON.stringify({run,error:e.message,record,pageErrors:errors},null,2)+'\n');throw e;}finally{await browser.close();}
