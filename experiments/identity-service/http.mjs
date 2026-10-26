// Local W02 gateway. Bind to loopback; terminate trusted HTTPS before deployment.
// The experiment drives this HTTP listener directly and asserts Secure cookies.
import { createServer } from 'node:http';
import { randomBytes } from 'node:crypto';
import { carrier } from './linkkeys.mjs';
import { decode } from './approval.mjs';
import * as wire from './generated/typescript/codec.gen.ts';
const cookie = (name, value) => `${name}=${value}; Path=/; Secure; HttpOnly; SameSite=Lax`;
function getCookie(request, name) {
  const values = (request.headers.cookie ?? '').split(';').map(p => p.trim()).filter(p => p.startsWith(`${name}=`));
  if (values.length !== 1) return '';
  const value = values[0].slice(name.length + 1);
  return /^[A-Za-z0-9_-]{43}$/.test(value) ? value : '';
}
export async function gateway({approval, receiver, resolveKey, statusRequesters}) {
  const server = createServer(async (req, res) => {
    res.setHeader('cache-control', 'no-store'); res.setHeader('referrer-policy', 'no-referrer');
    res.setHeader('content-type', 'application/cbor'); res.setHeader('x-content-type-options', 'nosniff');
    let id;
    const ok = (type, body) => res.end(carrier.RpcResponse.ok(type, wire[`to${type}Cbor`](body)).withId(id).encode());
    try {
      const url = new URL(req.url, 'http://localhost');
      if (req.method === 'GET' && url.pathname === '/auth/callback' && approval) {
        if (url.searchParams.getAll('lk_state').length !== 1 || url.searchParams.getAll('encrypted_token').length !== 1 || req.url.length > 32768) throw Error('callback');
        const s = await approval.finishLogin(url.searchParams.get('lk_state'), url.searchParams.get('encrypted_token'), getCookie(req, '__Host-opal-login'));
        res.setHeader('set-cookie', [cookie('__Host-opal-login', '') + '; Max-Age=0', cookie('__Host-opal-session', s.session)]);
        return ok('LoginSession', {human: s.human, csrf: s.csrf});
      }
      if (req.method !== 'POST' || url.pathname !== '/csil/v1/rpc' || req.headers['content-type'] !== 'application/cbor') throw Error('route');
      const chunks = []; let count = 0;
      for await (const c of req) {count += c.length; if (count > 40000) {res.statusCode = 413; res.end(); req.destroy(); return;} chunks.push(c);}
      const rpc = carrier.RpcRequest.decode(Buffer.concat(chunks)); id = rpc.id;
      if (rpc.service === 'ApprovalReceiver' && rpc.op === 'accept' && receiver) return ok('Accepted', {approvalId: await receiver.accept(rpc.payload)});
      if (rpc.service !== 'ApprovalService' || !approval) throw Error('service');
      if (rpc.op === 'status') return res.end(carrier.RpcResponse.ok('SignedStatus', await approval.authenticatedStatus(rpc.payload, resolveKey, statusRequesters)).withId(id).encode());
      if (req.headers.origin !== approval.origin) throw Error('origin');
      if (rpc.op === 'begin-login') {
        const body = decode('LoginStart', rpc.payload), binding = randomBytes(32).toString('base64url');
        const started = await approval.beginLogin(body.identity, binding);
        res.setHeader('set-cookie', cookie('__Host-opal-login', binding));
        return ok('LoginStarted', {redirectUrl: started.redirectUrl});
      }
      const auth = {session: getCookie(req, '__Host-opal-session'), csrf: req.headers['x-opal-csrf'], origin: req.headers.origin};
      if (rpc.op === 'review') {
        const body = decode('ReviewRequest', rpc.payload), r = approval.review(auth, body.allowance);
        return ok('ReviewResponse', {reviewId: r.id, allowance: r.bytes, digest: r.digest});
      }
      if (rpc.op === 'approve') {
        const body = decode('ApproveRequest', rpc.payload);
        return ok('Approval', approval.approve(auth, body.reviewId, body.digest, body.commandKey));
      }
      if (rpc.op === 'withdraw') {
        const body = decode('WithdrawRequest', rpc.payload); approval.withdraw(auth, body.approvalId);
        return ok('Accepted', {approvalId: body.approvalId});
      }
      throw Error('operation');
    } catch {
      // Do not put callback tokens, typed request bytes, or key-service errors
      // into HTTP replies or logs. A later audit path needs typed reason codes.
      res.statusCode = 403;
      res.end(carrier.RpcResponse.transportError(carrier.Status.Forbidden, 'Request refused').withId(id).encode());
    }
  });
  server.requestTimeout = 10000; server.headersTimeout = 10000; server.setTimeout(10000, s => s.destroy());
  await new Promise((resolve, reject) => {server.once('error', reject); server.listen(0, '127.0.0.1', resolve);});
  return {url: `http://127.0.0.1:${server.address().port}`, close: () => new Promise(resolve => {server.close(resolve); server.closeAllConnections();})};
}
