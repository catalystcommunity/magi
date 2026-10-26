# Magi development deployment

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

The read-only protocol service runs at
[magi.catalystsquad.local](https://magi.catalystsquad.local/). It has two replicas in
`magi-example`. This service enables only discovery and schema retrieval.
It has no database or task executor. A separate
[Q&A deployment](question-answer-example.md) now runs in the same namespace
at `workflows.catalystsquad.local`, with its own stores and LinkKeys integration.

Use [local development](native-protocol.md#local-development) for contract work.
Use this deployment for public TLS, routing, client, and rollout checks. It is
not the complete W07 federation environment.

## Ownership and isolation

The deploy helper checks the cluster API address, resource ownership labels,
and hostname conflicts before it writes. It refuses existing resources without
the Magi labels. Its writes are limited to these resources:

- Namespace `magi-example`.
- Service account, resource quota, Deployment, Service, disruption budget,
  network policy, and HTTPRoute named `opal` in that namespace.
- Image pull Secret `opal-registry` in that namespace.
- Images under the private `catalystcommunity/opal` registry path.

The helper preserves an existing namespace quota. The Q&A deployment has a
larger quota for its own roles and stores; a discovery rollout must not reduce it.

The route attaches to `gateway-system/contour`, listener `http`. The existing
router terminates public TLS and forwards traffic to the cluster VIP. Magi
does not change the router, gateway, certificate, shared LinkKeys service,
cluster roles, or other namespaces.

The Deployment runs as UID 65532 with a read-only root filesystem, no Linux
capabilities, no privilege escalation, and no mounted Kubernetes API token.
Each replica requests 25 millicores and 32 MiB. Its limits are 250 millicores
and 128 MiB. The namespace has a resource quota. Preferred anti-affinity puts
replicas on separate nodes when possible.

The network policy permits inbound traffic from the gateway namespace. It
declares no outbound permission. This policy must change before authenticated
services can reach LinkKeys or a database. The current image has no shell or
TLS root store. Do not enable an outbound integration without adding and
testing the required trust and network configuration.

The image pull Secret contains only the existing credential entry for
`registry.catalystsquad.local`. It is used by Kubernetes, not mounted in Magi.
It retains that credential's existing registry permissions. A dedicated
read-only registry credential would further reduce exposure. Do not print
the Secret or copy it to a report.

## Build and deploy

Run the local tests first. Build and publish only the Magi image:

```sh
npm test
node scripts/build-image.mjs --push
```

The build uses a static Linux amd64 Go binary. It does not copy the workspace,
credentials, experiments, or development tools into the image. The image tag
contains a binary hash. Deploy by the published digest, not by a mutable tag.
The build records the digest and binary hash in
[~/data/magi-evidence/deployments/default/image.json](evidence/index.md#evidence-059).

The current image is:

```text
registry.catalystsquad.local/magi@sha256:f86b857b3c18fdde2feac89ed341b32fdb7d4179bf7bb0d05c834c88a282d678
```

Use that complete value as `IMAGE` in these commands:

```sh
node deploy/apply.mjs IMAGE
node deploy/apply.mjs IMAGE --apply
```

The first command performs read-only checks. The second applies only the
listed resources. It uses the current workstation registry login without
printing its value. The default Kubernetes context is `default`. The helper
requires the API server at `https://192.0.2.10:6443`.

## Verify and recover

```sh
node scripts/probe.mjs https://magi.catalystsquad.local/csil/v1/rpc
go run ./cmd/magi-probe https://magi.catalystsquad.local/csil/v1/rpc
node scripts/live-check.mjs
node scripts/live-check.mjs --restart
```

The first two commands use different generated clients. They compare the
served descriptor with the local descriptor. Normal HTTPS certificate checks
remain enabled. The live check records only public capability results and
Magi pod metadata under `test-results/`.

`--restart` changes the Magi Deployment only. It probes public discovery while
Kubernetes replaces the pods. This measures one rolling restart. It is not
proof of database recovery, event durability, or uninterrupted service under
all failures.

The process handles SIGTERM, fails readiness, and gives active requests up to
five seconds to finish. Kubernetes allows fifteen seconds before termination.
The current service retains no application state, so replacement does not
require a database restore. Reapply the last tested image digest if an update
fails. Keep the same namespace and ownership labels.

Do not delete the namespace as a routine rollback. Future releases can add
persistent data. Check their migration and recovery procedure first.

## Measured checks

The 2026-10-13 [live report](evidence/index.md#evidence-001)
records 21 successful public discovery probes during one rolling restart.
Both replacement replicas became ready on separate nodes, with zero container
restarts. The Go and TypeScript probes retrieved the matching descriptor over
normally verified public TLS.

## Next Actions

Add the durable W04 store and integrate W02 authentication. Test lost replies,
process failure, replica races, and withdrawal before enabling protected public
operations. Give the secondary domain its own identity integration and store.
Do not share primary credentials to simulate an independent peer.
