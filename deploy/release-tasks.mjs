import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from '../scripts/operator-state.mjs';
import {runtimePolicy} from '../release/configuration.mjs';
// Operator setup for one release journal and one dedicated Corndogs store.
import {execFileSync} from 'node:child_process';
import {readFileSync,writeFileSync,existsSync,chmodSync} from 'node:fs';
import {randomUUID,randomBytes,createHash} from 'node:crypto';
import {setTimeout} from 'node:timers/promises';
import {readPrivate} from './qa-bootstrap.mjs';
import {namespace,labels} from './release-render.mjs';
import {taskStoreManifests,taskCheckJob,withTaskRuntime,taskLabels,taskProfile} from './release-tasks-render.mjs';
import {withEnvironment} from './environment-render.mjs';
import {need} from '../release/policy.mjs';
process.umask(0o077);
const mode=process.argv[2];need(process.argv.length===3 && ['--prepare','--enable','--verify'].includes(mode),'Use --prepare, --enable, or --verify');
const run=(args,input)=>{try{return execFileSync('kubectl',['--context', environmentConfig.kubeContext,'--request-timeout=15s',...args],{input,encoding:'utf8',stdio:['pipe','pipe','pipe'],maxBuffer:4*1024*1024});}catch{throw Error(`Release task operator ${args[0]} failed. Credentials were not printed.`);}};
const get=args=>JSON.parse(run(['get',...args,'-o','json']));
const hash=b=>createHash('sha256').update(b).digest('hex'),digest=v=>hash(JSON.stringify(v));
const owned=r=>r.metadata.labels?.['app.kubernetes.io/part-of']==='opal' && r.metadata.labels?.['app.kubernetes.io/managed-by']==='opal-release';
const old=(kind,name)=>{const s=run(['get',kind,name,'-n',namespace,'--ignore-not-found','-o','json']);return s.trim()?JSON.parse(s):undefined;};
need(JSON.parse(run(['config','view','--minify','-o','json'])).clusters?.[0]?.cluster?.server===environmentConfig.clusterServer,'wrong-cluster');
need(get(['namespace',namespace]).metadata.labels?.['app.kubernetes.io/part-of']==='opal','namespace-owner');
const imageReport=JSON.parse(readFileSync(operatorFile('release-controller-image.json'))),image=imageReport.image;
need(imageReport.taskArtifact,'task-helper-image-required');
for(const [p,h] of Object.entries(imageReport.taskArtifact.sourceHashes))need(hash(readFileSync(p))===h,'task-helper-source-drift');
for(const [p,h] of Object.entries(imageReport.files))if(!p.startsWith('bin/'))need(hash(readFileSync(p))===h,'controller-image-source-drift');
need(hash(readFileSync('deploy/release.Dockerfile'))===imageReport.recipeDigest,'controller-recipe-drift');
const dir=environmentConfig.releaseProfileDir,statePath=`${dir}/task-store.json`,reportPath=operatorFile('release-task-deployment.json');
const save=r=>writeFileSync(reportPath,JSON.stringify(r,null,2)+'\n');
const runtime=JSON.parse(readPrivate(`${dir}/controller.json`));
const app=a=>({identity:{domain:a.identity.domain,account_id:a.identity.accountId},application_id:a.applicationId,instance_id:a.instanceId});
const deployments=()=>Object.fromEntries(get(['deployments','-n',namespace]).items.filter(d=>d.metadata.name!=='release-controller').sort((a,b)=>a.metadata.name.localeCompare(b.metadata.name)).map(d=>[d.metadata.name,{uid:d.metadata.uid,generation:d.metadata.generation,specDigest:digest(d.spec)}]));
const inspectScript="const db=new(require('node:sqlite').DatabaseSync)('/data/controller.sqlite',{readOnly:true});console.log(JSON.stringify({active:db.prepare('SELECT count(*) n FROM releases WHERE terminal=0').get().n,releases:db.prepare('SELECT count(*) n FROM releases').get().n,observations:db.prepare('SELECT count(*) n FROM observations').get().n,inbox:db.prepare('SELECT count(*) n FROM delivery_inbox').get().n}));db.close();";
const inspectController=()=>{
  const d=get(['deployment','release-controller','-n',namespace]);need(owned(d) && d.spec.replicas===1 && d.spec.strategy.type==='Recreate','controller-owner');
  const status=JSON.parse(run(['-n',namespace,'exec','deployment/release-controller','--','node','-e',inspectScript]));
  need(status.active===0,'controller-release-active');return {d,status};
};
if(mode==='--prepare') {
  const {d,status}=inspectController();
  need(!d.spec.template.spec.volumes.some(v=>v.name==='task-profile'),'task-runtime-already-enabled');
  if(!existsSync(statePath))writeFileSync(statePath,JSON.stringify({installation:`opal-release-${randomUUID()}`,password:randomBytes(32).toString('base64url')})+'\n',{mode:0o600,flag:'wx'});
  const state=JSON.parse(readPrivate(statePath));need(/^opal-release-[a-f0-9-]{36}$/.test(state.installation) && /^[A-Za-z0-9_-]{43}$/.test(state.password),'task-private-state');
  const cert=`${dir}/release-corndogs.crt`,key=`${dir}/release-corndogs.key`;
  need(existsSync(cert)===existsSync(key),'task-partial-tls-state');
  if(!existsSync(cert)){
    try{execFileSync('openssl',['req','-x509','-newkey','rsa:3072','-nodes','-keyout',key,'-out',cert,'-days','90','-subj','/CN=release-corndogs','-addext',("subjectAltName=DNS:release-corndogs,DNS:release-corndogs." + environmentConfig.namespace + ".svc.cluster.local"),'-addext','extendedKeyUsage=serverAuth'],{stdio:'pipe'});}catch{throw Error('task-tls-generation');}chmodSync(key,0o600);
  }
  const ca=readFileSync((environmentConfig.qaProfileDir + "/ca.crt"),'utf8')+'\n'+readFileSync(cert,'utf8');
  const profile={database:`postgres://release_runtime:${state.password}@qa-postgres:5432/release_journal?sslmode=verify-full&sslrootcert=/trust/release/ca.crt`,installation:state.installation,corndogs_seeds:['release-corndogs:5080'],corndogs_ca_file:'/trust/release/ca.crt',corndogs_server_name:'release-corndogs',controller:app(runtime.controller),recorder:app(runtime.recorder),target:'opal-stateless-canary-v1'};
  const secret=(name,data)=>({apiVersion:'v1',kind:'Secret',metadata:{name,namespace,labels:taskLabels},type:'Opaque',data:Object.fromEntries(Object.entries(data).map(([k,v])=>[k,Buffer.from(v).toString('base64')]))});
  const items=[...taskStoreManifests(ca),secret('release-corndogs-tls',{'tls.crt':readFileSync(cert),'tls.key':readPrivate(key)}),secret(taskProfile,{'profile.json':JSON.stringify(profile)})];
  for(const r of items){const prior=old(r.kind,r.metadata.name);if(prior){need(owned(prior),'task-resource-owner');if(r.kind==='Secret')need(digest(prior.data)===digest(r.data),'task-secret-changed');}}
  const pg=get(['statefulset','qa-postgres','-n',namespace]);need(pg.metadata.labels?.['app.kubernetes.io/part-of']==='opal' && pg.metadata.labels?.['app.kubernetes.io/managed-by']==='opal-deploy','postgres-owner');
  const q=get(['resourcequota','opal','-n',namespace]);need(q.metadata.labels?.['app.kubernetes.io/part-of']==='opal','quota-owner');
  const changes=[['count/secrets','9','11'],['count/services','5','6'],['persistentvolumeclaims','4','5'],['requests.storage','8Gi','9Gi']];
  const patch=[{op:'test',path:'/metadata/resourceVersion',value:q.metadata.resourceVersion}];
  for(const [name,before,after] of changes){need([before,after].includes(q.spec.hard[name]),'task-quota-drift');if(q.spec.hard[name]===before)patch.push({op:'test',path:'/spec/hard/'+name.replaceAll('/','~1'),value:before},{op:'replace',path:'/spec/hard/'+name.replaceAll('/','~1'),value:after});}
  if(patch.length>1)run(['-n',namespace,'patch','resourcequota','opal','--type=json','-p',JSON.stringify(patch)]);
  const protectedState=deployments();
  for(const r of items.sort((a,b)=>(a.kind==='NetworkPolicy'?-1:0)-(b.kind==='NetworkPolicy'?-1:0))){run(['apply','--server-side','--field-manager=opal-release-tasks','-f','-'],JSON.stringify(r));console.log(`${r.kind}/${r.metadata.name} applied`);}
  const sql=(database,text)=>run(['-n',namespace,'exec','-i','statefulset/qa-postgres','--','psql','-X','-U','postgres','-d',database,'-At','-v','ON_ERROR_STOP=1','-f','-'],text).trim();
  const marker=`opal-release:${state.installation}`;
  const role=sql('postgres',"SELECT coalesce(shobj_description(oid,'pg_authid'),'') FROM pg_roles WHERE rolname='release_runtime';");
  if(!role){need(sql('postgres',"SELECT count(*) FROM pg_roles WHERE rolname='release_runtime';")==='0','unowned-release-role');sql('postgres',`CREATE ROLE release_runtime LOGIN PASSWORD '${state.password}' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 4; COMMENT ON ROLE release_runtime IS '${marker}';`);}else need(role===marker,'release-role-owner');
  const database=sql('postgres',"SELECT coalesce(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname='release_journal';");
  if(!database){need(sql('postgres',"SELECT count(*) FROM pg_database WHERE datname='release_journal';")==='0','unowned-release-database');sql('postgres',`CREATE DATABASE release_journal OWNER postgres; COMMENT ON DATABASE release_journal IS '${marker}';`);}else need(database===marker,'release-database-owner');
  sql('release_journal',`BEGIN; SELECT pg_advisory_xact_lock(427361010); ${readFileSync('workflow/schema.sql','utf8')} INSERT INTO workflow_journal_binding(installation) VALUES('${state.installation}') ON CONFLICT DO NOTHING; REVOKE ALL ON DATABASE release_journal FROM PUBLIC; GRANT CONNECT ON DATABASE release_journal TO release_runtime; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE ON SCHEMA public TO release_runtime; GRANT SELECT ON workflow_journal_binding TO release_runtime; GRANT SELECT,INSERT,UPDATE ON workflow_intents TO release_runtime; GRANT SELECT,INSERT ON workflow_results TO release_runtime; COMMIT;`);
  need(sql('release_journal','SELECT installation FROM workflow_journal_binding;')===state.installation,'journal-installation-binding');
  const permissions=sql('release_journal',"SELECT has_table_privilege('release_runtime','workflow_journal_binding','UPDATE') OR has_table_privilege('release_runtime','workflow_human_bindings','SELECT') OR has_schema_privilege('release_runtime','public','CREATE');");need(permissions==='f','journal-excess-authority');
  const readyBy=Date.now()+120000;
  while(true){const store=get(['statefulset','release-corndogs','-n',namespace]);if(store.status?.readyReplicas===1 && store.status?.observedGeneration===store.metadata.generation)break;need(Date.now()<readyBy,'release-store-startup-timeout');await setTimeout(1500);}
  const job=`release-task-check-${Date.now()}`;run(['create','-f','-'],JSON.stringify(taskCheckJob(image,job)));
  if(existsSync(reportPath))writeFileSync(`test-results/${new Date().toISOString().replaceAll(':','-')}-release-task-previous-setup.json`,readFileSync(reportPath));
  save({preparedAt:new Date().toISOString(),installation:state.installation,image,checkJob:job,protectedState,previousController:{uid:d.metadata.uid,generation:d.metadata.generation,image:d.spec.template.spec.containers[0].image,status},journalSchemaSha256:hash(readFileSync('workflow/schema.sql')),database:'release_journal',runtimeRole:'release_runtime',sqlPrivilegeCheck:'passed'});
  console.log(JSON.stringify({job,prepared:true,controller:'unchanged'}));
} else {
  const report=JSON.parse(readFileSync(reportPath));need(report.image===image,'task-image-binding');
  need(digest(report.protectedState)===digest(deployments()),'other-opal-deployment-changed');
  const job=get(['job',report.checkJob,'-n',namespace]);need(owned(job) && job.status?.conditions?.some(c=>c.type==='Complete'&&c.status==='True'),'task-check-job-not-complete');
  need(JSON.parse(run(['logs',`job/${report.checkJob}`,'-n',namespace,'-c','check'])).ready===true,'task-store-not-ready');
  if(mode==='--enable') {
    const {d,status}=inspectController();need(d.metadata.uid===report.previousController.uid && d.metadata.generation===report.previousController.generation && d.spec.template.spec.containers[0].image===report.previousController.image && digest(status)===digest(report.previousController.status),'controller-changed');
    const secret=get(['secret','release-controller','-n',namespace]);need(owned(secret),'controller-secret-owner');
    const profile=JSON.parse(Buffer.from(secret.data['controller.json'],'base64'));need(!profile.tasks && !profile.crashDrill,'controller-profile-conflict');
    profile.policy=runtimePolicy(runtime);
    profile.tasks={installation:report.installation,binary:'/app/bin/release-tasks',profile:'/run/release-tasks/profile.json'};
    const patchDeployment=(current,operations)=>run(['-n',namespace,'patch','deployment','release-controller','--type=json','-p',JSON.stringify([{op:'test',path:'/metadata/uid',value:d.metadata.uid},{op:'test',path:'/metadata/resourceVersion',value:current.metadata.resourceVersion},...operations])]);
    let secretChanged=false,upgraded=false;
    const changedData={...secret.data,'controller.json':Buffer.from(JSON.stringify(profile)).toString('base64')};
    report.maintenance={startedAt:new Date().toISOString(),phase:'stopping'};save(report);
    patchDeployment(d,[{op:'replace',path:'/spec/replicas',value:0}]);
    try {
      const stoppedBy=Date.now()+90000;
      while(get(['pods','-n',namespace,'-l','app.kubernetes.io/name=opal-release-controller']).items.some(p=>!['Succeeded','Failed'].includes(p.status.phase))){need(Date.now()<stoppedBy,'controller-stop-timeout');await setTimeout(1000);}
      // Read the PVC after the old process has stopped. This closes the race
      // between the initial idle check and a newly admitted approval.
      const checkName=`release-task-idle-${Date.now()}`,oldPod=d.spec.template.spec;
      // SQLite can need a writable shared-memory file even for a read-only
      // query. Copy the stopped database and WAL into disposable memory first.
      const snapshotScript="const fs=require('node:fs');for(const name of ['controller.sqlite','controller.sqlite-wal','controller.sqlite-shm']){const p='/data/'+name;if(fs.existsSync(p)){if(fs.statSync(p).size>32*1024*1024)throw Error('snapshot-size');fs.copyFileSync(p,'/snapshot/'+name);}}"+inspectScript.replace('/data/controller.sqlite','/snapshot/controller.sqlite');
      const inspectJob={apiVersion:'batch/v1',kind:'Job',metadata:{name:checkName,namespace,labels:taskLabels},spec:{backoffLimit:0,activeDeadlineSeconds:120,ttlSecondsAfterFinished:86400,template:{metadata:{labels:{...taskLabels,'app.kubernetes.io/name':'opal-release-task-check'}},spec:{serviceAccountName:'release-task-check',automountServiceAccountToken:false,enableServiceLinks:false,restartPolicy:'Never',imagePullSecrets:oldPod.imagePullSecrets,nodeSelector:oldPod.nodeSelector,securityContext:oldPod.securityContext,volumes:[{name:'data',persistentVolumeClaim:{claimName:'release-controller',readOnly:true}},{name:'snapshot',emptyDir:{medium:'Memory',sizeLimit:'128Mi'}}],containers:[{name:'inspect',image:d.spec.template.spec.containers[0].image,command:['node','-e',snapshotScript],securityContext:oldPod.containers[0].securityContext,resources:oldPod.containers[0].resources,volumeMounts:[{name:'data',mountPath:'/data',readOnly:true},{name:'snapshot',mountPath:'/snapshot'}]}]}}}};
      run(['create','-f','-'],JSON.stringify(inspectJob));
      const end=Date.now()+130000;let checked;
      while(Date.now()<end){const j=get(['job',checkName,'-n',namespace]);if(j.status?.conditions?.some(c=>c.type==='Complete'&&c.status==='True')){checked=JSON.parse(run(['logs',`job/${checkName}`,'-n',namespace,'-c','inspect']));break;}need(!j.status?.conditions?.some(c=>c.type==='Failed'&&c.status==='True'),'stopped-controller-inspection-failed');await setTimeout(1500);}
      need(checked && checked.active===0 && digest(checked)===digest(status),'controller-admitted-work-during-stop');
      report.maintenance={...report.maintenance,phase:'verified-idle',job:checkName,status:checked};save(report);
      const stopped=get(['deployment','release-controller','-n',namespace]);need(stopped.metadata.uid===d.metadata.uid && stopped.spec.replicas===0 && digest(stopped.spec.template)===digest(d.spec.template),'controller-changed-during-maintenance');
      // Use stdin and resourceVersion, never a command argument containing keys.
      run(['replace','-f','-'],JSON.stringify({...secret,data:changedData}));secretChanged=true;
      const next={...d,spec:{...d.spec,template:{...d.spec.template,spec:withTaskRuntime(d.spec.template.spec,image)}}};
      const environmentMap=withEnvironment([next]).find(r=>r.kind==='ConfigMap');
      const priorEnvironment=old('configmap',environmentMap.metadata.name);if(priorEnvironment)need(owned(priorEnvironment),'environment-config-owner');
      run(['apply','--server-side','--field-manager=opal-release-tasks','-f','-'],JSON.stringify(environmentMap));
      patchDeployment(stopped,[{op:'replace',path:'/spec/template/spec',value:next.spec.template.spec},{op:'replace',path:'/spec/replicas',value:1}]);upgraded=true;
      report.maintenance.phase='enabled';save(report);
    } catch(error) {
      if(!upgraded){
        if(secretChanged){const current=get(['secret','release-controller','-n',namespace]);need(owned(current) && digest(current.data)===digest(changedData),'controller-secret-recovery-conflict');run(['replace','-f','-'],JSON.stringify({...current,data:secret.data}));}
        const current=get(['deployment','release-controller','-n',namespace]);need(current.metadata.uid===d.metadata.uid && digest(current.spec.template)===digest(d.spec.template),'controller-maintenance-recovery-conflict');
        patchDeployment(current,[{op:'replace',path:'/spec/replicas',value:1}]);report.maintenance.phase='old-controller-resumed';save(report);
      }
      throw error;
    }
    const controllerReport=JSON.parse(readFileSync(operatorFile('release-controller-deployment.json')));
    (controllerReport.runtimeChanges??=[]).push({at:new Date().toISOString(),previousImage:controllerReport.image,image,purpose:'Corndogs release-task adapter'});controllerReport.image=image;
    writeFileSync(operatorFile('release-controller-deployment.json'),JSON.stringify(controllerReport,null,2)+'\n');
    report.enabledAt=new Date().toISOString();save(report);console.log(JSON.stringify({enabled:true,installation:report.installation,approval:'required for a new canary change'}));
  } else {
    const {d,status}=inspectController();need(d.spec.template.spec.containers[0].image===image && d.status.readyReplicas===1 && d.status.observedGeneration===d.metadata.generation,'task-controller-not-ready');
    need(digest(status)===digest(report.previousController.status),'retained-controller-evidence-changed');
    const result=JSON.parse(run(['-n',namespace,'exec','deployment/release-controller','--','/app/bin/release-tasks','--check','/run/release-tasks/profile.json']));need(result.ready===true,'controller-task-check');
    const binding=JSON.parse(run(['-n',namespace,'exec','deployment/release-controller','--','node','-e',"const d=new(require('node:sqlite').DatabaseSync)('/data/controller.sqlite',{readOnly:true});console.log(JSON.stringify({installation:d.prepare('SELECT installation FROM release_task_binding WHERE id=1').get().installation,links:d.prepare('SELECT count(*) n FROM release_task_links').get().n}));d.close();"]));
    need(binding.installation===report.installation && binding.links===0,'task-initial-binding');
    report.verifiedAt=new Date().toISOString();report.proof={controllerUid:d.metadata.uid,generation:d.metadata.generation,retainedState:status,helperReady:true,binding,protectedDeployments:'unchanged',checkJobUid:job.metadata.uid};save(report);console.log(JSON.stringify({file:reportPath,...report.proof}));
  }
}
