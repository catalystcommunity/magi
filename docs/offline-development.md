# Offline development exercise

Updated 2026-10-20. The local demo can run a bounded Python development task.
The task changes a copy of `harness/formatting.py`, checks it, and waits for
review. It cannot change this checkout or deploy a release.

Use [the conversation demo](conversation-demo.md) to start the system.
Select **Start task** in the browser. Text and speech sessions can continue
while the separate task worker runs. Use public test data only.

## Components and authority

The workflow service publishes `offline-development/v1`. Corndogs owns its
task state. The workflow has a development call, a human review, and a finish
step. Longhouse is not required for this exercise.

`workshop` composes the workflow, evidence store, model API, and executor.
`harness/loop.py` proposes actions. It has no model credential, executor,
deployment key, or durable local context store. The parent stores each model
request before dispatch and the response before it acknowledges the exchange.
It applies the same rule to tool proposals and results.

The installed executor, outside the proposal loop, permits only three tools:

- Read `solution.py`.
- Write the complete `solution.py`, up to 16384 bytes.
- Run the installed formatting checks, with no caller-selected arguments.

The model cannot select a shell command, host path, image, network, or secret.
Python in the candidate is still arbitrary code inside the container. The
container controls limit its resources. Tool names alone do not make code safe.

The loop has six model turns. It requires a successful read before another
tool. A final report requires a passing check after the last write. Invalid
JSON uses a bounded turn but runs no tool. An unknown tool outcome stops the
loop for investigation. It does not cause automatic replay.

The local model adapter requires schema-constrained JSON output. The schema
places the action name before its arguments. In the measured small-model run,
arguments-first output caused repeated reads. This ordering is an adapter
choice, not a requirement for other model protocols.

## Container profile

The profile is `python-offline-demo-v1`. The image is pinned in
`execution/container.go`. It contains Python 3.13 on Alpine.

| Control | Limit |
| --- | --- |
| User | UID and GID 65532 |
| Network | None; no published ports |
| Host bindings and credentials | None |
| Root filesystem | Read-only |
| Linux capabilities | All removed; no new privileges |
| CPU and memory | One CPU; 256 MiB; no extra swap |
| Processes | 64 |
| Workspace | 64 MiB memory-backed filesystem |
| Temporary files | 16 MiB memory-backed filesystem |
| Installed check | Eight seconds; 32 KiB output |
| Environment lifetime | Fifteen-minute hard deadline |

The supervisor enforces the hard deadline if the coordinator is lost.
The executor verifies the recorded container identity and controls before
commands and removal. This is a local Docker prototype, not a microVM or a
claim that hostile foreign code is safe. Docker access remains an operator
capability. Task code does not receive the Docker socket.

Microsandbox, Lima, mise, native maintenance tasks, directory bindings,
Windows, and macOS remain separate execution work. This exercise does not
select Docker as the only Magi backend. It exposes no public execution API.

## Live participation and checks

**Hold and join live** records a hold and returns a local shell command.
It does not pause the loop or cancel a command. Disconnecting the shell does
not release the hold. Use **Release hold** explicitly. The admitted hard
deadline still applies.

The direct shell is not yet captured by Magi. Its input and output do not
appear in the evidence archive. The hold request and installed tool calls do.
Do not use this path for private data or credentials.

An independent final check runs after the model loop. It hashes the exact
source bytes that it checks. The exported candidate must have that hash.
A concurrent edit produces `candidate_changed`, not `candidate_ready`.
Failed checks produce `checks_failed`, even if the model claims success.
These ordinary unit checks do not prevent deliberate test tampering by the
candidate. Review the source as well as its test result.

Review names the retained candidate digest. It records a graph interaction
and completes the workflow's human step. The local browser uses a synthetic
test application. This is not authenticated human release approval.

The private operator policy selects the human release approver for Example environment.
Verify its canonical account and domain through LinkKeys and obtain
approval of the exact release candidate and target. The [release prototype](release-demo.md)
is separate from task review. No production-domain deployment is allowed.
The task worker has no promotion authority.

## Evidence and recovery

The workshop keeps the task, initial source, image, policy, model request,
provider response, tool decision, output, candidate, and digest in PostgreSQL.
The source interaction and review link to the conversation graph. Workflow
call evidence links to the development record. Removing the environment does
not remove these records.

Creation has a durable intent before the Docker call. If creation succeeds
but its reply or database binding is lost, expiry cleanup finds the exact
named container and verifies its identity and controls. It does not start it
or repeat the task. Cleanup records its target before removal. A lost removal
reply can be resolved by confirmed absence. An unavailable Docker daemon is
not proof of absence.

