# Egress proxy: design

Status: built (2026-11-01). The proxy is `primitives/egress`, the plugin
interface is `contracts/egress.wit`, and the Magi side is
`EgressAskService` in `contracts/magi.csil`. The requirements come from
`docs/prompt-injection.md` section 7. Section 6 lists what is not done.

## 1. Purpose

All network traffic of LLM tooling goes through one Magi egress proxy:
calls to model providers, tool calls, apprentice environments, runner
sandboxes, and web fetch. The proxy:

- lets traffic go only to permitted destinations (layer 4 and layer 7);
- changes secret placeholders to real secrets, only for bound
  destinations;
- removes real secrets from all traffic, in both directions;
- stops traffic that is encoded more than a fixed depth, or that it cannot
  decode;
- blocks a request for review when a rule, a plugin, or the review model
  asks for a person; an admin decides later, and the decision is a rule
  for the next such requests;
- records each request and response, with secrets removed.

## 2. Decisions

1. **Rust.** The proxy is a Rust program, as the release controller is.
   mitmproxy (Python) is a reference for features, not the engine.
2. **WASM plugins.** Inspection and policy are plugins in WebAssembly. A
   plugin can be written in Rust (or another language that compiles to a
   WASM component).
3. **Plugins can call Magi.** A plugin calls a host function to ask Magi to
   do something, or to get information that Magi has. Example: a plugin
   that changes secret placeholders by destination domain or regular
   expression gets its rules from Magi.
4. **Placeholders.** An environment has only a placeholder for each secret.
   The real secret is never in the environment.
5. **Easy routing.** The proxy is a setting of each environment kind. It is
   the only route out.

## 3. Plugin model

### 3.1 Runtime

- Wasmtime (Apache-2.0), with the WebAssembly component model. The plugin
  interface is a WIT file in `contracts/`, with a version.
- A plugin gets an empty WASI context: no file system, network, or
  environment, and no output. Clocks and random numbers are available (the
  Rust standard library for `wasm32-wasip2` needs WASI). It can do only
  what the host functions permit. Thus a plugin cannot send data out by
  itself.
- Each flow gets a new instance in a new store. Each call has a fuel
  limit, a time limit, and a memory limit. When a plugin fails or goes over
  a limit, the request stops (fail closed).

### 3.2 Interface

The interface is `contracts/egress.wit`, world `filter`. A plugin exports
`check(flow) -> list<action>`. It imports `magi(op, request)` and `log`.
The actions are `pass`, `block`, `review`, `set-body`, `set-header`, and
`use-secret(placeholder, secret, place)`.

- **Secrets stay in the host.** A plugin decides that a placeholder must
  become secret `X` in the `Authorization` header. It returns
  `use-secret`. The proxy host gets the secret value and puts it in. The
  plugin never gets the value, so a bad plugin cannot leak it.
- **Grants.** Each plugin has a grant in the proxy policy: name, file,
  SHA-256, the stages it sees (out, in), the hosts, the Magi operations
  that it can call, and its limits. The proxy loads a plugin only when the
  digest of the file is the digest of the grant.
- **Calls to Magi** use the proxy's own token (`service:egress`, scope
  `magi:egress`). The host puts the plugin name and the flow in each
  request, so a plugin cannot speak for another plugin or another flow. A
  plugin can only call the operations in its grant.
- **Magi decides.** `EgressAskService/evaluate` uses the rules in
  `MAGI_EGRESS_RULES` first. When no rule matches, the default can be
  `model`: the review model (the local model first) gets the task run and a
  summary of the flow, and must answer with a verdict tool call. Any other
  answer, or an error, is `hold`. `secret-rules` gives the placeholder
  rules for the owner of the environment.

### 3.3 Order of work for one flow

1. Layer 4: is the destination permitted for this environment? If not,
   stop.
2. End TLS with the installation CA (the environments trust it). Decode the
   body: decompress, then decode encodings to the fixed depth. Still
   encoded, or not decodable: stop.
3. Built-in secret scan: a real secret in the flow is removed (out) or
   removed and recorded (in).
4. Plugins, in the configured order. Each returns actions. The scan runs
   again on what the plugins changed.
5. A `review` action blocks the flow at once (403). The proxy reports it
   to Magi (`EgressAskService/report`), and an admin decides later. The
   report has the placeholders, not the secrets.
6. Secrets go in place of placeholders, only for permitted hosts. A
   placeholder that no `use-secret` action took stops the request. One
   exception: in the body of a flow of a Magi service (`svc_*`, the model
   calls of Magi and the apprentice), a placeholder is data, because the
   body is a conversation that can quote tool output (for example the
   output of the proxy tests). A placeholder in the URL or a header of
   such a flow still stops it.
7. Send, and record.

Steps 1, 2, 3, and 6 are in the Rust host, not in plugins, so a missing or
failed plugin cannot turn them off. The review comes before the secrets,
so that a person never sees a secret. Magi ships default plugins (destination
policy, secret placeholders by domain or regular expression, the detector
of section 3.4 of the prompt injection document) written in Rust.

## 4. Routing

