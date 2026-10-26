import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
const files=[join(root,'README.md')];
function collect(dir) {
  for(const entry of readdirSync(dir,{withFileTypes:true})) {
    if(entry.name.startsWith('.')||['results','generated','generated-capture','target','node_modules'].includes(entry.name))continue;
    const path=join(dir,entry.name);
    if(entry.isDirectory())collect(path);
    else if(entry.name.endsWith('.md'))files.push(path);
  }
}
collect(join(root,'docs'));collect(join(root,'experiments'));
let checked=0;const failures=[];
for(const file of files) {
  const text=readFileSync(file,'utf8').replace(/^```[^\n]*\n[\s\S]*?^```\s*$/gm,'');
  for(const match of text.matchAll(/\[[^\]]*\]\(([^)]+)\)/g)) {
    const target=match[1].replace(/^<|>$/g,'').split('#')[0];
    if(!target||/^[a-z][a-z0-9+.-]*:/i.test(target))continue;
    const path=resolve(dirname(file),decodeURIComponent(target));
    checked++;
    if(!existsSync(path)||(!statSync(path).isFile()&&!statSync(path).isDirectory()))failures.push({file,target});
  }
}
console.log(JSON.stringify({documents:files.length,local_links:checked,failures},null,2));
process.exitCode=failures.length?1:0;
