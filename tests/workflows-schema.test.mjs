import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import * as all from '../.build/codec.gen.js';
import * as workflows from '../.build/workflows/codec.gen.js';
import {decode} from '../protocol/profile.mjs';
import {fixture} from './fixtures.mjs';

test('standalone workflows require no Longhouse, session, persona, or event service',()=>{
 const descriptor=workflows.decode(readFileSync(new URL('../generated/workflows/go/workflows.csil-schema.cbor',import.meta.url)));
 assert.deepEqual(descriptor.get('body').get('services').map(s=>s.get('name')).sort(),['Discovery','Workflows']);
 const names=descriptor.get('body').get('rules').map(r=>r.get('name'));
 for(const n of ['Session','Command','ActorBinding','TaskLink','Event','Persona','HumanBinding'])assert(!names.includes(n));
 const digest=createHash('sha256').update(workflows.encodeValue(descriptor.get('body'))).digest();
 assert.deepEqual(Buffer.from(descriptor.get('digest')),digest);
 assert.match(readFileSync(new URL('../generated/workflows/schema.go',import.meta.url),'utf8'),new RegExp(digest.toString('hex')));
 for(const name of names){if(typeof workflows[`to${name}Cbor`]!=='function')continue;const raw=fixture(name,true);assert.deepEqual(workflows[`to${name}Cbor`](decode(name,raw,workflows)),all[`to${name}Cbor`](decode(name,raw)));}
});
