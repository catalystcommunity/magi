import { test } from 'node:test';
import assert from 'node:assert/strict';
import { collectLines } from './test-events.mjs';

test('split and interleaved Go output reconstructs whole lines', () => {
  const lines=[];const accept=collectLines(line=>lines.push(line));
  accept({Test:'a',Output:'SUPPORT_CASE {"long":"'});
  accept({Test:'b',Output:'other test\n'});
  accept({Test:'a',Output:'quote"}\nnext\n'});
  assert.deepEqual(lines,['other test','SUPPORT_CASE {"long":"quote"}','next']);
});
test('oversized lines are rejected', () => {
  assert.throws(()=>collectLines(()=>{})({Output:'a'.repeat(1_048_577)}));
});
