import {requireDeployment} from '../config/deployment.mjs';
if(process.argv.includes('--push'))requireDeployment();
import {deployment as environmentConfig} from '../config/deployment.mjs';
import {operatorFile} from './operator-state.mjs';
import {execFileSync} from 'node:child_process';
import {mkdtempSync, mkdirSync, cpSync, readFileSync, writeFileSync, readdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';
import {createHash} from 'node:crypto';
import {releaseTaskArtifact} from './release-task-artifact.mjs';
import {copyReleaseImage} from './release-image-context.mjs';
const root = resolve(import.meta.dirname, '..'), context = mkdtempSync(join(tmpdir(), 'opal-release-image-'));
const controller = process.argv.includes('--controller');
const tasks = process.argv.includes('--tasks');
if (tasks && !controller || process.argv.slice(2).some(a => !['--push', '--controller', '--tasks'].includes(a))) throw Error('Use: node scripts/build-release-image.mjs [--controller [--tasks]] [--push]');
const app = join(context, 'app'); mkdirSync(app);
const hash = value => createHash('sha256').update(value).digest('hex');
// The explicit list excludes .local, databases, operator profiles, kubeconfig,
// build tools, task execution tools, and registry credentials.
copyReleaseImage(root, app);
const taskArtifact = tasks ? releaseTaskArtifact(root,app) : undefined;
cpSync(join(root, 'deploy/release.Dockerfile'), join(context, 'Dockerfile'));
const files = {};
function scan(path = '') {for (const f of readdirSync(join(app, path), {withFileTypes: true})) {const name = join(path, f.name); if (f.isDirectory()) scan(name); else files[name] = hash(readFileSync(join(app, name)));}}
scan();
const sourceDigest = hash(JSON.stringify(files)), recipeDigest = hash(readFileSync(join(context, 'Dockerfile')));
const repository = environmentConfig.repository;
const component = controller ? 'controller' : 'approval';
const tag = `${repository}:release-${component}-${sourceDigest.slice(0, 16)}`;
execFileSync('docker', ['build', '--platform=linux/amd64', '-t', tag, context], {stdio: 'inherit'});
if (process.argv.includes('--push')) {
  execFileSync('docker', ['push', tag], {stdio: 'inherit'});
  const digests = JSON.parse(execFileSync('docker', ['image', 'inspect', tag, '--format', '{{json .RepoDigests}}'], {encoding: 'utf8'}));
  const image = digests.find(v => v.startsWith(`${repository}@sha256:`)); if (!image) throw Error('image-digest');
  
  writeFileSync(operatorFile(`release-${component}-image.json`), JSON.stringify({image, sourceDigest, recipeDigest, files, taskArtifact, builtAt: new Date().toISOString()}, null, 2) + '\n');
  console.log(image);
} else console.log(`Built ${tag}`);
