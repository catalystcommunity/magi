# Security Review of the Magi Web-Facing API (2026-11)

Date: 2026-11-09. Commit: f979cdc (main).

This review examines the public routes of Magi. It does not test live
servers. It reads the code only.

## Scope and Method

The public HTTPRoute (deploy/render.py:437-456) sends traffic to two
backends:

| Path | Backend | Authentication |
|---|---|---|
| `/login/*` | identity | Browser flow (see "Sign In") |
| `/rpc/IdentityService/*` | identity | Bearer, except `begin-login` and `poll-login` |
| `/rpc/MagiService/*`, `/rpc/RunnerService/*`, `/rpc/EgressAskService/*` | magi | Bearer |
| `/runner/v1/chat/completions` | magi | Runner bearer (header) |
| `/runner/egress` | magi | Runner bearer (header), no request timeout |
| `/runner/terminal` | magi | Runner bearer (header) |
| `/inference/connect` | magi | Inference-host bearer (header), no request timeout |
| `/api/terminal` | magi | User bearer in the first WebSocket message |
| `/healthz`, `/healthz/deps` | magi | None |
| `/`, `/s/<hash>/*` | magi | None (static web client) |

The review followed each route into lib/rpc, lib/authn, lib/revconn,
magi/compose, magi/web, magi/runner, primitives/identity,
primitives/artifacts, primitives/workflow, primitives/conversation, and
primitives/egress.

"Confirmed" means that the review traced the code path from the route to
the effect. "Possible" means that the review did not trace all of the path,
or that the effect depends on configuration that is not in the repository.

## Summary

| Severity | Confirmed | Possible |
|---|---|---|
| Critical | 0 | 0 |
| High | 1 | 0 |
| Medium | 9 | 1 |
| Low | 9 | 2 |
| Info | 4 | 1 |

The review found no way for an unauthenticated user on the internet to read
or change data. The authentication core is sound:

- Each RPC checks the bearer before it reads the body (lib/rpc/rpc.go:99-115).
- Tokens are Ed25519, with strict base64, an issuer check, and a size limit
  (lib/authn/authn.go:99-135).
- The primitives check the owner of each record against the caller
  (for example primitives/workflow/server/server.go:76-78,
  primitives/artifacts/server/server.go:98-116).
- The browser login binds the callback to the browser with a cookie, and
  the poller needs a one-time code (primitives/identity/server/http.go:107-214,
  server.go:216-247).
- No route sets CORS headers, and no Magi API call uses a cookie. Thus
  cross-site request forgery and cross-site WebSocket hijacking do not work
  now.
- The page Markdown renderer makes DOM nodes, not HTML
  (magi/web/src/markdown.ts). Mermaid 11.17.2 runs with
  `securityLevel: "strict"`.

The largest risks are:

1. A stolen admin token can push code to every runner machine.
2. A compromised runner can change any variable of its runs, and thus get
   secrets that are for other personas.
3. A task sandbox credential for the egress proxy never expires.
4. Any authenticated caller can make magi-server run out of memory.
5. There is no way to end a user session or to revoke a user token.

## Confirmed Findings

### H1. An admin token can push unsigned code to all runner machines

- Severity: High
- Files: magi/compose/fleet.go:109-118 (isOperator), fleet.go:263-285
  (PutRunnerPackage, SetRunnerRelease), magi/compose/runner.go:181-221
  (Hello, GetPackage), magi/runner/update.go:231-233.
- Description: An operator is a token with scope `magi:operate`, or any token
  whose subject is in `MAGI_ADMINS`. A browser login of an admin gives an
  ordinary user bearer. The web client keeps it in localStorage
  (magi/web/src/api.ts:16-22). The bearer lives 12 hours and can be
  refreshed until the session ends (7 days). The runner checks only that
  the package has the SHA-256 digest that Magi sent. No publisher signature
  protects the package.
