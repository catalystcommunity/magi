import {runtimeDeployment} from '../config/deployment.mjs';

export function withEnvironment(resources) {
  const result=[];
  for(const resource of resources){
    const pod=resource.kind==='Deployment'?resource.spec.template.spec:resource.kind==='CronJob'?resource.spec.jobTemplate.spec.template.spec:undefined;
    if(!pod || !pod.containers.some(c=>['approval','controller','renewal'].includes(c.name))){result.push(resource);continue;}
    const name=resource.metadata.name+'-environment';
    const config={apiVersion:'v1',kind:'ConfigMap',metadata:{name,namespace:resource.metadata.namespace,labels:resource.metadata.labels},data:{'deployment.json':JSON.stringify(runtimeDeployment())}};
    const volume={name:'environment',configMap:{name,defaultMode:0o444}};
    pod.volumes=[...(pod.volumes??[]).filter(v=>v.name!=='environment'),volume];
    for(const c of pod.containers.filter(c=>['approval','controller','renewal'].includes(c.name))){
      c.env=[...(c.env??[]).filter(e=>e.name!=='MAGI_DEPLOYMENT_CONFIG'),{name:'MAGI_DEPLOYMENT_CONFIG',value:'/environment/deployment.json'}];
      c.volumeMounts=[...(c.volumeMounts??[]).filter(v=>v.name!=='environment'),{name:'environment',mountPath:'/environment',readOnly:true}];
    }
    result.push(config,resource);
  }
  return result;
}
