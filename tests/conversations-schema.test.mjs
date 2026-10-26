import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import * as all from '../.build/codec.gen.js';
import * as graph from '../.build/conversations/codec.gen.js';
import {decode} from '../protocol/profile.mjs';
import {fixture} from './fixtures.mjs';
test('conversation records preserve overlapping sessions without a mode enum',()=>{
 const descriptor=graph.decode(readFileSync(new URL('../generated/conversations/go/conversations.csil-schema.cbor',import.meta.url)));
 assert.deepEqual(descriptor.get('body').get('services').map(s=>s.get('name')).sort(),['Conversations','Discovery']);
 const names=descriptor.get('body').get('rules').map(r=>r.get('name'));assert(!names.includes('ConversationMode'));
 assert.deepEqual(Buffer.from(descriptor.get('digest')),createHash('sha256').update(graph.encodeValue(descriptor.get('body'))).digest());
 assert.match(readFileSync(new URL('../generated/conversations/schema.go',import.meta.url),'utf8'),new RegExp(Buffer.from(descriptor.get('digest')).toString('hex')));
 for(const name of names){if(typeof graph[`to${name}Cbor`]!=='function')continue;const raw=fixture(name,true);assert.deepEqual(graph[`to${name}Cbor`](decode(name,raw,graph)),all[`to${name}Cbor`](decode(name,raw)));}
});
