# Test release approval and recovery

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

Updated 2026-10-20 UTC. Status: human approval, live canary promotion, and
automatic rollback after a deliberate protocol failure passed. The restricted
controller service is installed. Live authority and direct-probe checks passed.
Recovery after a controller crash during active restoration also passed.
The connected Corndogs release test passed. The journal and queue retain the
same approved rollback result.
This plan extends the accepted [demo design](demo-prototype.md). The current
[development exercise](offline-development.md) produces a candidate and review.
It does not build, approve, or deploy a release.

## Current implementation

The separate [release contract](../csil/components/releases.csil) has generated
Go and TypeScript clients. The [approval recorder](../release/approval.mjs)
uses the pinned LinkKeys regular-RP SDK. It requires the exact authenticated
account, a browser-bound session, an origin check, a CSRF value, and a separate
approval action. A review alone does not approve a release. The recorder stores
the exact signed approval before it returns a reply. Repeated commands return
the same signed bytes. The controller checks a fresh signed status response.

Submission and approval are separate operations. The authorized orchestration
application submits the candidate through `Releases/submit`. Its signed request
does not grant approval. The service stores the exact bytes and returns a review
URL. Repeated submission returns the same receipt. A changed body under the same
candidate ID is refused. The title, reason, changes, and risks are part of the
candidate bytes, so approval also binds this explanation.

The human opens the URL, signs in, and reviews the stored proposal. No upload or
download is required. `Releases/review-candidate` requires the human session and
CSRF check. The old byte-upload review operation is disabled on the HTTP API.
Approval and its event enter the same SQLite transaction. The controller pulls
signed event batches and fetches the signed approval through the API. It checks
current approval status before admission and again before the first change.

The controller records admission before it acknowledges delivery. Its durable
inbox detects repeated events. A stop after admission but before acknowledgement
does not create another release. Events remain available until acknowledged.
An event-service outage does not prevent checks or recovery of an admitted
release. The new pull API is a single-controller delivery path for this
prototype. A relay to the separate event broker and user notification service
is not connected. The review URL is currently returned to the operator.

The [controller](../release/controller.mjs) records an intent before each target
change. It checks the installed image, configuration digest, resource UID,
generation, and operation marker. The [Kubernetes adapter](../release/kubernetes.mjs)
can change only `release-canary` in `magi-example` on the configured cluster.
It uses resource-version preconditions. It does not have an arbitrary command
or target parameter. Task containers do not receive its credentials.

The first live tests used an operator process outside the candidate. Its SQLite records
describe deployment observations and effects, not workflow task states. The
Corndogs adapter is optional. It is now enabled on the live test controller.
The queue and journal readiness check passed. The approved connected release
test also passed. The controller service has
API-enforced restrictions, a separate audit PVC, and Kubernetes supervision.
The recorder also uses SQLite and one replica. These are prototype limits,
not a horizontally scalable approval service.

