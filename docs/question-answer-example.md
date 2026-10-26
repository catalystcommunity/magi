# Example environment question and answer testing

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

The protected workflow API runs at
`https://workflows.catalystsquad.local/csil/v1/rpc`. Use the CLI to send a
question and inspect its evidence. There is no browser Q&A interface yet.
Use public test questions only. This is not a private-data deployment.

The existing discovery site at `magi.catalystsquad.local` is unchanged.
The [local development setup](question-answer.md) also remains available.

## Send a question

The private profile on this workstation is `~/.config/magi/profiles/qa/client.json`.
It uses the configured LinkKeys test identity. The application is
`opal-qa-cli`, instance `workstation-1`. It is not a production identity.
Do not print or share this file: it contains a signing key.

From the repository root:

```sh
printf '%s\n' 'What is 2 + 2?' | .build/magi-qa ask \
  -profile ~/.config/magi/profiles/qa/client.json -run remote-first-question

.build/magi-qa read \
  -profile ~/.config/magi/profiles/qa/client.json -run remote-first-question

.build/magi-qa evidence \
  -profile ~/.config/magi/profiles/qa/client.json -run remote-first-question
```

Use a new run ID for a different question. An identical request with the same
ID returns the existing run. It does not repeat the model call. If a wait ends
with an error, read the run and its evidence before taking another action.

The CPU model is Qwen2.5 1.5B Instruct Q4_K_M. Its weights and source commit
match the local model pins. The container uses a CPU build without AVX.
The tested cluster node exposes no AVX, AVX2, FMA, or F16C features. A cold
arithmetic call took about 48 seconds. Later cached calls took about nine
seconds from retained request to retained response. These are test results,
not latency guarantees. Short questions and short answers are suitable here.

The CLI checks public run state every five seconds. It checks pending call
evidence at most once every fifteen seconds. It can retry an explicitly
retryable read failure until the wait deadline. It does not retry submission
or model execution automatically. The current resolver opens three LinkKeys
TLS connections per authenticated operation. Faster polling can exceed the
default LinkKeys handshake limit.

## Authentication and authorization

LinkKeys authenticated the configured test account during application enrollment and attested
three Ed25519 application keys. The CLI signs each workflow request. The API
checks the signature, recipient, timestamp, current key evidence, and domain
trust. A separate `opal-workflows` account supplies the API's RP credential.
Only API containers receive that credential.

Magi grants the exact CLI application `read`, `write`, `run`, and `execute`
in its `qa-test` owner scope. LinkKeys does not grant these permissions.
The worker accepts only that actor, scope, and Q&A definition. Another
authenticated application does not receive access automatically.

Application keys expire on 2026-11-15 at about 01:21 UTC. Their initial
attestations expire on 2026-10-17 at about 01:21 UTC. Before testing on another
day, renew the attestations:

```sh
node deploy/qa-bootstrap.mjs --renew
```

This command uses the target key's possession proof and two sibling signatures.
It does not use the browser password. LinkKeys can return the current attestation
without signing a new one while more than half its lifetime remains. The command
prints the actual expiry times. Attestation renewal does not extend key lifetime.
Replace the keys before their key expiry. No renewal schedule is installed.

## Deployment boundary

All runtime resources are in `magi-example`:

| Component | Copies | Access |
| --- | --- | --- |
| Workflow API | Two | Public CSIL over the existing TLS route |
| Coordinator | Two | Internal database and Corndogs only |
| Timeout evaluator | One | Internal database and Corndogs only |
| Question worker and model | One pod | Model binds to pod loopback only |
| PostgreSQL | One | Verified TLS and restricted pod access |
| Corndogs 0.8.0 | One | Verified TLS and restricted pod access |

PostgreSQL has a 4 GiB Longhorn volume. Corndogs has a separate 2 GiB volume.
Both stores are single-instance test services, not a high-availability design.
Do not delete these claims to restart a service.

Runtime processes use a restricted database role. It cannot change workflow
grants, delete evidence, or create schema objects. Administrator credentials
are used for migration and policy setup, not by runtime containers.
No pod receives a Kubernetes API token. Model containers receive no database
or LinkKeys credential. Network policies permit only the required connections.
The model has no public Service or route. Model logging is disabled; the
workflow store retains bounded request and response evidence.

The deployment adds no cluster role and changes no router, gateway,
certificate, or shared workload. It uses the existing Magi registry credential.
The only LinkKeys account changes are the new Magi service account and the
new application instance under the configured test account.

## Repeat deployment

The setup files under `~/.config/magi/profiles/qa/` contain private keys and passwords.
Keep that directory private. The bootstrap helper uses the compiled LinkKeys
test SDK in `experiments/identity-service/.sdk/`. On a fresh workstation,
prepare the pinned SDK with `node experiments/identity-service/prepare.mjs`.
Use `node deploy/qa-bootstrap.mjs --enroll` only for initial enrollment.
Use `--render` to regenerate configuration from existing private setup state.

```sh
node scripts/build-qa-images.mjs --push
node deploy/qa-apply.mjs --check
node deploy/qa-apply.mjs --apply
node deploy/qa-apply.mjs --expose
```

The image helper builds the pinned llama.cpp source archive. It does not change
the shared checkout. The archive build reports `b0-unknown`; use the recorded
source commit, weight digest, and immutable image digest for provenance.
Base images are pinned. Compiler packages come from the base distribution's
package repository; bit-for-bit rebuilds are not established.

The apply helper checks cluster identity, ownership, and hostname conflicts.
It stores credentials without a last-applied annotation. It creates stores
before migration and starts runtime roles after migration and policy setup.
Each role waits for verified store TLS before startup. The route is a separate
step. Bootstrap Job names include a content digest to avoid changing an
immutable Job template on an image update.

Stop older roles before a future schema-version migration. This helper does
not provide a general online migration procedure. The discovery deployment
helper preserves the existing namespace quota, so it cannot reduce Q&A capacity.

Internal server certificates expire after ninety days. The CA expires after
one year. Certificate rotation, volume backup, storage alerts, and restore
reconciliation are still operator work. Do not call this a production service.

## Verified behavior

The [live report](evidence/index.md#evidence-016) records
three passes through public TLS and real LinkKeys authentication. Each pass
checks refusal of an unsigned request, an invalid signature, an ungranted
scope, a wrong recipient, and a stale proof. It then checks a real model answer,
retained evidence, and an identical retry with unchanged call evidence.
API pod replacement also retained identical evidence. This is not a database
restore test or a two-domain workflow test.

The live LinkKeys server used version 0.16.3. The ordinary-question run
`example-white-dwarf` also returned an answer and retained its evidence.
Its model call took about 42 seconds.

The [core regression report](evidence/index.md#evidence-018)
records 174 Go checks and 485 Node checks in each of three passes. Race detection,
vet, generated-code checks, and document-link checks passed.

The [Q&A suite](evidence/index.md#evidence-017) passed
36 checks per pass, three times, with race detection. Setup failures and their
diagnostics are retained in the
[setup report](evidence/index.md#evidence-015).
Inactive setup Jobs were removed after their logs were saved. No volume,
account, task, or evidence record was deleted.

## Next Actions

Test ordinary public questions with the deployed profile. Add authenticated
browser interaction, bounded identity caching or connection reuse, key rotation,
and operational alerts. Keep partner.local independent for cross-domain tests.
Add source policy and secret-reference capture before private or credentialed
work. A model answer alone is not an accepted knowledge record.
