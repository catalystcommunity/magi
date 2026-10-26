const $=id=>document.getElementById(id);
const fragment=new URLSearchParams(location.hash.slice(1));if(fragment.has('token')){sessionStorage.setItem('opal-demo-token',fragment.get('token'));history.replaceState(null,'',location.pathname);}
const token=sessionStorage.getItem('opal-demo-token')||'';
const id=prefix=>prefix+'-'+crypto.randomUUID().replaceAll('-','');
async function api(path,body,content='application/json'){
 const options={headers:{'X-Opal-Demo-Token':token}};
 if(body!==undefined){options.method='POST';options.headers['Content-Type']=content;options.body=content==='application/json'?JSON.stringify(body):body;}
 const response=await fetch('/api/'+path,options);if(!response.ok)throw Error(await response.text());return response;
}
const json=async(path,body)=> (await api(path,body)).json();
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const message=e=>$('status').textContent=e.message||String(e);
let speechInput='',recording,recordTimer,queue=[],playing=false,currentAudio;
$('split').onchange=()=>{$('split-fields').hidden=!$('split').checked;};
async function presentation(run,session,state){try{await json('presentation',{id:id('p'),run,session,state,observed_at:new Date().toISOString()});}catch{message('Playback receipt was not stored. Audio state is uncertain in the archive.');}}
async function playQueue(){
 if(playing)return;playing=true;
 while(queue.length){const item=queue.shift();try{
  const response=await api('audio/'+item.speech),url=URL.createObjectURL(await response.blob());
  const audio=new Audio(url);currentAudio=audio;const ended=new Promise(resolve=>{audio.onended=()=>resolve('completed');audio.onerror=()=>resolve('interrupted');audio.onpause=()=>{if(!audio.ended)resolve('interrupted');};});
  try{await audio.play();await presentation(item.run,item.session,'started');await presentation(item.run,item.session,await ended);}catch{await presentation(item.run,item.session,'interrupted');message('Audio did not start. Use Play on the response.');}finally{URL.revokeObjectURL(url);currentAudio=null;}
 }catch(e){message('Speech retrieval failed. The text response remains available. '+e.message);}}
 playing=false;
}
$('stop-playback').onclick=()=>{for(const item of queue)presentation(item.run,item.session,'interrupted');queue=[];currentAudio?.pause();};
async function enqueue(item){await presentation(item.run,item.session,'queued');queue.push(item);playQueue();}
async function watch(run,session){
 const article=document.createElement('article'),title=document.createElement('strong'),text=document.createElement('p');title.textContent=session+' · '+run;text.textContent='Waiting for the workflow…';article.append(title,text);$('responses').prepend(article);
 const inspect=document.createElement('button');inspect.textContent='Inspect this run';inspect.onclick=()=>{$('run-id').value=run;};article.append(inspect);
 const until=Date.now()+300000;
 while(Date.now()<until){
  try{const state=await json('run/'+run);if(state.result){const values=Object.fromEntries(state.result.values.map(v=>[v.name,v.value]));text.textContent=values.answer;
   if(values.speech_error){const note=document.createElement('p');note.textContent='Text fallback: '+values.speech_error;article.append(note);}
   if(values.speech_id){const item={run,session,speech:values.speech_id},button=document.createElement('button');button.textContent='Play';button.onclick=()=>enqueue(item);article.append(button);enqueue(item);}return;
  }if(state.steps.some(s=>['reconcile_required','rejected'].includes(s.phase))){text.textContent='This run needs investigation. Inspect its evidence. It was not repeated.';return;}}
  catch(e){text.textContent='The run is unavailable. Its ID is retained above. '+e.message;return;}await sleep(1500);
 }text.textContent='The wait ended. The workflow was not cancelled. Read this run ID to recover its state.';
}
$('send').onclick=async()=>{
 $('send').disabled=true;
 try{const destinations=[{session:$('session').value.trim(),text:$('split').checked?$('first').value:$('text').value}];if($('split').checked)destinations.push({session:$('second-session').value.trim(),text:$('second').value});
 const request={interaction_id:id('i'),observed_at:new Date().toISOString(),text:$('text').value,destinations,destination:0,speak:$('speak').checked,voice:$('voice').value,...(speechInput?{speech_input:speechInput}:{})};
 const predicted=destinations.map((d,i)=>({run:request.interaction_id+'-'+i,session:d.session}));sessionStorage.setItem('opal-demo-last',JSON.stringify({request,runs:predicted}));$('run-id').value=predicted[0].run;
 const results=await json('turn',request);message('Submitted. Each destination has its own workflow.');for(const result of results){if(result.error)message(result.error);watch(result.run,result.session);}speechInput='';
 }catch(e){message(e);}finally{$('send').disabled=false;}
};
async function transcribe(wav){
 speechInput='';const request=id('stt');$('transcription').textContent='Transcription request: '+request;
 await api('transcribe/'+request,wav,'audio/wav');
 for(let n=0;n<90;n++){const state=await json('speech/'+request);if(state.state==='completed'){speechInput=request;$('text').value=state.transcript;$('transcription').textContent='Confirm or correct the transcript before you send it. Request: '+request;return;}if(['failed','cancelled'].includes(state.state))throw Error('Transcription stopped: '+state.error_code);await sleep(1500);}
 throw Error('Transcription wait ended. Request: '+request);
}
$('file').onchange=async()=>{try{const file=$('file').files[0];if(file)await transcribe(file);}catch(e){message(e);}};
// Capture does not authenticate a speaker. The browser may apply microphone processing.
$('record').onclick=async()=>{
 try{const stream=await navigator.mediaDevices.getUserMedia({audio:{channelCount:1}});const chunks=[],rec=new MediaRecorder(stream);recording=rec;
 rec.ondataavailable=e=>{if(e.data.size)chunks.push(e.data);};rec.onstop=async()=>{clearTimeout(recordTimer);for(const track of stream.getTracks())track.stop();$('record').disabled=false;$('stop-record').disabled=true;recording=null;
  try{const context=new AudioContext(),buffer=await context.decodeAudioData(await new Blob(chunks).arrayBuffer());await context.close();const frames=Math.min(960000,Math.ceil(buffer.duration*16000));if(!frames)throw Error('No audio was captured.');const offline=new OfflineAudioContext(1,frames,16000),source=offline.createBufferSource();source.buffer=buffer;source.connect(offline.destination);source.start();const rendered=await offline.startRendering(),pcm=rendered.getChannelData(0),wav=new ArrayBuffer(44+frames*2),view=new DataView(wav);const tag=(offset,text)=>{for(let i=0;i<text.length;i++)view.setUint8(offset+i,text.charCodeAt(i));};tag(0,'RIFF');view.setUint32(4,36+frames*2,true);tag(8,'WAVEfmt ');view.setUint32(16,16,true);view.setUint16(20,1,true);view.setUint16(22,1,true);view.setUint32(24,16000,true);view.setUint32(28,32000,true);view.setUint16(32,2,true);view.setUint16(34,16,true);tag(36,'data');view.setUint32(40,frames*2,true);for(let i=0;i<frames;i++)view.setInt16(44+i*2,Math.max(-32768,Math.min(32767,Math.round(pcm[i]*32767))),true);await transcribe(wav);
  }catch(e){message(e);}
 };rec.start();$('record').disabled=true;$('stop-record').disabled=false;recordTimer=setTimeout(()=>{if(rec.state==='recording')rec.stop();},58000);$('transcription').textContent='Recording. Stop within 58 seconds. Playback is not stopped automatically.';
 }catch(e){message(e);}
};
$('stop-record').onclick=()=>{if(recording?.state==='recording')recording.stop();};
const inspect=async path=>{try{$('inspection').textContent=JSON.stringify(await json(path),null,2);}catch(e){message(e);}};
$('read-run').onclick=()=>inspect('run/'+$('run-id').value.trim());$('evidence').onclick=()=>inspect('evidence/'+$('run-id').value.trim());
let cursor=0;
async function historyPage(reset){try{if(reset)cursor=0;const page=await json('history',{session:$('session').value.trim(),after:cursor});cursor=page.next_sequence;$('more').disabled=!page.more;$('inspection').textContent=JSON.stringify(page,null,2);}catch(e){message(e);}}
$('history').onclick=()=>historyPage(true);$('more').onclick=()=>historyPage(false);
json('voices').then(v=>{const profiles=v.profiles.filter(p=>p.engine==='kokoro');if(profiles.length){$('voice').replaceChildren(...profiles.map(p=>{const o=document.createElement('option');o.value=p.voice;o.textContent=p.voice;return o;}));$('voice').value='af_heart';}}).catch(message);
if(!token)message('Open the local URL with the token printed by npm run dev:demo.');
const previous=sessionStorage.getItem('opal-demo-last');if(previous){try{const v=JSON.parse(previous);$('run-id').value=v.runs[0].run;message('The last run ID was restored. Use Read run to inspect it. No request was repeated.');}catch{}}
async function taskState(){const state=await json('development/'+$('task-run').value.trim());$('task-output').textContent=JSON.stringify(state,null,2);$('candidate-digest').value=state.evidence?.digest||'';return state;}
$('start-task').onclick=async()=>{const run=id('task');$('task-run').value=run;sessionStorage.setItem('opal-demo-task',run);$('start-task').disabled=true;try{await json('develop',{id:run,task:$('task').value,session:'development-'+run,observed_at:new Date().toISOString()});message('Task admitted. Speech and other sessions can continue.');const until=Date.now()+180000;while(Date.now()<until){const state=await taskState();if(state.workflow.result||state.workflow.steps.some(s=>s.step_id==='review'&&s.phase==='waiting_human'||['reconcile_required','rejected'].includes(s.phase)))break;await sleep(1500);}}catch(e){message(e);}finally{$('start-task').disabled=false;}};
$('inspect-task').onclick=()=>taskState().catch(message);
for(const [button,action]of [['hold-task','hold'],['release-task','release'],['cleanup-task','cleanup']])$(button).onclick=async()=>{try{const result=await json('development/control',{id:id('control'),run:$('task-run').value.trim(),action,digest:'',note:''});$('task-output').textContent=JSON.stringify(result,null,2);}catch(e){message(e);}};
$('review-task').onclick=async()=>{try{await json('development/review',{id:id('review'),run:$('task-run').value.trim(),action:'review',digest:$('candidate-digest').value,note:$('review-note').value});message('Review recorded. No source file was applied and no deployment was approved.');await taskState();}catch(e){message(e);}};
if(sessionStorage.getItem('opal-demo-task'))$('task-run').value=sessionStorage.getItem('opal-demo-task');
