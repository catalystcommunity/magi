import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import * as all from '../.build/codec.gen.js';
import * as events from '../.build/events/codec.gen.js';
import {decode} from '../protocol/profile.mjs';
import {fixture} from './fixtures.mjs';

test('standalone event contract has no task or persona dependency',()=>{
 const descriptor=events.decode(readFileSync(new URL('../generated/events/go/events.csil-schema.cbor',import.meta.url)));
 assert.deepEqual(descriptor.get('body').get('services').map(s=>s.get('name')).sort(),['Discovery','Events']);
 const names=descriptor.get('body').get('rules').map(r=>r.get('name'));
 for(const n of ['Session','Command','ActorBinding','TaskLink','EnvironmentRequest','Persona'])assert(!names.includes(n));
 assert.deepEqual(Buffer.from(descriptor.get('digest')),createHash('sha256').update(events.encodeValue(descriptor.get('body'))).digest());
 assert.match(readFileSync(new URL('../generated/events/schema.go',import.meta.url),'utf8'),new RegExp(Buffer.from(descriptor.get('digest')).toString('hex')));
 for(const name of names){if(typeof events[`to${name}Cbor`]!=='function')continue;const raw=fixture(name,true);assert.deepEqual(events[`to${name}Cbor`](decode(name,raw,events)),all[`to${name}Cbor`](decode(name,raw)));}
});
