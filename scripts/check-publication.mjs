import {execFileSync} from 'node:child_process';
import {readFileSync, lstatSync, existsSync} from 'node:fs';
import {homedir} from 'node:os';
import {resolve} from 'node:path';

// This is a publication guard, not a complete secret detector.
export function publicationFindings(path, bytes, privateValues = []) {
  const findings = [];
  if (/(^|\/)(\.local|\.build|test-results|node_modules|__pycache__)(\/|$)/.test(path) || /^experiments\/[^/]+\/results\//.test(path)) findings.push('private-or-generated-path');
  if (/\.(?:sqlite(?:-wal|-shm)?|db|key|pem|p12|pfx|wav|log|jsonl)$/.test(path)) findings.push('raw-evidence-or-credential-file');
  if (/^config\/.*\.json$/.test(path) && !path.endsWith('.example.json')) findings.push('non-example-configuration');
  if (/(^|\/)\.env(?:\.|$)/.test(path) && !path.endsWith('.env.example') || /\.(?:local|private)\.json$/.test(path)) findings.push('private-configuration');
  const text = bytes.toString('utf8');
  const normalized=(path+'\n'+text).replaceAll('\\.','.').replaceAll('[.]','.');
  if(privateValues.some(value=>normalized.toLowerCase().includes(value.toLowerCase())))findings.push('private-environment-value');
  const patterns = [
    ['private-key', /-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----/],
    ['github-token', /\bgh[pousr]_[A-Za-z0-9]{30,}\b/],
    ['aws-access-key', /\bAKIA[0-9A-Z]{16}\b/],
    ['bearer-jwt', /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{20,}\b/],
  ];
  for (const [name, pattern] of patterns) if (pattern.test(text)) findings.push(name);
  if (path.endsWith('.json')) {
    try {
      const visit = value => {
        if (!value || typeof value !== 'object') return;
        if (['OKP','EC','RSA'].includes(value.kty) && typeof value.d === 'string') findings.push('private-jwk');
        for (const child of Object.values(value)) visit(child);
      };
      visit(JSON.parse(text));
    } catch { findings.push('invalid-json'); }
  }
  return [...new Set(findings)];
}

export function checkPublication(root, {staged = false, privateValues = []} = {}) {
  const git = args => execFileSync('git', args, {cwd: root, maxBuffer: 64 * 1024 * 1024});
  const paths = [...new Set(git(staged ? ['ls-files','-z'] : ['ls-files','--cached','--others','--exclude-standard','-z']).toString().split('\0').filter(Boolean))];
  const findings = [];
  for (const path of paths) {
    try {
      let bytes;
      if (staged) {
        const entry = git(['ls-files','--stage','--',path]).toString();
        if (!entry.startsWith('100644 ') && !entry.startsWith('100755 ')) {findings.push({path, rules:['non-regular-index-entry']}); continue;}
        bytes = git(['show',`:${path}`]);
      } else {
        if (!lstatSync(resolve(root,path)).isFile()) {findings.push({path,rules:['non-regular-file']}); continue;}
        bytes = readFileSync(resolve(root,path));
      }
      const rules = publicationFindings(path, bytes, privateValues);
      if (rules.length) findings.push({path,rules});
    } catch { findings.push({path,rules:['unreadable-file']}); }
  }
  return {scope:staged?'Git index':'tracked and non-ignored working files',files:paths.length,findings};
}

if (process.argv[1] === import.meta.filename) {
  const args=process.argv.slice(2);let staged=false,privateValues=[];
  for(let i=0;i<args.length;i++){
    if(args[i]==='--staged'){staged=true;continue;}
    if(args[i]==='--operator-config'&&args[i+1]){
      const operator=JSON.parse(readFileSync(resolve(args[++i])));
      const keys=['domain','secondaryDomain','namespace','clusterAddress','registry','discoveryHost','workflowHost','approvalHost','canaryHost','linkkeysHost','linkkeysNamespace','applicationInstance','workflowInstance','installation','kubeconfig','qaProfileDir','releaseProfileDir','speechProfileDir','operatorStateDir','releasePolicy','legacyReleasePolicyPath','loginCredentials'];
      privateValues=keys.map(k=>operator[k]).filter(v=>typeof v==='string'&&v.length>=8);
      if(typeof operator.releasePolicy==='string'){
        const policyPath=operator.releasePolicy.replace(/^~\//,homedir()+'/');
        if(existsSync(policyPath)){
          const policy=JSON.parse(readFileSync(policyPath));
          for(const value of [policy.approver?.domain,policy.approver?.account_id])if(typeof value==='string'&&value.length>=8)privateValues.push(value);
        }
      }
      continue;
    }
    throw Error('Use: node scripts/check-publication.mjs [--staged] [--operator-config PATH]');
  }
  const report = checkPublication(resolve(import.meta.dirname,'..'), {staged,privateValues});
  console.log(JSON.stringify(report,null,2));
  process.exitCode = report.findings.length ? 1 : 0;
}
