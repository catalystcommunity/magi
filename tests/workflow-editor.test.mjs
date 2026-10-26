import test from 'node:test';
import assert from 'node:assert/strict';
import {example,ports,bindings,addStep,removeStep} from '../editor/model.mjs';
import * as w from '../.build/workflows/codec.gen.js';
import {decode} from '../protocol/profile.mjs';
test('editor draft uses generated workflow records and keeps layout separate',()=>{
 const d=example(),raw=w.toWorkflowDraftCbor(d),saved=decode('WorkflowDraft',raw,w);
 assert.deepEqual(w.toWorkflowDraftCbor(saved),raw);const semantic=w.toWorkflowDefinitionCbor(d.definition);
 d.positions[0].x+=10;assert.deepEqual(w.toWorkflowDefinitionCbor(d.definition),semantic);
});
test('editor ports and connections parse without expressions',()=>{
 assert.equal(ports('amount: decimal, when: timestamp').length,2);
 assert.deepEqual(bindings('subject = $input.subject'),[{input:'subject',sourceStep:'$input',output:'subject'}]);
 for(const text of ['value: float','bad name: text','text'])assert.throws(()=>ports(text));
 for(const text of ['x = fetch(secret)','x = y.z.extra','x = y'])assert.throws(()=>bindings(text));
});
test('editor removal retains broken connections for explicit repair',()=>{
 const d=example(),next=removeStep(d,'draft');assert.equal(d.definition.steps.length,3);assert.equal(next.definition.steps.length,2);assert.deepEqual(next.definition.steps[0].dependsOn,['draft']);
 const added=addStep(d,'human');assert.equal(added.draft.definition.steps.length,4);assert(!d.definition.steps.some(s=>s.id===added.id));assert.throws(()=>addStep(d,'shell'));
});
