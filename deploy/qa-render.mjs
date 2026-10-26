import {immutableImage} from '../config/deployment.mjs';
import {deployment as environmentConfig} from '../config/deployment.mjs';
export const namespace=environmentConfig.namespace;
export const hostname=environmentConfig.workflowHost;
export const labels={'app.kubernetes.io/name':'opal-qa','app.kubernetes.io/part-of':'opal','app.kubernetes.io/managed-by':'opal-deploy'};
export const postgres='postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd';
export const corndogs=environmentConfig.corndogsImage;
const locked={allowPrivilegeEscalation:false,readOnlyRootFilesystem:true,capabilities:{drop:['ALL']}};
const resources=(cpu,memory,limitCPU='500m',limitMemory='256Mi')=>({requests:{cpu,memory},limits:{cpu:limitCPU,memory:limitMemory}});
const meta=name=>({name,namespace,labels});
const selector=component=>({'app.kubernetes.io/name':'opal-qa','opal.component':component});
const secretEnv=(name,secret,key)=>({name,valueFrom:{secretKeyRef:{name:secret,key}}});
const trust={name:'trust',configMap:{name:'qa-trust'}};
const mountTrust={name:'trust',mountPath:'/trust',readOnly:true};
const config={name:'config',configMap:{name:'qa-config'}};
const mountConfig={name:'config',mountPath:'/config',readOnly:true};
function pod(component,containers,volumes=[],uid=65532) {
  return {metadata:{labels:{...labels,...selector(component)}},spec:{serviceAccountName:'qa',automountServiceAccountToken:false,enableServiceLinks:false,nodeSelector:{'kubernetes.io/arch':'amd64'},terminationGracePeriodSeconds:150,imagePullSecrets:[{name:'opal-registry'}],securityContext:{runAsNonRoot:true,runAsUser:uid,runAsGroup:uid,fsGroup:uid,seccompProfile:{type:'RuntimeDefault'}},containers,volumes}};
}
const service=(name,component,port)=>({apiVersion:'v1',kind:'Service',metadata:meta(name),spec:{selector:selector(component),ports:[{name:'tcp',port,targetPort:port}]}});
const policy=(name,component,ingress=[],egress=[])=>({apiVersion:'networking.k8s.io/v1',kind:'NetworkPolicy',metadata:meta(name),spec:{podSelector:{matchLabels:selector(component)},policyTypes:['Ingress','Egress'],ingress,egress}});
const peer=component=>({podSelector:{matchLabels:selector(component)}});
const tcp=port=>({protocol:'TCP',port});
const dns={to:[{namespaceSelector:{matchLabels:{'kubernetes.io/metadata.name':'kube-system'}},podSelector:{matchLabels:{'k8s-app':'kube-dns'}}}],ports:[{protocol:'UDP',port:53},tcp(53)]};

