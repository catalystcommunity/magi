import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync,writeFileSync,readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join,resolve } from 'node:path';
import { createHash,createPrivateKey,createPublicKey } from 'node:crypto';
import * as wire from '../.build/codec.gen.js';
import { decode,checkFrame,signed,verified,limits } from '../protocol/profile.mjs';
import { rules,descriptor,fixture,scope } from './fixtures.mjs';

const root=resolve(import.meta.dirname,'..');
const scratch=mkdtempSync(join(tmpdir(),'opal-wire-test-'));
const seed=Buffer.from(Array.from({length:32},(_,i)=>i+1));
const privateKey=createPrivateKey({key:Buffer.concat([Buffer.from('302e020100300506032b657004220420','hex'),seed]),format:'der',type:'pkcs8'});
const publicKey=createPublicKey(privateKey);
const codecRules=[...rules].filter(([,rule])=>rule.has('Group'));
const cases=codecRules.map(([name])=>`case "${name}": v,e:=p.Decode(b,w.Decode${name},w.Encode${name});if e!=nil{return nil,e};return w.Encode${name}(v),nil`).join('\n');
const helper=join(scratch,'main.go');
writeFileSync(helper,`package main
import("encoding/hex";"encoding/json";"fmt";"io";"os";"crypto/ed25519";p "github.com/catalystcommunity/magi/protocol";w "github.com/catalystcommunity/magi/generated/go")
func roundtrip(kind string,b []byte)([]byte,error){switch kind {${cases}};return nil,fmt.Errorf("type")}
func main(){var req struct{Type string;Hex string;Mode string;Scope p.SignatureScope};if json.NewDecoder(io.LimitReader(os.Stdin,1048576)).Decode(&req)!=nil{os.Exit(2)};b,e:=hex.DecodeString(req.Hex);if e!=nil{os.Exit(2)};var result []byte
seed:=make([]byte,32);for i:=range seed{seed[i]=byte(i+1)};key:=ed25519.NewKeyFromSeed(seed)
switch req.Mode{case "verify":v,e1:=p.Decode(b,w.DecodeSignedRecord,w.EncodeSignedRecord);if e1!=nil{e=e1;break};_,result,e=p.Verify(v,key.Public().(ed25519.PublicKey),req.Scope)
case "sign":v,e1:=p.Decode(b,w.DecodeSignatureInput,w.EncodeSignatureInput);if e1!=nil{e=e1;break};s:=p.SignatureScope{Context:v.Context,Schema:v.Schema,Signer:v.Signer,Recipient:v.Recipient};q,e1:=p.Sign(s,v.Body,v.KeyId,v.SignedAt,key);e=e1;result=w.EncodeSignedRecord(q)
default:result,e=roundtrip(req.Type,b)};if e!=nil{fmt.Fprintln(os.Stderr,"wire rejected");os.Exit(1)};fmt.Print(hex.EncodeToString(result))}
`);
execFileSync('go',['build','-o',join(scratch,'interop'),helper],{cwd:root,env:{...process.env,GOCACHE:process.env.GOCACHE||'/tmp/opal-knowledge-go-build-20261011'}});
const gos= {Context:scope.context,Schema:{name:scope.schema.name,version:scope.schema.version,digest:Buffer.from(scope.schema.digest).toString('base64')},Signer:scope.signer,Recipient:scope.recipient};
// Go's JSON field tags use snake_case. Preserve the same canonical identities.
const app=a=>({identity:{domain:a.identity.domain,account_id:a.identity.accountId},application_id:a.applicationId,instance_id:a.instanceId});
gos.Signer=app(scope.signer);gos.Recipient=app(scope.recipient);
function go(type,bytes,mode='roundtrip',expected=gos){return Buffer.from(execFileSync(join(scratch,'interop'),[],{input:JSON.stringify({type,hex:Buffer.from(bytes).toString('hex'),mode,scope:expected}),encoding:'utf8',stdio:['pipe','pipe','pipe']}),'hex');}

