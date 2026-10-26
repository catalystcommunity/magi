import {environment} from './environment.mjs';
import {readFileSync,writeFileSync,mkdirSync,mkdtempSync,unlinkSync,rmdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
const root=resolve(import.meta.dirname,'..');
if(!environment('MAGI_PLAYWRIGHT_MODULE')||!environment('MAGI_DEMO_BROWSER_URL'))throw Error('Set OPAL_PLAYWRIGHT_MODULE and OPAL_DEMO_BROWSER_URL. The URL must contain the local demo token.');
const {firefox}=await import(pathToFileURL(environment('MAGI_PLAYWRIGHT_MODULE')));
const browser=await firefox.launch({headless:true});const page=await browser.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
const stamp=new Date().toISOString().replaceAll(':','-'),prefix='b'+Date.now(),reports=[];
const check=(value,message)=>{if(!value)throw Error(message);};
async function api(path,body){return page.evaluate(async({path,body})=>{const options={headers:{'X-Opal-Demo-Token':sessionStorage.getItem('opal-demo-token')}};if(body!==undefined){options.method='POST';options.headers['Content-Type']='application/json';options.body=JSON.stringify(body);}const response=await fetch('/api/'+path,options);if(!response.ok)throw Error('API '+path+' failed: '+response.status);return response.json();},{path,body});}
async function send(){await page.locator('#send').click();await page.waitForFunction(()=>!document.getElementById('send').disabled);const request=await page.evaluate(()=>JSON.parse(sessionStorage.getItem('opal-demo-last')));check(request?.runs?.length,'No admitted run IDs');return request;}
async function result(run){const deadline=Date.now()+180000;while(Date.now()<deadline){const state=await api('run/'+run);if(state.result)return Object.fromEntries(state.result.values.map(v=>[v.name,v.value]));if(state.steps.some(s=>['reconcile_required','rejected'].includes(s.phase)))throw Error('Run requires reconciliation: '+run);await new Promise(r=>setTimeout(r,500));}throw Error('Run timeout: '+run);}
async function graph(session){let after=0;const records=[];for(let n=0;n<40;n++){const page=await api('history',{session,after});records.push(...page.records);after=page.next_sequence;if(!page.more)return records;}throw Error('Graph history page budget');}
let temp;
try{
 await page.goto(environment('MAGI_DEMO_BROWSER_URL'));await page.locator('#voice option').nth(11).waitFor({state:'attached'});check(await page.locator('#voice option').count()===12,'Voice inventory');
 await page.locator('#session').fill(prefix+'stars');await page.locator('#text').fill('What is 2 + 2? Use one short sentence.');
 const first=await send(),firstRun=first.runs[0].run,answer=await result(firstRun);check(/four|4/i.test(answer.answer),'Unexpected arithmetic response');check(answer.speech_id&&!answer.speech_error,'TTS did not finish');
 const evidence=await api('evidence/'+firstRun);check(evidence.answer.request.messages[1].content.includes('2 + 2'),'Missing model input evidence');check(evidence.prepare.metadata.outcome==='returned'&&evidence.present.metadata.outcome==='returned','Component evidence incomplete');
 reports.push({case:'text-model-tts',run:firstRun,result:answer});
 await page.locator('#stop-playback').click();
 await page.locator('#split').check();await page.locator('#text').fill('What was the arithmetic result? Also, how many days are in a week?');await page.locator('#first').fill('What was the arithmetic result?');await page.locator('#second-session').fill(prefix+'calendar');await page.locator('#second').fill('How many days are in a week?');await page.locator('#speak').uncheck();
 const mixed=await send();check(mixed.runs.length===2,'Missing second run');const mixedResults=[];for(const run of mixed.runs)mixedResults.push(await result(run.run));check(/four|4/i.test(mixedResults[0].answer),'Session recall failed');check(/seven|7/i.test(mixedResults[1].answer),'Second session answer failed');
 const stars=await graph(prefix+'stars'),calendar=await graph(prefix+'calendar');const ref=mixed.request.interaction_id;
 const sourceA=stars.find(r=>r.receipt.ref.kind==='interaction'&&r.receipt.ref.record_id===ref),sourceB=calendar.find(r=>r.receipt.ref.kind==='interaction'&&r.receipt.ref.record_id===ref);check(sourceA&&sourceB&&sourceA.receipt.sequence===sourceB.receipt.sequence,'Shared interaction was duplicated or lost');
 const secondEvidence=await api('evidence/'+mixed.runs[1].run);check(!secondEvidence.answer.request.messages[1].content.includes('2 + 2'),'Cross-session context leak');
 reports.push({case:'one-source-two-sessions',runs:mixed.runs,sharedSequence:sourceA.receipt.sequence,results:mixedResults});
 // Use real Kokoro output as a reproducible microphone fixture for real Parakeet.
 const audio=await page.evaluate(async id=>{const r=await fetch('/api/audio/'+id,{headers:{'X-Opal-Demo-Token':sessionStorage.getItem('opal-demo-token')}});if(!r.ok)throw Error('Audio fetch');return Array.from(new Uint8Array(await r.arrayBuffer()));},answer.speech_id);
 const original=Buffer.from(audio),rate=original.readUInt32LE(24),count=Math.floor((original.length-44)/2*16000/rate),wav=Buffer.alloc(44+count*2);original.copy(wav,0,0,44);wav.writeUInt32LE(wav.length-8,4);wav.writeUInt32LE(16000,24);wav.writeUInt32LE(32000,28);wav.writeUInt32LE(count*2,40);for(let n=0;n<count;n++)wav.writeInt16LE(original.readInt16LE(44+Math.floor(n*rate/16000)*2),44+n*2);
 temp=mkdtempSync(join(tmpdir(),'opal-demo-browser-'));const file=join(temp,'input.wav');writeFileSync(file,wav,{mode:0o600});
 await page.locator('#split').uncheck();await page.locator('#session').fill(prefix+'voice');await page.locator('#file').setInputFiles(file);await page.waitForFunction(()=>document.getElementById('transcription').textContent.startsWith('Confirm or correct'),{},{timeout:120000});const transcript=await page.locator('#text').inputValue();check(/four|4/i.test(transcript),'Unexpected Parakeet transcript: '+transcript);
 const voice=await send();await result(voice.runs[0].run);const voiceGraph=await graph(prefix+'voice');check(voiceGraph.some(r=>r.receipt.ref.kind==='transcript'),'Missing transcript observation');const source=voiceGraph.find(r=>r.receipt.ref.kind==='interaction');check(source.entry.node.channel==='voice'&&source.entry.node.media,'Missing original audio reference');
 reports.push({case:'kokoro-parakeet-conversation',run:voice.runs[0].run,transcript,inputHash:createHash('sha256').update(wav.subarray(44)).digest('hex')});
 await page.locator('#run-id').fill(firstRun);await page.locator('#evidence').click();await page.waitForFunction(()=>document.getElementById('inspection').textContent.includes('model.answer.v1'));
 check(!errors.length,'Browser script errors: '+errors.join('; '));mkdirSync(join(root,'test-results'),{recursive:true});await page.screenshot({path:join(root,'test-results',stamp+'-demo-browser.png'),fullPage:true});
 writeFileSync(join(root,'test-results',stamp+'-demo-browser.json'),JSON.stringify({browser:await browser.version(),reports,pageErrors:errors,scope:'local synthetic identities; real pinned model, Kokoro, and Parakeet'},null,2)+'\n');console.log(JSON.stringify({report:'test-results/'+stamp+'-demo-browser.json',cases:reports.map(r=>r.case)}));
}finally{if(temp){unlinkSync(join(temp,'input.wav'));rmdirSync(temp);}await browser.close();}
