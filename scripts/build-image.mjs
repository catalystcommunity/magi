import {requireDeployment} from '../config/deployment.mjs';
if(process.argv.includes('--push'))requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {readFileSync,mkdirSync,writeFileSync,readdirSync} from 'node:fs';
import {resolve,join} from 'node:path';
const root=resolve(import.meta.dirname,'..');
const run=(cmd,args,env=process.env)=>execFileSync(cmd,args,{cwd:root,env,stdio:'inherit'});
mkdirSync(join(root,'dist'),{recursive:true});
const sourceHashes={};
const sha=value=>createHash('sha256').update(value).digest('hex');
function scan(path){for(const f of readdirSync(join(root,path),{withFileTypes:true})){if(f.name==='__pycache__')continue;const p=join(path,f.name);if(f.isDirectory())scan(p);else sourceHashes[p]=sha(readFileSync(join(root,p)));}}
for(const p of ['cmd','server','protocol','generated','csil','vendor','identity','eventing','workflow','question','speech','conversation','dialogue','execution','harness','workshop','editor'])scan(p);
for(const p of ['schema.go','go.mod','go.sum','Dockerfile','scripts/build-image.mjs'])sourceHashes[p]=sha(readFileSync(join(root,p)));
const sourceDigest=sha(JSON.stringify(Object.fromEntries(Object.entries(sourceHashes).sort(([a],[b])=>a.localeCompare(b,'en')))));
const compiler=execFileSync('go',['version'],{encoding:'utf8'}).trim();
const recipeDigest=sha(JSON.stringify({dockerfile:sourceHashes.Dockerfile,script:sourceHashes['scripts/build-image.mjs'],compiler,CGO_ENABLED:'0',GOOS:'linux',GOARCH:'amd64',flags:['-buildvcs=false','-trimpath']}));
run('go',['build','-buildvcs=false','-trimpath','-o','dist/magi','./cmd/magi'],{...process.env,CGO_ENABLED:'0',GOOS:'linux',GOARCH:'amd64',GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'});
for(const[p,d]of Object.entries(sourceHashes))if(sha(readFileSync(join(root,p)))!==d)throw Error(`Source changed during build: ${p}`);
const digest=createHash('sha256').update(readFileSync(join(root,'dist/magi'))).digest('hex');
const repository=environmentConfig.repository;
const tag=`${repository}:dev-${digest.slice(0,16)}`;
run('docker',['build','--platform=linux/amd64','--label',`org.opencontainers.image.title=Magi protocol lab`,'--label',`opal.binary-sha256=${digest}`,'-t',tag,'.']);
if(process.argv.includes('--push')){
 run('docker',['push',tag]);
 const images=JSON.parse(execFileSync('docker',['image','inspect',tag,'--format','{{json .RepoDigests}}'],{encoding:'utf8'}));
 const image=images.find(s=>s.startsWith(repository+'@sha256:'));if(!image)throw Error('Pushed digest missing');
 
 const report=JSON.stringify({image,tag,binarySha256:digest,sourceDigest,recipeDigest,compiler,sourceHashes,builtAt:new Date().toISOString()},null,2)+'\n';
 writeFileSync(operatorFile('image.json'),report);
 writeFileSync(operatorFile(`candidate-image-${digest.slice(0,16)}.json`),report);
 console.log(image);
}else console.log(`Built ${tag}; use --push to publish to the private registry`);
