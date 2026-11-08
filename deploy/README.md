# Deployment

This guide installs Magi in one Kubernetes namespace. Execution environments
run in a second namespace. Model and speech engines can run on other hosts.

The repository holds no private values. Keep them in a values file and in
credential files outside the repository.

## Parts

| Part | Where | Notes |
| --- | --- | --- |
| Primitives and `magi` | namespace `magi` | One Deployment and Service each |
| Apprentice worker | namespace `magi` | Claims `apprentice` workflow steps |
| Environments | namespace `magi-envs` | Pods that the execution service creates |
| PostgreSQL | namespace `magi` | CloudNativePG cluster `magi-db`, one database per primitive (`base/postgres.yaml`) |
| Corndogs | namespace `magi` | Task state for the workflow service; only workflow can connect |
| Image registry | namespace `magi` | `registry/registry.yaml`; Magi's own credentials |
| Model | any host | OpenAI-compatible Chat Completions endpoint |
| Speech engines | any host | `primitives/speech/engine/install.sh --provision HOST` |
| Egress proxy | namespace `magi` | Optional (`EGRESS=true`); the only route out for environments with network `proxy` and for Linux runner sandboxes |

## Image registry

The registry uses its own generated credential. Nodes pull from
`localhost:30500`, because kube-proxy accepts localhost NodePorts and
containerd uses plain HTTP for localhost. This needs kube-proxy in iptables
mode with localhost NodePorts (the default).

1. Make a credential file `registry.json` with `username` and `password`.
2. Make an htpasswd file with bcrypt (`htpasswd -nBi magi`).
3. Create Secret `registry-htpasswd` in `magi`. Then render and apply the
   registry with the PostgreSQL cluster ("Install or update").
4. Create Secret `magi-registry` (type `kubernetes.io/dockerconfigjson`, server
   `localhost:30500`) in each namespace that pulls Magi images.
5. Run `tools/registry-forward.sh` to push through a port-forward.

## Credentials

Make the issuer key and the service tokens with the identity command:

```sh
identity keygen --issuer magi --out issuer.json --public issuer-public.json
identity mint --key issuer.json --subject service:magi --ttl 2160h --out token-magi \
  --scopes conversation:act-as,conversation:admin,knowledge:act-as,artifacts:act-as,workflow:act-as,workflow:worker,execution:act-as,source:act-as,release:operate,release:read,speech:lexicon
identity mint --key issuer.json --subject service:apprentice --ttl 2160h --out token-apprentice \
  --scopes workflow:worker,execution:act-as,artifacts:act-as
```

Create these Secrets in namespace `magi`:

| Secret | Key | Content |
| --- | --- | --- |
| `magi-auth-public` | `issuer.json` | Public issuer key; every service mounts it |
| `magi-issuer` | `issuer.json` | Private issuer key; only identity mounts it |
| `magi-token-magi` | `token` | Service token of `magi` |
| `magi-token-apprentice` | `token` | Service token of the apprentice worker |
| `magi-model` | `api-key` | Model API key |
| `magi-speech-engine` | `token` | Speech engine token |
| `magi-inference-token` | `token` | With `PERSONA_LOCAL_URL`: a token with the scope `inference:use`, signed by an issuer key in the `auth_keys` of the inference host |
| `magi-linkkeys` | `runtime.json` | LinkKeys relying-party runtime file (see `primitives/identity/README.md`) |

## Values file

```sh
NAMESPACE=magi
ENV_NAMESPACE=magi-envs
PUBLIC_HOST=magi.example.local
PUBLIC_ROUTE=true
GATEWAY_NAME=contour
GATEWAY_NAMESPACE=projectcontour
GATEWAY_SECTION=http
MODEL_BASE_URL=http://model.example.local:8091/v1
MODEL_NAME=magi-coder
PERSONA_LOCAL_URL=            # optional: an inference host (primitives/inference) that the persona tries first; Secret magi-inference-token
PERSONA_LOCAL_MODEL=          # optional: the model name on that host (default "local")
STORAGE_CLASS=                # optional: the storage class of the volumes; empty: the default class of the cluster
SPEECH_STT_URL=http://speech.example.local:8092
SPEECH_TTS_URL=http://speech.example.local:8093
SPEECH_TURN_URL=              # optional: speech detection and turn classifier engine (speech engine host, port 8095)
ALLOWED_DOMAINS=example.local
ENV_IMAGE=localhost:30500/magi/dev-env:TAG
RUNNER=                       # optional: default runner for code changes
PUBLISHER=                    # optional: runner that pushes merged changes upstream
ADMINS=                       # optional: user subjects for operator calls, comma-separated
LEXICON_EDITORS=              # optional: user subjects that can change the household lexicon; empty: ADMINS
EGRESS=                       # optional: true adds the egress proxy (see "Egress proxy")
EGRESS_RULES=                 # optional: true mounts ConfigMap magi-egress-rules into magi
EGRESS_SSH=                   # optional: true lets the proxy end SSH (Secret magi-egress-ssh-key)
MODEL_EGRESS=                 # optional, with EGRESS: true sends the model calls of magi and apprentice through the proxy
DETECTOR_URL=                 # optional: the prompt injection detector engine (speech engine host, port 8094; Secret magi-speech-engine)
DETECTOR_MODE=                # optional: flag (default; pass now, tell the person later) or review (block a finding for an admin)
DETECTOR_THRESHOLD=           # optional: 0.98
```