export function manifests(images,configuration,ca) {
  for(const image of [images.workflows,images.model])if(!immutableImage(image))throw Error('Use immutable private Magi images');
  if(configuration.application?.identity?.domain!==environmentConfig.domain||configuration.question_worker?.actor?.identity?.domain!==environmentConfig.domain)throw Error('Only the example test domain is supported');
  const all=[
    {apiVersion:'v1',kind:'ResourceQuota',metadata:{name:'opal',namespace,labels:{...labels,'app.kubernetes.io/name':'opal'}},spec:{hard:{pods:'16','requests.cpu':'4','requests.memory':'8Gi','limits.cpu':'10','limits.memory':'12Gi','count/secrets':'8','count/services':'5','persistentvolumeclaims':'3','requests.storage':'8Gi'}}},
    {apiVersion:'v1',kind:'ServiceAccount',metadata:meta('qa'),automountServiceAccountToken:false},
    {apiVersion:'v1',kind:'ConfigMap',metadata:meta('qa-config'),data:{'workflows.json':JSON.stringify(configuration)}},
    {apiVersion:'v1',kind:'ConfigMap',metadata:meta('qa-trust'),data:{'ca.crt':ca}},
  ];
  for(const [name,size] of [['qa-postgres','4Gi'],['qa-corndogs','2Gi']])all.push({apiVersion:'v1',kind:'PersistentVolumeClaim',metadata:meta(name),spec:{storageClassName:environmentConfig.storageClass,accessModes:['ReadWriteOnce'],resources:{requests:{storage:size}}}});
  const pg={name:'postgres',image:postgres,args:['postgres','-c','ssl=on','-c','ssl_cert_file=/tls/tls.crt','-c','ssl_key_file=/tls/tls.key'],securityContext:locked,resources:resources('100m','128Mi','1','1Gi'),env:[secretEnv('POSTGRES_PASSWORD','qa-db-admin','password'),{name:'PGDATA',value:'/data/pgdata'}],volumeMounts:[{name:'data',mountPath:'/data'},{name:'tls',mountPath:'/tls',readOnly:true},{name:'tmp',mountPath:'/tmp'},{name:'run',mountPath:'/var/run/postgresql'}],readinessProbe:{exec:{command:['pg_isready','-h','127.0.0.1','-U','postgres']},periodSeconds:3},livenessProbe:{exec:{command:['pg_isready','-h','127.0.0.1','-U','postgres']},initialDelaySeconds:30,periodSeconds:10}};
  all.push({apiVersion:'apps/v1',kind:'StatefulSet',metadata:meta('qa-postgres'),spec:{serviceName:'qa-postgres',replicas:1,selector:{matchLabels:selector('postgres')},template:pod('postgres',[pg],[{name:'data',persistentVolumeClaim:{claimName:'qa-postgres'}},{name:'tls',secret:{secretName:'qa-postgres-tls',defaultMode:0o440}},{name:'tmp',emptyDir:{sizeLimit:'64Mi'}},{name:'run',emptyDir:{sizeLimit:'16Mi'}}],999)}});
  const cd={name:'corndogs',image:corndogs,securityContext:locked,resources:resources('100m','64Mi','500m','256Mi'),env:Object.entries({STORAGE_BACKEND:'file',CORNDOGS_FILESTORE_DIR:'/data',CORNDOGS_FILESTORE_SYNC:'always',CORNDOGS_SUBMISSION_KEY_POLICY:'required',CORNDOGS_TASK_GUARD_POLICY:'required',CORNDOGS_TLS_CERT_FILE:'/tls/tls.crt',CORNDOGS_TLS_KEY_FILE:'/tls/tls.key',LOGLEVEL:'error'}).map(([name,value])=>({name,value})),volumeMounts:[{name:'data',mountPath:'/data'},{name:'tls',mountPath:'/tls',readOnly:true}],readinessProbe:{tcpSocket:{port:5080},periodSeconds:3},livenessProbe:{tcpSocket:{port:5080},initialDelaySeconds:20,periodSeconds:10}};
  all.push({apiVersion:'apps/v1',kind:'StatefulSet',metadata:meta('qa-corndogs'),spec:{serviceName:'qa-corndogs',replicas:1,selector:{matchLabels:selector('corndogs')},template:pod('corndogs',[cd],[{name:'data',persistentVolumeClaim:{claimName:'qa-corndogs'}},{name:'tls',secret:{secretName:'qa-corndogs-tls',defaultMode:0o440}}])}});
  all.push(service('qa-postgres','postgres',5432),service('qa-corndogs','corndogs',5080));
  for(const [component,flag,replicas] of [['api',null,2],['coordinator','-coordinate',2],['sweeper','-sweep',1],['worker','-question-worker',1]]) {
    const app={name:component,image:images.workflows,args:['-config','/config/workflows.json',...(flag?[flag]:[])],securityContext:locked,resources:resources('25m','64Mi','250m','256Mi'),env:[secretEnv('WORKFLOWS_DATABASE_URL','qa-db-runtime','url'),{name:'GOMEMLIMIT',value:'192MiB'}],volumeMounts:[mountConfig,mountTrust]};
    if(component==='api') {
      app.env.push(secretEnv('WORKFLOWS_RP_API_KEY','qa-rp','api-key'),{name:'WORKFLOWS_LISTEN',value:'0.0.0.0:8082'},{name:'WORKFLOWS_ORIGIN',value:`https://${hostname}`});
      app.readinessProbe={httpGet:{path:'/health/ready',port:8082},periodSeconds:5,timeoutSeconds:9};
      app.livenessProbe={httpGet:{path:'/health/live',port:8082},periodSeconds:10};
    }
    if(component==='sweeper')app.readinessProbe={exec:{command:['/usr/local/bin/workflows','-config','/config/workflows.json','-sweep-check']},periodSeconds:10,timeoutSeconds:10};
    const containers=[app];
    if(component==='worker')containers.push({name:'model',image:images.model,args:['-m','/opt/model/model.gguf','--host','127.0.0.1','--port','8088','-ngl','0','-t','2','-tb','2','-c','4096','-np','1','--no-webui','--alias','opal-test-small','--seed','1','--log-disable'],securityContext:locked,resources:resources('1','1536Mi','2','3Gi'),startupProbe:{exec:{command:['/usr/local/bin/python3','-c',"import urllib.request; urllib.request.urlopen('http://127.0.0.1:8088/health', timeout=2).close()"]},periodSeconds:5,timeoutSeconds:3,failureThreshold:60},readinessProbe:{exec:{command:['/usr/local/bin/python3','-c',"import urllib.request; urllib.request.urlopen('http://127.0.0.1:8088/health', timeout=2).close()"]},periodSeconds:5,timeoutSeconds:3}});
    const template=pod(component,containers,[trust,config]);
    template.spec.initContainers=[waitStores(images.workflows)];
    // Worker replacement must not double the model's resource footprint.
    all.push({apiVersion:'apps/v1',kind:'Deployment',metadata:meta('qa-'+component),spec:{replicas,revisionHistoryLimit:2,selector:{matchLabels:selector(component)},strategy:component==='worker'?{type:'Recreate'}:{type:'RollingUpdate',rollingUpdate:{maxSurge:1,maxUnavailable:0}},template}});
  }
  all.push(service('qa-api','api',8082),{apiVersion:'policy/v1',kind:'PodDisruptionBudget',metadata:meta('qa-api'),spec:{minAvailable:1,selector:{matchLabels:selector('api')}}});
  const roles=['api','coordinator','sweeper','worker','bootstrap'];
  all.push(policy('qa-postgres','postgres',[{from:roles.map(peer),ports:[tcp(5432)]}]),policy('qa-corndogs','corndogs',[{from:roles.map(peer),ports:[tcp(5080)]}]));
  for(const component of roles) {
    const egress=[dns,{to:[peer('postgres')],ports:[tcp(5432)]},{to:[peer('corndogs')],ports:[tcp(5080)]}];
    if(component==='api')egress.push({to:[{namespaceSelector:{matchLabels:{'kubernetes.io/metadata.name':environmentConfig.linkkeysNamespace}},podSelector:{matchLabels:{'app.kubernetes.io/name':'linkkeys','app.kubernetes.io/instance':environmentConfig.linkkeysInstance}}}],ports:[tcp(4987)]});
    all.push(policy('qa-'+component,component,component==='api'?[{from:[{namespaceSelector:{matchLabels:{'kubernetes.io/metadata.name':environmentConfig.ingressNamespace}}}],ports:[tcp(8082)]}]:[],egress));
  }
  return all;
}
export function route() {
  return {apiVersion:'gateway.networking.k8s.io/v1',kind:'HTTPRoute',metadata:meta('qa-api'),spec:{parentRefs:[{name:environmentConfig.gatewayName,namespace:environmentConfig.ingressNamespace,sectionName:environmentConfig.gatewaySection}],hostnames:[hostname],rules:[{matches:[{path:{type:'PathPrefix',value:'/'}}],backendRefs:[{name:'qa-api',port:8082}]}]}};
}
export function bootstrapJob(image,name='qa-bootstrap-v4') {
  const env=[secretEnv('WORKFLOWS_DATABASE_URL','qa-db-admin','url')];
  const init=flag=>({name:flag.slice(1),image,args:['-config','/config/workflows.json',flag],env,securityContext:locked,resources:resources('25m','64Mi'),volumeMounts:[mountConfig,mountTrust]});
  const sql="\\getenv runtime_password OPAL_RUNTIME_PASSWORD\nSELECT format('CREATE ROLE workflow_runtime LOGIN PASSWORD %L', :'runtime_password') WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname='workflow_runtime') \\gexec\nREVOKE CREATE ON SCHEMA public FROM PUBLIC;\nGRANT USAGE ON SCHEMA public TO workflow_runtime;\nGRANT SELECT ON workflow_service, workflow_grants, workflow_journal_binding TO workflow_runtime;\nGRANT SELECT, INSERT, UPDATE ON workflow_definitions,workflow_drafts,workflow_runs,workflow_run_steps,workflow_coordination,workflow_intents,workflow_results,workflow_human_bindings,workflow_sweep_health,workflow_timeout_work,workflow_timeout_attempts,workflow_call_evidence TO workflow_runtime;\n";
  const template=pod('bootstrap',[{name:'runtime-grants',image:postgres,command:['psql','-h','qa-postgres','-U','postgres','-d','postgres','-v','ON_ERROR_STOP=1','-f','/sql/grants.sql'],env:[secretEnv('PGPASSWORD','qa-db-admin','password'),secretEnv('OPAL_RUNTIME_PASSWORD','qa-db-runtime','password'),{name:'PGSSLMODE',value:'verify-full'},{name:'PGSSLROOTCERT',value:'/trust/ca.crt'}],securityContext:locked,resources:resources('25m','32Mi'),volumeMounts:[mountTrust,{name:'sql',mountPath:'/sql',readOnly:true}]}],[trust,config,{name:'sql',configMap:{name:'qa-grants'}}]);
  template.spec.restartPolicy='Never';template.spec.initContainers=[waitStores(image),init('-migrate'),init('-apply-policy')];
  return [{apiVersion:'v1',kind:'ConfigMap',metadata:meta('qa-grants'),data:{'grants.sql':sql}},{apiVersion:'batch/v1',kind:'Job',metadata:meta(name),spec:{backoffLimit:1,activeDeadlineSeconds:180,template}}];
}
function waitStores(image) {
  return {name:'wait-stores',image,env:[],securityContext:locked,resources:resources('25m','32Mi'),volumeMounts:[mountTrust],command:['/usr/local/bin/python3','-c',`import socket,ssl,struct,time
c=ssl.create_default_context(cafile='/trust/ca.crt')
end=time.monotonic()+45
while True:
 try:
  for host,port in [('qa-postgres',5432),('qa-corndogs',5080)]:
   s=socket.create_connection((host,port),2)
   if port==5432:
    s.sendall(struct.pack('!II',8,80877103))
    assert s.recv(1)==b'S'
   with c.wrap_socket(s,server_hostname=host): pass
  print('Both stores passed verified TLS readiness');break
 except Exception as e:
  if time.monotonic()>end: raise
  print(type(e).__name__,flush=True);time.sleep(1)
`]};
}
