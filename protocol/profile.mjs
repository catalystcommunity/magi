import { isDeepStrictEqual } from 'node:util';
import { sign, verify } from 'node:crypto';
import * as wire from '../.build/codec.gen.js';

export const limits = Object.freeze({frame: 262144, blob: 131072, text: 16384, collection: 256, depth: 24, nodes: 8192, timeout: 10000});
const fail = () => {throw Error('invalid or unsupported wire value');};
export function checkFrame(data) {
  if (!(data instanceof Uint8Array) || !data.length || data.length > limits.frame) fail();
  let pos = 0, nodes = 0;
  const take = n => {if (n > data.length-pos) fail(); const b=data.subarray(pos,pos+n);pos+=n;return b;};
  const utf8 = new TextDecoder('utf-8', {fatal: true});
  function item(depth,decimal=false) {
    if (++nodes > limits.nodes || depth > limits.depth) fail();
    const head=take(1)[0], major=head>>5, small=head&31;
    let n=BigInt(small);
    if (small>=24) {
      const size=({24:1,25:2,26:4,27:8})[small];if (!size) fail();
      n=0n;for (const b of take(size)) n=(n<<8n)|BigInt(b);
      if (major!==7 && (n<24n || size>1&&n<=255n || size>2&&n<=65535n || size>4&&n<=4294967295n)) fail();
    }
    switch(major) {
      case 0:if(!decimal&&n>9007199254740991n)fail();return n;
      case 1:if(n>9223372036854775807n||!decimal&&n>=9007199254740991n)fail();return -1n-n;
      case 2:case 3:{if(n>BigInt(major===2?limits.blob:limits.text))fail();const b=take(Number(n));return major===2?Uint8Array.from(b):utf8.decode(b);}
      case 4:{if(n>BigInt(limits.collection))fail();return Array.from({length:Number(n)},()=>item(depth+1,decimal));}
      case 5:{if(n>BigInt(limits.collection))fail();const m=new Map();for(let i=0;i<Number(n);i++){const k=item(depth+1);if(typeof k!=='string'||m.has(k))fail();m.set(k,item(depth+1));}return m;}
      case 6:{
        if (![0n,2n,3n,4n,24n].includes(n))fail();const v=item(depth+1,n===4n);
        if(n===0n){
          if(typeof v!=='string'||!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,3})?Z$/.test(v))fail();
          const d=new Date(v);if(!Number.isFinite(+d))fail();
          const norm=s=>s.replace(/\.([0-9]*?)0+Z$/,(_,f)=>f?`.${f}Z`:'Z');
          if(norm(v)!==norm(d.toISOString()))fail();return {tag:0,value:+d};
        }
        if(n===2n||n===3n){if(!(v instanceof Uint8Array)||!v.length||v.length>64||v[0]===0)fail();}
        if(n===4n){if(!Array.isArray(v)||v.length!==2||typeof v[0]!=='bigint'||v[0]<-128n||v[0]>128n||!(typeof v[1]==='bigint'||[2,3].includes(v[1]?.tag)))fail();}
        if(n===24n&&!(v instanceof Uint8Array))fail();return {tag:Number(n),value:v};
      }
      case 7:if(small===20)return false;if(small===21)return true;if(small===22)return null;
    }
    fail();
  }
  const result=item(0);if(pos!==data.length)fail();return result;
}

export function decode(type, bytes, codec = wire) {
  const before=checkFrame(bytes);
  const value=codec[`from${type}Cbor`](bytes);
  const after=checkFrame(codec[`to${type}Cbor`](value));
  if(!isDeepStrictEqual(before,after)) fail();
  return value;
}

export function signed(scope, body, keyId, signedAt, privateKey) {
  const input=wire.toSignatureInputCbor({...scope,body,keyId,signedAt});
  decode('SignatureInput',input);
  if(scope.schema.digest.length!==32||!keyId)fail();
  return {input,signature:sign(null,input,privateKey)};
}

// Key trust, freshness, body semantics, and authorization belong to the caller.
export function verified(packet, publicKey, expected) {
  const input=Uint8Array.from(packet.input);
  const value=decode('SignatureInput',input);
  const scope={context:value.context,schema:value.schema,signer:value.signer,recipient:value.recipient};
  if(value.schema.digest.length!==32||!value.keyId||!isDeepStrictEqual(scope,expected)||packet.signature.length!==64||!verify(null,input,publicKey,packet.signature))throw Error('signature verification failed');
  return {value,input};
}
