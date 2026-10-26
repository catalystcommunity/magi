import {createServer} from 'node:http';
import {readFileSync} from 'node:fs';
import {randomBytes} from 'node:crypto';
import {checkFrame, limits} from '../protocol/profile.mjs';
import {wire, decode, schema, descriptor, need} from './policy.mjs';

const cookie = (name, value) => `${name}=${value}; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=300`;
function getCookie(request, name) {
  const found = (request.headers.cookie ?? '').split(';').map(s => s.trim()).filter(s => s.startsWith(`${name}=`));
  return found.length === 1 && /^[A-Za-z0-9_-]{43}$/.test(found[0].slice(name.length + 1)) ? found[0].slice(name.length + 1) : '';
}
const files = new Map([
  ['/', ['text/html; charset=utf-8', new URL('./index.html', import.meta.url)]],
  ['/app.mjs', ['text/javascript; charset=utf-8', new URL('./browser.mjs', import.meta.url)]],
  ['/codec.gen.js', ['text/javascript; charset=utf-8', new URL('../.build/releases/codec.gen.js', import.meta.url)]],
  ['/types.gen.js', ['text/javascript; charset=utf-8', new URL('../.build/releases/types.gen.js', import.meta.url)]],
]);
export function releaseHTTP({recorder, resolveKey}) {
  let loginWindow = 0, loginCount = 0;
  const server = createServer(async (req, res) => {
    res.setHeader('cache-control', 'no-store'); res.setHeader('referrer-policy', 'no-referrer');
    res.setHeader('x-content-type-options', 'nosniff'); res.setHeader('x-frame-options', 'DENY');
    res.setHeader('content-security-policy', "default-src 'none'; script-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'");
    const send = (type, value, id = 1) => {
      res.setHeader('content-type', 'application/cbor');
      res.end(wire.encodeValue(new Map([['v', 1], ['id', id], ['status', 0], ['variant', type], ['payload', {tag: 24, value: wire[`to${type}Cbor`](value)}]])));
    };
    try {
      need(req.url.length <= 32768, 'url-limit');
      const url = new URL(req.url, 'http://localhost');
      if (req.method === 'GET' && files.has(url.pathname) && (url.pathname === '/' || !url.search)) {
        const [content, path] = files.get(url.pathname); res.setHeader('content-type', content); return res.end(readFileSync(path));
      }
      if (req.method === 'GET' && url.pathname === '/config.json') {
        res.setHeader('content-type', 'application/json');
        return res.end(JSON.stringify({loginHint: recorder.config.login_hint, approver: `${recorder.config.approver.account_id}@${recorder.config.approver.domain}`, target: `${recorder.config.deployment}.${recorder.config.namespace}`, domain: recorder.config.target_domain}));
      }
      if (req.method === 'GET' && url.pathname === '/health/ready') {
        recorder.db.prepare('SELECT 1').get(); res.setHeader('content-type', 'text/plain'); return res.end('ready\n');
      }
      if (req.method === 'GET' && url.pathname === '/auth/callback') {
        need(url.searchParams.getAll('lk_state').length === 1 && url.searchParams.getAll('encrypted_token').length === 1, 'callback');
        const session = await recorder.finishLogin(url.searchParams.get('lk_state'), url.searchParams.get('encrypted_token'), getCookie(req, '__Host-opal-release-login'));
        res.setHeader('set-cookie', [cookie('__Host-opal-release-login', '') + '; Max-Age=0', cookie('__Host-opal-release-session', session.session)]);
        // The CSRF secret is not an authentication token. Keep it out of URLs
        // sent to the server, referrers, logs, and persistent browser storage.
        res.statusCode = 303; res.setHeader('location', `${recorder.origin}/#csrf=${session.csrf}`); return res.end();
      }
      need(req.method === 'POST' && url.pathname === '/csil/v1/rpc' && !url.search && req.headers['content-type'] === 'application/cbor', 'route');
      const chunks = []; let count = 0;
      for await (const chunk of req) {count += chunk.length; need(count <= limits.frame, 'frame-limit'); chunks.push(chunk);}
      const raw = Buffer.concat(chunks); checkFrame(raw); const request = wire.decode(raw);
      need(request instanceof Map && request.size === 5 && request.get('v') === 1 && Number.isSafeInteger(request.get('id')) && request.get('payload')?.tag === 24, 'envelope');
      const service = request.get('service'), op = request.get('op'), bytes = request.get('payload').value, id = request.get('id');
      if (service === 'Discovery' && op === 'describe') {
        const q = decode('DescribeRequest', bytes);
        const features = ['strict-cbor', 'human-exact-candidate', 'current-approval-status'];
        need(q.versions.includes('0alpha1') && q.requiredFeatures.every(f => features.includes(f)), 'version');
        const authenticated = ['review-candidate', 'approve', 'status', 'withdraw', 'submit', 'pull-events', 'acknowledge-event', 'fetch-approval'].map(op => `Releases/${op}`);
        return send('ServiceDescription', {protocol: '0alpha1', schema, implementedOperations: ['Discovery/describe', 'Discovery/read-schema', 'Releases/begin-login', ...authenticated], features, maxFrameBytes: limits.frame, maxCollectionItems: limits.collection, maxDepth: limits.depth, requestTimeoutMs: limits.timeout, authenticatedOperations: authenticated}, id);
      }
      if (service === 'Discovery' && op === 'read-schema') {
        const q = decode('SchemaRequest', bytes);
        need(q.schema.name === schema.name && q.schema.version === schema.version && Buffer.from(q.schema.digest).equals(Buffer.from(schema.digest)), 'schema');
        return send('SchemaDocument', {schema, descriptor}, id);
      }
      need(service === 'Releases', 'service');
      if (op === 'status') return send('SignedRecord', await recorder.status(decode('SignedRecord', bytes), resolveKey), id);
      if (op === 'submit') return send('ReleaseSubmitted', await recorder.submit(decode('SignedRecord', bytes), resolveKey), id);
      if (op === 'pull-events') return send('SignedRecord', await recorder.pullEvents(decode('SignedRecord', bytes), resolveKey), id);
      if (op === 'acknowledge-event') return send('ReleaseAcknowledged', await recorder.acknowledgeEvent(decode('SignedRecord', bytes), resolveKey), id);
      if (op === 'fetch-approval') return send('SignedRecord', await recorder.fetchApproval(decode('SignedRecord', bytes), resolveKey), id);
      need(req.headers.origin === recorder.origin, 'origin');
      if (op === 'begin-login') {
        const window = Math.floor(Date.now() / 60000);
        if (window !== loginWindow) {loginCount = 0; loginWindow = window;}
        need(++loginCount <= 10, 'login-rate-limit');
        const body = decode('ReleaseLoginStart', bytes), binding = randomBytes(32).toString('base64url');
        const started = await recorder.beginLogin(body.identity, binding);
        res.setHeader('set-cookie', cookie('__Host-opal-release-login', binding));
        return send('ReleaseLoginStarted', {redirectUrl: started.redirectUrl}, id);
      }
      const auth = {session: getCookie(req, '__Host-opal-release-session'), csrf: req.headers['x-opal-csrf'], origin: req.headers.origin};
      if (op === 'review-candidate') return send('ReleaseReviewed', recorder.reviewCandidate(auth, decode('ReleaseCandidateRef', bytes).candidateId), id);
      if (op === 'approve') return send('SignedRecord', recorder.approve(auth, decode('ReleaseApprove', bytes)), id);
      if (op === 'withdraw') return send('ReleaseWithdrawn', recorder.withdraw(auth, decode('ReleaseWithdraw', bytes).approvalId), id);
      throw Error('operation');
    } catch {
      // Do not log exception strings: upstream errors can contain credentials.
      res.statusCode = 403; res.setHeader('content-type', 'text/plain'); res.end('Request refused\n');
    }
  });
  server.requestTimeout = 10000; server.headersTimeout = 10000; server.setTimeout(10000, socket => socket.destroy());
  return server;
}
