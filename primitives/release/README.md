# release

The release controller applies container images to Kubernetes Deployments.
It waits for each rollout, checks the health of the new pods, and rolls back
all items of a release if one item fails. It is written in Rust.

## API

The contract is `contracts/release.csil`. The transport is the same as
`lib/rpc`: `POST /rpc/ReleaseService/{op}` with a CBOR body. An error is a
non-2xx status with a JSON body `{"code": <status>, "message": "..."}`.
`GET /healthz` returns `ok`.

| Operation | Scope | Result |
| --- | --- | --- |
| `create` | `release:operate` | Records the release with status `pending` and puts it in the queue. |
| `get` | `release:read`, or the owner | One release. |
| `list` | `release:read` (all), else the caller's own releases | Newest first. Default limit 50, maximum 500. |
| `rollback` | `release:operate` | Starts a new release that restores the previous images of a `succeeded` release. When it succeeds, the old release becomes `rolled-back`. |
| `list-targets` | `release:read` | The configured targets. |

`release:admin`, `release:*`, and `*` permit all operations. The owner of a
release is the subject of the caller.

Every call needs a bearer token from `lib/authn`. The controller verifies the
issuer, the key ID, the Ed25519 signature, and the expiry.

### Release process

1. Validate the targets and images. Reject an unknown or repeated target.
2. In the queue, run one release at a time.
3. Read and record the current image of each item (`previous_image`).
4. Patch the container image of each Deployment. Add the annotations
   `magi.release/id` and `magi.release/previous-image`.
5. Wait for each rollout: `observedGeneration`, `updatedReplicas`,
   `replicas`, and `availableReplicas`. Stop at the timeout, at
   `ProgressDeadlineExceeded`, or when a new pod shows `ImagePullBackOff`,
   `InvalidImageName`, `CrashLoopBackOff`, or `CreateContainerConfigError`.
6. During the stabilization window, check all pods of the new ReplicaSet
   every 2 seconds. Each pod must be ready and must not restart. If the target
   has `health_path`, each pod must answer `GET <health_path>` with 2xx.
7. Set the status to `succeeded`.

If one item fails, the controller sets `rolling-back`, restores the previous
image of all items, and waits for each rollout. The result is `rolled-back`.
If a restore fails, the result is `failed`. The `message` of an item gives
the cause.

Status values: `pending`, `rolling`, `verifying`, `succeeded`,
`rolling-back`, `rolled-back`, `failed`.

If the process stops during a release, the next start restores the previous
images of that release.

## Configuration

Each flag has an environment variable default.

| Flag | Variable | Default |
| --- | --- | --- |
| `--listen` | `MAGI_LISTEN` | `:8080` |
| `--auth-keys` | `MAGI_AUTH_KEYS` | none. Comma-separated issuer key files. Only `public_key` is read. With no keys, all calls are rejected. |
| `--targets` | `MAGI_RELEASE_TARGETS` | `/etc/magi/release/targets.yaml` |
| `--data` | `MAGI_RELEASE_DATA` | `/var/lib/magi-release`. The controller writes `releases.jsonl` here. |
| `--timeout` | `MAGI_RELEASE_TIMEOUT` | `180` seconds for each rollout |
| `--stabilize` | `MAGI_RELEASE_STABILIZE` | `20` seconds |
| `--health` | `MAGI_RELEASE_HEALTH` | `direct`: probe `podIP:port`. `proxy`: probe through the API server pod proxy (needs `pods/proxy` get). |
| `--kube-context` | `MAGI_KUBE_CONTEXT` | none. Without it, the controller uses the in-cluster configuration, then the default kubeconfig. |

`RUST_LOG` sets the log level. Logs are JSON on stdout.

### Targets file

YAML or JSON. Use a list, or a map with a `targets` list.

```yaml
targets:
  - name: web                # name in API calls
    namespace: magi
    deployment: web
    container: web
    health_path: /healthz    # optional; must start with /
    health_port: 8080        # optional; default: first containerPort, else 8080
    timeout_seconds: 180     # optional; local only
    stabilize_seconds: 20    # optional; local only
```

The controller reads the file at start. Restart it after a change.

## Install

1. Edit the namespace in `deploy/rbac.yaml`. The Role must be in the
   namespace of the target Deployments. For more namespaces, copy the Role
   and RoleBinding into each namespace.
2. Apply `deploy/rbac.yaml`.
3. Edit and apply `deploy/release.yaml`. It has an example ConfigMap, a PVC,
   a Deployment with one replica, and a Service.

Build the image from the repository root:

```sh
docker build -f primitives/release/Dockerfile -t localhost:30500/magi/release:<tag> .
```

The runtime image is `distroless/cc` and runs as user 65532.
`Dockerfile.dockerignore` limits the context when BuildKit is used.

## Tests

```sh
cd primitives/release
cargo test                     # unit and transport tests with a fake cluster
MAGI_TEST_KUBE=1 cargo test    # also the live test
```

- `src/auth.rs` verifies tokens from `testdata/authn-vector.json`. Go
  `lib/authn` makes this file. To make it again:
  `cd testdata/gen && GOWORK=off go run . > ../authn-vector.json`.
- `tests/service.rs` calls every operation through the HTTP transport with
  a fake cluster. It tests success, manual rollback, rollout failure, health
  failure, restore failure, queue order, restart recovery, validation, and
  authorization.
- `tests/kube.rs` uses kube context `default` (`MAGI_TEST_KUBE_CONTEXT`) and
  namespace `magi-release-test` only. It creates Deployment `release-probe`
  with a public image. It releases a good image, then a missing image tag,
  then an image with a failed health path. It makes sure that the last two
  roll back. It deletes the Deployment at the end. It uses `proxy` health
  checks, because the test host cannot reach pod IPs.

## Limits

- One replica only. The JSON-lines file is not shared.
- One release at a time. There is no cancel operation.
- Only Deployments, and only one container of each Deployment.
- The restore does not do a health check. It waits for the rollout only.
- A restart during a release always restores the previous images.
- The targets file is read only at start.
- The live test covers `proxy` health checks only. `direct` checks need a
  network path from the controller pod to the target pods.
- The image is built for amd64 only.