| Environment | How the proxy is the only route |
| --- | --- |
| Cluster task environment | Network `proxy` (execution service): a NetworkPolicy that permits DNS and the proxy port only. The init container `envd-net` (the only container with a capability: `NET_ADMIN`) adds a firewall: DNS goes to envd, which gives fake addresses, and each other TCP connection goes to envd, which sends `CONNECT name:port` to the proxy address. Tools use the proxy port `127.0.0.1:3128` of envd. The Secret of the environment has the proxy URL with its credential, the CA certificate, and the SSH known_hosts of the proxy (`GIT_SSH_COMMAND` uses it). |
| Runner sandbox (bubblewrap) | A network namespace with no real interface. `envd netns` sets it up as in a task VM (fake DNS, firewall, transparent port), and then starts bubblewrap with a user namespace of its own, so commands have no capability. envd sends each connection to a unix socket. The runner sends each connection through `GET /runner/egress` on Magi (an HTTP upgrade) to the proxy. Magi gives the proxy user (the run) only to the runner that holds a step of the run. The view has the proxy CA in the system trust store and the SSH known_hosts of the proxy. |
| Runner task VM (Cloud Hypervisor, Linux) | No network card. The guest DNS gives fake addresses (`198.18.0.0/15`) and keeps the names. The guest firewall sends each TCP connection to envd, which sends `CONNECT name:port` over vsock to the runner, and the runner sends it through `GET /runner/egress`. Tools with no proxy setting also go through the proxy. |
| Runner task VM (macOS) | No network card. The proxy port `127.0.0.1:3128`, and the transparent route for all other TCP: routes on `lo0` and a pf rule send each connection to envd, which finds the destination with `DIOCNATLOOK`; the DNS of envd is the resolver for all domains (`scutil`). envd sends it all over vsock to the runner, and the runner through `GET /runner/egress`. SSH to `ssh_hosts` works. |
| Runner sandbox (Seatbelt, macOS) | Not isolated: Seatbelt cannot give a sandbox its own network. |
| Model calls of Magi and the apprentice service | `MODEL_EGRESS=true`: the same proxy, as the proxy users `svc_magi` and `svc_apprentice`. Magi answers their owner as `service:magi` and `service:apprentice`, and passes their flows with no review (a review would call the model again). The provider key is the proxy secret `model`; the services hold only the placeholder `magi-secret-model`. |

The runner connects out to Magi only. The tunnel uses the form of a
WebSocket upgrade, so that HTTP gateways pass it; after the 101 answer the
bytes are the proxy protocol. Code-change environments in the cluster use
network `proxy` when Magi has the proxy (`MAGI_ENV_NETWORK`).

## 4.1 Reviews, not holds

A request does not wait for a person. A person rarely answers in the time
that a tool waits, so a hold only adds a timeout. The proxy blocks the
request at once and reports it. The task gets a 403 with the reason and
can continue in a different way.

| What blocks | Review kind | What the admin decides |
| --- | --- | --- |
| A `review` action (a rule, a plugin, or the review model) | `request` | `allow-host` (this method to this host), `allow-path` (this method, host, and path), `block` (with no new review), or `dismiss` (no rule) |
| A destination that is not in the policy | `destination` | `allow-host` (permit this host and port), `block`, or `dismiss` |

- Magi keeps one review for each kind, host, method, and path, with a
  count. It records at most one report for each of them each minute.
- A decision is a rule for all environments of the installation. Decided
  rules come before the rules file. The proxy asks Magi about a
  destination that its policy does not permit (`EgressAskService/
  destination`) and keeps the answer for one minute.
- A decided review can take a new decision, which replaces its rule;
  `dismiss` removes the rule.
- Only admins see and decide reviews: `magi egress-reviews`, `magi
  egress-review`, the web tab Network, and the Android screen Network.
- The store is append-only (artifacts with the label `magi.kind`
  `egress-report` or `egress-decision`), so each decision has a record of
  who made it and when.

## 5. Decisions on the open questions

- **One proxy for the installation** first. The same program can run on a
  runner machine later; it then sends reports and records to Magi.
- **hyper with rustls**, not Pingora. A forward proxy that ends TLS needs
  CONNECT, certificates for each host, and control of each request. Pingora
  is made mainly for reverse proxies.
- **Protocols by their first bytes**, not by port. TLS and HTTP are
  inspected. SSH ends in the proxy (the proxy holds the keys). Other
  protocols stop unless the destination is permitted at layer 4 (a
  `passthrough` destination, or the review decision `allow-layer4`).
  linkkeys is layer 4 only: it pins its keys to DNS, and Magi trusts it.
  HTTP/2 and WebSocket stop.
- **Decode depth 2.** One or two nested encodings pass; three stop. A
  `decode_exceptions` entry gives a host a larger depth (for example a
  files API that sends base64). Only base64 or hex runs that decode to text
  (or gzip) count, so identifiers and binary data are not layers.
  Responses have no depth limit.

## 6. Not done yet

- macOS guests connect to Apple services by themselves. The transparent
  route sends these to the proxy, which blocks them and opens destination
  reviews. Block the hosts once, or add them to the policy.
- macOS runner sandboxes (Seatbelt) do not use the proxy. macOS task VMs
  do.
- Cluster environments get the proxy CA in the CA variables only, not in
  the system trust store of the image.
- A plugin that finds prompt injection in responses (section 3.4 of the
  prompt injection document).
- Flow records are in memory. Reviews and decisions are in the artifacts
  service.
