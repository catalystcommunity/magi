# Deployment

This guide installs Magi in one Kubernetes namespace. Execution environments
run in a second namespace. Model and speech engines can run on other hosts.

The repository holds no private values. Keep them in a values file and in
credential files outside the repository.

## Parts

| Part | Where | Notes |
| --- | --- | --- |
| Primitives and `magi` | namespace `magi` | One Deployment and Service each |
| Agent worker | namespace `magi` | Claims `agent` workflow steps |
| Environments | namespace `magi-envs` | Pods that the execution service creates |
| PostgreSQL | namespace `magi` | CloudNativePG cluster `magi-db`, one database per primitive (`base/postgres.yaml`) |
| Corndogs | namespace `magi` | Task state for the workflow service; only workflow can connect |
| Image registry | namespace `magi` | `registry/registry.yaml`; Magi's own credentials |
| Model | any host | OpenAI-compatible Chat Completions endpoint |
| Speech engines | any host | `primitives/speech/engine/install.sh HOST` |

## Image registry

The registry uses its own generated credential. Nodes pull from
`localhost:30500`, because kube-proxy accepts localhost NodePorts and
containerd uses plain HTTP for localhost. This needs kube-proxy in iptables
mode with localhost NodePorts (the default).

1. Make a credential file `registry.json` with `username` and `password`.
2. Make an htpasswd file with bcrypt (`htpasswd -nBi magi`).
3. Create Secret `registry-htpasswd` in `magi`, then apply `registry/registry.yaml`.
4. Create Secret `magi-registry` (type `kubernetes.io/dockerconfigjson`, server
   `localhost:30500`) in each namespace that pulls Magi images.
5. Run `tools/registry-forward.sh` to push through a port-forward.

## Credentials

Make the issuer key and the service tokens with the identity command:

```sh
identity keygen --issuer magi --out issuer.json --public issuer-public.json
identity mint --key issuer.json --subject service:magi --ttl 2160h --out token-magi \
  --scopes conversation:act-as,knowledge:act-as,artifacts:act-as,workflow:act-as,workflow:worker,execution:act-as,source:act-as,release:operate,release:read
identity mint --key issuer.json --subject service:agent --ttl 2160h --out token-agent \
  --scopes workflow:worker,execution:act-as,artifacts:act-as
```

Create these Secrets in namespace `magi`:

| Secret | Key | Content |
| --- | --- | --- |
| `magi-auth-public` | `issuer.json` | Public issuer key; every service mounts it |
| `magi-issuer` | `issuer.json` | Private issuer key; only identity mounts it |
| `magi-token-magi` | `token` | Service token of `magi` |
| `magi-token-agent` | `token` | Service token of the agent worker |
| `magi-model` | `api-key` | Model API key |
| `magi-speech-engine` | `token` | Speech engine token |
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
SPEECH_STT_URL=http://speech.example.local:8092
SPEECH_TTS_URL=http://speech.example.local:8093
ALLOWED_DOMAINS=example.local
ENV_IMAGE=localhost:30500/magi/dev-env:TAG
```

## Install or update

```sh
tools/build-web.sh                       # when the web client changed (builds tsgo with Go; no Node)
tools/build-images.sh                    # builds and pushes; writes .build/images.env
kubectl apply -f deploy/base/postgres.yaml
kubectl apply -f primitives/execution/deploy/rbac.yaml
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
your chats:

```sh
magi usage -days 7    # your replies by model in the last 7 days
```

The `persona turn` log line of `magi` also gives the time and the endpoints
that were skipped, with the reason.

## Source repository

Code changes use the repository named by `MAGI_REPO` (default `magi`) in the
source service. Create it once from a Git bundle with `create-repo`.

## Limits

- One replica of each service. The source service and the release controller
  must stay at one replica.
- All primitives use one PostgreSQL owner role, in separate databases.
- The release controller is not yet deployed by `render.py`; see
  `primitives/release/deploy/`.
