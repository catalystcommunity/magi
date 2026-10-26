import {spawnSync} from 'node:child_process';
import {homedir} from 'node:os';
import {join,resolve} from 'node:path';
import {loadDeployment} from '../config/deployment.mjs';

const root=resolve(import.meta.dirname,'..');
const [script,...args]=process.argv.slice(2);
if(!script || !/^(?:deploy|scripts)\/[a-z0-9-]+\.mjs$/.test(script) || script==='deploy/run.mjs')throw Error('Use: node deploy/run.mjs deploy/SCRIPT.mjs [arguments]');
const path=process.env.MAGI_DEPLOYMENT_CONFIG ?? join(homedir(),'.config/magi/deployment.json');
const config=loadDeployment(path);
if(config.kind!=='operator')throw Error('An operator overlay is required');
const result=spawnSync(process.execPath,[join(root,script),...args],{cwd:root,stdio:'inherit',env:{...process.env,MAGI_DEPLOYMENT_CONFIG:path,KUBECONFIG:config.kubeconfig,MAGI_CORNDOGS_IMAGE:config.corndogsImage,MAGI_SPEECH_PROFILE_DIR:config.speechProfileDir,MAGI_QA_PROFILE_DIR:config.qaProfileDir}});
if(result.error)throw Error('Deployment command could not start');
process.exitCode=result.status??1;
