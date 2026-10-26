# Execution environments

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

Updated 2026-10-05. Status: accepted boundaries with proposed API mechanisms.
Update 2026-10-17: a limited [offline development exercise](offline-development.md)
now implements local container allocation, bounded tools, live holds, and
retained evidence. The broader mechanisms below remain design requirements.

Magi owns task execution. Reuse selected Reactorcide components without making
its coordinator the Magi execution service. Keep model inference separate from
the environment in which tools run. Kubernetes is an optional backend.

## Environment composition

An environment definition describes the required tools and operating system.
An environment instance is one admitted allocation. A command operation is one
invocation inside that instance. Several commands can share one workspace.
By default, allocate an ephemeral environment for one task attempt. Reuse or
sharing across attempts needs explicit admission. A human hold can extend its
lifetime without extending the task's credentials or command authority.

Compose these fields independently:

| Field | Meaning |
| --- | --- |
| Target | Exact host identity or a set of required host capabilities |
| Mechanism | Native process, container, sandbox runtime, Windows VM, or macOS VM |
| Definition | Immutable image or VM bundle; native tool and configuration revisions |
| Bindings | Temporary workspace, named host directory, device, or local service |
| Authority | Persona identity, represented user, allowance evidence, tool admission, and local resource policy |
| Credentials | Secret references and permitted delivery methods |
| Limits | CPU, memory, disk, network, time, and spending budget |
| Lifetime | Active task uses, human holds, export requirements, and cleanup rules |

A host executor advertises supported combinations, not just a flat capability
list. Admission fails if a required combination is unavailable. Do not replace
a named host with another host or weaken isolation without new authorization.
Resolve immutable host identity separately from its current network address.

The receiver selects local resources and applies its own policy for foreign
requests. Authentication by another domain does not grant execution rights.
A foreign caller does not receive cluster or hypervisor administration access.

## Host resources and native work

Publish named resource bindings that the host resolves locally. Each binding
declares read or write access, permitted subpaths, and sharing rules. Enforce
path containment and prevent symlink or mount changes from widening access.
Arbitrary host paths require a separate explicit grant.

Native work runs under an approved local account with process supervision and
platform-specific limits. Record controls that the platform cannot enforce.
Reject a request whose required controls are missing. Native execution is not
a sandbox claim. A container with a writable host mount can also change durable
host data. Cleanup removes only owned temporary resources, never bound host data.

For a speech-server test, select that host and bind its model cache read-only.
Use a separate scratch directory for output. Testing the API does not grant
permission to restart or replace it. Service maintenance requires its own
operation and permission. Avoid scheduling all repair work through the service
being repaired; retain an independent administration path.

Treat desktop automation as a separate capability from a headless OS command.
An available Windows or macOS VM does not prove an approved desktop session.

## Reuse and provisioning

Review Reactorcide components for container and VM lifecycle, image retrieval,
cloning, process termination, resource measurement, and secret handling.
Telemetry code is also a candidate, subject to archive delivery requirements.
Do not import CI project defaults, VCS admission, or its workflow controller.
Select shared libraries or maintained copies after a code and dependency review.

Use pinned Webi or mise tools to prepare reusable environments. Resolve image
and bundle tags to immutable digests before admission. Record tool versions and
native configuration revisions too. An image digest identifies bytes, not trust.
Keep source revision, build evidence, and environment digest linked.

Cache immutable downloads where useful. Keep mutable workspaces and credentials
separate between tasks unless sharing is explicitly authorized. A cache hit
does not authorize a task to read another task's private data.

