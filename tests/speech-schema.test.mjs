import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import * as all from '../.build/codec.gen.js';
import * as speech from '../.build/speech/codec.gen.js';
import {decode} from '../protocol/profile.mjs';
import {fixture} from './fixtures.mjs';

test('standalone speech contract does not require a persona, task, or workflow',()=>{
 const descriptor=speech.decode(readFileSync(new URL('../generated/speech/go/speech.csil-schema.cbor',import.meta.url)));
 assert.deepEqual(descriptor.get('body').get('services').map(s=>s.get('name')).sort(),['Discovery','Speech']);
 const names=descriptor.get('body').get('rules').map(r=>r.get('name'));
 for(const n of ['Session','Command','ActorBinding','WorkflowRun','Persona'])assert(!names.includes(n));
 assert.deepEqual(Buffer.from(descriptor.get('digest')),createHash('sha256').update(speech.encodeValue(descriptor.get('body'))).digest());
 assert.match(readFileSync(new URL('../generated/speech/schema.go',import.meta.url),'utf8'),new RegExp(Buffer.from(descriptor.get('digest')).toString('hex')));
 for(const name of names){if(typeof speech[`to${name}Cbor`]!=='function')continue;const raw=fixture(name,true);assert.deepEqual(speech[`to${name}Cbor`](decode(name,raw,speech)),all[`to${name}Cbor`](decode(name,raw)));}
});
