import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const source = readFileSync(new URL('./listen/app.js', import.meta.url), 'utf8');
const key = 'opal-tts-review-v1';
function fixture(initial = {}) {
  const storage = new Map([[key, JSON.stringify(initial)]]);
  const handlers = new Map();
  const fields = [];
  let exported;
  const context = vm.createContext({
    localStorage: {getItem: (k) => storage.get(k), setItem: (k, value) => storage.set(k, value)},
    document: {
      getElementById: (id) => ({addEventListener: (event, fn) => handlers.set(`${id}:${event}`, fn)}),
      createElement: () => ({click() {}}), querySelectorAll: () => fields, activeElement: null,
    },
    window: {addEventListener: (event, fn) => handlers.set(`window:${event}`, fn)},
    fetch: () => new Promise(() => {}),
    Blob, URL: {createObjectURL: (blob) => {exported = blob; return 'blob:test';}, revokeObjectURL() {}},
    setTimeout: (fn) => fn(),
  });
  vm.runInContext(source, context);
  return {storage, handlers, context, fields, exported: () => exported};
}

test('saving a review keeps changes from another tab', () => {
  const f = fixture({one: {rating: 'keep'}});
  f.storage.set(key, JSON.stringify({one: {rating: 'reject', notes: 'New note'}, two: {rating: 'keep'}}));
  vm.runInContext("save('one', 'notes', 'Updated note')", f.context);
  assert.deepEqual(JSON.parse(f.storage.get(key)), {
    one: {rating: 'reject', notes: 'Updated note'}, two: {rating: 'keep'},
  });
});

test('export reads the latest stored review', async () => {
  const f = fixture();
  f.storage.set(key, JSON.stringify({one: {rating: 'keep'}}));
  f.handlers.get('export:click')();
  assert.deepEqual(JSON.parse(await f.exported().text()).reviews, {one: {rating: 'keep'}});
});

test('storage events update unfocused fields without replacing an active edit', () => {
  const f = fixture();
  const rating = {dataset: {reviewKey: 'one', reviewField: 'rating'}, value: ''};
  const notes = {dataset: {reviewKey: 'one', reviewField: 'notes'}, value: 'Editing'};
  f.fields.push(rating, notes);
  f.context.document.activeElement = notes;
  f.storage.set(key, JSON.stringify({one: {rating: 'keep', notes: 'From another tab'}}));
  f.handlers.get('window:storage')({key});
  assert.equal(rating.value, 'keep');
  assert.equal(notes.value, 'Editing');
});
