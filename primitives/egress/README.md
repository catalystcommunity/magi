# egress

The egress proxy is the only route out for LLM tooling. It is written in
Rust. Inspection and policy are WebAssembly (WASM) plugins. A plugin can
ask Magi for information or for a decision. The design is in
`docs/egress-proxy.md`.

## What it does

For each request and each response, in this order:

1. **Destination.** The destination (host and port) must be in the policy,
   or a decided review in Magi must permit it (`EgressAskService/destination`,
   only on `review_ports`; the proxy keeps the answer for one minute). The
   decision says inspect or layer 4 (`allow-layer4`). Else
   the request gets 403 with `x-magi-egress: review`, and the proxy reports
   the destination to Magi (`report`, kind `destination`), one time for each
   environment and destination in one minute. When Magi does not answer,
   the destination is not permitted.
2. **Protocol.** In a CONNECT tunnel, the first bytes of the client give
   the protocol, on any port (see "Protocols"). TLS: the proxy ends TLS
   with the installation CA, with a certificate for each host. HTTP: the
   proxy reads the requests. The `Host` header must be the CONNECT host.
3. **Decode.** It removes `gzip` and `deflate`. Another content encoding
   stops the request. It asks the upstream for `identity`.
4. **Secret scan.** It removes each registered secret from the URL, the
   headers, and the body: the raw value, base64, base64url, hex, and percent
   encoding. It decodes each base64 or hex run that holds text (or gzip) and
   scans the text again. A run that holds a secret becomes
   `[redacted:<id>]`. A secret that it finds only after percent decoding
   stops the request.
5. **Decode depth.** A request with more nested encodings than
   `decode_depth` (default 2) stops. Responses have no depth limit.
6. **Plugins**, in the order of the grants. A plugin returns actions:
   `pass`, `block`, `review`, `set-body`, `set-header`, `use-secret`.
7. **Review.** A `review` action stops the flow at once: 403 with
   `x-magi-egress: review`. The proxy does not keep the flow. It sends the
   flow after the scan (placeholders, not secrets; at most 8 KiB of body)
   to Magi (`report`, kind `request`). An admin reviews it later in Magi,
   and the decision changes the policy for the next requests.
8. **Secrets.** For each `use-secret` action, the proxy puts the value of
   the secret in place of the placeholder. The placeholder must have the
   form `magi-secret-<name>`, and the policy must permit the secret for the
   host. The plugin never sees the value.
9. **Placeholders.** A placeholder that is still in the request stops it.
10. **Send and record.** The record has no body.

Steps 1 to 5, 8, and 9 are in the host. A plugin cannot turn them off.

A plugin or a step that fails stops the flow (fail closed). The client gets
403 (or 502 for an upstream failure), the header `x-magi-egress` with the
outcome, and a text body with the reason.

## Clients

A client uses the proxy as an HTTP proxy: `HTTPS_PROXY=http://ENV:CRED@HOST:3128`.
`ENV` is the environment id. `CRED` is the hex HMAC-SHA256 of
`"magi-egress-env:" + ENV` with the environment key. The client must trust
the CA certificate. The execution service sets all of this for network
`proxy` (`primitives/execution/README.md`). A runner sends the
connections of its sandboxes through `GET /runner/egress` on Magi
(`magi/README.md`, Egress).

The proxy asks Magi for the owner of each environment
(`EgressAskService/environment`) and keeps the answer for 5 minutes. A
Magi service is the proxy user `svc_<name>`; Magi answers its owner as
`service:<name>`. Magi sends its model calls through the proxy this way
(`MODEL_EGRESS`, `deploy/README.md`).