A database that `postgres.yaml` adds later is not made in an existing
cluster (`postInitSQL` runs one time). Make it by hand, for example:
`kubectl -n magi exec magi-db-1 -- psql -c 'CREATE DATABASE speech OWNER magi'`.

## Egress proxy

The egress proxy (`primitives/egress`) is optional. With `EGRESS=true`, an
environment with network `proxy` can connect only to the proxy, and the
proxy is its only route out. Read `docs/egress-proxy.md` first.

1. Make the CA and the environment key with the proxy command. Keep the
   files outside the repository:

   ```sh
   magi-egress ca --out DIR          # DIR/ca.crt, DIR/ca.key (mode 600)
   magi-egress key --out DIR/env.key # mode 600
   identity mint --key issuer.json --subject service:egress --ttl 2160h --out token-egress --scopes magi:egress
   ```

2. Add `egress:read` to the scopes of the `service:magi` token (see
   "Credentials"), and mint it again. Magi keeps egress reviews in the
   artifacts service, so the token needs `artifacts:act-as` too.
3. Create these objects in namespace `magi`:

   | Object | Key | Content |
   | --- | --- | --- |
   | Secret `magi-egress-ca-key` | `ca.key` | CA private key; only the proxy mounts it |
   | ConfigMap `magi-egress-ca` | `ca.crt` | CA certificate; the proxy, execution, and Magi mount it |
   | Secret `magi-egress-env-key` | `key` | Environment key; the proxy, execution, and Magi mount it |
   | Secret `magi-token-egress` | `token` | Token of `service:egress` |
   | Secret `magi-egress-secrets` | one key for each secret | The secret values. The key is the secret id. It can be empty. |
   | ConfigMap `magi-egress-policy` | `policy.json` | Destinations, secret hosts, and plugin grants (`primitives/egress/README.md`) |
   | ConfigMap `magi-egress-rules` | `rules.json` | Optional (`EGRESS_RULES=true`): placeholder rules and review rules for Magi (`magi/README.md`) |
   | Secret `magi-egress-ssh-key` | `key` | Optional (`EGRESS_SSH=true`): the SSH host key of the proxy (`magi-egress ssh-key`) |
   | ConfigMap `magi-egress-ca` | `ssh.pub` | With `EGRESS_SSH=true`: the public key line that `magi-egress ssh-key` prints; Magi gives it to runner sandboxes, and execution gives it to Pods |
   | Secret `magi-egress-apprentice` | `url` | With `MODEL_EGRESS=true`: `http://svc_apprentice:CRED@egress.NAMESPACE.svc:3128`, where CRED is the hex HMAC-SHA256 of `magi-egress-env:svc_apprentice` with the environment key |

   With `MODEL_EGRESS=true`, Magi and the apprentice do not mount
   `magi-model`. They send the placeholder `magi-secret-model` as the key,
   and the proxy puts the key in its place:
   - `magi-egress-secrets` has the key `model` (the provider key). Copy it
     in the cluster, so that it is not written out:
     `m=$(kubectl -n magi get secret magi-model -o jsonpath='{.data.api-key}'); kubectl -n magi patch secret magi-egress-secrets --type merge -p "{\"data\":{\"model\":\"$m\"}}"`.
   - The policy has the secret `{"id": "model", "hosts": ["<provider host>"]}`
     and the destinations of the provider and of the inference host.
   - `rules.json` has the placeholder rule `{"placeholder":
     "magi-secret-model", "secret": "model", "hosts": ["<provider host>"],
     "place": "header:authorization", "owners": ["service:magi",
     "service:apprentice"]}`.
4. Get the plugin digests for the grants in the policy. The image has no
   shell, so copy the file out:
   `docker create --name e IMAGE && docker cp e:/opt/magi/plugins/SHA256SUMS - | tar -xO; docker rm e`.
