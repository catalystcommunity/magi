# execution

The execution primitive makes isolated environments. An environment runs
commands and holds files. Apprentices and checks use environments to work on
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
| `POST /v1/pty` | A shell on a pseudo-terminal, over an HTTP upgrade (below). |

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

### Terminal (`POST /v1/pty`)

The request has `Connection: Upgrade`, `Upgrade: magi-pty`, the bearer
token, and a JSON body: `argv` (default `bash -l`, or `sh -l` when there
is no bash), `cwd` (default: the work directory), `env`, `cols`, and
`rows`. envd starts the program on a new pseudo-terminal (a new session
and process group, `TERM=xterm-256color`) and answers
`101 Switching Protocols`. Then the connection carries frames in the two
directions (`envd/frame.go`):

| Type | Direction | Payload |
| --- | --- | --- |
| `d` data | both | Input bytes to the terminal, or output bytes from it |
| `r` resize | to envd | cols (u16), rows (u16) |
| `x` exit | from envd | The exit status (i32; 128+N for signal N). The last frame. |
| `e` error | to the client | UTF-8 text. The last frame. |
| `k` ping | both | None. The receiver ignores it. |

A frame is the type byte, the payload length (u32, big-endian), and the
payload (at most 64 KiB). A receiver ignores unknown types. When the
client closes the connection, envd sends SIGHUP to the process group, and
SIGKILL after 2 s. When the shell exits, envd waits at most 2 s for the
last output (a background process can keep the terminal open), sends the
exit frame, and closes the connection. The shell runs with the network of
the environment: in a sandbox with a proxy, all traffic goes through the
egress proxy.

The service serves the same stream at `GET /v1/pty?environment=ID&cols=N&rows=N[&cwd=DIR]`:
an HTTP upgrade (WebSocket form) with a bearer token. The caller must own
the environment, or have `execution:act-as` or `execution:admin`. Magi
uses it for terminals of tasks in the cluster.

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
  - Network `proxy` sends all traffic through the Magi egress proxy
    (`primitives/egress`). A NetworkPolicy permits DNS and the proxy port
    (pods `app.kubernetes.io/name=egress` in `MAGI_EXEC_EGRESS_NAMESPACE`)
    only. The Secret of the environment has the proxy URL with the
    credential of the environment, and the CA certificate of the proxy.
    `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` (and the lower-case forms) come
    from the Secret and point to the proxy port of envd, `127.0.0.1:3128`.
    `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`,
    `NODE_EXTRA_CA_CERTS`, `GIT_SSL_CAINFO`, and `PIP_CERT` point to the CA
    file. Without `MAGI_EXEC_EGRESS_PROXY`, network `proxy` is refused.
  - The transparent route: tools with no proxy setting also go through the
    proxy. The init container `envd-net` runs `envd podnet -exclude IP:PORT`
    as root with only `NET_ADMIN`. It adds a firewall: DNS (port 53) goes to
    envd at `127.0.0.1:15053`, and each other TCP connection goes to envd at
    port 15001, except loopback and the proxy. envd gives each name a fake
    address (search-domain names get "no such name") and sends each
    connection as `CONNECT name:port` to the proxy. The service resolves the
    proxy host to its address when it makes the Pod, because the Pod cannot
    resolve names itself. The main container has no capability.
  - With `MAGI_EXEC_EGRESS_SSH_PUB` (the SSH host key of the proxy), the
    Secret has `ssh_known_hosts` and `GIT_SSH_COMMAND` uses it.
- `local`: envd runs as a child process on the service host, in a directory
  below `MAGI_EXEC_LOCAL_ROOT`. It gives no isolation and ignores the image.
  It is for tests.

Package `sandbox` runs envd on one machine in a bubblewrap sandbox, without
this service. A Magi runner uses it (see `magi/README.md`). The sandbox sees
its work directory, read-only `/usr` and a few `/etc` files, and configured
tool directories; it does not see the home directory of the user. With
bubblewrap, envd listens on a unix socket in the state directory of the
sandbox (Seatbelt: loopback). A sandbox that starts with a `Proxy` has its
own network namespace with no real interface; the caller serves a unix
socket, the only route out. `envd netns` starts in a new user and network
namespace (as root of it), sets up the network as in a task VM (below),
and then starts bubblewrap with a user namespace of its own, so commands
have no capability. envd copies each connection to `127.0.0.1:3128`
(`-forward`) and each other TCP connection (`-transparent`) to the socket.
The view has the resolv.conf of envd, the proxy CA in the system trust
store, and the SSH known_hosts of the proxy. The host kernel needs
nf_tables with NAT and redirect (modules load on demand).
Package `egressenv` has the proxy credential and the proxy variables.

With `Config.VM`, each environment is a Cloud Hypervisor VM (Linux, KVM).
virtiofsd shares the bubblewrap view from inside bubblewrap as the guest
root. envd is the guest init (`-vm`; its flags and environment are in
`/run/magi/state/vm.json`) and listens on vsock port 1024. The guest has no
network card. With a `Proxy`, envd (`-transparent`) sends each TCP
connection of the guest as `CONNECT name:port` to host vsock port 3128,
which the caller serves (`ListenProxy`). The guest DNS (`127.0.0.53`) gives
each name an address in `198.18.0.0/15`. The kernel comes from
`tools/vm/build-kernel.sh`; `tools/vm/build-kernel.sh --install
[USER@HOST]` puts it where the runner configuration expects it. `MAGI_TEST_VM_KERNEL` turns on the VM tests.

With `VM.Template` (a Mac), each environment is an APFS clone of a macOS
template that `magi-vz` (`tools/vm/macos`) runs with no network card. The
shares are at `/Volumes/My Shared Files`; a launchd daemon in the template
starts envd from the share `magi`. vsock uses the same socket names as
Cloud Hypervisor. With a proxy, the guest gets the same transparent route
as a Linux VM, with pf in place of nftables (`envd/vm_darwin.go`). At most
two macOS VMs run at a time (the macOS licence).
`MAGI_TEST_MAC_TEMPLATE` turns on `TestMacVM`.

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
| `MAGI_EXEC_EGRESS_PROXY` | none | Egress proxy `host:port` for network `proxy`, such as `egress.magi.svc:3128` |
| `MAGI_EXEC_EGRESS_NAMESPACE` | control namespace | Namespace of the egress proxy |
| `MAGI_EXEC_EGRESS_ENV_KEY` | none | Environment key file of the egress proxy |
| `MAGI_EXEC_EGRESS_CA` | none | CA certificate file of the egress proxy |
| `MAGI_EXEC_EGRESS_SSH_PUB` | none | SSH public host key file of the egress proxy, for the known_hosts of Pods |
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
- A terminal in a Kubernetes environment needs an envd build with
  `/v1/pty`: environments that started before such a release do not have
  it (the error is 404). It needs `/dev/pts` in the container (container
  runtimes supply it). It was tested with the local backend only.
- `cluster` network does not limit egress to the cluster.
- With fetch delivery and network `none`, the environment can connect to
  the control namespace on the service port.
- Restart keeps records. A port-forward is opened again on the next call.
  The local backend finds its processes again from their PID files.
