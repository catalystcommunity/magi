import {spawn,execFileSync} from 'node:child_process';
import {mkdirSync,existsSync,writeFileSync} from 'node:fs';
import {generateKeyPairSync} from 'node:crypto';
import {resolve,join} from 'node:path';
const root=resolve(import.meta.dirname,'..'),directory=join(root,'.local/conversations');
const env={...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'};
const run=(cmd,args,extra={})=>execFileSync(cmd,args,{cwd:root,env,stdio:'inherit',...extra});
mkdirSync(directory,{recursive:true,mode:0o700});mkdirSync(join(root,'.build'),{recursive:true});
if(!existsSync(join(directory,'server.json'))){
 if(existsSync(join(directory,'client.json')))throw Error('An existing client profile will not be overwritten');
 const {publicKey,privateKey}=generateKeyPairSync('ed25519');
 const pub=Buffer.from(publicKey.export({format:'jwk'}).x,'base64url');
 const secret=Buffer.concat([Buffer.from(privateKey.export({format:'jwk'}).d,'base64url'),pub]);
 const actor={identity:{domain:'local.opal.test',account_id:'graph-tester'},application_id:'graph-client',instance_id:'workstation'};
 const application={identity:{domain:'local.opal.test',account_id:'graph-service'},application_id:'conversations',instance_id:'development'};
 const owner={authority:actor.identity,scope_id:'public-fixtures'},key_id='local-graph-key';
 const config={application,domains:[],rp:{tcp_address:'',fingerprints:[]},grants:['read','write'].map(action=>({actor,owner,action,allowed:true})),public_test_data_only:true,local_keys:[{actor,key_id,public_key:pub.toString('base64')}]};
 const profile={endpoint:'http://127.0.0.1:8095/csil/v1/rpc',actor,recipient:application,owner,key_id,private_key:secret.toString('base64')};
 for(const [name,value]of [['server.json',config],['client.json',profile]])writeFileSync(join(directory,name),JSON.stringify(value,null,2)+'\n',{flag:'wx',mode:0o600});
}
run('go',['build','-o','.build/magi-conversations','./cmd/conversations']);
run('docker',['compose','-p','opal-conversations','-f','deploy/conversations.compose.yaml','up','-d','--wait']);
const runtime={...env,CONVERSATIONS_DATABASE_URL:'postgres://postgres:local-graph-fixture-only@127.0.0.1:25435/postgres?sslmode=disable',CONVERSATIONS_LISTEN:'127.0.0.1:8095',CONVERSATIONS_ORIGIN:''};
const args=['-config',join(directory,'server.json'),'-local-test-keys'];
for(const role of ['-migrate','-apply-policy'])run('.build/magi-conversations',[...args,role],{env:runtime});
const child=spawn(join(root,'.build/magi-conversations'),args,{cwd:root,env:runtime,stdio:'inherit'});
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,()=>child.kill(signal));
child.on('error',()=>{process.exitCode=1;});child.on('exit',code=>{process.exitCode=code||0;});
console.log('Conversation API: http://127.0.0.1:8095. Public test data only. Ctrl-C retains the dedicated database.');