- Attack: An attacker gets an admin bearer (XSS, a stolen laptop, a log, or a
  compromised magi-server). The attacker calls `put-runner-package` with a
  malicious binary and `set-runner-release`. Each runner downloads and runs
  it on the next `hello`. The attacker now runs code on every runner host,
  outside the sandbox. Runner hosts are household machines.
- Fix:
  1. Sign runner packages with an offline release key. Put the public key
     in the runner configuration. Make the runner refuse a package with no
     valid signature.
  2. Do not give `isOperator` to subjects in `MAGI_ADMINS` for fleet
     operations. Require a minted operator token with scope `magi:operate`.
  3. Log each release change with the caller subject, and send a notice to
     all admins.

### M1. A runner can overwrite any run variable, and thus get secrets of another persona

- Severity: Medium
- Files: magi/compose/runner.go:112-120 (Complete),
  primitives/workflow/server/server.go:408 (`maps.Copy(r.Vars, req.Outputs)`),
  magi/compose/runner.go:335-356 (EgressAccess), magi/compose/egress.go:123-135,
  179-195, 285-295 (persona of a flow).
- Description: A runner completes a step with any map of outputs. The
  workflow copies all outputs into the run variables. Magi uses run
  variables for security decisions: `persona` selects the egress secret
  rules and the task environment variables (for example `GH_TOKEN`),
  `runner` selects the terminal target, `patch` selects the artifact that
  `get-task-diff` reads with the service token.
- Attack: A compromised runner (a household laptop) gets a code-change task
  of the default persona. It completes the first step with the output
  `persona=persona:engineering`. In the next step, `egress-access` gives the
  sandbox the `GH_TOKEN` placeholder, and the proxy inserts the real GitHub
  token for that run. The runner can also set `patch` to the digest of an
  artifact of another person. `get-task-diff` then reads that artifact with
  the privileged service token (magi/compose/service.go:111-122), because
  the artifacts service gives a privileged caller the oldest record of any
  owner (primitives/artifacts/server/server.go:107-109).
- Fix:
  1. In the workflow, accept only the output keys that the step definition
     declares. Refuse outputs that change `persona`, `runner`, `publisher`,
     `repo`, `stream`, `owner_name`, or `environment`.
  2. In `GetTaskDiff` and `taskOutput`, read the artifact with an act-for of
     the run owner, or check that the artifact record owner is the run
     owner.
  3. In `Runners.PutArtifact` (runner.go:122-141), drop the label `kind`
     from runner labels, or allow only a fixed set of kinds.

### M2. The egress proxy credential of a run never expires

- Severity: Medium
- Files: magi/compose/runner.go:344 (`Credential(p.Key, req.Run)`),
  magi/compose/egress.go:244-267 (environment), primitives/egress/src/proxy.rs:54-69.
- Description: The proxy credential is HMAC(key, run id). It has no expiry
  and no nonce. The proxy asks Magi for the owner of the environment. Magi
  answers for any run, also for a run that ended. The secret rules then
  apply as for a live task.
- Attack: A prompt-injected task writes `HTTP_PROXY` (it has the credential)
  into a commit, an artifact, or a request to a permitted host. Later, any
  party that can reach port 3128 uses it: another task pod in the execution
  namespace, or any runner through `/runner/egress`. The proxy inserts the
  secrets of the old run (for example the GitHub token) for months.
- Fix: Put an expiry in the credential (HMAC over run id and expiry), and
  check it in the proxy. In `Egress.environment`, refuse runs that are not
  `running` or `waiting`. Rotate the environment key after a leak.

### M3. Any caller can make magi-server run out of memory

- Severity: Medium
- Files: magi/cmd/magi-server/main.go:162 (`MaxBody: 80 << 20`),
  lib/rpc/rpc.go:107-117, deploy/render.py:426 (`mem="512Mi"`).
