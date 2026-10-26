import {requireDeployment} from '../config/deployment.mjs';
if(process.argv.includes('--push'))requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {environment} from './environment.mjs';
import {execFileSync} from 'node:child_process';
import {copyFileSync,mkdtempSync,readFileSync,writeFileSync,rmSync} from 'node:fs';
import {tmpdir,homedir} from 'node:os';
import {join,resolve} from 'node:path';
import {createHash} from 'node:crypto';
import {hashFile,pins} from './qa-model.mjs';

const root=resolve(import.meta.dirname,'..');
const scratch=mkdtempSync(join(tmpdir(),'opal-qa-images-'));
const run=(cmd,args,options={})=>execFileSync(cmd,args,{cwd:root,stdio:'inherit',...options});
try {
  const model=environment('MAGI_QA_GGUF')||join(homedir(),'models',pins.file);
  if(await hashFile(model)!==pins.sha256)throw Error('Model hash differs from the pin');
  run('go',['build','-buildvcs=false','-trimpath','-o',join(scratch,'workflows'),'./cmd/workflows'],{env:{...process.env,CGO_ENABLED:'0',GOOS:'linux',GOARCH:'amd64',GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'}});
  run('git',['-C',environment('MAGI_QA_LLAMA_SOURCE')||join(homedir(),'llamacpp/llama.cpp'),'archive','--format=tar',`--output=${join(scratch,'llama.tar')}`,pins.llama_cpp_commit]);
  copyFileSync(model,join(scratch,'model.gguf'));
  copyFileSync(join(root,'question/model-pin.json'),join(scratch,'model-pin.json'));
  copyFileSync(join(root,'deploy/qa-images.Dockerfile'),join(scratch,'Dockerfile'));
  const binaryHash=await hashFile(join(scratch,'workflows'));
  const recipeHash=createHash('sha256').update(readFileSync(join(scratch,'Dockerfile'))).digest('hex');
  const report={builtAt:new Date().toISOString(),binaryHash,recipeHash,model:pins,images:{}};
  for(const target of ['workflows','model']) {
    const repository=environmentConfig.repository;
    const tag=`${repository}:qa-${target}-${binaryHash.slice(0,12)}-${recipeHash.slice(0,12)}`;
    run('docker',['build','--platform=linux/amd64','--target',target,'-t',tag,scratch]);
    if(process.argv.includes('--push')) {
      run('docker',['push',tag]);
      const digests=JSON.parse(run('docker',['image','inspect',tag,'--format','{{json .RepoDigests}}'],{encoding:'utf8',stdio:['ignore','pipe','pipe']}));
      report.images[target]=digests.find(v=>v.startsWith(repository+'@sha256:'));
      if(!report.images[target])throw Error('Pushed image digest is absent');
    } else report.images[target]=tag;
  }
  writeFileSync(operatorFile('qa-images.json'),JSON.stringify(report,null,2)+'\n');
  console.log(JSON.stringify(report));
} finally {rmSync(scratch,{recursive:true,force:true});}
