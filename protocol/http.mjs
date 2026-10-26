import * as wire from '../.build/codec.gen.js';
import { checkFrame, decode, limits } from './profile.mjs';

// CSIL-RPC v1 envelope-in-body carrier. It matches the pinned Go transport.
export class HTTPTransport {
  constructor(endpoint, {codec=wire,responses={'Discovery/describe':'ServiceDescription','Discovery/read-schema':'SchemaDocument'}} = {}) {
    this.codec=codec; this.responses=Object.freeze({...responses});
    this.endpoint=new URL(endpoint);
    if(this.endpoint.username||this.endpoint.password||this.endpoint.search||this.endpoint.hash||
      !(this.endpoint.protocol==='https:'||this.endpoint.protocol==='http:'&&['127.0.0.1','localhost','[::1]'].includes(this.endpoint.hostname)))throw Error('invalid RPC endpoint');
  }
  async call(service,op,payload) {
    const wire=this.codec;
    checkFrame(payload);
    const request=wire.encodeValue(new Map([['v',1],['id',1],['service',service],['op',op],['payload',{tag:24,value:payload}]]));
    checkFrame(request);
    const response=await fetch(this.endpoint,{method:'POST',headers:{'content-type':'application/cbor'},body:request,redirect:'error',signal:AbortSignal.timeout(limits.timeout)});
    if(response.status!==200||response.headers.get('content-type')?.split(';')[0]!=='application/cbor'){await response.body?.cancel();throw Error('RPC carrier refused response');}
    const reader=response.body.getReader();const chunks=[];let size=0;
    try{for(;;){const {done,value}=await reader.read();if(done)break;size+=value.length;if(size>limits.frame)throw Error('RPC frame limit');chunks.push(value);}}finally{await reader.cancel();}
    const bytes=new Uint8Array(size);let offset=0;for(const b of chunks){bytes.set(b,offset);offset+=b.length;}
    checkFrame(bytes);const envelope=wire.decode(bytes);
    if(!(envelope instanceof Map)||[...envelope.keys()].some(k=>!['v','id','status','variant','error','payload'].includes(k))||envelope.get('v')!==1||envelope.get('id')!==1||!Number.isSafeInteger(envelope.get('status')))throw Error('invalid RPC response');
    const body=envelope.get('payload');if(body?.tag!==24||!(body.value instanceof Uint8Array))throw Error('invalid RPC payload');
    if(envelope.get('status')!==0)throw Error(`RPC transport status ${envelope.get('status')}`);
    if(envelope.get('variant')==='ServiceError'){
      const detail=decode('ServiceError',body.value,wire);if(detail.message.length>256)throw Error('invalid RPC error');
      const error=Error(detail.message);error.code=detail.code;error.retryable=detail.retryable;throw error;
    }
    const expected=this.responses[`${service}/${op}`];
    if(!expected||envelope.get('variant')!==expected)throw Error('invalid RPC variant');
    decode(expected,body.value,wire);return body.value;
  }
}