5. Render and apply. Code-change environments in the cluster then use
   `network: "proxy"`. Runner sandboxes (bubblewrap) send their traffic
   through `GET /runner/egress` on Magi; the route has no request timeout.
   Put the hosts that the checks need (module proxies, crate registries)
   in the policy destinations.

## Web search

`SEARCH=true` (with `EGRESS=true`) adds the SearXNG service `search`. Only
the Magi pod can reach it (NetworkPolicy). `SEARCH_IMAGE` is the image, for
example a copy of `docker.io/searxng/searxng:<tag>@sha256:<digest>` in the
Magi registry.

1. Write the settings and make the Secret. The file has the proxy
   credential of `svc_searxng`; the script does not print it:

   ```sh
   deploy/search-settings.py --namespace magi --env-key <egress env.key> --out settings.yml
   kubectl -n magi create secret generic magi-search-settings --from-file=settings.yml
   ```

2. Add to the egress policy the engine hosts for `svc_searxng`
   (`deploy/search-settings.py --hosts --namespace x --env-key x --out x`
   prints them) and `{"host": "*", "environments": ["svc_search"]}` for
   the pages that the personas read. Magi refuses private addresses for
   `svc_search` (`magi/compose/search.go`).
3. Restart the egress proxy, so that it reads the policy.

## Install or update

```sh
tools/build-web.sh                       # when the web client changed (builds tsgo with Go; no Node)
tools/build-images.sh                    # builds and pushes; writes .build/images.env
python3 deploy/render.py --base --values VALUES.env | kubectl apply -f -   # PostgreSQL and the registry
kubectl apply -f primitives/execution/deploy/rbac.yaml
kubectl apply -f primitives/release/deploy/rbac.yaml
python3 deploy/render.py --images .build/images.env --values VALUES.env > out.json
kubectl apply -f out.json
```

Use `--only name,name` to render a subset. The public route sends `/login/`
and `/rpc/IdentityService/` to identity and all other paths to `magi`.
Every API call except login needs a token.

## Model use

Each persona reply records the model that wrote it (`generated_by`). With
a local inference host first in `PERSONA_LOCAL_URL`, this shows how often
the local model answers and how often the remote provider answers. The
command uses the API with your sign-in, from any machine. It counts only
your own conversation:

```sh
magi usage -days 7    # your replies by model in the last 7 days
```

The `persona turn` log line of `magi` also gives the time and the endpoints
that were skipped, with the reason.

With `PERSONA_LOCAL_URL`, the librarian (`MAGI_LIBRARIAN_MODELS`) uses only
the local host. When the host is busy, utterances stay private and not
classified until it is free.

## Source repository

Code changes use the repository named by `MAGI_REPO` (default `magi`) in the
source service. Create it once from a Git bundle with `create-repo`.

## Restarts

A restart or release of a service without a volume drops no request:

- The new pod starts and becomes ready before the old pod stops
  (`maxUnavailable: 0`, `maxSurge: 1`).
- On SIGTERM, `lib/rpc` drains. Responses close their connections, so
  clients connect to the new pod. The server accepts requests for 5 s, then
  waits up to 35 s for open requests, also long polls (25 s maximum).
- `terminationGracePeriodSeconds` is 50, longer than the drain.

A service with a volume (`artifacts`, `source`, `release`, `corndogs`) uses
`Recreate`, because its volume is ReadWriteOnce. Calls to it fail while its
pod restarts.

## Release order and checks

Apply a service before the services that call it, also by hand. A client
that is built from a newer contract can need a field that an older server
does not send ("csil cbor: missing field").

Each release of `magi` through the release controller does these checks:

1. Preflight: the new image runs `magi-server preflight` in one pod, with
   the pod spec of the Deployment. It calls one read operation of each
   service that Magi uses. When a call fails, the release stops before it
   changes the Deployment.
2. Stabilization: each new pod must answer `GET /healthz/deps` (the same
   calls) with 200. Else the controller rolls back.

The readiness probe stays `/healthz`. A release with more items applies
them in the order of the list, and runs the preflight of each item after
the items before it are applied: list a server before its clients.

## Limits

- One replica of each service. The source service and the release controller
  must stay at one replica.
- All primitives use one PostgreSQL owner role, in separate databases.
- Magi releases only its own `magi` Deployment. A merged change to a
  primitive is not released; build and apply that image as above.
- Without a runner, the hosted `magi` repository does not follow GitHub.
  Update it with `tools/seed-repo.py --update` after you push, or a release
  rebuilds old code. With a runner (`RUNNER` in the values file, see
  `magi/README.md`), tasks start from the upstream and publish to it.