- Description: The 80 MiB limit is for runner uploads, but it applies to all
  operations of all three services. The server reads the full body into
  memory. The CBOR decoder then makes more copies. Some operations send the
  bytes on to other services (for example `say` with audio).
- Attack: A user, a runner, or an inference host sends four parallel 80 MiB
  `MagiService/say` requests. The pod has a 512 MiB limit. Kubernetes kills
  it. The attacker repeats this, and Magi stays down.
- Fix: Give each operation its own limit. Use 80 MiB only for
  `RunnerService/put-artifact` and the operator `put-*-package` calls. Use
  1 MiB as the default, and a few MiB for `say` audio. Add a limit on the
  number of concurrent large requests. Set the same limits at the gateway.

### M4. There is no way to end a user session or to revoke a user token

- Severity: Medium
- Files: primitives/identity/server/server.go:269-286 (issueUser),
  server.go:331-351 (Refresh), primitives/identity/api/dispatch.gen.go
  (no end-session operation), magi/web/src/api.ts:43 (signOut).
- Description: A user token has no session id. `refresh` issues a new token
  if the subject has any active session. The web client "Sign out" only
  deletes localStorage. No operation ends a session. magi-server never
  checks a revocation list.
- Attack: An attacker steals a bearer (shared computer, log, XSS). The
  attacker calls `refresh` every 12 hours. The token stays valid until the
  session expires (7 days). The person cannot stop this, also after "Sign
  out".
- Fix: Add `sid` to the user token. In `refresh`, check that this session is
  active. Add `end-session` and `end-all-sessions` operations, and call
  `end-session` from "Sign out". Make the default token life shorter (for
  example 1 hour), and use the rotating refresh cookie pattern
  (`~/agent-learning/browser-session-refresh-cookie.md`).

### M5. One anonymous client can stop all sign-ins

- Severity: Medium
- Files: primitives/identity/server/server.go:102-119 (allow),
  server.go:144, server.go:329 (BeginGrant uses the same bucket).
- Description: `begin-login` needs no token. All callers share one token
  bucket: burst 10, then 1 per second. `begin-grant` uses the same bucket.
  Any `name@domain` of an admitted domain passes the checks before the
  bucket. The error message of a refused domain names the domain, so the
  admitted domains are easy to find.
- Attack: An attacker sends `begin-login` for `x@<admitted domain>` two times
  each second. All other people get 429 "too many logins". New devices and
  people with expired sessions cannot sign in. Each accepted call also costs
  one LinkKeys request and one database row.
- Fix: Use a bucket per client IP (this needs a trusted client IP, see
  "Without Cloudflare") and a bucket per identity. Keep a global bucket only
  as a high backstop. Give `begin-grant` its own bucket per subject.

### M6. Machine tokens can use all user operations

- Severity: Medium
- Files: lib/rpc/rpc.go:99-106, magi/compose/service.go:38-55 (StartTask),
  magi/compose/conversation.go:41-112 (Say), magi/compose/fleet.go:249-261
  (ListRunners).
- Description: magi-server accepts every token of the issuer for every
  `MagiService` operation. Most operations do not check that the caller
  kind is `user`. Runner tokens (`runner:<name>`), inference-host tokens,
  and service tokens (for example the egress proxy token) can thus act as a
  person whose subject is the machine name.