The database is a single local PostgreSQL instance. These checks establish
process and lost-reply behavior, not disk-loss recovery or multi-host failover.
Secret-reference capture exists in a separate experiment; it is not connected
to this task path. No secrets are admitted here.

## Source export and staging

The local demo API now provides `GET /api/development/<run-id>/source`.
It requires the same loopback host, origin, and private token as other demo
requests. It reads the workflow and evidence under the configured owner.
This is a local demo route, not a public authenticated build service or a new
CSIL component.

The response is a versioned [source bundle](../workshop/source.go). It contains
the owner, evidence reference, original source, edited source, their hashes,
and three retained tool receipts: seed, final check, and export. The final
check must pass for the exact exported bytes. Missing results, changed bytes,
failed checks, and unchanged source are refused. The installed image and tool
policy must match. Environment removal does not prevent a later export.

The only admitted source path is `harness/formatting.py`. The agent's temporary
`solution.py` maps to that fixed path. The bundle cannot select another file,
command, image, host, or credential. It does not contain a build recipe.

The [staging command](../cmd/magi-source-stage/main.go) reads this JSON response
from standard input. Build the command with:

```sh
go build -o .build/magi-source-stage ./cmd/magi-source-stage
```

An authorized caller can send the API response directly to this command.
Use `-checkout` to select the source checkout and `-output-parent` to select
an existing artifact directory. Both are operator arguments, not agent inputs.
The command compares the exact current source with the retained base. It
refuses a changed base and symbolic links. It then creates a new directory
with `base/harness/formatting.py`, `source/harness/formatting.py`, `bundle.json`,
and a final `manifest.json`. It does not overwrite an existing artifact.
It copies no other checkout files. A failed write leaves an incomplete
artifact for inspection. A later build must check its inputs again.

Staging does not execute the candidate, run a new test, build an image, edit
this checkout, or approve deployment. The manifest states these limits.
Hashes establish integrity, not source authority. Obtain the bundle through
an admitted caller. A separate build and release process must apply current
policy and obtain approval of the exact proposed release.

## Source review

All files in `harness`, `execution`, and `workshop` were written for Magi.
No lo-agent or Reactorcide code was copied. The review used the MIT-era
lo-agent boundary `9e1c6d7d0fc30d816a250c0d66c0fb3d7d6c323e` for design ideas.
It did not import current restricted lo-agent code. The key lesson is to
record requests before effects and avoid replay when tool results are missing.

The Reactorcide review confirmed that its older `runnerlib/src/container.py`
is a compatibility path. Do not treat it as the current worker lifecycle.
The new prototype does not use Reactorcide job submission or its coordinator.

## Verification

```text
npm run test:demo
```

The repeated suite uses deterministic model replies, real Python, disposable
PostgreSQL, Corndogs, and offline containers. It checks receipt ordering,
candidate retention, live holds, cleanup, lost bindings, and service recovery.

`scripts/test-task-browser.mjs` uses the real local model. Set the same
Playwright module and private local URL variables as the conversation browser
test. It joins a running container, records a fixture review, removes the
environment, and checks that evidence survives. The test does not approve a
release or change the source checkout.

Real-model browser reports `2026-10-18T00-36-44.554Z-task-browser.json` and
`2026-10-18T00-38-35.829Z-task-browser.json` under `test-results` passed with
the same candidate digest. Each used six model calls and nine tool calls.
The second overlapped the passing speech and conversation browser test.
Both retained binary hashes and reported no browser script errors.

The [source-staging regression](evidence/index.md#evidence-055)
passed three times with 230 Go and 639 Node checks per pass. Race detection,
vet, generated contracts, and documentation links passed. The source tests
check exact evidence, missing or failed receipts, changed bases, fixed paths,
symbolic links, and separate artifact directories. This is not a live release
of agent-generated source.

The [demo integration report](evidence/index.md#evidence-054)
has three passes of 38 Go checks and six Python checks. It uses deterministic
model replies, real Python, disposable PostgreSQL and Corndogs, and offline
containers. It confirms that the source bundle is unchanged after environment
removal. The new source API route has not had a real-model browser test yet.

## Next Actions

Add captured shell sessions, explicit context updates, separate pause and
command cancellation, and a standalone execution contract. Connect current
policy and secret-reference capture before credentialed work. Add independent
build isolation and source provenance before applying candidate code. The
separate release prototype has passed live approval, rollback, controller-crash
recovery, and Corndogs task completion. Connect staged source to a fixed build
recipe and a new release proposal. Do not treat task review or staging as release
approval.
