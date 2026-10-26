import {readFileSync, statSync} from 'node:fs';
import {homedir} from 'node:os';
import {isAbsolute, join} from 'node:path';
import {BlockList, isIPv4} from 'node:net';

export const examplePath = new URL('./deployment.example.json', import.meta.url);
export const exampleDeployment = Object.freeze(JSON.parse(readFileSync(examplePath)));
const pathKeys = ['kubeconfig','qaProfileDir','releaseProfileDir','speechProfileDir','operatorStateDir','releasePolicy','loginCredentials'];
const dns = s => typeof s === 'string' && /^[a-z0-9]+(?:[.-][a-z0-9]+)*$/.test(s) && s.length <= 253;
const label = s => dns(s) && !s.includes('.') && s.length <= 63;
const need = (ok, message) => {if (!ok) throw Error(`deployment-config: ${message}`);};

export function validateDeployment(overlay, home = homedir()) {
  need(overlay && typeof overlay === 'object' && !Array.isArray(overlay), 'object required');
  need(Object.keys(overlay).every(k => Object.hasOwn(exampleDeployment,k)), 'unknown field');
  const value = {...exampleDeployment,...overlay};
  need(value.version === 1 && ['operator','example'].includes(value.kind), 'version or kind');
  for (const [key,v] of Object.entries(value)) if(key!=='version') need(typeof v==='string' && v.length>0 && !/[\x00-\x1f\x7f]/.test(v), `invalid ${key}`);
  for (const key of ['domain','secondaryDomain','registry','discoveryHost','workflowHost','approvalHost','canaryHost','linkkeysHost']) need(dns(value[key]), `invalid ${key}`);
  for (const key of ['namespace','linkkeysNamespace','linkkeysService','linkkeysInstance','applicationInstance','workflowInstance','installation','storageClass','ingressNamespace','gatewayName','gatewaySection']) need(label(value[key]), `invalid ${key}`);
  need(/^[A-Za-z0-9._:-]+$/.test(value.kubeContext), 'invalid context');
  need(/^[A-Za-z0-9._@-]+$/.test(value.testUsername), 'invalid test username');
  need(/^config\/[a-z0-9-]+\.json$/.test(value.legacyReleasePolicyPath), 'invalid legacy policy path');
  need(isIPv4(value.clusterAddress), 'invalid cluster address');
  need(value.clusterServer === `https://${value.clusterAddress}:6443` && value.clusterApiCIDR === `${value.clusterAddress}/32`, 'cluster endpoint binding');
  need(value.repository.startsWith(value.registry+'/') && /^[a-z0-9./_-]+$/.test(value.repository), 'repository binding');
  need(/^[a-z0-9./_-]+@sha256:[0-9a-f]{64}$/.test(value.corndogsImage), 'immutable Corndogs image');
  need(/^[a-z0-9./_-]+@sha256:[0-9a-f]{64}$/.test(value.linkkeysImage), 'immutable LinkKeys image');
  const [network,prefix,...extra]=value.podCIDR.split('/');
  need(!extra.length && isIPv4(network) && /^\d+$/.test(prefix) && Number(prefix)>=8 && Number(prefix)<=30, 'pod CIDR');
  for(const key of pathKeys){
    if(value[key].startsWith('~/')) value[key]=join(home,value[key].slice(2));
    need(isAbsolute(value[key]), `absolute ${key} required`);
  }
  return Object.freeze(value);
}

export function loadDeployment(path = process.env.MAGI_DEPLOYMENT_CONFIG) {
  if(path===undefined) return validateDeployment({}); // Pure render and unit-test defaults only.
  need(isAbsolute(path), 'MAGI_DEPLOYMENT_CONFIG must be absolute');
  const stat=statSync(path);need(stat.isFile() && !(stat.mode&0o022), 'configuration must not be group or world writable');
  return validateDeployment(JSON.parse(readFileSync(path)));
}

export const deployment = loadDeployment();
export function requireDeployment() {
  need(deployment.kind==='operator' && !!process.env.MAGI_DEPLOYMENT_CONFIG, 'select an operator overlay with deploy/run.mjs');
  process.env.KUBECONFIG=deployment.kubeconfig;
  return deployment;
}
export const immutableImage = image => typeof image==='string' && image.startsWith(deployment.repository+'@sha256:') && /^[0-9a-f]{64}$/.test(image.slice(deployment.repository.length+8));
export function podAddressAllowed(ip) {
  if(!isIPv4(ip))return false;
  const [network,prefix]=deployment.podCIDR.split('/'), allow=new BlockList();
  allow.addSubnet(network,Number(prefix),'ipv4');
  return allow.check(ip,'ipv4') && ip!==deployment.clusterAddress && !ip.startsWith('127.') && !ip.startsWith('169.254.');
}
export function runtimeDeployment() {
  // Mount routing and scope settings, not workstation paths or login hints.
  return Object.fromEntries(Object.entries(deployment).filter(([k])=>!pathKeys.includes(k)&&k!=='testUsername'));
}