[Daytona](https://www.daytona.io/docs/en/) is an API reference for environments,
commands, sessions, and files.
It is not a selected runtime dependency. SSH and Kubernetes exec can implement
parts of the transport. They must not become Magi's public authority model.

## Coordinator and executor ownership

The coordinator owns admission, placement, plan revisions, and environment
records. The host executor owns local resource binding, process control, and
cleanup. The secret service owns resolution and credential issuance policy.
These are logical boundaries; they do not require separate services initially.

Keep assignments, operation receipts, and human holds in durable records.
Coordinator replicas use atomic claims and revisions to avoid competing writes.
Corndogs remains the authority for task state. Environment state describes a
different resource; it is not a second editable task status.

Each command carries its operation ID, input digest, environment ID, and current
control generation. The executor rejects commands from an older generation.
A repeated ID with different input is a conflict. Recover uncertain command
admission before resubmission. Persist result publication and retry it after
coordinator failover.
Bind command equality to admitted secret references and binding IDs. Include
available version metadata without requiring historical-value retrieval.
Do not publish hashes of secret-bearing input as command fingerprints.
A replacement credential changes the binding; it is not an unnoticed retry.

A process can continue after a connection fails. Define local deadlines and
the permitted duration of disconnected operation. Stop new protected work when
required authorization expires. Stopping a process or rejecting its late result
does not undo an external effect. Reconcile such effects before retry.

## Identity and credential lifecycle

Workers use the responsible persona's stable LinkKeys identity. Record each
session, task, agent instance, and attempt with internal IDs. These are not
temporary authenticated identities. A persona has versioned voice, personality,
and focus settings; changing them does not replace its identity.

When representing a user, supply the applicable allowance evidence. Each
receiving service decides what to honor. Tools can also authenticate through
selected SSH keys, JWTs, or other credentials. Record those bindings separately
from the persona and represented user.

Stable persona and host enrollment, renewal, and recovery need supported
protocols. Temporary task identity enrollment is not an Magi requirement. See
[Personas sessions and trust](personas-sessions-and-trust.md).

A dedicated Magi identity domain is permitted. Its name and setup remain open.
This does not replace catalystsquad.local and partner.local as the primary and
secondary test domains. Trust specific foreign identities for specific purposes.

Keep installation keys, enrolled host keys, and task credentials separate.
Long-lived credentials can remain necessary for enrollment, decryption, or
external credential issuance. Keep them with the component that needs them.
Prefer local key generation. Do not distribute domain administration keys to
workers. Provide rotation, revocation, and recovery when Magi itself is down.

Inject credentials directly when the task's tools need them. An Engineering
Manager can arrange an SSH key for GitHub or a JWT for a service without
LinkKeys support. Select delivery methods per tool and task. A mediated adapter
is an option, not a universal requirement. Persona credentials are not subject
to a universal proxy-only rule either; their delivery needs the same review.

Resolve admitted secret references. Do not routinely provide general access to
the secret store. Prefer short-lived or narrowly scoped credentials where
available. A credential can still permit more than the task needs. Record that
trust decision and apply the selected tool approval and environment controls.
Scope review does not narrow the credential's permissions by itself.

Short-lived credentials can enter an authorized session's context when needed.
This includes model context only when its destination and data policy permit
that disclosure. Long-lived credentials stay outside context. Register every
issued credential and its reference before use. Persistent context snapshots,
logs, and evidence contain references, not values. See the
[archive contract](evidence-archive.md).
At the end of authorized use, remove injected copies and cached values. Revoke
session-issued credentials where supported; otherwise enforce their expiry.
Do not revoke a shared source credential merely to remove one task's copy.
A human hold does not renew credentials or preserve an expired grant.

## Tool evaluation

Magi evaluates proposed tool use outside the worker's context. It assembles
the relevant task scope, plan, policies, arguments, credential references, and
observations from the evidence graph. A worker's explanation is evidence, not
permission to expand its task. Record the inputs and result of the evaluation.

Separate deterministic restrictions from model judgment and trusted behavior.
An approved shell command can start programs that use injected credentials.
Do not claim that reviewing that command proves every subsequent action stays
within scope. Declare what the executor and credential issuer actually enforce.
Apply source privacy and processor trust to the evaluation context too.

## Human participation

Authorize inspection, command submission, shell attachment, plan changes, and
secret use separately. A shell can read credentials already present under its
OS account. Do not grant it to a person who lacks authority over that data.
Inspection-only access needs enforceable read-only operations, not a UI label.

Joining is live. It does not pause an agent or cancel running commands. Pause,
resume, and command cancellation are separate requested API operations available
through voice, web, or CLI clients. A lifetime hold also does not pause work.

A human can separately request control changes. Record those changes and
affected resources. Shared mutation requires a defined rule:
for example, separate worktrees or version checks. Do not assume a shared
filesystem is unchanged after a human edits it.

Do not inject file-watcher notifications into agent context. A user can tell
the initiating persona about a change when useful. That persona decides what
additional context to send to the worker. Evidence collection and ordinary
tool reads remain separate from this context decision.

A plan change creates a new revision. Record which commands are retained,
cancelled, or still uncertain. Recheck resource versions before new commands.
New permissions or secret access require new admission, not just a plan edit.
Retain the previous plan and the evidence used to change it.

Proposed lifetime mechanism: a human obtains a durable hold on the environment.
The hold is separate from command authority and task state. Task completion,
shell exit, or a lost client connection does not release it automatically.
Release is an authenticated operation with an idempotent receipt.

Cleanup requires no active task uses, investigation uses, or human holds.
Required evidence exports must also be acknowledged. Check these conditions
atomically against a new hold or attachment. Once cleanup starts,
reject new attachments. Releasing one hold does not remove other holds.

Permission withdrawal can stop human access without implying that the user
released retained data. Emergency termination and hard resource ceilings are
explicit operator actions or admitted policies. Record any forced termination
and incomplete export. Never promise indefinite availability after host loss.

## Failure investigation

Magi decides how to investigate a failure and handle its environment per task.
Do not use one failure cleanup timer or require human release for every failure.
Gather available evidence and test assumptions before requesting human help.
Retain the resources needed for that investigation under the admitted limits.

This applies beyond code. A failed service request can require receipt checks.
A failed media task can require its input and output artifacts. Some tasks need
a human action rather than more computation. Magi can arrange that involvement
through the normal interaction and assignment APIs.

Record observations, assumptions, checks, remaining uncertainty, and the chosen
next action. Preserve the failed attempt. New attempts or changed plans do not
erase it. Reconcile uncertain external effects before a retry. Continue within
existing authority and budgets; investigation does not create new permissions.

## Command API requirements

Provide command start, status, input, output reads, cancellation, and authorized
terminal attachment. Support reconnect through stable operation and stream IDs.
Capture output from attached commands; do not assume the environment's initial
stdout includes a separate SSH or exec session.

Define argument arrays and shell commands as distinct input forms. Record the
shell and version when shell interpretation is requested. Input, output, and
file operations have size limits, flow control, and explicit end markers.

Import and export files through authorized paths and manifests. Reject archive
path traversal. Separate process exit, artifact publication, and environment
cleanup outcomes. None of these implies that an external business effect
occurred exactly once.

## Next Actions

Review the proposed records and APIs in [Contracts](contracts.md). Implement
no extraction before selecting the smallest reusable components. Prove exact
host placement, resource bindings, human holds, coordinator failover, and
credential handling with the [acceptance tests](test-environments.md). Include
live joining, independent pause and cancellation, graph-based evaluation, and
failure investigation for tasks that are not code tasks.
