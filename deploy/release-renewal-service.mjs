import {requireDeployment} from '../config/deployment.mjs';
requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from '../scripts/operator-state.mjs';
// Operator-only installation. Never copy this file or kubeconfig into a pod.
import {execFileSync} from 'node:child_process';
import {readFileSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {readPrivate} from './qa-bootstrap.mjs';
import {renewalConfig,renewalEndpoint} from '../release/renewal.mjs';
import {renewalManifests,renewalName,renewalLabels} from './release-renewal-render.mjs';
import {namespace} from './release-render.mjs';
import {need} from '../release/policy.mjs';
const mode=process.argv[2];need(process.argv.length===3 && ['--check','--apply','--run-once','--verify'].includes(mode),'Use --check, --apply, --run-once, or --verify');
const run=(args,input)=>{try{return execFileSync('kubectl',['--context', environmentConfig.kubeContext,'--request-timeout=15s',...args],{input,encoding:'utf8',stdio:['pipe','pipe','pipe'],maxBuffer:4*1024*1024});}catch{throw Error(`Renewal operator ${args[0]} failed. Credentials were not printed.`);}};
const get=args=>JSON.parse(run(['get',...args,'-o','json']));
const digest=value=>createHash('sha256').update(JSON.stringify(value)).digest('hex');
const owned=r=>r.metadata.namespace===namespace && Object.entries(renewalLabels).every(([k,v])=>r.metadata.labels?.[k]===v);
need(JSON.parse(run(['config','view','--minify','-o','json'])).clusters?.[0]?.cluster?.server===environmentConfig.clusterServer,'wrong-cluster');
need(get(['namespace',namespace]).metadata.labels?.['app.kubernetes.io/part-of']==='opal','namespace-owner');
const imageReport=JSON.parse(readFileSync(operatorFile('release-renewal-image.json'))),image=imageReport.image;
// Refuse a stale build. Keys and settings are not part of the image digest.
for(const [path,hash] of Object.entries(imageReport.files))need(createHash('sha256').update(readFileSync(path)).digest('hex')===hash,'renewal-image-source-drift');
need(createHash('sha256').update(readFileSync('deploy/release-renewal.Dockerfile')).digest('hex')===imageReport.recipeDigest,'renewal-image-recipe-drift');
const resources=renewalManifests(image),reportPath=operatorFile('release-renewal-deployment.json');
const deployments=()=>Object.fromEntries(get(['deployments','-n',namespace]).items.sort((a,b)=>a.metadata.name.localeCompare(b.metadata.name)).map(d=>[d.metadata.name,{uid:d.metadata.uid,generation:d.metadata.generation,specDigest:digest(d.spec)}]));
const save=r=>writeFileSync(reportPath,JSON.stringify(r,null,2)+'\n');
const existing=(kind,name)=>{const value=run(['get',kind,name,'-n',namespace,'--ignore-not-found','-o','json']);return value.trim()?JSON.parse(value):undefined;};
const apply=r=>{run(['apply','--server-side','--field-manager=opal-release-renewal','-f','-'],JSON.stringify(r));console.log(`${r.kind}/${r.metadata.name} applied`);};
if(mode==='--check'||mode==='--apply') {
  const enrollment=JSON.parse(readPrivate((environmentConfig.releaseProfileDir + "/enrollment.json"))),base=JSON.parse(readPrivate((environmentConfig.qaProfileDir + "/bootstrap.json")));
  const config=renewalConfig({version:1,tcpAddress:renewalEndpoint,fingerprints:base.fingerprints,applications:['approval','controller'].map(name=>{
    const entry=enrollment.applications[name],runtime=JSON.parse(readPrivate(((environmentConfig.releaseProfileDir + "/") + (name) + ".json")));
    need(entry.enrolled && digest(entry.app)===digest(runtime.app) && entry.keys.some(k=>k.keyId===runtime.signer.keyId && k.key.x===runtime.signer.key.x),'renewal-enrollment-binding');
    return {app:entry.app,keys:entry.keys.map(k=>({keyId:k.keyId,key:k.key}))};
  })});
  const secret={apiVersion:'v1',kind:'Secret',metadata:{name:renewalName,namespace,labels:renewalLabels},type:'Opaque',data:{'renewal.json':Buffer.from(JSON.stringify(config)).toString('base64')}};
  for(const r of [...resources,secret]){const old=existing(r.kind,r.metadata.name);if(old)need(owned(old),'renewal-resource-owner');}
  // Do not replace live key custody silently. Rotation is a separate operation.
  const priorSecret=existing('secret',renewalName);if(priorSecret)need(priorSecret.data?.['renewal.json']===secret.data['renewal.json'],'renewal-secret-change-requires-rotation');
  need(get(['service',environmentConfig.linkkeysService,'-n',environmentConfig.linkkeysNamespace]).spec.ports.some(p=>p.port===4987),'linkkeys-service');
  const q=get(['resourcequota','opal','-n',namespace]);need(q.metadata.labels?.['app.kubernetes.io/part-of']==='opal' && ['8','9','11'].includes(q.spec.hard['count/secrets']),'renewal-quota');
  for(const r of resources)run(['apply','--server-side','--dry-run=server','--field-manager=opal-release-renewal','-f','-'],JSON.stringify(r));
  console.log(JSON.stringify({preflight:'passed',image,applications:config.applications.map(e=>e.app.applicationId),keyCount:6,rpToken:false,extraSecretSlots:q.spec.hard['count/secrets']==='8'?1:0}));
  if(mode==='--apply') {
    const protectedState=deployments();
    if(q.spec.hard['count/secrets']==='8')run(['-n',namespace,'patch','resourcequota','opal','--type=json','-p',JSON.stringify([
      {op:'test',path:'/metadata/resourceVersion',value:q.metadata.resourceVersion},{op:'test',path:'/spec/hard/count~1secrets',value:'8'},{op:'replace',path:'/spec/hard/count~1secrets',value:'9'},
    ])]);
    // Install isolation before any pod can mount the keys.
    for(const r of resources.filter(r=>r.kind!=='CronJob'))apply(r);
    apply(secret);apply(resources.find(r=>r.kind==='CronJob'));
    need(digest(protectedState)===digest(deployments()),'existing-deployment-changed');
    save({installedAt:new Date().toISOString(),image,protectedState,applications:config.applications.map(e=>({app:e.app,keyIds:e.keys.map(k=>k.keyId)})),keyCustody:'dedicated-cluster-secret; local recovery copy retained',quota:'Magi Secret limit 9; storage unchanged'});
  }
} else {
  const report=JSON.parse(readFileSync(reportPath));need(report.image===image,'renewal-image-binding');
  need(digest(report.protectedState)===digest(deployments()),'existing-deployment-changed');
  const cron=get(['cronjob',renewalName,'-n',namespace]);need(owned(cron) && cron.spec.jobTemplate.spec.template.spec.containers[0].image===image,'renewal-cron-binding');
  if(mode==='--run-once') {
    const jobs=get(['jobs','-n',namespace,'-l','app.kubernetes.io/name=opal-release-renewal']).items;
    need(!jobs.some(j=>!j.status?.conditions?.some(c=>['Complete','Failed'].includes(c.type)&&c.status==='True')),'renewal-job-active');
    const job=`release-renewal-check-${Date.now()}`;
    run(['create','-f','-'],JSON.stringify({apiVersion:'batch/v1',kind:'Job',metadata:{name:job,namespace,labels:renewalLabels,ownerReferences:[{apiVersion:'batch/v1',kind:'CronJob',name:renewalName,uid:cron.metadata.uid,controller:true,blockOwnerDeletion:true}]},spec:cron.spec.jobTemplate.spec}));
    report.checkJob=job;save(report);console.log(JSON.stringify({job,created:true}));
  } else {
    need(report.checkJob,'renewal-check-job-missing');
    const job=get(['job',report.checkJob,'-n',namespace]);need(owned(job) && job.status?.conditions?.some(c=>c.type==='Complete'&&c.status==='True'),'renewal-job-not-complete');
    const pods=get(['pods','-n',namespace,'-l',`job-name=${report.checkJob}`]).items;
    const pod=pods.find(p=>p.status.phase==='Succeeded');need(pod && owned(pod) && pod.spec.containers.length===1 && pod.spec.containers[0].image===image,'renewal-pod-binding');
    need(pod.spec.automountServiceAccountToken===false && pod.spec.serviceAccountName===renewalName && pod.spec.volumes.every(v=>v.secret?.secretName===renewalName),'renewal-pod-authority');
    const logs=run(['logs',pod.metadata.name,'-n',namespace,'-c','renewal']);
    const records=logs.trim().split('\n').map(l=>JSON.parse(l)),completed=records.filter(r=>r.event==='renewal-complete');
    need(completed.length===1 && records.filter(r=>r.event==='renewal-key-verified').length===6 && completed[0].keys.length===6,'renewal-receipts');
    const expected=report.applications.flatMap(e=>e.keyIds.map(keyId=>`${e.app.applicationId}/${keyId}`)).sort();
    need(JSON.stringify(completed[0].keys.map(k=>`${k.applicationId}/${k.keyId}`).sort())===JSON.stringify(expected),'renewal-receipt-key-binding');
    need(completed[0].keys.every(k=>Date.parse(k.attestationExpiresAt)>Date.now()+4*3600000),'renewal-attestation-expiry');
    const denied=[];
    for(const args of [['get','secrets'],['patch','deployments.apps'],['create','pods/exec']]){
      // can-i returns exit 1 for an expected denial; capture only its verdict.
      let value;try{value=execFileSync('kubectl',['--context', environmentConfig.kubeContext,'--request-timeout=15s','auth','can-i',...args,'-n',namespace,`--as=system:serviceaccount:${namespace}:${renewalName}`],{encoding:'utf8',stdio:['pipe','pipe','pipe']}).trim();}catch(e){value=String(e.stdout??'').trim();}
      need(value==='no','renewal-service-account-authority');denied.push(args.join(' '));
    }
    report.verifiedAt=new Date().toISOString();report.proof={job:job.metadata.name,jobUid:job.metadata.uid,podUid:pod.metadata.uid,imageID:pod.status.containerStatuses[0].imageID,completed:completed[0],denied,existingDeployments:'unchanged'};
    save(report);console.log(JSON.stringify({file:reportPath,job:job.metadata.name,keys:completed[0].keys.length,denied,existingDeployments:'unchanged'}));
  }
}
