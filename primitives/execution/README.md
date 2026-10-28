# execution

The execution primitive makes isolated environments. An environment runs
commands and holds files. Agents and checks use environments to work on
source code.

## How it works

Each environment runs **envd**, a small static Go program (`cmd/envd`,
`CGO_ENABLED=0`). envd serves an HTTP JSON API on port 7070 inside the
environment:

| Path | Function |
| --- | --- |
| `GET /healthz` | Health. No token. |
| `POST /v1/ping` | Checks the token. |
| `POST /v1/exec` | Runs argv with cwd, env, stdin, timeout, and an output limit. On timeout, envd kills the process group. |
| `POST /v1/read-file`, `/v1/write-file`, `/v1/list-files` | File operations. Relative paths start at the work directory. |

Every call except `/healthz` needs `Authorization: Bearer <token>`. The
token is random and different for each environment. envd compares it in
constant time. envd reads it from `MAGI_ENVD_TOKEN_FILE` (or
`MAGI_ENVD_TOKEN`). The request and response bodies use the JSON form of
the contract types.

The control plane (`cmd/execution`) keeps environment records in
PostgreSQL. It starts environments through a **backend** and sends each
operation to envd. A backend has three functions: create, status, and
destroy. The status gives the envd address.

envd does not depend on a container. A VM backend starts the same envd from
the VM init system. A host backend starts it as a host service. Only the
start and the connection change. Callers and the API do not change.

### Backends

- `kubernetes`: one Pod for each environment in `MAGI_EXEC_NAMESPACE`.
  The Pod uses the requested image. An init container (`MAGI_ENVD_FETCH_IMAGE`,
  default `busybox:1.37`) puts envd into an `emptyDir`. Then the main
  container runs envd as its command. Thus no envd image is necessary.
  - Delivery `fetch` (default in a cluster): the init container downloads
    `MAGI_EXEC_PUBLIC_URL/envd/linux-<arch>` from this service and checks the
    SHA-256 that this service gives it.
  - Delivery `push` (default outside a cluster): the init container waits.
    This service writes envd into it through the Kubernetes exec API.
  - Connection `direct` (default in a cluster): Pod IP, port 7070.
  - Connection `port-forward` (default outside a cluster): Kubernetes
    port-forward.
  - The token is in a Secret, mounted as a file. `spec.env` values are in
    the same Secret and go to the container through `secretKeyRef`.
  - Requests and limits come from the spec: CPU default 1000m, memory
    default 2048Mi, ephemeral storage from `disk_mb`. All capabilities are
    dropped. Privilege escalation is off. The service account token is not
    mounted. Labels: `app.kubernetes.io/part-of=magi`, `magi.environment=<id>`.
  - `spec.host` selects a node (`kubernetes.io/hostname`).
  - Network `none` adds a NetworkPolicy that denies egress. With fetch
    delivery, it permits DNS and TCP to `MAGI_EXEC_CONTROL_NAMESPACE` on the
    public URL port, because the init container must download envd.
    `cluster` and `egress` add no policy.
- `local`: envd runs as a child process on the service host, in a directory
  below `MAGI_EXEC_LOCAL_ROOT`. It gives no isolation and ignores the image.
  It is for tests. It is the model for a future `host` backend.

## API

`contracts/execution.csil`, service `ExecutionService`:
`create` (optional `wait_ready_ms`), `get`, `list`, `destroy`, `hold`,
`extend`, `exec`, `read-file`, `write-file`, `list-files`.

- Status: `creating`, `ready`, `failed`, `destroyed`.
- The owner is the caller. `execution:act-as` lets a caller set `owner`, use
  other owners' environments, and list them with `list.owner`.
  `execution:admin` permits all operations. `list` without `owner` gives the
  caller's environments (all of them for an admin). `list` without `status`
  does not show destroyed environments.
- The service does not store or return `spec.env` values. A response shows
  only the names, with empty values.
- The reaper destroys environments when their TTL ends, unless they are
  held. Default TTL 1 hour, maximum 7 days.
- Empty paths and paths with NUL are rejected.
- Limits: exec timeout default 2 minutes, maximum 2 hours. Output default
  1 MiB for each stream, maximum 8 MiB. Read default 1 MiB, maximum 8 MiB.
  Write maximum 12 MiB.

`GET /envd/linux-amd64` and `GET /envd/linux-arm64` serve the envd builds
without a token.

## Configuration