The host certificates of the proxy have the authority key identifier of
the CA. Strict verifiers need it (Python 3.13 and later: "Missing
Authority Key Identifier").

## Plugins

The interface is `contracts/egress.wit` (world `filter`). A plugin is a
WASM component. Build the default plugins:

```sh
cd plugins && cargo build --release --target wasm32-wasip2
```

| Plugin | Stages | Magi operations | What it does |
| --- | --- | --- | --- |
| `placeholders` | out | `secret-rules` | Gets the placeholder rules of the owner from Magi. Returns `use-secret` for each placeholder whose rule matches the host and the path. |
| `magi-review` | out | `evaluate` | Magi decides: pass, review, or block. When Magi cannot answer, the flow stops for review. |
| `injection-detector` | in | `detect` | Sends the text of a successful text response (HTML without tags but with comments and some attributes, plain text, JSON, XML; at most 32 KiB in chunks of 4,000 characters) to Magi's prompt injection detector. Magi decides: in the default mode it answers pass at once and checks later; in the mode "review" a finding is a review. When Magi cannot answer, the response passes (the detector is triage, not a gate). |
| `test-actions` | out, in | (test) | Only for the tests. The header `x-test-plugin` selects actions. |

Plugin limits:

- A new instance and store for each flow.
- An empty WASI context: no files, no network, no environment variables,
  and no output. Clocks and random numbers are available.
- `fuel` (default 2e9), `memory_mb` (default 64), and `timeout_ms`
  (default 30000) for each call.
- The host function `magi(op, request)` calls only the operations in
  `magi_ops` of the grant. The request must be a CBOR map. The host puts
  the keys `plugin` and `flow` in it (in place of any keys with those
  names), and sends it to `MAGI_URL/rpc/EgressAskService/<op>` with the
  proxy token. The flow body that goes to Magi is at most 8 KiB.
- The proxy loads a plugin only when the SHA-256 of the file is the
  `sha256` of its grant.

## Protocols

| What the client sends first | Inspected destination | Layer 4 destination |
| --- | --- | --- |
| TLS | TLS ends here; the requests go through all steps | Bytes pass |
| HTTP | The requests go through all steps | Bytes pass |
| SSH | The SSH proxy, when the host is in `ssh_hosts` (below); else stopped and reported | Bytes pass |
| Other, or nothing in 5 seconds (the server speaks first) | Stopped; the destination goes to review (kind `destination`, an admin can decide `allow-layer4`) | Bytes pass |

A layer 4 destination is for a protocol that the proxy must not end, for
example linkkeys: its client pins the server key to DNS, so a proxy
certificate would break it. The proxy records the flow, and does not see
the bytes.

### DNS

Task VMs give each name a fake address, and send the other DNS queries
(TXT, SRV, and so on) here: a CONNECT to `magi-dns.internal:53`, then DNS
over TCP. The proxy answers a query only for a permitted name: a
destination or passthrough host (any port), a destination on port 53, or a
decided review for `name:53`. Leading labels that start with `_` do not
count, so `_linkkeys.example.org` is a query for `example.org`. Other
queries get REFUSED, and the name goes to review as `name:53`. The proxy
asks the resolver in `MAGI_EGRESS_DNS` (`host:port`), or the first
nameserver in `/etc/resolv.conf`.

### SSH

With an SSH host key (`MAGI_EGRESS_SSH_HOST_KEY`), the proxy ends SSH to
the hosts in `ssh_hosts`:

```json
"ssh_hosts": [{"host": "github.com", "host_keys": ["ssh-ed25519 AAAAC3Nza…"], "secret": "gh-ssh", "user": "git"}]
```

- The environment trusts the proxy host key: task VMs and bubblewrap
  sandboxes get `/etc/ssh/ssh_known_hosts` with `* <key>`, and cluster
  environments get `GIT_SSH_COMMAND` with a known_hosts file. Any user name and no key are
  accepted: the proxy credential of the tunnel tells who it is.
- The proxy connects to the host itself. It checks the host key against
  `host_keys`, and signs in as `user` with the OpenSSH private key in the
  secret. The environment never has the key.
- Only `exec` runs. A shell, a terminal, a subsystem, and environment
  variables are refused.
- Each command is a flow (method `SSH`, URL `ssh://host/<command>`) that
  goes through the secret scan and the plugins. Thus Magi's rules and
  decided reviews permit or block commands. A refused command gets the
  reason on stderr and exit status 1. The bytes of a command (for example a
  Git pack) are not inspected.
- An `ssh_hosts` entry permits its host and port (inspected). `port`
  defaults to 22, `user` to `git`. `environments` limits it as for
  destinations.

## Policy

`MAGI_EGRESS_POLICY` is a JSON file. It has no secret values.

```json
{
  "destinations": [
    {"host": "api.example.local"},
    {"host": "*.pypi.example.local", "ports": [443]},
    {"host": "git.example.local", "environments": ["env_*"]}
  ],
  "passthrough": [],
  "secrets": [{"id": "gh-token", "hosts": ["api.example.local"]}],
  "decode_depth": 2,
  "decode_exceptions": [{"hosts": ["api.example.local"], "depth": 3}],
  "stream_hosts": ["files.example.local"],
  "max_body": 16777216,
  "review_ports": [],
  "ssh_hosts": [],
  "plugins": [
    {"name": "placeholders", "file": "/opt/magi/plugins/placeholders.wasm", "sha256": "…", "stages": ["out"], "magi_ops": ["secret-rules"]},
    {"name": "magi-review", "file": "/opt/magi/plugins/magi_review.wasm", "sha256": "…", "stages": ["out"], "hosts": ["*"], "magi_ops": ["evaluate"]}
  ]
}
```

- `host` patterns: `api.example.local`, `*.example.local` (each name below
  it, not the name itself), or `*`.
- Empty `ports` permits 80 and 443.
- `passthrough` destinations are layer 4 only: the proxy copies the bytes
  and does not inspect them. Use them only for protocols that are not HTTP.
- `review_ports` (default empty, which means all ports): the ports where a
  decided review in Magi can permit a destination that is not in the
  policy.
- `stream_hosts`: a response larger than `max_body` from these hosts goes
  to the client without inspection. From other hosts it stops.

`MAGI_EGRESS_SECRETS_DIR` has one file for each secret. The file name is
the secret id. A final newline is not part of the value. Names that start
with `.` are skipped. A secret shorter than 8 bytes is not loaded.

## API

The contract is `contracts/egress.csil`. The transport is the same as
`lib/rpc`: `POST /rpc/EgressService/{op}` with a CBOR body. `GET /healthz`
returns `ok`.

| Operation | Scope | Result |
| --- | --- | --- |
| `list-flows` | `egress:read` | The last 5000 flow records, newest first. |

`egress:admin` permits all operations.

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `--listen` | `MAGI_LISTEN` | `:8080` (API) |
| `--proxy-listen` | `MAGI_EGRESS_PROXY_LISTEN` | `:3128` |
| `--auth-keys` | `MAGI_AUTH_KEYS` | none: the API rejects every call |
| `--policy` | `MAGI_EGRESS_POLICY` | required |
| `--ca-cert`, `--ca-key` | `MAGI_EGRESS_CA_CERT`, `MAGI_EGRESS_CA_KEY` | required |
| `--env-key` | `MAGI_EGRESS_ENV_KEY` | required (at least 32 bytes) |
| `--secrets-dir` | `MAGI_EGRESS_SECRETS_DIR` | none |
| `--upstream-ca` | `MAGI_EGRESS_UPSTREAM_CA` | none: the public web roots only |
| `--magi-url` | `MAGI_URL` | none: plugins cannot call Magi, and flows have no owner |
| `--magi-token` | `MAGI_EGRESS_TOKEN_FILE` | with `MAGI_URL` |
| `--ssh-host-key` | `MAGI_EGRESS_SSH_HOST_KEY` | none: SSH is not ended, and stops |
| | `MAGI_EGRESS_DNS` | the first nameserver in `/etc/resolv.conf` |

Commands that make the files (each file has mode 600, and an existing
file is not replaced):

```sh
magi-egress ca --out DIR [--name NAME]
magi-egress key --out FILE
magi-egress ssh-key --out FILE           # prints the public key line
magi-egress credential --env-key FILE --env ENV --out FILE
```

## Tests

```sh
cargo test
```

The tests build the plugins (they need the `wasm32-wasip2` target). The
end-to-end tests (`tests/proxy.rs`) start a TLS upstream, a fake Magi, and
the proxy, and send requests with `curl`. They cover credentials,
destinations, secret removal in both directions, placeholders, the decode
depth, each plugin action, the fuel and memory limits, calls to Magi,
reviews (the report to Magi, and the destination check with its cache and
the one-report limit), protocol detection, layer 4 destinations, DNS, the
SSH proxy (with a russh host and client), and the flow records through the
API.

## Known limits

- HTTP/1.1 only. HTTP/2, WebSocket, and other upgrades stop.
- Flow records are in memory. A restart loses them. Reviews are in Magi.
- A secret in a form that the scan does not know (for example encrypted, or
  compressed with a method other than gzip) is not found. The plugins and
  the Magi review are the next layers.
- The proxy resolves host names itself. It does not check the addresses
  that a permitted name resolves to.
- Runner sandboxes on macOS (Seatbelt) do not use the proxy: Seatbelt
  cannot isolate the network.
