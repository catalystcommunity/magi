import { readFileSync } from 'node:fs';
import * as wire from '../.build/codec.gen.js';
export const descriptor=wire.decode(readFileSync(new URL('../generated/go/magi.csil-schema.cbor',import.meta.url)));
export const rules=new Map(descriptor.get('body').get('rules').map(r=>[r.get('name'),r.get('definition').get('Type')]));

// Mechanical structural fixtures. Semantic cases are separate tests.
export function fixture(type, optional=false, arm=0) {
  function value(expr,choice=0) {
    const [[kind,data]]=[...expr];
    switch(kind){
      case 'Reference':return value(rules.get(data));
      case 'Builtin':return ({text:'fixture',uint:3,int:-3,bool:true,bytes:Uint8Array.of(1,2,3),timestamp:{tag:0,value:'2026-10-13T01:02:03.123Z'},decimal:{tag:4,value:[-2,900719925474099312n]}})[data];
      case 'Group':return new Map(data.get('entries').filter(e=>optional||e.get('occurrence')!=='Optional').map(e=>[e.get('key').get('Bare'),value(e.get('value'))]));
      case 'Array':return optional?[value(data.get('element'))]:[];
      case 'Literal':return [...data][0][1];
      case 'Choice':return data.every(a=>a.has('Literal'))?value(data[choice]):[choice,value(data[choice])];
      default:throw Error(`fixture does not support ${kind}`);
    }
  }
  return wire.encodeValue(value(rules.get(type),arm));
}

export const identity={domain:'catalystsquad.local',accountId:'01a11ea7-24d3-7612-8586-8e4645452613'};
export const application={identity,applicationId:'opal',instanceId:'test'};
export const scope={context:'opal.command.0alpha1',schema:{name:'Command',version:'0alpha1',digest:Uint8Array.from(descriptor.get('digest'))},signer:application,recipient:{...application,identity:{...identity,domain:'partner.local'}}};
