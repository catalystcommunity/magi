import {requireDeployment} from '../config/deployment.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
// Test-domain operator setup. Credentials never enter manifests or reports.
import {execFileSync} from 'node:child_process';
import {readFileSync,writeFileSync,mkdirSync,existsSync,lstatSync,renameSync} from 'node:fs';
import {homedir} from 'node:os';
import {join,resolve} from 'node:path';
import {randomBytes,generateKeyPairSync,createHash,sign,createPrivateKey} from 'node:crypto';
import {resolveTxt} from 'node:dns/promises';
const root=resolve(import.meta.dirname,'..');
const dir=environmentConfig.qaProfileDir;
const statePath=join(dir,'bootstrap.json');
const origin=("https://" + environmentConfig.linkkeysHost);
const run=(cmd,args,options={})=>{
  try{return execFileSync(cmd,args,{encoding:'utf8',stdio:['pipe','pipe','pipe'],...options});}
  catch{throw Error(`${cmd} setup operation failed; credentials were not printed`);}
};
export function readPrivate(path) {
  const st=lstatSync(path);
  if(!st.isFile()||st.isSymbolicLink()||(st.mode&0o077))throw Error('Private file permissions are required');
  return readFileSync(path,'utf8');
}
function save(state){
  const tmp=join(dir,`state-${randomBytes(8).toString('hex')}.tmp`);
  writeFileSync(tmp,JSON.stringify(state,null,2)+'\n',{mode:0o600,flag:'wx'});renameSync(tmp,statePath);
}
async function main() {
  requireDeployment();
  if(process.argv.length!==3||!['--enroll','--render','--renew'].includes(process.argv[2]))throw Error('Use --enroll, --render, or --renew');
  mkdirSync(dir,{recursive:true,mode:0o700});
  const stat=lstatSync(dir);if(!stat.isDirectory()||stat.isSymbolicLink()||(stat.mode&0o077))throw Error('Private setup directory is required');
  let state=existsSync(statePath)?JSON.parse(readPrivate(statePath)):{};
  if(process.argv[2]==='--enroll') {
    const kube=JSON.parse(run('kubectl',['--context', environmentConfig.kubeContext,'config','view','--minify','-o','json']));
    if(kube.clusters?.[0]?.cluster?.server!==environmentConfig.clusterServer)throw Error('Unexpected cluster');
    const {codec,carrier,transport,call,tuple}=await import('../experiments/identity-service/linkkeys.mjs');
    if(!state.fingerprints) {
      const records=(await resolveTxt(("_" + environmentConfig.linkkeysHost))).map(parts=>parts.join(''));
      state.fingerprints=records.filter(v=>v.startsWith('v=lk1 ')).flatMap(v=>[...v.matchAll(/(?:^| )fp=([0-9a-f]{64})(?= |$)/g)].map(m=>m[1]));
      if(state.fingerprints.length<2)throw Error('LinkKeys DNS pins are missing');
      save(state);
    }
    if(!state.rp) {
      // Capture stdout and stderr in memory. The user-create reply contains a key.
      const output=run('kubectl',['--context', environmentConfig.kubeContext,'--request-timeout=30s','exec','-n',environmentConfig.linkkeysNamespace,("deployment/" + environmentConfig.linkkeysService),'--','linkkeys','user','create','opal-workflows','Magi workflow test service','--api-key','--relation','api_access']);
      const apiKey=output.match(/API key: (\S+)/)?.[1],accountId=output.match(/User created: id=(\S+)/)?.[1];
      if(!apiKey||!accountId)throw Error('LinkKeys service account reply was incomplete; inspect before retry');
      state.rp={apiKey,accountId};save(state);
    }
    let cookie;
    const browser=async(service,operation,type,body,responseType)=>{
      const payload=codec[`to${type}Cbor`](body);
      const response=await fetch(origin+'/csil/v1/rpc',{method:'POST',redirect:'error',signal:AbortSignal.timeout(10000),headers:{'content-type':'application/cbor',origin,...(cookie?{cookie}:{})},body:new carrier.RpcRequest(service,operation,payload).withId(1).encode()});
      if(!response.ok)throw Error(`LinkKeys HTTP ${response.status}`);
      const raw=await response.arrayBuffer();if(raw.byteLength>1024*1024)throw Error('LinkKeys response limit');
      const result=carrier.RpcResponse.decode(new Uint8Array(raw));
      if(result.status!==0)throw Error(`LinkKeys refused ${service}/${operation}: ${result.status}`);
      const set=response.headers.getSetCookie();if(set.length)cookie=set.map(c=>c.split(';')[0]).join('; ');
      return codec[`from${responseType}Cbor`](result.payload);
    };
    if(!state.enrolled) {
      const password=readPrivate(environmentConfig.loginCredentials).split('\n').find(s=>s.startsWith(environmentConfig.testUsername+': '))?.slice(environmentConfig.testUsername.length+2);
      if(!password)throw Error('The test browser password is unavailable');
      try {
        const login=await browser('Session','login-password','SessionPasswordLoginRequest',{username:environmentConfig.testUsername,password},'SessionPasswordLoginResponse');
        state.actor={identity:{domain:environmentConfig.domain,account_id:login.session.user.id},application_id:'opal-qa-cli',instance_id:'workstation-1'};
        if(!state.keys) {
          state.keys=Array.from({length:3},(_,i)=>{const key=generateKeyPairSync('ed25519').privateKey.export({format:'jwk'});return {keyId:`qa-key-${i}`,key};});save(state);
        }
        const rp=transport({tcpAddress:(environmentConfig.linkkeysHost + ":4987"),fingerprints:state.fingerprints,apiKey:state.rp.apiKey});
        const additions=[];
        for(const entry of state.keys) {
          const publicKey=Buffer.from(entry.key.x,'base64url');
          const challenge=await call(rp,'ApplicationKeys','start-key-challenge','StartApplicationKeyChallengeRequest',{subjectUserId:state.actor.identity.account_id,applicationId:state.actor.application_id,instanceId:state.actor.instance_id,purpose:'add',keyUsage:'sign',algorithm:'ed25519',publicKey},'StartApplicationKeyChallengeResponse');
          const now=new Date();
          const addition=codec.toApplicationKeyAdditionCbor({subjectUserId:state.actor.identity.account_id,subjectDomain:environmentConfig.domain,applicationId:state.actor.application_id,instanceId:state.actor.instance_id,keyId:entry.keyId,keyUsage:'sign',algorithm:'ed25519',publicKey,fingerprint:createHash('sha256').update(publicKey).digest('hex'),requestedKeyLifetimeSeconds:30*86400,challengeId:challenge.challengeId,challenge:challenge.challenge,requestedAt:now.toISOString(),expiresAt:new Date(+now+60000).toISOString()});
          const input=tuple.encodeCborTuple([tuple.text('linkkeys-application-key-possession-v1alpha'),tuple.bytes(addition)]);
          additions.push({addition,signatures:[],possessionProof:sign(null,input,createPrivateKey({key:entry.key,format:'jwk'}))});
        }
        const result=await browser('Account','enroll-application-instance','EnrollApplicationInstanceRequest',{applicationId:state.actor.application_id,instanceId:state.actor.instance_id,keys:additions},'EnrollApplicationInstanceResponse');
        state.enrolled=true;state.enrolledAt=new Date().toISOString();state.attestations=result;save(state);
      } finally {if(cookie)await browser('Session','logout','EmptyRequest',{},'SessionLogoutResponse').catch(()=>{});}
    }
  }
  if(!state.enrolled)throw Error('Enrollment must complete first');
  if(process.argv[2]==='--renew') {
    const {codec,transport,call,tuple}=await import('../experiments/identity-service/linkkeys.mjs');
    const rp=transport({tcpAddress:(environmentConfig.linkkeysHost + ":4987"),fingerprints:state.fingerprints,apiKey:state.rp.apiKey});
    const actor=state.actor, renewed=[];
    for(const entry of state.keys) {
      const challenge=await call(rp,'ApplicationKeys','start-key-challenge','StartApplicationKeyChallengeRequest',{subjectUserId:actor.identity.account_id,applicationId:actor.application_id,instanceId:actor.instance_id,purpose:'renew',keyUsage:'sign',algorithm:'ed25519',publicKey:Buffer.from(entry.key.x,'base64url')},'StartApplicationKeyChallengeResponse');
      const now=new Date();
      const renewal=codec.toApplicationKeyRenewalCbor({subjectUserId:actor.identity.account_id,subjectDomain:actor.identity.domain,applicationId:actor.application_id,instanceId:actor.instance_id,keyId:entry.keyId,challengeId:challenge.challengeId,challenge:challenge.challenge,requestedAt:now.toISOString(),expiresAt:new Date(+now+60000).toISOString()});
      const envelope=tag=>tuple.encodeCborTuple([tuple.text(tag),tuple.bytes(renewal)]);
      const request={renewal,possessionProof:sign(null,envelope('linkkeys-application-key-possession-v1alpha'),createPrivateKey({key:entry.key,format:'jwk'})),signatures:state.keys.filter(k=>k!==entry).map(k=>({signedByKeyId:k.keyId,signature:sign(null,envelope('linkkeys-application-key-renewal-v1alpha'),createPrivateKey({key:k.key,format:'jwk'}))}))};
      const result=await call(rp,'ApplicationKeys','renew-attestation','RenewApplicationKeyAttestationRequest',{request},'RenewApplicationKeyAttestationResponse');
      renewed.push(result.attestation);
      const a=codec.fromApplicationKeyAttestationCbor(result.attestation.attestation);
      console.log(JSON.stringify({keyId:entry.keyId,signed:result.signed,keyExpiresAt:a.keyExpiresAt,attestationExpiresAt:a.attestationExpiresAt}));
      await new Promise(ok=>setTimeout(ok,1100));
    }
    state.attestations.attestations=renewed;save(state);
    return;
  }
  if(!state.database) {state.database={admin:randomBytes(32).toString('hex'),runtime:randomBytes(32).toString('hex')};save(state);}
  // Dedicated CA and server certificates. Never use or change the router certificate.
  if(!existsSync(join(dir,'ca.crt'))) {
    run('openssl',['req','-x509','-newkey','rsa:3072','-nodes','-keyout',join(dir,'ca.key'),'-out',join(dir,'ca.crt'),'-days','365','-subj','/CN=Magi Example environment Test CA','-addext','basicConstraints=critical,CA:TRUE','-addext','keyUsage=critical,keyCertSign,cRLSign']);
  }
  for(const name of ['qa-postgres','qa-corndogs']) {
    if(existsSync(join(dir,`${name}.crt`)))continue;
    run('openssl',['req','-new','-newkey','rsa:3072','-nodes','-keyout',join(dir,`${name}.key`),'-out',join(dir,`${name}.csr`),'-subj',`/CN=${name}`]);
    writeFileSync(join(dir,`${name}.ext`),("subjectAltName=DNS:" + (name) + ",DNS:" + (name) + ("." + environmentConfig.namespace + ".svc.cluster.local\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n")),{mode:0o600});
    run('openssl',['x509','-req','-in',join(dir,`${name}.csr`),'-CA',join(dir,'ca.crt'),'-CAkey',join(dir,'ca.key'),'-CAcreateserial','-out',join(dir,`${name}.crt`),'-days','90','-extfile',join(dir,`${name}.ext`)]);
  }
  const recipient={identity:{domain:environmentConfig.domain,account_id:state.rp.accountId},application_id:'workflows',instance_id:environmentConfig.workflowInstance};
  const owner={authority:state.actor.identity,scope_id:'qa-test'};
  const entry=state.keys[0];
  const profile={endpoint:("https://" + environmentConfig.workflowHost + "/csil/v1/rpc"),actor:state.actor,recipient,owner,key_id:entry.keyId,private_key:Buffer.concat([Buffer.from(entry.key.d,'base64url'),Buffer.from(entry.key.x,'base64url')]).toString('base64')};
  writeFileSync(join(dir,'client.json'),JSON.stringify(profile,null,2)+'\n',{mode:0o600});
  const config={application:recipient,installation:environmentConfig.installation,corndogs_seeds:['qa-corndogs:5080'],corndogs_ca_file:'/trust/ca.crt',corndogs_server_name:'qa-corndogs',domains:[{domain:environmentConfig.domain,tcp_address:(environmentConfig.linkkeysService + "." + environmentConfig.linkkeysNamespace + ".svc.cluster.local:4987"),fingerprints:state.fingerprints}],rp:{tcp_address:(environmentConfig.linkkeysService + "." + environmentConfig.linkkeysNamespace + ".svc.cluster.local:4987"),fingerprints:state.fingerprints},grants:['read','write','run','execute'].map(action=>({actor:state.actor,owner,action,allowed:true})),question_worker:{actor:state.actor,owner,model:{endpoint:'http://127.0.0.1:8088/v1/chat/completions',model:'opal-test-small',python:'/usr/local/bin/python3',max_tokens:256},call_seconds:120}};
  writeFileSync(join(dir,'workflows.json'),JSON.stringify(config,null,2)+'\n',{mode:0o600});
  console.log(JSON.stringify({enrolled:state.enrolled,actor:state.actor,recipient,profile:join(dir,'client.json')}));
}
if(process.argv[1]===import.meta.filename)main().catch(e=>{console.error(e.message);process.exitCode=1;});