| Variable | Default | Use |
| --- | --- | --- |
| `MAGI_LISTEN` | `:8080` | Listen address |
| `MAGI_AUTH_KEYS` | (required) | Comma-separated issuer key files |
| `MAGI_DATABASE_URL` | (required) | PostgreSQL URL |
| `MAGI_EXEC_BACKEND` | `kubernetes` | Comma-separated backends: `kubernetes`, `local`. The first is the default |
| `MAGI_EXEC_NAMESPACE` | `magi-envs` | Namespace for environments |
| `MAGI_EXEC_CONTROL_NAMESPACE` | `magi` | Namespace of this service (network `none` with fetch) |
| `MAGI_EXEC_PUBLIC_URL` | (required for fetch) | This service as Pods see it, for example `http://execution.magi.svc:8080` |
| `MAGI_EXEC_ENVD_DELIVERY` | `fetch` in a cluster, else `push` | envd delivery |
| `MAGI_EXEC_CONNECT` | `direct` in a cluster, else `port-forward` | envd connection |
| `MAGI_EXEC_ENVD_DIR` | `/usr/local/lib/magi/envd` | Holds `envd-linux-amd64`, `envd-linux-arm64` |
| `MAGI_ENVD_FETCH_IMAGE` | `busybox:1.37` | Init container image. It needs `sh`, `wget`, `sha256sum` |
| `MAGI_EXEC_PULL_SECRET` | `magi-registry` | Image pull secret in the environment namespace. Empty: none |
| `MAGI_EXEC_DEFAULT_IMAGE` | `golang:1.26-bookworm` | Image for a spec without one |
| `MAGI_EXEC_DEFAULT_TTL`, `MAGI_EXEC_MAX_TTL` | `1h`, `168h` | TTL limits |
| `MAGI_EXEC_REAP_INTERVAL` | `15s` | Reaper interval |
| `MAGI_EXEC_LOCAL_ROOT` | `$TMPDIR/magi-envs` | Local backend directory |
| `KUBECONFIG`, `MAGI_EXEC_KUBE_CONTEXT` | in-cluster config, else `~/.kube/config` | Kubernetes access |

If `MAGI_EXEC_ENVD_DIR` has no build for the service platform, the service
serves its own executable. The `execution` program runs as envd when its file
name is `envd`. This fallback needs a static build (`CGO_ENABLED=0`). It is
approximately 50 MB, and it gives no build for other platforms.

## Images

- `Dockerfile` (build from the repository root) has the service and both
  envd builds:
  `docker build -f primitives/execution/Dockerfile .`
- `images/dev/Dockerfile` is the default development environment:
  Go 1.26, Python 3 with venv and pip, ripgrep, git, curl, make, jq. User
  `magi` (uid 1000), home `/home/magi`, work directory `/work`. Push it only
  to the private Magi registry, for example
  `127.0.0.1:55000/magi/dev-env:<tag>`. Nodes pull
  `localhost:30500/magi/dev-env:<tag>`.

## Permissions

`deploy/rbac.yaml` has the ServiceAccount `execution` in namespace `magi`,
and a Role and RoleBinding in `magi-envs` for pods, `pods/exec`,
`pods/portforward`, `pods/status`, `pods/log`, secrets, and
networkpolicies.

## Tests

```sh
cd primitives/execution
go vet ./... && go test ./...
# Integration tests with PostgreSQL and the local backend:
MAGI_TEST_DATABASE_URL='postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
# Kubernetes (context "default", namespace magi-envs, push and fetch delivery):
MAGI_TEST_KUBE=1 MAGI_TEST_KUBE_PULL_SECRET=magi-registry \
MAGI_TEST_DATABASE_URL=... go test -run Kubernetes -timeout 20m ./server/
```

Optional: `MAGI_TEST_KUBE_DEV_IMAGE=localhost:30500/magi/dev-env:<tag>` runs
the development image. `MAGI_TEST_KUBE_ARM64_NODE=<node>` runs the arm64
build on that node. `MAGI_TEST_KUBE_CONTEXT` and `MAGI_TEST_KUBE_NAMESPACE`
change the cluster. The fetch test uses a temporary busybox `httpd` Pod as
the download server, because Pods cannot always connect to the test host.

## Known limits

- Container isolation only. Do not run hostile code without a stronger
  runtime (for example gVisor or Kata) or a VM backend.
- With cgroup v2, an out-of-memory kill stops the whole container, envd
  included. The environment then becomes `failed`. Ask for more memory for
  large builds.
- The main container drops all capabilities. Package installation as root
  (`apt-get`) does not work. Put tools in the image.
- Nodes with taints are not used. There are no tolerations.
- `vm` and `host` backends do not exist yet.
- `cluster` network does not limit egress to the cluster.
- With fetch delivery and network `none`, the environment can connect to
  the control namespace on the service port.
- Restart keeps records. A port-forward is opened again on the next call.
  The local backend finds its processes again from their PID files.
