import {environment} from './environment.mjs';
import {once} from 'node:events';
import {readFileSync, writeFileSync, mkdirSync} from 'node:fs';
import {resolve, join} from 'node:path';
import {createHash} from 'node:crypto';
import {fixture} from '../tests/release-fixture.mjs';
import {releaseHTTP} from '../release/http.mjs';
import {wire} from '../release/policy.mjs';
const modulePath = environment('MAGI_PLAYWRIGHT_MODULE');
if (!modulePath) throw Error('Set OPAL_PLAYWRIGHT_MODULE to the installed Playwright module');
const {firefox} = await import(modulePath), f = fixture();
const server = releaseHTTP({recorder: f.recorder, resolveKey: f.resolveKey});
server.listen(0, '127.0.0.1'); await once(server, 'listening');
const origin = `http://127.0.0.1:${server.address().port}`;
const browser = await firefox.launch({headless: true});
const diagnostics = [];
const finish = f.recorder.finishLogin.bind(f.recorder);
f.recorder.finishLogin = async (...args) => {
  try {const session = await finish(...args); diagnostics.push({callback: 'accepted'}); return session;}
  catch (error) {diagnostics.push({callback: 'refused', code: error.code ?? error.message, browserBindingPresent: !!args[2]}); throw error;}
};
try {
  const submission = await f.client.submit(wire.toReleaseCandidateCbor(f.value));
  const context = await browser.newContext({acceptDownloads: true}), page = await context.newPage();
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  // This entire browser test uses synthetic RP operations. Intercept both
  // origins; no request goes to the real human's identity domain.
  await context.route(`${f.origin}/**`, async route => {
    const url = new URL(route.request().url());
    // The test proxy changes the transport host. Preserve only this test app's
    // cookies; Playwright's API cookie jar would otherwise scope them to localhost.
    const cookies = await context.cookies(f.origin);
    diagnostics.push({path: url.pathname, cookieNames: cookies.map(c => c.name)});
    const headers = {...route.request().headers(), cookie: cookies.map(c => `${c.name}=${c.value}`).join('; ')};
    const response = await route.fetch({url: `${origin}${url.pathname}${url.search}`, headers, maxRedirects: 0});
    diagnostics.push({responsePath: url.pathname, status: response.status(), method: route.request().method(), origin: headers.origin, setsCookie: !!response.headers()['set-cookie']});
    if (response.status() === 303) {
      // Firefox can continue an intercepted redirect outside the route. Start
      // a new navigation so this fixture cannot contact the live application.
      const replyHeaders = {...response.headers(), 'content-type': 'text/html'};
      delete replyHeaders.location; delete replyHeaders['content-security-policy']; delete replyHeaders['content-length'];
      await route.fulfill({status: 200, headers: replyHeaders, body: `<script>location.replace(${JSON.stringify(response.headers().location)})</script>`});
    } else await route.fulfill({response});
  });
  await context.route(`https://${f.config.approver.domain}/**`, async route => {
    diagnostics.push({idp: 'intercepted'});
    const url = new URL(route.request().url()), callback = new URL(url.searchParams.get('callback_url'));
    callback.searchParams.set('encrypted_token', url.searchParams.get('nonce'));
    await route.fulfill({status: 200, contentType: 'text/html', body: `<script>location.replace(${JSON.stringify(callback.href)})</script>`});
  });
  await page.goto(submission.reviewUrl);
  if (await page.locator('input[type=file]').count()) throw Error('Manual file upload is present');
  await page.getByRole('button', {name: 'Sign in with LinkKeys'}).click();
  await page.getByText(f.value.explanation.title, {exact: true}).waitFor();
  if (new URL(page.url()).hash) throw Error('CSRF fragment not removed');
  await page.getByRole('button', {name: 'Approve this exact candidate'}).waitFor({state: 'visible'});
  await page.locator('#approve:enabled').waitFor();
  if (!(await page.locator('#details').textContent()).includes(f.value.image)) throw Error('Image not visible in review');
  if (f.recorder.db.prepare('SELECT count(*) AS n FROM approvals').get().n !== 0) throw Error('Review issued approval');
  await page.getByRole('button', {name: 'Approve this exact candidate'}).click();
  await page.getByText('Approval recorded and queued for the controller. No file transfer is needed. This does not yet confirm deployment.', {exact: true}).waitFor();
  if (await page.locator('a[download]').count()) throw Error('Manual approval download is present');
  if ((await f.client.events()).length !== 1) throw Error('Approval event not retained');
  const count = f.recorder.db.prepare('SELECT count(*) AS n FROM approvals').get().n;
  if (count !== 1) throw Error('Approval not recorded exactly once');
  await page.getByRole('button', {name: 'Withdraw this approval'}).click();
  await page.getByText('Approval withdrawn. An already admitted release is not cancelled.', {exact: true}).waitFor();
  if (f.recorder.db.prepare('SELECT count(*) AS n FROM withdrawals').get().n !== 1 || errors.length) throw Error('Browser withdrawal or script error');
  const root = resolve(import.meta.dirname, '..'), dir = join(root, 'test-results'); mkdirSync(dir, {recursive: true});
  const sourceHashes = Object.fromEntries(['release/browser.mjs', 'release/index.html', 'release/http.mjs', 'release/approval.mjs', 'scripts/test-release-browser.mjs'].map(path => [path, createHash('sha256').update(readFileSync(join(root, path))).digest('hex')]));
  const file = join(dir, `${new Date().toISOString().replaceAll(':', '-')}-release-browser.json`);
  writeFileSync(file, JSON.stringify({scope: 'Synthetic RP; intercepted HTTPS origins; real Firefox. No real human login or approval.', browser: await browser.version(), checks: ['machine-submission', 'no-file-upload', 'review-link-survives-login', 'browser-bound-login', 'csrf-fragment-removed', 'readable-explanation', 'exact-candidate-visible', 'review-is-not-approval', 'explicit-approval', 'durable-approval-event', 'no-file-download', 'withdrawal'], pageErrors: errors, sourceHashes}, null, 2) + '\n');
  console.log(file);
} catch (error) {
  const file = resolve(import.meta.dirname, '../test-results', `${new Date().toISOString().replaceAll(':', '-')}-release-browser.failed.json`);
  writeFileSync(file, JSON.stringify({scope: 'Synthetic browser test', diagnostics}, null, 2) + '\n');
  throw Error(`Release browser test failed; see ${file}`);
} finally {await browser.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); f.close();}
