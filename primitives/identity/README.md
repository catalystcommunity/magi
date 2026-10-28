# identity

The identity primitive signs people in with LinkKeys and issues Magi bearer
tokens (`lib/authn`). It is a LinkKeys regular relying party (RP). It does
not authorize records. Each primitive authorizes its own records.

## API

Contract: `contracts/identity.csil`, service `IdentityService`.

| Operation | Token | Result |
| --- | --- | --- |
| `begin-login` | none | `login_id` (secret), `url`, `expires_ms` (10 minutes). Optional `return_url` on the Magi origin: after the sign in, the browser goes back to it in the same tab. |
| `poll-login` | none | Waits up to `wait_ms` (default 25 s, maximum 30 s). Status `pending`, `complete`, `failed`, or `expired`. The token comes once, on the first poll after completion. |
| `who-am-i` | any | Subject, kind, scopes, and expiry of the caller |
| `refresh` | user | New user token for the same subject. The login session must be active (7 days from login). |
| `issue-service-token` | `identity:admin` | Token for `service:<name>` with the given scopes |

User tokens last 12 hours and have no scopes. The subject is
`user:<user_id>@<domain>`, in lowercase. `<user_id>` is the LinkKeys user
ID from the verified assertion. It does not change. The token also has a
`name` for display: the LinkKeys display name or the optional `handle`
claim. The name is not an identifier, and Magi does not check its claim
signature. The login name that the person types is only a hint to fill the
IDP form; Magi does not trust it.

HTTP routes on the same server:

| Route | Use |
| --- | --- |
| `GET /login/start?t=...` | Shows the identity, the program name, and a Continue button |
| `POST /login/start` | Same origin only. Sets the browser binding cookie and redirects to the IDP |
| `GET /login/callback` | LinkKeys returns here. Redirects to `return_url`, or shows "Signed in as ..." or an error |
| `GET /healthz` | Health check |

## Login flow and bindings

1. The client calls `begin-login`. Magi calls `Rp/sign-request` and keeps
   the pending login in PostgreSQL. The client keeps `login_id` and gives
   `url` to the person.
2. The person opens `url` and clicks Continue. A web client on the Magi
   origin can post the start form from its own page instead, so the person
   goes directly to LinkKeys.
3. `POST /login/start` sets a random cookie (`HttpOnly`, `Secure`,
   `SameSite=Lax`, path `/login/callback`) and keeps only its SHA-256 hash.
   The start link works one time.
4. LinkKeys sends the browser to `/login/callback?lk_state=...`. Magi takes
   the pending login one time, by `lk_state`. The cookie must match. Then
   Magi calls `Rp/decrypt-token`, `Rp/verify-assertion`, and
   `Rp/userinfo-fetch`. It checks the domain, the audience, the nonce, the
   times, and that every claim is about the same user and was authorized.
   Then it checks the domain policy again. If the client gave `return_url`,
   the browser goes back there in the same tab.
5. `poll-login` returns the token one time.

Why: the poller and the browser have different secrets. `login_id` does not
go to the browser. `lk_state` and the cookie do not go to the poller. A
callback from a different browser fails (login CSRF). A replayed callback
finds no login. The Continue click (or the same-origin post) makes sure
that a link alone cannot complete a sign-in when the IDP already has a
session. The database keeps
only hashes of `login_id`, the start token, `lk_state`, and the cookie.

## Configuration

Flags have environment defaults.

| Variable | Default | Use |
| --- | --- | --- |
| `MAGI_LISTEN` | `:8080` | Listen address |
| `MAGI_ISSUER_KEY` | required | Issuer private key file |
| `MAGI_AUTH_KEYS` | empty | More issuer key files to accept. The issuer key is always accepted |
| `MAGI_PUBLIC_URL` | required | Public base URL, for example `https://magi.example.local`. It must be a subdomain of the RP domain |
| `MAGI_ALLOWED_DOMAINS` | `todandlorna.com,squizzlezig.com` | Identity domains that can sign in |
| `MAGI_LINKKEYS_RUNTIME` | required | Private RP file (below) |
| `MAGI_DATABASE_URL` | required | PostgreSQL URL |
| `MAGI_TOKEN_TTL` | `12h` | User token lifetime |
| `MAGI_SESSION_TTL` | `168h` | Login session lifetime |

The runtime file is private. Do not put it in an image or in the repository:

```json
{"rp_domain": "example.local", "tcp_address": "rp.example.local:4987",
 "fingerprints": ["<64 hex>"], "api_key": "<RP API key>"}
```

`tcp_address` is the CSIL-RPC TCP endpoint of the RP server. Port 4987 is
the default. `fingerprints` are the SHA-256 pins of the RP TLS key.

## Bootstrap commands

```sh
identity keygen --issuer magi --out key.json --public key.pub.json
identity mint --key key.json --subject service:magi --scopes a,b --ttl 720h --out token.txt
```

Both commands make files with mode 600 and do not overwrite files. Give
`key.pub.json` to other services in `MAGI_AUTH_KEYS`.

## Code

- `server/`: service, HTTP routes.
- `store/`: PostgreSQL store with an embedded schema.
- `linkkeys/`: Go port of the begin and complete steps of the LinkKeys
  TypeScript regular-RP SDK. It has its own pinned CSIL-RPC TLS transport.
  `rp.csil` is a copy of the needed part of the LinkKeys contract. Run
  `linkkeys/gen.sh` after you change it. This module does not import the
  LinkKeys Go SDK, because that SDK needs Go 1.26.4 and the Magi modules
  use Go 1.26.0.
- `internal/fakelk/`: fake LinkKeys domain and RP server for tests.

## Tests

```sh
go test ./...
MAGI_TEST_DATABASE_URL='postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable' go test -race ./...
```

The server tests use the real HTTP transport, the generated client, a
real pinned TLS connection to a fake RP, and a disposable database.

Live tests (real RP; they need `MAGI_TEST_LINKKEYS=1` and
`MAGI_LINKKEYS_RUNTIME`):

- `go test -run TestLiveRP ./linkkeys/` calls `Rp/sign-request` and
  `Rp/resolve-domain-keys`.
- `go test -run TestLiveLogin -v ./server/` also needs
  `MAGI_TEST_DATABASE_URL`. It serves on `127.0.0.1:18471`, writes the
  start URL to `MAGI_LIVE_START_FILE`, and waits 9 minutes for a login.
  LinkKeys accepts only an https callback on the RP domain, so the browser
  must send requests for `https://magi.<rp domain>` to the local address.
  With Playwright, use a context route that calls
  `route.fetch({url: 'http://127.0.0.1:18471' + path})` and
  `route.fulfill`. Then open the start URL, click Continue, and sign in.

## Known limits

- Subjects from before 2026-10-28 used the handle. Records owned by those
  subjects are not moved to the new subjects.
- `refresh` uses the newest active session of the subject. There is no
  sign-out or session revocation operation yet.
- The rate limit of `begin-login` is per process (burst 10, then 1 each
  second).
- A person who opens a sign-in link that another person started, and then
  signs in, gives that person a token (the device-flow phishing risk). A
  loopback redirect for command-line clients (RFC 8252) is the fix.
- Each login makes five new TLS connections to the RP server.
