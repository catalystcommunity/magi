// Reproduce the local ABI compatibility patch. Do not change cryptographic code.
import {execFileSync} from 'node:child_process';
import {readFileSync,writeFileSync} from 'node:fs';
import {resolve,join} from 'node:path';
const root=resolve(import.meta.dirname,'..');
const cache=execFileSync('go',['env','GOMODCACHE'],{encoding:'utf8',cwd:root}).trim();
const version='v0.0.0-20261012235123-03fcbd8e7007';
const check=process.argv.includes('--check');
if(!check)execFileSync('go',['mod','vendor'],{cwd:root,stdio:'inherit'});
for(const [sdk,file]of [['local-rp','rpc.go'],['regular-rp','transport.go']]){
 const module=`github.com/catalystcommunity/linkkeys/sdks/${sdk}/go`;
 const original=readFileSync(join(cache,`${module}@${version}`,file),'utf8');
 const before='\tcarrier := rpctransport.NewStreamCarrierWithMaxFrame(conn, maxFrameSize)';
 const after='\tcarrier, carrierErr := rpctransport.NewStreamCarrierWithMaxFrame(conn, maxFrameSize)\n\tif carrierErr != nil {\n\t\treturn nil, carrierErr\n\t}';
 if(original.split(before).length!==2)throw Error('LinkKeys source pin or constructor changed');
 const expected=original.replace(before,after),dest=join(root,'vendor',module,file);
 if(check){if(readFileSync(dest,'utf8')!==expected)throw Error(`unexpected LinkKeys patch: ${sdk}`);}else writeFileSync(dest,expected);
}
// Keep upstream examples independent of an operator's identity domain.
// These two comment changes do not change SDK behavior or license notices.
const module='github.com/catalystcommunity/linkkeys/sdks/local-rp/go';
let browser=readFileSync(join(cache,`${module}@${version}`,'browser.go'),'utf8');
for(const [pattern,replacement] of [
 [/^\/\/ The identity domain \(the domain the user selected, e\.g\. `[^`]+`\)$/m,'// The identity domain (the domain the user selected, e.g. `catalystsquad.local`)'],
 [/^\/\/ \(e\.g\. `https:\/\/linkkeys\.[^`]+` or$/m,'// (e.g. `https://linkkeys.catalystsquad.local` or'],
]){
 if(!pattern.test(browser))throw Error('LinkKeys browser example changed');
 browser=browser.replace(pattern,replacement);
}
const browserPath=join(root,'vendor',module,'browser.go');
if(check){if(readFileSync(browserPath,'utf8')!==browser)throw Error('unexpected LinkKeys browser comment patch');}else writeFileSync(browserPath,browser);
console.log('LinkKeys compatibility and public example patches verified');
