import * as wire from './codec.gen.js';
const element = id => document.getElementById(id);
const csrf = new URLSearchParams(location.hash.slice(1)).get('csrf');
const requested = new URLSearchParams(location.search).get('candidate');
if (requested && /^[A-Za-z0-9._:-]{1,256}$/.test(requested)) sessionStorage.setItem('opal-release-candidate', requested);
const candidateId = requested ?? sessionStorage.getItem('opal-release-candidate');
history.replaceState(null, '', candidateId ? `/?candidate=${encodeURIComponent(candidateId)}` : '/');
let review, commandKey, approvalId;
const hex = bytes => [...bytes].map(n => n.toString(16).padStart(2, '0')).join('');
function status(text) {element('status').textContent = text;}
async function rpc(op, type, value, responseType) {
  const raw = wire.encodeValue(new Map([['v', 1], ['id', 1], ['service', 'Releases'], ['op', op], ['payload', {tag: 24, value: wire[`to${type}Cbor`](value)}]]));
  const res = await fetch('/csil/v1/rpc', {method: 'POST', credentials: 'same-origin', headers: {'content-type': 'application/cbor', ...(csrf ? {'x-opal-csrf': csrf} : {})}, body: raw, redirect: 'error', signal: AbortSignal.timeout(10000)});
  if (res.status !== 200 || res.headers.get('content-type') !== 'application/cbor') throw Error('Request refused. Sign in again if the session has expired.');
  const bytes = new Uint8Array(await res.arrayBuffer()); if (bytes.length > 262144) throw Error('Response too large');
  const reply = wire.decode(bytes);
  if (reply.get('v') !== 1 || reply.get('id') !== 1 || reply.get('status') !== 0 || reply.get('variant') !== responseType || reply.get('payload')?.tag !== 24) throw Error('Invalid response');
  return wire[`from${responseType}Cbor`](reply.get('payload').value);
}
const act = action => async () => {try {await action();} catch (e) {status(e.message);}};
const config = await (await fetch('/config.json', {cache: 'no-store'})).json();
element('target').textContent = `Target: ${config.target} (${config.domain})`;
element('identity').textContent = `Required approver: ${config.approver}`;
element('review').disabled = !csrf || !candidateId;
if (!candidateId) status('Open the review link from Magi. No file upload is needed.');
element('login').onclick = act(async () => {
  const started = await rpc('begin-login', 'ReleaseLoginStart', {identity: config.loginHint}, 'ReleaseLoginStarted');
  const next = new URL(started.redirectUrl); if (next.protocol !== 'https:') throw Error('Invalid login URL'); location.assign(next.href);
});
const loadReview = async () => {
  review = undefined; element('approve').disabled = true;
  if (!candidateId) throw Error('Open a proposal review link.');
  const response = await rpc('review-candidate', 'ReleaseCandidateRef', {candidateId}, 'ReleaseReviewed');
  const calculated = new Uint8Array(await crypto.subtle.digest('SHA-256', response.candidate));
  if (hex(calculated) !== hex(response.digest)) throw Error('Candidate bytes changed');
  const value = wire.fromReleaseCandidateCbor(response.candidate);
  if (value.candidateId !== candidateId || !value.explanation) throw Error('Proposal does not match the request');
  element('title').textContent = value.explanation.title;
  element('reason').textContent = value.explanation.reason;
  for (const name of ['changes', 'risks']) element(name).replaceChildren(...value.explanation[name].map(text => {const item = document.createElement('li'); item.textContent = text; return item;}));
  element('scope').textContent = `Target: ${value.target.targetId} in ${value.target.domain}. No data migration is permitted. If checks fail, the controller attempts to restore the previous image.`;
  element('checks').textContent = `Required checks: readiness and the exact CSIL Discovery response; ${value.checks.successCount} consecutive passes. Evidence references are in the technical details.`;
  element('expiry').textContent = `Approval must be used before ${value.expiresAt.toISOString()}.`;
  element('proposal').hidden = false;
  element('details').textContent = JSON.stringify(value, (_, v) => v instanceof Uint8Array ? {hex: hex(v)} : v, 2);
  element('digest').textContent = `Exact candidate SHA-256: ${hex(calculated)}`;
  review = response; commandKey = crypto.randomUUID(); element('approve').disabled = false;
  status('Review the target, image, checks, evidence, and expiry. Approval is a separate action.');
};
element('review').onclick = act(loadReview);
element('approve').onclick = act(async () => {
  if (!review) throw Error('Review a candidate first');
  // Keep this key if the reply is lost. Retrying must not issue a new approval.
  const packet = await rpc('approve', 'ReleaseApprove', {reviewId: review.reviewId, digest: review.digest, commandKey}, 'SignedRecord');
  const approval = wire.fromReleaseApprovalCbor(wire.fromSignatureInputCbor(packet.input).body);
  if (hex(approval.candidateDigest) !== hex(review.digest)) throw Error('Approval differs from review');
  approvalId = approval.approvalId;
  element('approve').disabled = true; element('withdraw').disabled = false;
  status('Approval recorded and queued for the controller. No file transfer is needed. This does not yet confirm deployment.');
});
if (csrf && candidateId) await act(loadReview)();
element('withdraw').onclick = act(async () => {
  await rpc('withdraw', 'ReleaseWithdraw', {approvalId}, 'ReleaseWithdrawn');
  element('withdraw').disabled = true; status('Approval withdrawn. An already admitted release is not cancelled.');
});
