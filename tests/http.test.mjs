import test,{before,after} from 'node:test';
import assert from 'node:assert/strict';
import { spawn,execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdtempSync,readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join,resolve } from 'node:path';
import { DiscoveryAsyncClient } from '../.build/client.async.gen.js';
import * as wire from '../.build/codec.gen.js';
import { HTTPTransport } from '../protocol/http.mjs';
import { descriptor } from './fixtures.mjs';

const root=resolve(import.meta.dirname,'..');
const dir=mkdtempSync(join(tmpdir(),'opal-http-test-'));
let child,origin,client,stopped=false;
before(async()=>{
  execFileSync('go',['build','-o',join(dir,'opal'),'./cmd/magi'],{cwd:root,env:{...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'}});
  child=spawn(join(dir,'opal'),[],{env:{...process.env,OPAL_LISTEN:'127.0.0.1:0',OPAL_ORIGIN:''},stdio:['ignore','ignore','pipe']});
  origin=await new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>reject(Error('server start timeout')),10000);let log='';
    child.once('error',reject);child.once('exit',()=>reject(Error('server stopped before readiness')));
    child.stderr.on('data',b=>{log+=b;const match=log.match(/ready at (http:\/\/127\.0\.0\.1:\d+);/);if(match){clearTimeout(timer);resolve(match[1]);}});
  });
  client=new DiscoveryAsyncClient(new HTTPTransport(`${origin}/csil/v1/rpc`));
});
after(async()=>{if(child&&!stopped){const exit=once(child,'exit');child.kill('SIGTERM');await exit;}});

test('generated TypeScript client calls the Go server',async()=>{
  const d=await client.describe({versions:['0alpha1'],requiredFeatures:['strict-cbor']});
  assert.deepEqual(d.implementedOperations,['Discovery/describe','Discovery/read-schema']);
  assert.deepEqual(d.authenticatedOperations,[]);const s=await client.readSchema({schema:d.schema});
  assert.deepEqual(Buffer.from(s.descriptor),readFileSync(join(root,'generated/go/magi.csil-schema.cbor')));
});
test('versions, required features, and wrong schema are refused',async()=>{
  await assert.rejects(client.describe({versions:['future'],requiredFeatures:[]}),{code:1002});
  await assert.rejects(client.describe({versions:['0alpha1'],requiredFeatures:['execute']}),{code:1002});
  await assert.rejects(client.readSchema({schema:{name:'opal',version:'0alpha1',digest:new Uint8Array(32)}}),{code:1002});
});
for(const s of descriptor.get('body').get('services'))for(const op of s.get('operations')){
  if(s.get('name')==='Discovery')continue;
  test(`unimplemented ${s.get('name')}/${op.get('name')} has no public handler`,async()=>{
    const raw=wire.encodeValue(new Map([['v',1],['id',9],['service',s.get('name')],['op',op.get('name')],['payload',{tag:24,value:Uint8Array.of(0xa0)}]]));
    const r=await fetch(`${origin}/csil/v1/rpc`,{method:'POST',headers:{'content-type':'application/cbor'},body:raw});
    assert.equal(r.status,200);const response=wire.decode(new Uint8Array(await r.arrayBuffer()));assert.equal(response.get('status'),2);assert.equal(response.get('id'),9);
  });
}
test('SIGTERM drains and exits without a forced kill',async()=>{
  const exit=once(child,'exit');child.kill('SIGTERM');const [code,signal]=await exit;stopped=true;
  assert.equal(code,0);assert.equal(signal,null);
});
