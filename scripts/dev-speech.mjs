import {environment} from './environment.mjs';
import {deployment} from '../config/deployment.mjs';
import {spawn,execFileSync} from 'node:child_process';
import {mkdirSync,existsSync,readFileSync,writeFileSync} from 'node:fs';
import {generateKeyPairSync} from 'node:crypto';
import {resolve,join} from 'node:path';
const root=resolve(import.meta.dirname,'..');
const directory=deployment.speechProfileDir;
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(cmd,args,extra={})=>execFileSync(cmd,args,{cwd:root,env,...extra});
mkdirSync(directory,{recursive:true,mode:0o700});mkdirSync(join(root,'.build'),{recursive:true});
if(!existsSync(join(directory,'server.json'))){
 const backends={};
 for(const kind of ['tts','stt']){
  const prefix=`MAGI_SPEECH_${kind.toUpperCase()}`;
  const python=environment(prefix+'_PYTHON'),model_dir=environment(prefix+'_MODEL');
  if(!python||!model_dir)throw Error(`Set ${prefix}_PYTHON and ${prefix}_MODEL for the first start. Use MAGI_SPEECH_SSH_HOST for a native remote host.`);
  backends[kind]={kind,python,model_dir,...(environment('MAGI_SPEECH_SSH_HOST')?{ssh_host:environment('MAGI_SPEECH_SSH_HOST')}:{})};
 }
 if(existsSync(join(directory,'client.json')))throw Error('Client profile already exists without a server configuration; do not overwrite it');
 const {publicKey,privateKey}=generateKeyPairSync('ed25519');
 const pub=publicKey.export({format:'jwk'}),priv=privateKey.export({format:'jwk'});
 const publicBytes=Buffer.from(pub.x,'base64url'),secret=Buffer.concat([Buffer.from(priv.d,'base64url'),publicBytes]);
 const actor={identity:{domain:'local.opal.test',account_id:'speech-tester'},application_id:'speech-client',instance_id:'workstation'};
 const application={identity:{domain:'local.opal.test',account_id:'speech-service'},application_id:'speech',instance_id:'development'};
 const owner={authority:actor.identity,scope_id:'public-fixtures'},key_id='local-speech-key';
 const server={application,domains:[],rp:{tcp_address:'',fingerprints:[]},grants:[{actor,owner,allowed:true}],backends,public_test_data_only:true,local_keys:[{actor,key_id,public_key:publicBytes.toString('base64')}]};
 const client={endpoint:'http://127.0.0.1:8094/csil/v1/rpc',actor,recipient:application,owner,key_id,private_key:secret.toString('base64')};
 for(const [name,value] of [['server.json',server],['client.json',client]])writeFileSync(join(directory,name),JSON.stringify(value,null,2)+'\n',{mode:0o600,flag:'wx'});
}
const configuration=JSON.parse(readFileSync(join(directory,'server.json')));
if(configuration.public_test_data_only!==true)throw Error('Public test data is required');
run('go',['build','-o','.build/magi-speech','./cmd/speech'],{stdio:'inherit'});
run('go',['build','-o','.build/magi-speech-client','./cmd/magi-speech-client'],{stdio:'inherit'});
run('docker',['compose','-p','opal-speech','-f','deploy/speech.compose.yaml','up','-d','--wait'],{stdio:'inherit'});
const runtime={...env,SPEECH_DATABASE_URL:'postgres://postgres:local-speech-fixture-only@127.0.0.1:25434/postgres?sslmode=disable',SPEECH_LISTEN:'127.0.0.1:8094',SPEECH_ORIGIN:''};
const args=['-config',join(directory,'server.json'),'-local-test-keys'];
for(const role of ['-migrate','-apply-policy'])run('.build/magi-speech',[...args,role],{env:runtime,stdio:'inherit'});
const children=[];let stopping=false;
function stop(){if(stopping)return;stopping=true;for(const child of children)child.kill('SIGTERM');}
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,stop);
for(const role of [[],['-worker','tts'],['-worker','stt']]){
 const child=spawn(join(root,'.build/magi-speech'),[...args,...role],{cwd:root,env:runtime,stdio:'inherit'});children.push(child);
 child.once('error',()=>{process.exitCode=1;stop();});child.once('exit',code=>{if(!stopping){process.exitCode=code||1;stop();}});
}
console.log('Speech development API: http://127.0.0.1:8094. Synthetic identities and public test data only.');
console.log('Ctrl-C stops these processes. It retains the dedicated speech database and profiles.');