- Attack: An attacker takes over an inference host (a GPU machine at
  someone's home). With its token, the attacker calls `say` to
  "Engineering, ..." or `start-task`. The task is owned by
  `inference:<name>`. Secret rules with empty `owners` apply to it. The
  attacker also uses the provider model at the household's cost, and reads
  the runner list.
- Fix: In `MagiService`, require `Kind == user` and a `user:` subject, except
  for the operations that operators or services need (list them). The
  rpc.Server can take a per-service check function.

### M7. Page reads can reach internal addresses (DNS rebinding)

- Severity: Medium (the exploit needs a DNS rebinding host)
- Files: magi/compose/search.go:168-192 (Read resolves and checks),
  primitives/egress/src/proxy.rs:123 and the upstream client in
  primitives/egress/src/flow.rs (the proxy resolves the name again),
  magi/compose/egress.go:304-308 (all `svc_` flows pass),
  deploy/README.md:184 (`{"host": "*", "environments": ["svc_search"]}`),
  deploy/render.py:146-157 (the egress pod has only an Ingress policy).
- Description: `read_page` resolves the host and refuses private addresses.
  Then the request goes through the proxy as `svc_search`. The proxy
  resolves the name again and connects. The proxy has no check for private,
  loopback, link-local, or cluster addresses. The policy for `svc_search`
  permits all hosts. `Evaluate` passes all flows of `svc_` users with no
  review. The egress pod can reach all cluster addresses.
- Attack: A web page or a message tells the persona to read
  `http://rebind.attacker.example/`. The first DNS answer is a public
  address. The second answer is `10.x.y.z` or `169.254.169.254`. The proxy
  fetches the internal page on port 80 or 443. The persona puts the text in
  its reply, or a later injected instruction sends it out.
- Fix: In the proxy, check the resolved address at connect time. Refuse
  private, loopback, link-local, CGNAT, and the cluster pod and service
  ranges for all environments, unless a policy entry names the address. Add
  an Egress NetworkPolicy for the egress pod that blocks the cluster ranges.

### M8. The GitHub token is open to all code in an engineering task sandbox

- Severity: Medium (the impact depends on the rules file, which is not in the repository)
- Files: magi/compose/egress.go:123-135 (TaskEnv), egress.go:179-195
  (secretRules), magi/compose/conversation.go:69-76 (the caller selects the
  persona), magi/runner/pr.go:112-122, magi/compose/runner.go:350.
- Description: The rule for the GitHub token is scoped to a persona. The
  persona is not a security boundary: any person (and any machine token, see
  M6) can address "Engineering". The sandbox gets `GH_TOKEN` at the start of
  the task, not only for the merge step. The apprentice runs model-written
  commands in that sandbox. The proxy inserts the real token in each
  request to the rule hosts. The scanner stops the token from leaving, but
  it does not stop misuse of the token.
- Attack: A task reads a repository file or a web page with a prompt
  injection. The apprentice runs
  `curl -H "Authorization: token $GH_TOKEN" -X DELETE https://api.github.com/repos/<org>/<repo>`,
  or it pushes to another repository, or it merges a pull request with no
  human decision.
- Fix:
  1. Set `owners` on the GitHub secret rule.
  2. Set `path` on the rule to the merge endpoint of the permitted
     repositories, and permit only the needed methods.
  3. Use a fine-grained token for one repository with the least
     permissions.
  4. Give the placeholder only to the pr-merge step, after the human
     decision, with a separate short-lived credential.

### M9. The web client has no security headers

- Severity: Medium (no XSS is known now; the headers limit the damage of a future one)
- Files: magi/web/web.go:29-41, magi/web/src/api.ts:16-22.
- Description: The web handler sets only `Cache-Control` and `Content-Type`.
  There is no `Content-Security-Policy`, no `frame-ancestors` or
  `X-Frame-Options`, no `X-Content-Type-Options`, no
  `Strict-Transport-Security`, and no `Referrer-Policy`. The bearer is in
  localStorage, so script in the page can read it. The page loads a large
  third-party library (mermaid) and puts its SVG in `innerHTML`
  (magi/web/src/page.tsx:39).
- Attack: A future mermaid sanitizer bypass, or a mistake in a new view,
  runs script on the Magi origin. The script reads `magi.token` and sends it
  out. With an admin token, this leads to H1. A framing site can also try a
  click-jacking attack on "Approve" in a task view (browser storage
  partitioning reduces this).
- Fix: Set on all responses of the web handler:
  `Content-Security-Policy: default-src 'self'; script-src 'self' 'sha256-<importmap hash>'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self' https:`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, and
  `Strict-Transport-Security: max-age=31536000`. Set `nosniff` and
  `frame-ancestors 'none'` on RPC responses too. Later, keep the bearer in
  memory only and use an httpOnly refresh cookie (see M4).

### L1. Anonymous long polls load the identity database

- Severity: Low
- Files: primitives/identity/server/server.go:180-214.
- Description: `poll-login` needs no token. A poll with a valid `login_id`
  waits up to 30 s and reads the database every 250 ms. There is no limit on
  concurrent polls.
- Attack: An attacker makes logins at the bucket rate (about 600 live ids in
  10 minutes) and opens many polls for each. 1000 polls make 4000 database
  reads each second.
- Fix: Limit concurrent polls for each `login_id` (one) and in total. Wait on
  an in-process notification or PostgreSQL LISTEN, not on a timer.

### L2. `/healthz/deps` is public

- Severity: Low
- Files: magi/compose/deps.go:120-147, deploy/render.py:443 (the `/`
  prefix rule sends it to magi).
- Description: An anonymous request starts calls to all internal services
  with the service token (at most every 2 s). The answer names the internal
  services and their state. All requests wait on one mutex while the check
  runs.
- Fix: Do not route `/healthz` and `/healthz/deps` from the gateway. Add an
  Exact rule that the gateway refuses, or serve health on a separate port.

### L3. Upgrade endpoints have no connection limits

- Severity: Low
- Files: lib/revconn/revconn.go:79-135 (no limit on idle connections for
  each agent), magi/compose/runner.go:363-412 (no limit on tunnels for each
  runner), magi/compose/terminal.go:337-347 (an anonymous client can hold a
  WebSocket for 15 s before the hello).
- Description: A token holder can open many long connections. An anonymous
  client can open many WebSockets on `/api/terminal`. Each costs a goroutine
  and a file descriptor.
- Fix: Limit connections for each subject (for example 32 idle for an
  inference host, 64 tunnels for a runner). Limit unauthenticated WebSockets
  in total, and shorten the hello time to 5 s.

### L4. The model proxy has no budget, and the apprentice holds the full runner token

- Severity: Low
- Files: magi/compose/runner.go:250-297 (ModelProxy),
  magi/runner/steps.go:172-174 (`MAGI_MODEL_API_KEY_FILE=` the runner token
  file).
- Description: Any runner token can send any number of model requests with
  the household provider key. The apprentice process gets the runner token
  file as its model key. That token also opens RunnerService, the egress
  tunnel, and terminals.
- Fix: Add a request and token budget for each runner. Give the apprentice a
  separate token with only a model-proxy scope.

### L5. Login return URLs accept any reverse-DNS scheme

- Severity: Low
- Files: primitives/identity/server/server.go:136-143, 170-178.
- Description: `return_url` can be any scheme that has a dot, for example
  `com.attacker.app:/x`. The code goes to that app. The code alone is not
  useful, but an attacker that started the login has the `login_id`.
- Attack: The attacker starts a login with its own app scheme and sends the
  start link to a person who has the attacker's app installed. If the person
  continues and signs in, the attacker app gets the code, and the attacker
  gets a token for the person. The start page warns "Continue only if you
  started this sign-in".
- Fix: Accept only a configured list of app schemes (for example
  `community.catalyst.magi`).

### L6. Longhouse connect starts one goroutine for each call

- Severity: Low
- Files: magi/compose/connect.go:205-214.
- Description: Each `connect` for Longhouse starts a goroutine that polls
  Longhouse until the code expires. There is no limit for each person.
- Fix: Keep one pending connect for each person and system.

### L7. The HTTP server has no body read timeout

- Severity: Low (the gateway limits this now)
- Files: lib/rpc/rpc.go:279.
- Description: The server sets only `ReadHeaderTimeout`. A client can send a
  body slowly. The anonymous identity operations read the body (64 KiB
  limit). The gateway request timeout of 120 s limits this now.
- Fix: Read the body with a deadline (`http.NewResponseController(w).SetReadDeadline`)
  for each operation, except long uploads.

### L8. Any token can read the runner fleet

- Severity: Low
- Files: magi/compose/fleet.go:249-261, 294-311.
- Description: `list-runners`, `get-app-release`, and `get-app-package` need
  only a valid token of any kind. The answer has runner names, operating
  systems, repositories, and versions.
- Fix: Require a user token (see M6). Show repositories only to admins.

### L9. Internal error texts go to clients

- Severity: Low
- Files: lib/rpc/rpc.go:121-133, magi/compose/terminal.go:293-296.
- Description: Errors that carry a status code go to the client with their
  full text, for example internal URLs and errors of other services.
- Fix: Map errors of other services to short messages. Log the details.

### I1. The terminal WebSocket accepts all origins

- Severity: Info
- Files: magi/compose/terminal.go:308-315.
- Description: This is safe now, because the token comes in the first
  message and never in a cookie. If a later change adds a session cookie,
  cross-site WebSocket hijacking becomes possible.
- Fix: Check `Origin` against `MAGI_PUBLIC_URL` (allow no `Origin` for the
  CLI). This costs nothing and protects future changes.

### I2. The static file server lists directories

- Severity: Info
- Files: magi/web/web.go:28.
- Description: `http.FileServer` lists the files of a directory under
  `/s/<hash>/`. The files are public, so this shows nothing secret.
- Fix: Wrap the file system so that a directory gives 404.

### I3. Secrets are not in logs or URLs in the paths that the review read

- Severity: Info
- Description: The review found no log line with a token, a key, or a
  credential value. Tokens go in headers, not in URLs. The one exception is
  the login code, which goes in the return URL query. The web client
  removes it at once (magi/web/src/app.tsx:55-60). `get-credential` needs
  scope `identity:present`, so a user cannot read a stored credential back.
  The proxy holds a request before it puts in a secret, so reviews show only
  placeholders.

### I4. Android and CLI token storage

- Severity: Info
- Description: Android keeps the token in private SharedPreferences with
  `allowBackup="false"` and `usesCleartextTraffic="false"`. The CLI writes
  its profile with mode 0600 (magi/cmd/magi/main.go:925).

## Possible Findings

### P1. The sandbox can read the runner state directory

- Severity: Medium (if true)
- Files: magi/runner/local.go:65-90 (apprentice token in `StateDir`),
  magi/runner/runner.go:56 (`token_file`).
- Description: The review did not trace the bubblewrap and VM mounts. If a
  sandbox can read `StateDir` or the runner token file, task code gets the
  runner token (see L4 and M6).
- Check: Start a task and run `ls` on the state directory and on the token
  path from inside the sandbox.

### P2. Cross-person prompt injection through shared personas

- Severity: Low to Medium (design)
- Description: A persona with sharing `public` sees messages of several
  people. A message from one person can carry instructions that the persona
  follows in the turn of another person (calendar writes, Longhouse tasks,
  code-change tasks). docs/prompt-injection.md describes the detector. The
  review did not test it.
- Check: Write an eval where person A plants an instruction and person B
  asks a normal question.

### P3. xterm.js escape sequences from a sandbox

- Severity: Low
- Description: Task code writes to the terminal of a person. xterm.js handles
  OSC sequences (links, titles). The review did not check the xterm options
  in magi/web/src/terminal.tsx for OSC 52 (clipboard) and link handlers.

### P4. Gateway TLS and origin exposure

- Severity: Info (configuration outside the repository)
- Description: The repository does not show which listener of the gateway
  ends TLS, or whether the origin accepts traffic that does not come from
  Cloudflare. If the gateway accepts any source, an attacker can go around
  Cloudflare now, and all Cloudflare protections are already optional.

## Without Cloudflare

### What Cloudflare protects now

The repository has no Cloudflare configuration and no code that reads
`CF-Connecting-IP` or `X-Forwarded-For`. Thus Magi gets nothing from
Cloudflare that the code depends on. Cloudflare can give these protections,
if the zone has them on:

- TLS at the edge, and HSTS if it is set in the dashboard.
- It hides the origin address, but only if the origin refuses traffic that
  does not come from Cloudflare addresses (see P4).
- It absorbs volumetric attacks (layer 3, 4, and 7 floods).
- It limits request bodies to 100 MB (Free and Pro plans) and stops
  requests that are idle for 100 s.
- WAF managed rules, rate limiting rules, and bot rules, if the zone has
  them.

Cloudflare does not protect against the findings in this report. All of
them come from valid tokens or from requests that look normal.

### What must be true before the origin is exposed directly

1. TLS at the gateway: a valid certificate, TLS 1.2 or higher, and HSTS.
   Redirect port 80 to 443 for the web client only.
2. A trusted client IP: configure Envoy (Contour) to set the client address
   from the TCP peer, and to ignore `X-Forwarded-For` from the internet.
   Per-IP limits need this.
3. Per-IP rate limits at the gateway (Contour HTTPProxy local rate limits,
   or Envoy Gateway BackendTrafficPolicy):
   - `/rpc/IdentityService/begin-login`: a few per minute.
   - `/rpc/IdentityService/poll-login`: a few concurrent.
   - `/login/*` and `/api/terminal`: low.
   - All other routes: a general limit.
4. Fix M5 (per-identity and per-IP login buckets), L1 (poll limits), and L2
   (do not route health endpoints).
5. Request size limits at the gateway for each route, as in M3. Fix M3 in
   magi-server too.
6. Connection limits: a maximum number of connections for each client IP,
   and timeouts on the upgrade routes (`/runner/egress`,
   `/inference/connect` have no request timeout now; give them an idle
   timeout).
7. Security headers in the application (M9), because Cloudflare cannot add
   them then.
8. Monitoring: alerts on 429 rates, on login failures, on pod restarts
   (OOM), and on new runner releases.
9. A plan for floods: the cluster has no protection against a volumetric
   attack. Keep the Cloudflare zone ready so that DNS can go back to it.

## Next Actions

Ranked by risk. The reason for each is in the finding.

1. **H1**: Sign runner packages and pin the key in the runner. Remove
   `MAGI_ADMINS` from fleet operator checks. Reason: one stolen browser
   token gives code execution on all runner machines.
2. **M1**: Limit runner outputs to declared keys, and protect `persona`,
   `runner`, `repo`, and `environment`. Reason: a compromised runner gets the
   GitHub token and other persons' artifacts.
3. **M8**: Set `owners` and `path` on the GitHub secret rule, and use a
   fine-grained token. Give the placeholder only to the merge step. Reason:
   prompt injection in any engineering task can misuse the token now.
4. **M2**: Add an expiry to proxy credentials, and refuse ended runs.
   Reason: a leaked credential gives the secrets of a run for ever.
5. **M6**: Require a user token for `MagiService` user operations. Reason:
   machine tokens on less trusted hosts act as people.
6. **M3**: Per-operation body limits. Reason: one user can stop Magi.
7. **M4**: Session id in tokens, `end-session`, and a sign-out that ends
   the session. Reason: a stolen token cannot be stopped.
8. **M7**: Block private addresses in the egress proxy, and add an egress
   NetworkPolicy. Reason: SSRF into the cluster and the home network.
9. **M9**: Add the security headers. Reason: they limit any future XSS,
   which would expose the bearer.
10. **M5, L1, L2**: Per-IP and per-identity login limits, poll limits, and
    no public health endpoints. Reason: anonymous denial of service. These
    are required before the origin is exposed without Cloudflare.
11. **P1**: Check the sandbox mounts of the runner state directory.
12. **L3 to L9, I1, I2**: Fix in normal work.
