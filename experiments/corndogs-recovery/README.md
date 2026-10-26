# Corndogs recovery experiment

This experiment sends CSIL-RPC requests to real Corndogs containers.
It tests storage recovery, protected operations, and legacy compatibility.
It covers the Corndogs boundary of Magi's T08/T09 requirements.
It does not change Corndogs source or use a production cluster.

## Requirements

- Linux with a local Docker daemon and permission to use it.
- Go 1.25.7 or later. The recorded runs used Go 1.27.0.
- The sibling Corndogs checkout at `../../../corndogs`.
- The two images identified by digest in `recovery_test.go`.

The client module uses the sibling checkout. Before a comparison run, check
that its source matches release commit
`f1b6ec6086dd0341c09629791bfaa959397b3f9c` (Corndogs 0.8.0):

```sh
git -C ../../../corndogs diff --exit-code f1b6ec6086dd0341c09629791bfaa959397b3f9c -- clients/corndogs
```

Do not change a shared checkout to make this command pass. Use a separate
checkout and adjust the module replacement if another revision is required.

## Run

From this directory:

```sh
OPAL_RECOVERY_DOCKER=1 go test -race -v -count=1 -timeout=15m .
```

To repeat only the protected matrix:

```sh
OPAL_RECOVERY_DOCKER=1 go test -race -json -count=2 -timeout=15m -run '^TestProtectedRecovery$' .
```

To run only the independent legacy file-cluster tests:

```sh
OPAL_RECOVERY_DOCKER=1 go test -race -v -count=3 -timeout=10m -run '^TestIsolatedCluster$' .
```

Without `OPAL_RECOVERY_DOCKER=1`, the tests compile but skip container work.
That result does not validate recovery. The harness does not pull images.
Install the specified images before running it if they are absent.

The protected suite fails when the stated requirement is not met. The legacy
suite checks compatibility, including unsafe behavior that remains unchanged.
A passing legacy test does not prove safe Magi recovery. Neither suite runs
an Magi journal, worker effect, or Longhouse integration.

## Configurations

| Name | Storage and server count |
| --- | --- |
| file_group | One file-backed server, group sync |
| file_always | One file-backed server, sync on each write |
| postgres_single | One server and one PostgreSQL 17 instance |
| postgres_two | Two servers sharing one PostgreSQL 17 instance |
| file_cluster_three | Three replicated file-backed servers, default majority acknowledgement |

Each configuration has fresh storage. File storage is in each test container's
writable layer. PostgreSQL uses its own test volume. SIGKILL and restart keep
that storage. Final cleanup deletes it.

## Protected checks

`protected_test.go` runs each configuration with both admission policies set
to `required`. It checks these cases:

- Required features and policies are reported. Keyless and unguarded work is
  rejected without task creation.
- Eight concurrent submissions of one key create one task. Changed input
  conflicts. A separate queue has a separate key namespace.
- Lost submission replies recover through stream and cluster transport
  retries. A new client also recovers the original saved request.
- Lost claim and group-claim replies replay the original claim.
- A timeout and reassignment reject old update, completion, and cancellation
  requests. This also holds when the state name repeats. An old claim replay
  reports that the claim was superseded.
- Update, completion, and cancellation receipts recover after a lost reply.
  Human-wait completion needs no claim worker. Terminal tasks stay terminal.
- Eight concurrent clients claim 64 tasks once each.
- Full process restart retains 24 task and submission records, four operation
  receipts, and 20 payloads. All four guarded mutations replay after restart.
- Server or leader loss preserves task and receipt recovery. A restarted
  node can route a recovery read after it learns the leader.
- A file-cluster minority cannot acknowledge a keyed write. After quorum
  returns, the original key converges on one task.

## Legacy checks

`recovery_test.go` uses the default compatibility policies. It checks these
cases with the 0.8.0 Go client and unchanged legacy request shapes. It does
not run each previously released language client.

- Submit 24 tasks. Complete one, cancel one, claim one, and update one.
  Kill all servers, then restart them. Also kill and restart PostgreSQL for
  its configurations. Compare all metadata and claim 20 retained payloads.
- Submit 64 tasks. Use eight concurrent clients to claim each task once.
  The two-server PostgreSQL case sends claims through both servers.
- Let a claim expire without a sweep. Check that it remains claimed.
  Run the sweep and reclaim the same task.
- Drop a submission response. Verify an uncertain outcome. An explicit
  legacy retry still creates a duplicate task.
- Repeat the response drop with the Go cluster-aware client. Verify that it
  reports uncertainty without an automatic legacy retry.
- Drop a completion response. Query the known task ID and repeat completion.
- Confirm that legacy completion and update remain unguarded. These APIs
  must not be used for protected Magi work.
- Confirm that legacy operations cannot claim or change a guarded task.
- Kill one server or the file-cluster leader. Check retained tasks, new writes,
  and the restarted node's view.
- Stop both file-cluster followers. Check that the remaining node does not
  acknowledge a write within the five-second request deadline. Restore them
  and check that another write succeeds.

The proxy reads a complete response frame from the real server before it
closes the caller's connection. It drops only the first response. Later
connections pass through, so client retries remain visible.

For stale-worker tests, the sweep uses a supplied future timestamp. No worker
process has to wait for that deadline. The separate timeout test waits past
the actual deadline before checking that no automatic sweep occurred.

## Safety and limits

Containers and networks have unique `opal-recovery-*` names and an ownership
label. Cleanup checks that label before removal. It removes only resources
created by the fixture, including its anonymous volumes. RPC and health ports
are published on loopback only. The test PostgreSQL port is not published.
No host directories or production credentials are mounted.

Cleanup runs after ordinary test failures. If the test process is forcibly
terminated, inspect resources with the `opal.recovery` label. Verify ownership
before manual removal. Do not use a global Docker prune operation.

HTTP health is not leader-discovery readiness. The rejoin test permits a
bounded read retry while a restarted node learns the leader. It fails if a
successful recovery read returns the wrong data. Normal operation calls have
an eight-second deadline. The minority write has a five-second deadline.

All nodes run on one host. These tests do not prove power-loss durability,
disk-loss recovery, PostgreSQL replication, network-partition safety, backup
restore, receipt expiry, long outage recovery, or audit-log completeness. The race detector
covers the Go test process and client, not the prebuilt server image.

## Results

See [the 0.8.0 recovery report](../../docs/corndogs-recovery-080.md) and
[the historical 0.7.6 report](../../docs/corndogs-recovery.md). Raw Go test events
are retained under `results/`. Early runs include test setup and readiness
problems. The report identifies which runs support each conclusion.

## Next Actions

Implement the [Magi adapter contract](../../docs/corndogs-adapter.md). Test its
durable intent, retention deadline, result, and publication recovery boundaries.