for(const [name,rule]of codecRules){
  for(const optional of [false,true])test(`Go and TypeScript ${name} optional=${optional}`,()=>{
    const raw=fixture(name,optional);const v=decode(name,raw);const encoded=wire[`to${name}Cbor`](v);
    assert.deepEqual(go(name,encoded),Buffer.from(encoded));
  });
  if(rule.has('Choice'))for(let n=0;n<rule.get('Choice').length;n++)test(`${name} wire arm ${n}`,()=>{
    const raw=fixture(name,false,n);assert.deepEqual(go(name,raw),Buffer.from(wire[`to${name}Cbor`](decode(name,raw))));
  });
  if(rule.has('Group'))test(`${name} rejects unknown fields in both clients`,()=>{
    const tree=wire.decode(fixture(name));tree.set('unrecognized_required_semantics',true);const raw=wire.encodeValue(tree);
    assert.throws(()=>decode(name,raw));assert.throws(()=>go(name,raw));
  });
}
for(const [name,rule]of rules){
  if(!rule.has('Choice'))continue;
  let host,field;
  if(name==='Segment'){host='CapturedContent';field='segments';}
  else if(name==='CommandForm'){host='CommandOperation';field='form';}
  else {
    for(const [candidate,record]of codecRules){
      const entry=record.get('Group').get('entries').find(e=>e.get('value').get('Reference')===name);
      if(entry){host=candidate;field=entry.get('key').get('Bare');break;}
    }
  }
  if(!host)throw Error(`No fixture host for ${name}`);
  for(let n=0;n<rule.get('Choice').length;n++)test(`${name} wire arm ${n}`,()=>{
    const tree=wire.decode(fixture(host));const arm=wire.decode(fixture(name,false,n));tree.set(field,name==='Segment'?[arm]:arm);const raw=wire.encodeValue(tree);
    assert.deepEqual(go(host,raw),Buffer.from(wire[`to${host}Cbor`](decode(host,raw))));
  });
}
test('descriptor bytes and semantic digest agree across targets',()=>{
  const a=readFileSync(join(root,'generated/go/magi.csil-schema.cbor')),b=readFileSync(join(root,'generated/typescript/magi.csil-schema.cbor'));
  assert.deepEqual(a,b);assert.equal(descriptor.get('format'),'csil-schema');
  const hash=createHash('sha256').update(wire.encodeValue(descriptor.get('body'))).digest();
  assert.deepEqual(hash,Buffer.from(descriptor.get('digest')));
  assert.match(readFileSync(join(root,'schema.go'),'utf8'),new RegExp(hash.toString('hex')));
  const seen=new Set();for(const s of descriptor.get('body').get('services')){assert.ok(!seen.has(s.get('wire_id')));seen.add(s.get('wire_id'));const ops=new Set();for(const op of s.get('operations')){assert.ok(!ops.has(op.get('wire_id')));ops.add(op.get('wire_id'));}}
});
test('exact decimal stays exact above binary floating point precision',()=>{
  const raw=fixture('Quantity');const q=decode('Quantity',raw);assert.equal(q.value.toString(),'9007199254740993.12');assert.deepEqual(go('Quantity',raw),Buffer.from(wire.toQuantityCbor(q)));
});
test('Go verifies TypeScript signatures and TypeScript verifies Go signatures',()=>{
  const packet=signed(scope,fixture('Command'),'key-test',new Date('2026-10-13T01:02:03.120Z'),privateKey);
  assert.deepEqual(go('SignedRecord',wire.toSignedRecordCbor(packet),'verify'),Buffer.from(packet.input));
  const fromGo=decode('SignedRecord',go('SignatureInput',packet.input,'sign'));
  assert.notDeepEqual(fromGo.input,packet.input); // Go .12Z and TS .120Z are equal instants.
  const verifiedGo=verified(fromGo,publicKey,scope);assert.deepEqual(verifiedGo.input,Uint8Array.from(fromGo.input));
  for(const field of ['context','recipient','signer','schema','body','keyId','signedAt']){
    const v=decode('SignatureInput',packet.input);
    if(field==='context'||field==='keyId')v[field]+='x';else if(field==='body')v.body=Uint8Array.of(0xa0);else if(field==='schema')v.schema.version+='x';else if(field==='signedAt')v.signedAt=new Date(+v.signedAt+1);else v[field].instanceId+='x';
    const changed={...packet,input:wire.toSignatureInputCbor(v)};assert.throws(()=>verified(changed,publicKey,scope));assert.throws(()=>go('SignedRecord',wire.toSignedRecordCbor(changed),'verify'));
  }
  assert.throws(()=>verified(packet,publicKey,{...scope,context:'other'}));
  assert.throws(()=>go('SignedRecord',wire.toSignedRecordCbor(packet),'verify',{...gos,Context:'other'}));
});
test('bounded malformed input corpus is refused',()=>{
  for(const hex of ['', 'a000','a2616101616102','9bffffffffffffffff','5bffffffffffffffff','bf616101ff','1817','61ff','1b0020000000000000','3b001fffffffffffff'])assert.throws(()=>checkFrame(Buffer.from(hex,'hex')));
  assert.throws(()=>checkFrame(new Uint8Array(limits.frame+1)));
  assert.throws(()=>checkFrame(wire.encodeValue(Array(257).fill(0))));
  let tree=0;for(let i=0;i<=limits.depth;i++)tree=[tree];assert.throws(()=>checkFrame(wire.encodeValue(tree)));
  for(const date of ['2026-02-31T00:00:00Z','2026-01-02T03:04:05.123456789Z'])assert.throws(()=>checkFrame(wire.encodeValue({tag:0,value:date})));
});
test('stored 0alpha1 vectors and wire ordinals remain compatible',()=>{
  const baseline=JSON.parse(readFileSync(join(root,'tests/baseline.json')));
  for(const v of baseline.vectors){const raw=Buffer.from(v.hex,'hex');const typed=decode(v.type,raw);assert.deepEqual(go(v.type,raw),Buffer.from(wire[`to${v.type}Cbor`](typed)));}
  for(const [name, previous] of Object.entries(baseline.services)){
    const s=descriptor.get('body').get('services').find(s=>s.get('name')===name);
    assert.ok(s, `missing service ${name}`); assert.equal(s.get('wire_id'),previous.id);
    for(const [name,id] of Object.entries(previous.operations))assert.equal(s.get('operations').find(o=>o.get('name')===name)?.get('wire_id'),id);
  }
  for(const [name,arms] of Object.entries(baseline.unions))assert.deepEqual(rules.get(name).get('Choice').map(a=>a.get('Reference')||a.get('Literal')?.get('Text')),arms);
});
