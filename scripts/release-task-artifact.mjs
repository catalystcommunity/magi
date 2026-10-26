import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {readFileSync, writeFileSync, mkdirSync, cpSync, readdirSync, existsSync} from 'node:fs';
import {resolve, join, relative} from 'node:path';

// Build only the trusted queue helper. Do not copy operator profiles, source
// workspaces, compiler caches, or deployment credentials into the image.
export function releaseTaskArtifact(root, app) {
  const env = {...process.env, CGO_ENABLED:'0', GOOS:'linux', GOARCH:'amd64', GOCACHE:process.env.GOCACHE || '/tmp/opal-knowledge-go-build-20261011'};
  const run = args => execFileSync('go', args, {cwd:root, env, encoding:'utf8', maxBuffer:8*1024*1024});
  const hash = b => createHash('sha256').update(b).digest('hex');
  const inputs = new Set(['go.mod','go.sum','vendor/modules.txt']);
  const listing = run(['list','-mod=vendor','-deps','-f','{{if not .Standard}}{{.Dir}}|{{join .GoFiles ","}}|{{join .EmbedFiles ","}}{{end}}','./cmd/release-tasks']);
  for (const line of listing.trim().split('\n').filter(Boolean)) {
    const [dir,go,embedded] = line.split('|');
    for (const file of [go,embedded].flatMap(v=>v.split(',')).filter(Boolean)) {
      const path = relative(root,resolve(dir,file));
      if (path.startsWith('..') || path.startsWith('/')) throw Error('helper-source-outside-repository');
      inputs.add(path);
    }
  }
  const sourceHashes = Object.fromEntries([...inputs].sort().map(p=>[p,hash(readFileSync(join(root,p)))]));
  mkdirSync(join(app,'bin'),{recursive:true});
  const build = ['build','-mod=vendor','-trimpath','-buildvcs=false','-ldflags=-buildid=','-o',join(app,'bin/release-tasks'),'./cmd/release-tasks'];
  run(build);
  for (const [p,h] of Object.entries(sourceHashes)) if (hash(readFileSync(join(root,p)))!==h) throw Error('helper-source-drift');
  const artifact = {go:run(['version']).trim(),platform:'linux/amd64',cgo:false,sourceHashes,binarySha256:hash(readFileSync(join(app,'bin/release-tasks')))};
  writeFileSync(join(app,'bin/release-tasks.provenance.json'),JSON.stringify(artifact,null,2)+'\n');
  cpSync(join(root,'licenses/corndogs-ISC.txt'),join(app,'bin/corndogs-ISC.txt'));
  const goLicense = [join(run(['env','GOROOT']).trim(),'LICENSE'), '/usr/share/licenses/go/LICENSE'].find(existsSync);
  if (!goLicense) throw Error('go-license-not-found');
  cpSync(goLicense,join(app,'bin/Go-LICENSE'));
  function licenses(path) {for(const f of readdirSync(join(root,path),{withFileTypes:true})){
    const p=join(path,f.name);if(f.isDirectory())licenses(p);
    else if(/^(LICENSE|COPYING|NOTICE)([.-]|$)/i.test(f.name)){mkdirSync(join(app,'bin/licenses',path),{recursive:true});cpSync(join(root,p),join(app,'bin/licenses',p));}
  }}
  licenses('vendor');
  return artifact;
}
