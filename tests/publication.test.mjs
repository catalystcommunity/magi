import test from 'node:test';
import assert from 'node:assert/strict';
import {publicationFindings} from '../scripts/check-publication.mjs';

test('publication guard rejects raw evidence and private configuration', () => {
  for (const path of ['test-results/a.json','experiments/speech/results/review.json','.local/profile.json','config/real.json','profile.private.json','.env.production','a.sqlite','a.pem','a.wav']) assert.ok(publicationFindings(path,Buffer.from('{}')).length,path);
  for (const path of ['config/release-policy.example.json','package.json','tests/fixtures/release-policy.json','.env.example']) assert.deepEqual(publicationFindings(path,Buffer.from('{}')),[],path);
});
test('publication guard reports secret types without secret values', () => {
  const key = ['-----BEGIN ', 'PRIVATE KEY-----'].join('');
  assert.deepEqual(publicationFindings('a.txt',Buffer.from(key)),['private-key']);
  assert.deepEqual(publicationFindings('a.json',Buffer.from(JSON.stringify({key:{kty:'OKP',d:'synthetic'}}))),['private-jwk']);
  assert.deepEqual(publicationFindings('a.txt',Buffer.from('gh'+'p_'+'a'.repeat(36))),['github-token']);
  assert.deepEqual(publicationFindings('a.json',Buffer.from('{')),['invalid-json']);
});

test('publication guard can check environment values from a private overlay',()=>{
  assert.deepEqual(publicationFindings('a.txt',Buffer.from('cluster.private.example'),['cluster.private.example']),['private-environment-value']);
  assert.deepEqual(publicationFindings('a.txt',Buffer.from('CLUSTER.Private.Example'),['cluster.private.example']),['private-environment-value']);
  assert.deepEqual(publicationFindings('a.txt',Buffer.from('cluster\\.private\\.example'),['cluster.private.example']),['private-environment-value']);
  assert.deepEqual(publicationFindings('a.txt',Buffer.from('catalystsquad.local'),['cluster.private.example']),[]);
});