The approval page is available at `https://approval.catalystsquad.local/`.
The [live check](evidence/index.md#evidence-027)
confirmed one ready replica for each release Deployment, verified public TLS,
and received HTTP 200. Both release applications had valid current LinkKeys
keys. Existing Magi deployment specifications and generations were unchanged.
No reapply or restart was needed. This check made no cluster changes.

The [baseline check](evidence/index.md#evidence-028)
confirmed readiness and the exact CSIL Discovery response from the canary.
That candidate matched the resource UID, generation, image, and configuration
at the recorded check. Its expiry was `2026-10-19T05:10:48.770Z`. The public approval page,
configured approver, and release Discovery schema passed their checks.
The human later uploaded it and created one review, but no approval. At the
human's request, that review was deleted before the new flow was tested. No
controller database or deployment task existed for it. The cleanup retained
an audit entry and the original local evidence. Promotion and live rollback
were untested at that check. The sections below record the later successful tests.

The [previous check](evidence/index.md#evidence-026)
recorded Kyverno webhook timeouts and HTTP 503. The cluster operator repaired
the control-plane network before this retry. The Magi session did not change
or bypass a shared admission policy.

Two separate application instances, `opal-release-approval` and
`opal-release-controller`, were enrolled under the existing Example environment test
account. Their current keys verified through LinkKeys. These are application
identities. They are not human approvals and do not impersonate the approver.

## Approved live release

The system submitted candidate `c50ce3f5-d247-4a46-8753-2c486362e6e9` through
the authenticated API. Open its [review page](https://approval.catalystsquad.local/?candidate=c50ce3f5-d247-4a46-8753-2c486362e6e9).
It expires at `2026-10-19T19:38:23.760Z`. This proposal tests the isolated
discovery deployment; it is not the complete Magi demo. Its explanation is
visible after sign-in. No candidate file is required from the human.

The human approved this candidate through the review page. Approval ID:
`f8ece7c8-1d4f-4cfb-a74a-a9924bddc717`. The controller received and acknowledged
event `e6a5a58d-3e3a-4246-8776-375f59b114a6`. No approval file was transferred.

The first admission stopped because a final authorization check failed. No
deployment change occurred. Repeated key checks encountered TLS connection resets.
Both applications now reuse raw key evidence within the existing one-second
freshness budget. Every use still verifies signatures and current validity.
Failed refresh does not permit stale evidence. No LinkKeys limit was changed.

A guarded `--retry-unstarted` command rechecked the same approval and unchanged
baseline. The controller deployed exactly the approved image:
`sha256:ef05a247267e3707d02ab922d78cc79a91ac3e9e5f79ba2309b891a55cb0a8bf`.
The canary advanced from generation 2 to 3. Three consecutive readiness and
exact-protocol checks passed. Its final state is `healthy`. The failure and
retry remain in the audit. The [promotion report](evidence/index.md#evidence-031)
binds the stored approval, event receipt, image, and observations. A later
protocol check passed. A controller restart did not repeat the change.

The [live service check](evidence/index.md#evidence-030)
confirmed both release services were ready, public TLS passed, and application
keys verified. Existing Magi deployment specifications and generations were
unchanged. No production workload or shared LinkKeys configuration was changed.
The operator controller has finished and is not running. It is not supervised.

## Live rollback proof

The human separately approved candidate `a4ea949d-b2b1-4e45-a73e-015819a84902`.
Its digest is `90d7619c0930c4fbb607953cbd55333df751297a010ed03b5cae577db864c07c`.
Approval ID: `a0d12113-02b3-41bf-8df1-0ab4c7fc2eb3`. The system submitted the
proposal. The human used the review link without a file transfer.

The [test fixture](../experiments/release-rollback/main.go) reports readiness
204 but returns protocol status 503 with a fixed failure message. It has no
task credentials, mounts, or network egress. Its image contains only a static
binary. Local image checks confirmed this failure and the healthy baseline
before submission. The required protocol result remained the healthy response.
The test did not change success criteria to accept the failure.

The live controller changed only `release-canary`. Generation 3 used the
healthy image. Generation 4 used the deliberate failure image. Ten consecutive
checks failed; nine reached the ready pod and recorded the exact failure body.
The controller automatically restored the original image at generation 5.
Three consecutive readiness and exact-protocol checks passed. Restoration
and its checks took 16.553 seconds from the recorded restoration intent.
No operator deployment change was needed.

The [rollback report](evidence/index.md#evidence-034)
binds the exact approval, acknowledged event, durable inbox entry, failure
observations, both image transitions, and restoration checks. A further live
protocol check passed. The [restart check](evidence/index.md#evidence-036)
confirmed that `--resume` did not change the target or add another observation.
The controller returns exit code 2 for `restored`: the candidate failed even
though recovery succeeded. The completed test listener was stopped.

The [final service check](evidence/index.md#evidence-035)
confirmed both release services were Ready, TLS passed, and current application
keys verified. Existing Magi deployment specifications and generations stayed
unchanged. The approval service image did not change during this drill.

This proves rollback for one stateless protocol failure on the isolated canary.
It does not prove database migration recovery, worker fencing, cluster failure
recovery, or a controller crash during live restoration. Those remain separate
tests. Local tests cover controller restart and lost replies around effects.

To prepare another deliberate failure test:

```sh
node scripts/build-release-rollback-image.mjs --push
node scripts/prepare-release-candidate.mjs --rollback-drill
node release/submit.mjs ~/.config/magi/profiles/release/controller.json \
  ~/.config/magi/profiles/release/rollback-candidate.cbor
node release/run.mjs ~/.config/magi/profiles/release/controller.json --watch
```

The human must approve the new exact proposal through its returned review URL.
The completed proposal cannot be reused against the changed target. The drill
uses separate candidate files and reports; it does not overwrite the earlier
successful proposal. Preparation checks both immutable images in temporary
containers and checks the live baseline without changing it. It removes its
temporary containers and network but retains the evidence.

After the controller reaches `restored`, run the read-only proof check:

```sh
node scripts/check-release-rollback.mjs candidate-uuid
```

The checker refuses incomplete evidence, a missing deliberate failure, wrong
images or target generations, incomplete restoration checks, and late recovery.

## Restricted controller service

The new [runtime](../release/controller-cluster.mjs) runs outside the canary in
its own Deployment. Its published image is
`sha256:c83efa08d5a6a7f0d33c1af1a3b6ef0d4df656a5054803511b02bc6015c3e764`.
The [build receipt](evidence/index.md#evidence-064) records the
source and recipe hashes. The image contains no private runtime profile or
operator kubeconfig. The Deployment must select `release/controller-init.mjs`
as its command; the shared image recipe has a different default command.
This launcher runs the controller as a child process. It forwards shutdown
signals and returns the child's exit status. It does not restart the child;
Kubernetes does that.

The [manifest generator](../deploy/release-controller-render.mjs) defines these
limits:

- One replica, a Recreate update strategy, and a separate 1 GiB audit PVC.
- A non-root process with a read-only root filesystem and explicit resource limits.
- A projected service-account token with a one-hour requested lifetime. The
  adapter reads the current token for each request. It verifies the API TLS
  certificate against the mounted cluster CA.
- Get and patch permission for only the named `release-canary` Deployment.
  Pod and ReplicaSet reads cover the Magi namespace because this RBAC rule
  cannot enforce the adapter's label selector. No Secret read, pod creation,
  exec, port-forward, or RBAC change is permitted.
- An API admission policy limited to this controller's service account. It
  permits an immutable Magi image and an operation marker. It refuses changes
  to the service account, mounts, commands, pod labels, replicas, and other
  configuration. The installer checks the API schema field list before use.

The admission policy is necessary because permission to patch a Deployment
does not limit which fields can change. The
[live authority check](evidence/index.md#evidence-038)
passed 26 checks. The allowed dry-run patch passed. All forbidden changes failed
through the named Magi admission policy. No canary change occurred. See the
Kubernetes documentation for
[RBAC](https://kubernetes.io/docs/reference/access-authn-authz/rbac/),
[admission policies](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/),
and [projected tokens](https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/).

The live tests found three details that local structure checks did not prove:
the API omits an empty `request.subResource`; it omits the empty `typeChecking`
status after a completed check; and its CEL static type omits pod `overhead`.
The policy uses optional access for the first field and a dynamic comparison
for overhead. A forbidden overhead change failed through the policy. A test
for privileged execution must use a structurally valid security context;
otherwise generic API validation can reject it before this policy is tested.

The native adapter probes the verified canary pod directly on the pod network.
It needs no kubectl binary or port-forward permission. This internal probe is
plain HTTP and contains public test data. It is not a private-data transport.
Health checks show controller-loop progress, not dependency availability.
One replica and one PVC do not provide high availability or worker fencing.

The installer copies the existing SQLite database without deleting the source.
It checks the copy hash, SQLite integrity, and absence of active releases. A
temporary importer pod has only the data volume, with no runtime Secret or
service-account token. After installation, a local marker blocks mutating CLI
commands against the old database. This is an operator guard, not a distributed
lock. The evidence scripts then export the current controller database through
the separate operator access path. The service does not receive those credentials.

Installation changes only Magi-owned resources. It raises the Magi PVC count
limit from three to four; the 8 GiB storage limit stays unchanged. It adds no
Service and changes no shared LinkKeys or Kyverno configuration. The admission
policy is cluster-scoped, but matches only the new Magi service account.

Command review blocked the first installation attempt. The retry completed
installation. The [installation record](evidence/index.md#evidence-062)
retains the image update and migration receipt. The audit copy matched all two
releases, 38 observations, and two inbox entries. The temporary importer pod was
deleted with a UID precondition. The local database remains available.
The canary stayed at generation 5. This is the procedure for a fresh installation;
do not rerun it against the installed service:

```sh
npm run test:releases
npm test
node deploy/release-controller.mjs --prepare
node scripts/check-release-controller-authority.mjs
kubectl --context default -n magi-example wait \
  --for=condition=Ready pod/release-controller-seed --timeout=45s
node deploy/release-controller.mjs --seed
node deploy/release-controller.mjs --start
kubectl --context default -n magi-example rollout status \
  deployment/release-controller --timeout=45s
node deploy/release-controller.mjs --verify
```

Stop on any failed check. Before `--seed` and `--start`, the authority check must
accept the allowed server-side dry-run patch and reject each prohibited patch
through the named admission policy. It must confirm that the canary did not
change. A generic request error is not a passing denial test. This check uses
operator impersonation and does not prove projected-token rotation in the pod.

After installation, verify the copied release, observation, and inbox records.
The [runtime check](evidence/index.md#evidence-039)
passed the direct canary probe with the actual projected token. A Secret read
with that token returned 403. This does not prove a full live token-rotation
cycle. Check an idle controller restart before preparing a separate
proposal with `--rollback-drill --controller-crash`. Its explanation includes
one SIGKILL after the restoration API reply and before the effect receipt.
Arm only that candidate with `--arm-crash candidate-id`, then submit it through
the existing API. The human must approve its exact review page. No earlier
approval permits this new test.

The durable fault marker prevents a second deliberate crash after restart.
Local tests simulate that boundary and recovery. The live proof must also
retain the container termination status, restart count, restored image,
unchanged recovery deadline, and final protocol checks. The rollback checker
now verifies the exact prior container, exit 137, one restart in the same pod,
the durable fault marker, and the missing pre-crash receipt. It rejects an
out-of-memory termination and an ordinary clean exit. Record its baseline with
`node scripts/check-release-controller-runtime.mjs --baseline candidate-id`
after the armed controller becomes Ready and before submission. Disable the
test hook with `--clear-crash` only after the release is terminal and its proof
has been collected. Do not approve another release during this test.

The first idle SIGKILL attempt did not stop PID 1. A separate local container
test confirmed that self-SIGKILL also returned without termination in that
layout. The launcher fixes this failure. The
[exact-image test](evidence/index.md#evidence-063)
then returned 137 after an actual child self-SIGKILL. See the Linux
[PID namespace signal rules](https://www.man7.org/linux/man-pages/man7/pid_namespaces.7.html).
Do not count an accepted signal call as proof of process termination.

The [live idle-crash report](evidence/index.md#evidence-042)
records exit 137 and one Kubernetes restart in the same pod. The recovered
controller retained the exact audit digest. Its projected-token Secret read
was still denied, and its direct canary probe passed. The canary image and
generation did not change. This proves idle crash recovery, not recovery
during an active restoration.

## Active-restoration crash proof

Candidate `747c5c78-69e2-4fa3-9b7f-335f2950a8ad` was submitted through the
authenticated API. Its digest is
`e06cc9adefd9697528a953d180a47a4dd69745e5886a4625ee6ab5426ad9248c`.
The human approved this exact proposal through the
[review page](https://approval.catalystsquad.local/?candidate=747c5c78-69e2-4fa3-9b7f-335f2950a8ad).
Approval ID: `146def08-2779-4e40-ad20-092c633f035b`.
The controller admitted event `b05b283b-a218-490c-ae73-c364910231cf`
and acknowledged it after durable storage. The proof checked the event digest,
exact signed approval, current approval status, and configured human identity.

The one-shot fault applied only to this candidate. The
[baseline record](evidence/index.md#evidence-061)
identifies the controller container and audit before approval. The
[completed proof](evidence/index.md#evidence-043)
records the following results:

- The canary advanced from generation 5 to 6 with the deliberate fault image.
  Ten checks failed. Nine observed the ready fault pod and its expected 503 reply.
- The controller stored a restoration intent and restored the healthy image
  at generation 7. At `2026-10-20T02:10:34.042Z`, the fault killed the controller
  after the API change and before it stored the effect receipt.
- The container ended with exit 137, not an out-of-memory termination.
  Kubernetes restarted it once in the same pod at `2026-10-20T02:10:35Z`.
- The restarted controller observed the restored target. It did not repeat
  the deployment change. Three restored protocol checks passed.
- Restoration finished in 11.003 seconds, within the original 300-second
  deadline. Existing Magi deployment specifications and generations were unchanged.

The proof retains the original and replacement container IDs, fault marker,
21 ordered observations, image digests, approval, event receipt, and live probe.
Collect this evidence before removing a fault hook or replacing its pod.
After a pod replacement, the old container baseline cannot prove a new live crash.
Keep the completed report and use a separate check for cleanup.

The [cleanup check](evidence/index.md#evidence-045)
confirmed that the fault hook was removed from the running configuration.
The new controller pod was Ready. Its database passed the integrity check.
All 21 observations for this candidate and the fault record were unchanged.
The database retained three releases, 59 observations, and three inbox entries.
No release was active. The canary remained at generation 7 and passed its probe.
The volume moved to the new node during the controller restart. No storage
configuration or shared cluster service was changed.

The [final service check](evidence/index.md#evidence-044)
found all three release Deployments Ready. Public TLS, HTTPS readiness, and
both current application keys passed. Existing Magi Deployments were unchanged.

This test covers one stateless canary and one supervised controller. It does not
prove recovery from a cluster failure, data migration, or concurrent workers.

## Corndogs release tasks

The [task adapter](../release/tasks.mjs) connects the controller to the existing
[operation journal](../workflow/journal.go) through a fixed
[local helper](../cmd/release-tasks/main.go). It does not add a public API or
change the Releases CSIL contract. The helper uses the generated Corndogs client.
It has queue and journal access, but no LinkKeys signing key or Kubernetes access.
The controller still verifies human approval and owns deployment recovery.

The adapter is optional for an installation that has not used it. Enable it
only while the controller has no active release. Its SQLite binding records
the Corndogs installation. A later restart cannot omit the adapter or change
that installation. Old terminal releases are not submitted as new tasks.
The live Example environment controller remains on its tested image without this adapter.

The initial sequence is:

1. Authenticate and admit the exact approved candidate as before.
2. Record a task binding before calling the helper. Include candidate and
   approval digests, identities, and the target. No credentials enter this record.
3. Submit one guarded task in `queued`. Use a stable journal key. The same
   candidate ID with changed approval bytes must conflict.
4. Use a guarded update from revision 1 to `running` at revision 2. Read the
   current task before permitting deployment intent. A stored receipt alone
   cannot prove that the reservation is still current.
5. Record deployment intent with the queue, task ID, and revision. Continue
   recovery without the queue or journal database. The existing controller
   still checks approval before the first deployment change.
6. After a terminal outcome, retain its audit digest and result in SQLite.
   Commit that result and a guarded completion intent together in PostgreSQL.
   Complete the original task revision, then retain the receipt in SQLite.

The release task has no timeout and no automatic reassignment. One supervised
controller owns this durable operation. This is not a distributed worker lease.
Corndogs cancellation after deployment intent does not cancel or undo the
external operation. Use the release controller's recovery path. Do not give
other workers permission to claim these queues.

Submission converts timeout `-1` to stored `0`. In the pinned Corndogs 0.8.0,
a guarded update retains `-1`. Both disable timeout evaluation. The adapter
checks the exact update result, including the current revision and state.

Queue failure before deployment intent permits no change. Waiting does not
extend the admission deadline. Deadline expiry stops the release without even
a same-image restoration. Once intent exists, queue failure cannot stop the
controller's bounded recovery. Result publication runs only when no release
is active. It cannot consume an active restoration deadline.

Lost submission, reservation, and completion replies reuse their original
operation IDs. The journal's receipt-retention limits still apply. An uncertain
operation after its retry window requires reconciliation. A stale completion
never borrows a new task revision. Its retained result remains available, and
the adapter records `completion-requires-reconciliation`. It does not repeatedly
send that rejected result or block publication for another terminal release.
There is no public reconciliation command yet. Do not edit receipts to retry.

A completed Corndogs task means that a result was stored. It does not mean
that promotion succeeded. The result is `healthy`, `restored`, or
`recovery_required`, with its reason and evidence digest.

### Local installation

Build the helper from the same checked source as the controller:

```sh
go build -o .build/release-tasks ./cmd/release-tasks
```

Create a private helper profile with mode `0600`. It must be a regular file,
not a symbolic link. Supply these fields:

- `database`: the dedicated journal PostgreSQL connection string.
- `installation`: the stable Corndogs storage authority ID.
- `corndogs_seeds`: the permitted host and port pairs.
- `corndogs_server_name` and optional `corndogs_ca_file`: TLS configuration.
  Only loopback addresses can omit TLS.
- `controller`, `recorder`: exact application references with the Go JSON
  field names `identity`, `account_id`, `application_id`, and `instance_id`.
- `target`: the fixed release target ID.

Initialize the journal with a migration role. Then give the runtime role
SELECT on `workflow_journal_binding`, SELECT/INSERT/UPDATE on
`workflow_intents`, and SELECT/INSERT on `workflow_results`. It needs no
schema, policy, human-binding, or deployment writes. Corndogs must require
both submission keys and task guards. Its network must admit only trusted
service processes; a task key does not authenticate a caller.

```sh
.build/release-tasks --migrate /absolute/private/release-tasks.json
```

Add `tasks` to the controller's private runtime profile. Set `installation`
to the same authority ID. Set `binary` and `profile` to absolute paths for the
installed helper and private profile. The helper reads bounded JSON on standard
input. This is a trusted local process interface, not the public wire contract.
It runs without a shell or inherited service credentials. It has a ten-second
deadline; the parent kills it after twelve seconds if necessary.

Do not add these credentials to task environments. Do not connect a restored
journal backup without cross-store reconciliation.

Build the optional helper into the controller image with this command:

```sh
node scripts/build-release-image.mjs --controller --tasks --push
```

The build records the Go source hashes, binary hash, and compiler version.
It includes the Go and dependency license files. The normal image has no helper.
The helper also has a read-only `--check` mode. This checks the journal binding,
TLS, Corndogs features, admission policies, and receipt retention.

### Verification

```sh
npm run test:release-tasks
```

The matrix uses disposable PostgreSQL and Corndogs file/PostgreSQL storage.
It uses synthetic human authentication and a simulated deployment target.
It tests lost replies, a helper SIGKILL after a remote reservation, changed
approval bytes, stale task revisions, and rollback during queue/database loss.
These tests do not prove a live LinkKeys-to-Corndogs release or worker fencing.

The [restricted-role report](evidence/index.md#evidence-047)
has three passes of 22 checks. It includes both parent tests. The runtime role
can check and use the stores. It cannot apply the journal schema.

### Test-cluster installation

The [deployment report](evidence/index.md#evidence-067) records
the enabled adapter. The controller image is
`sha256:d64d0c19169e457fc086a21666731817629c84d9c67386aa05217b4d8177a907`.
The dedicated `release-corndogs` store uses TLS and synchronous file storage
on a separate 1 GiB PVC. It requires submission keys and task guards.
Only the release controller and its check Jobs can reach this store.

The `release_journal` database runs on Magi's existing PostgreSQL server.
It does not share workflow tables with the Q&A database. Its `release_runtime`
role has only the table permissions listed above. It is not a database owner
and cannot create roles, databases, schemas, or journal tables. The controller
gets no database administrator credential or Corndogs TLS private key.
The two databases still share server availability and physical storage.

Only Magi namespace quotas changed: Secrets 9 to 11, Services 5 to 6,
PVCs 4 to 5, and storage 8 GiB to 9 GiB. The earlier renewal installation
changed Secrets from 8 to 9. Shared cluster policies and LinkKeys configuration
did not change.

The installer stops the idle controller before it changes the runtime profile.
It checks the stopped database again. A newly admitted release stops the upgrade
and resumes the old controller. The first inspection failed because SQLite
needed a writable shared-memory file. The old controller resumed without an
image or profile change. The corrected check copies the stopped database and
WAL into disposable memory. The source PVC stays read-only.

The successful upgrade retained three releases, 59 observations, and three
inbox entries. No release was active. The installed helper passed its check
from inside the controller. The task installation had no task links at that check.
Other Deployment specs and the canary generation were unchanged.

The setup is operator work, not an agent tool:

```sh
node deploy/release-tasks.mjs --prepare
node deploy/release-tasks.mjs --enable
node deploy/release-tasks.mjs --verify
```

Keep the setup report. An operator process failure during maintenance needs
inspection before retry. Do not start a second controller. Do not remove the
adapter after it binds its installation in SQLite. The initial verification
expects no task links. It is not the check for a completed release.

### Completed live task test

The human approved candidate `a26dfe8b-13dd-4839-a77d-9a2d31ca45de`.
Approval ID: `0b8f9004-af8d-4e1e-899e-4a558b0ade21`. The controller received
and acknowledged event `8d1b9ab3-b338-4732-b62b-1045b8c09e2e`.
No approval file was required. No new deployment command was sent by the
operator during verification.

The [rollback proof](evidence/index.md#evidence-049)
records generations 7, 8, and 9. Ten candidate checks failed. Eight checks
received the exact deliberate fault response. The controller restored the
healthy image in 8,405 ms and passed three restoration checks. Other Magi
Deployment specifications did not change.

The [task proof](evidence/index.md#evidence-050)
compares independent reads from controller SQLite, PostgreSQL, and Corndogs.
It binds the exact candidate and approval digests, application identities,
target, task, and all 23 release observations. It confirms:

1. Task `7b44b70a-ef95-4292-8ddf-ca8a114d9251` was reserved at revision 2
   before the controller recorded promotion intent.
2. Submit, start, and finish each have one confirmed journal intent. Each
   intent has one recorded send attempt and its exact request and receipt.
3. The journal stores one result at reservation revision 2. Its outcome is
   `restored`, with reason `candidate-check-failed` and the matching audit digest.
4. Corndogs reports the guarded task as terminal at revision 3. Its completion
   receipt matches both the journal and controller records.

Task completion does not mean that the candidate passed its checks. It means
that the system retained the result. This test did not inject a live queue
outage or controller crash. It does not prove distributed worker fencing.

To repeat the read-only checks while remote receipts remain available, run:

```sh
node scripts/check-release-task.mjs a26dfe8b-13dd-4839-a77d-9a2d31ca45de
```

The command first checks the signed approval, delivery receipt, and live restored
target. It then reads a controller snapshot, a read-only PostgreSQL transaction,
and two fixed Corndogs lookup operations over verified TLS. It does not enqueue,
reserve, complete, or retry work. Receipt expiry or a later target change can
prevent a new live check. The retained report remains available for analysis.
Corndogs timestamps use i64 nanoseconds. The report retains raw CBOR bytes;
the checker does not round these values to JavaScript numbers.

The [release report](evidence/index.md#evidence-052)
has three passes of 99 checks. These include refusal of changed authority,
request bytes, revisions, results, and remote receipts. The
[full regression report](evidence/index.md#evidence-053)
has three passes of 212 Go and 639 Node checks. Race detection, vet, generation,
and documentation checks passed. The
[live service check](evidence/index.md#evidence-051)
confirmed all three release Deployments Ready, public TLS, and current keys.
The [authority check](evidence/index.md#evidence-048)
passed 26 checks without changing the canary.

## Application attestation renewal

Both application attestations expired during the pause. The keys themselves
remain valid until 2026-11-17. `node deploy/release-bootstrap.mjs --renew`
renewed the six existing attestations with possession proofs and sibling
signatures. It used no password, created no identity, and granted no approval.
The command does not rewrite runtime profiles. Renewal does not extend key lifetime.

The dedicated `release-renewal` CronJob is now installed in `magi-example`.
It runs at minute 17 every four hours, in UTC. It does not run concurrent Jobs.
It holds the six existing quorum keys in its own Secret. The controller keeps
only its runtime key. Local recovery copies remain in the private setup directory.
The renewal service has no RP API token, password, human session, Kubernetes
token, Role, Service, or persistent volume. Its image has no controller or
approval server. Only DNS and the test LinkKeys TCP service are permitted.

The service requests a fresh challenge for each key. It signs the possession
proof and two sibling proofs. It verifies the returned domain signature, subject,
public key, and validity times. It then reads the current key set and verifies
revocations. It uses the pinned SDK's low-level `call` with no RPC auth field.
It does not pass a false API key to the RP transport.

The [first cluster run](evidence/index.md#evidence-066)
verified all six keys. The attestations were still fresh. LinkKeys returned the
stored attestations with `signed=false`; no new signature was required.
This proves the request and verification path, not a later scheduled extension.
The [network check](evidence/index.md#evidence-046)
reached LinkKeys. Connections to the Kubernetes API, approval service, and
PostgreSQL failed under the same network policy. The probe had no application
keys. API permission checks refused Secret reads, Deployment changes, and exec.

These keys are not cryptographically limited to renewal. A compromised service
could sign as the applications. Network policy and lack of other credentials
reduce this risk. They do not remove the authority of the signing keys.

Public Job logs include key IDs, expiry times, and attestation hashes. They do
not include private keys. Successful and failed Jobs have bounded retention.
Automatic alerts are not connected. Monitor failed Jobs and expiry times.
The service warns when a key has less than seven days left. Plan key rotation
before 2026-11-17. Renewal alone cannot extend that date.

Operator commands:

```sh
node scripts/build-release-renewal-image.mjs --push
node deploy/release-renewal-service.mjs --check
node deploy/release-renewal-service.mjs --apply
node deploy/release-renewal-service.mjs --run-once
node deploy/release-renewal-service.mjs --verify
```

The installer accepts only the two enrolled release applications. It refuses a
changed existing key Secret. Rotation is a separate operation. The verification
report binds the completed Job, pod, image, six receipts, and deployment snapshot.
A later intended Deployment change can invalidate that snapshot comparison.
Retain the original proof; use the general live check for current health.

## Local checks and operator commands

Run the tests from the Magi repository:

```sh
npm run test:releases
MAGI_PLAYWRIGHT_MODULE=/absolute/path/to/playwright/index.mjs \
  node scripts/test-release-browser.mjs
```

The first command runs three passes. It tests login state, account checks,
review binding, duplicate commands, withdrawal, restart recovery, lost replies,
failed checks, failed restoration, external changes, and HTTP behavior.
The browser test uses Firefox and synthetic RP operations. Its test proxy
intercepts the application and identity-provider origins. These tests do not
prove a live human login or a live Kubernetes rollback.

The [release test report](evidence/index.md#evidence-032)
has three passes of 58 checks. This includes actual child-process termination
with `SIGKILL` before and after approval commit. The controller restart tests
use injected failures and a simulated target. The [browser report](evidence/index.md#evidence-029)
records machine submission, sign-in, review, approval, and withdrawal without
a file upload or download. The [full regression report](evidence/index.md#evidence-033)
has three passes of 208 Go checks and 639 Node checks. Race detection, vet,
generated-contract checks, and documentation link checks passed.

The [initial controller report](evidence/index.md#evidence-037)
has three passes of 63 checks. Later changes to installation and evidence
scripts are not covered by that run. The
[focused controller report](evidence/index.md#evidence-065)
has one pass of 47 checks against the later source. It covers token-file
rotation, adapter limits, generated manifests, and simulated crash recovery.
It does not cover network services, live admission, or a real controller kill.
The later [release report](evidence/index.md#evidence-040)
has three passes of 71 checks, including actual child SIGKILL and separate
proof checks for clean restart and crash recovery. The
[full regression report](evidence/index.md#evidence-041)
has three passes of 208 Go and 639 Node checks. Race detection, vet, generation,
and documentation checks passed. The shared CSIL installation changed during
this work; the rerun used locally cached artifacts with the unchanged pinned
hashes. No shared installation was reverted.

Use [the image check](../scripts/prepare-release-candidate.mjs) after the builder
and target setup. It runs the baseline and candidate images in temporary
containers on an internal Docker network. Neither container gets credentials or
host directories. A separate probe checks readiness and a CSIL Discovery call.
The script also checks the candidate's embedded binary against the build receipt.
It removes its test containers and network but keeps the evidence.

```sh
node scripts/build-image.mjs --push
node scripts/prepare-release-candidate.mjs
```

The unsigned candidate is `~/.config/magi/profiles/release/candidate.cbor`. The exact
candidate, image checks, source hashes, recipe hash, and binary hash are recorded
in [the candidate report](evidence/index.md#evidence-060).
The candidate expires after one day. Changed target configuration or generation
requires a new candidate and approval.
The `schema_changes: none` field means no persistent-data migration. The API
schema description can change between the two images.

The build or orchestration system submits the candidate. These commands are
operator commands, not human approval steps:

```sh
node release/submit.mjs ~/.config/magi/profiles/release/controller.json \
  ~/.config/magi/profiles/release/candidate.cbor
node release/run.mjs ~/.config/magi/profiles/release/controller.json \
  --watch
```

The submit command returns the review URL. Send that URL to the approver.
The listener waits for an explicit approval, retrieves it, and runs the
controller. Submission alone does not cause a deployment. Keep the listener
outside the candidate environment. `--once` performs one delivery and controller
step for diagnostics. Restart `--watch` with the same database to resume work.
Idle event polling runs every ten seconds. Failures increase this interval to
a maximum of sixty seconds. Active controller steps run each second. This
keeps recovery separate from event polling. Two-second polling caused repeated
TLS connection resets during the live test. Each proof check opens several
LinkKeys connections. The LinkKeys default handshake limit is sixty per minute
per source, with a burst of twenty. No shared LinkKeys limit was changed.

The existing operator commands remain available for recovery and evidence:

```sh
node release/run.mjs ~/.config/magi/profiles/release/controller.json \
  --resume candidate-id
node release/run.mjs ~/.config/magi/profiles/release/controller.json \
  --evidence candidate-id
node release/run.mjs ~/.config/magi/profiles/release/controller.json \
  --retry-unstarted candidate-id
```

Use `--retry-unstarted` only after a release stopped with `approval-unavailable`
before any promotion intent. The command refuses all other terminal reasons
and any history of an attempted change. It verifies current authorization,
candidate expiry, the original target, and baseline health. It retains the
failure and records the retry. Only this path starts a new observation period
for a release that never attempted a change. It does not extend an observation
or restoration period after a promotion intent. Withdrawal or target drift
prevents the retry. The normal controller checks authorization again before
the first change.

The old `--admit` file command remains an operator-only diagnostic path. It is
not part of the human review flow. An admitted release can restore
its baseline after the approval expires. Withdrawal prevents new admission;
it does not cancel an admitted release. Restoration has its own fixed deadline.
An unsuccessful restoration stops in `recovery_required`. It does not start
an unlimited sequence of version changes. The installed controller uses its
own PVC and projected credentials. Use the current PVC
database, not the retained local copy, for recovery and evidence.

The test uses the operator's kubeconfig. The adapter restricts its operations,
but the kubeconfig can have broader permissions. The new service uses the
restricted role and projected token described above. Test the API-side limits
before starting it. Do not put the operator's kubeconfig in a pod.

## Authority

The private operator policy selects the approver's canonical account and
domain. It binds them to the isolated Example environment target. The
[configuration guide](../config/README.md) describes private storage and the
public example. The example grants no authority. A login name
alone grants no permission. Each approval still needs authenticated evidence
of this exact account and approval of the exact candidate.

The first target is an isolated Magi candidate installation in Example environment.
The approver's identity domain is not the deployment domain. Production domains
are excluded. Partner environment remains a separate federation installation, not an
implicit second deployment target.

Use the [tested allowance approval boundary](identity-service.md): browser-bound
login state, one-time callback consumption, short application sessions, matching
origin, CSRF checks, review of exact bytes, and durable approval before publication.
An application signature identifies the approval recorder. It does not prove
that the human approved those bytes. The receiver must explicitly trust the
recorder for this role.

The current tested assertion provides its issue time. It does not prove the
time of a password entry or a particular factor. If release policy requires
assurance that the protocol cannot prove, refuse the request. Do not silently
reduce the policy.

## Exact candidate

The release record must identify all of these inputs:

- Source snapshot, patch, and base revision or base-content digest.
- Immutable artifact digest and build recipe digest.
- Test evidence references and the artifact that each check covered.
- Exact deployment target, installed configuration digest, and control generation.
- Current known-good artifact and the recovery procedure.
- Database compatibility declaration, required checks, deadlines, and expiry.
- The requesting application, responsible human, and applicable source policy.

Changing any input requires a new candidate and approval. A task's passing
source check does not prove that a later built artifact passed. The builder must
link the source, recipe, output digest, and artifact checks. Keep registry and
deployment credentials outside the task environment.

Retain the exact approved bytes, authentication evidence reference, approver,
approval service, approval time, and publication receipt. A repeated approval
command with identical bytes returns the same record. Conflicting bytes fail.
The release receiver checks current permission and approval status at admission.
It admits the release once. A lost reply is not permission to repeat deployment.

## Independent recovery controller

Run the controller outside the candidate's process and failure boundary. It
does not call the candidate model to decide whether to restore the last tested
version. Retain an independent operator access path.

Use the existing Corndogs workflow and durable operation journal for release
work. Keep deployment observations and effect receipts separate from editable
task state. Record an intended operation before each external change. Read the
actual target and resource revision to resolve a lost reply. Do not replay an
uncertain change from a timeout alone.

The first recovery trial must use a separate stateless candidate installation
and disposable data. It must not replace the current public Q&A service.
Checks must cover readiness and a synthetic protocol operation. One health
endpoint alone is insufficient. Bind every observation to the expected artifact
and configuration generation.

Keep separate expected protocol responses for the candidate and baseline. Their
schema descriptions can differ. A readiness response can have status 204 and no
body. Protocol probes must send the configured Host header. Deployment annotation
changes also advance the generation, including a same-image recovery fence.
This follows the [Kubernetes Deployment update strategy](https://github.com/kubernetes/kubernetes/blob/master/pkg/registry/apps/deployment/strategy.go).

The controller has a fixed observation deadline and failure threshold. An
unsuccessful candidate causes restoration of the known-good artifact and route.
Check the restored service. Stop in a recovery-required state if restoration
cannot be confirmed. Do not alternate between versions in an unlimited loop.

Do not include data migration in the first trial. Later stateful releases need
backward-compatible schemas during the rollback window and worker fencing or
draining. Restoring a binary does not undo data changes or external effects.
An incompatible migration needs a separately approved recovery plan.

## Required tests

1. Refuse the wrong account, application-only approval, changed candidate,
   changed target, expired session, replayed callback, and missing assurance.
2. Preserve exact approval after recorder restart and lost publication replies.
3. Refuse candidate code access to release credentials and policy editing.
4. Test a healthy candidate, failed readiness, failed synthetic behavior,
   delayed observations, and a controller restart during each external change.
5. Restore the known-good version after an unsuccessful candidate. Confirm its
   protocol behavior. Retain both the failed candidate and recovery evidence.
6. Stop safely when rollback fails or the target changed outside the controller.
7. Verify that the current Q&A deployment and other cluster systems are unchanged.

## Next Actions

The live controller-crash recovery test passed. Keep the controller outside
the candidate failure boundary. The Corndogs adapter and scheduled attestation
renewal are installed. The approved queue-to-rollback test passed. Retain its
cross-store proof with the release audit.
Add renewal failure alerts and key rotation before key expiry.
Test worker fencing before a wider deployment.
Future proposals must use the current generation and healthy image as their
baseline. Each changed proposal needs its own exact approval.
The task exercise can now export and stage its checked source without changing
this checkout. Connect that artifact to an isolated build and a new exact
release proposal. Staging alone does not authorize deployment.
