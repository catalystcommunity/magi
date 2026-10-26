# Magi

Magi is licensed under the [Apache License, Version 2.0](LICENSE).
Third-party components retain their own licenses and notices.

Magi is a planned system for personal, household, and business automation.
It connects identities, knowledge, workflows, models, tools, and devices.
Services communicate through APIs. Human interfaces are clients of those APIs.

Status: native protocol, a standalone durable event service, and a deployed
read-only discovery service. The event service has a separate protected API.
It is not yet deployed or connected to the Magi coordinator.
The Corndogs adapter now has a durable operation journal, result storage, and
internal human-task bindings. An independent workflow service now adds typed
definitions, protected APIs, and an initial call, human, and finish interpreter.
A local graph editor uses the same definition records. Longhouse is optional.
The workflow service is a development prototype with an isolated public Q&A
deployment. It is not a general task runner.
Its separate coordinator process recovers admitted transitions automatically.
It does not execute tools or complete human steps.
A separate timeout evaluator releases expired call claims to reconciliation.
New tool calls require its readiness check. Expiry does not repeat a tool call.
The services use Go. Browser clients use generated TypeScript and JavaScript.
The first question worker uses Python. CSIL contracts connect the clients and
services. A [local question and answer setup](docs/question-answer.md) now runs
the signed CLI, durable workflow, Python worker, and a small local model.
It retains model evidence. Use test data only. Local setup keys are not LinkKeys.
A [Example environment Q&A deployment](docs/question-answer-example.md) now runs the
same flow with real LinkKeys application authentication and explicit Magi grants.
The project was renamed from Opal to Magi on 2026-10-21.
The baseline is dated 2026-09-27.
A [connected local demo](docs/conversation-demo.md) now adds independent speech
and conversation APIs, text and voice interaction, and a bounded Python task
exercise. It uses public fixtures and synthetic identities. Task review does
not approve source changes or deployment.
The execution, persona, session, and trust interview was incorporated on 2026-10-05.
Knowledge, live interaction, and reinterpretation requirements were expanded
on 2026-10-06. Storage and adaptive learning mechanisms remain open.
The knowledge requirements were approved on 2026-10-09. The user approved the
record and storage proposal as a starting point on 2026-10-11. The synthetic
experiment passes its codec and storage checks. Text search does not meet the
proposed recall target.
The 2026-10-12 retrieval comparison favors combined search with evidence links
on a small synthetic test set. The support experiment shows that the small
local model is not reliable enough to select as the default evaluator.
Reference-only capture, private spool recovery, and Go/Rust capture codecs
now have local tests. W02 now passes real cross-domain login, durable allowance
approval, and receiving-service checks. Capture and knowledge integration
remain open.

The public example uses `magi.catalystsquad.local` for the W03 service.
Actual deployment addresses are selected by a private
[configuration overlay](config/README.md), not stored in this repository.
It exposes discovery and schema retrieval only. It does not accept private
data, authenticate users, or run tasks. See the [native protocol](docs/native-protocol.md)
and [deployment procedure](docs/deployment.md).

## Project rename

The project name is Magi. The Go module is
`github.com/catalystcommunity/magi`. Command directories use `cmd/magi*`.
The npm package is `magi-contracts`. The aggregate contract is
[csil/magi.csil](csil/magi.csil).

Runtime settings accept `MAGI_` names. The previous `OPAL_` names remain valid.
If both names are set, the `MAGI_` value takes precedence, including an empty
value. Experiment-only test settings retain their existing names.

The rename does not change these stored identifiers:

- CSIL wire names, signature contexts, and descriptor digests.
- LinkKeys application identities and local test domains.
- Kubernetes resource names, registry paths, and Docker Compose project names.
- Database records, queue keys, container ownership labels, and release formats.
- Browser storage keys, authentication headers, and cookies.
- Historical evidence, test reports, and remote model directories.

These identifiers retain `opal` where compatibility requires it. Do not replace
them through a text search. A later change needs its own data migration and
authorization review. Both `/schema/magi.csil-schema.cbor` and the old schema
URL return the same bytes. The generator uses the old input basename in a
temporary directory to preserve the signed descriptor. It publishes the output
with the new filename.

After a checkout directory move, rebuild local commands with the normal
`npm run dev:*` commands. Check private profiles for absolute checkout paths.
Do not move a checkout while its local workers are running. Experiment Python
environments can contain absolute paths; recreate those environments if needed.

To resume a Codex session after a directory move, use its session ID and the
new working directory:

```sh
codex resume SESSION_ID --cd /path/to/magi
```

This uses the [supported resume override](https://learn.chatgpt.com/docs/developer-commands?surface=cli).
Do not replace paths inside session transcripts. Paginated history can retain
byte offsets into those files. Keep the original transcript and session ID.

## Read the design

See the [configuration guide](config/README.md) before adding operator settings.
Use the [publication checklist](docs/publication.md) before a public commit.
The [retained evidence guide](docs/evidence/README.md) describes the private
archive and public milestone records. Generated test reports are not source.

Start with the [demo prototype design](docs/demo-prototype.md). It records the
speech, sensitive-operation authentication, harness, and conversation-graph
contracts still needed.
The [integrated release design](docs/integrated-release.md) gives the wider
delivery scope. Component test results do not mean that either is complete.

| Document | Purpose |
| --- | --- |
| [Demo prototype](docs/demo-prototype.md) | Conversation graph, replaceable speech, Python harness, and self-development demo requirements |
| [Parakeet CPU test](docs/parakeet-cpu-test.md) | Public-fixture timing on the speech host, pinned runtime, and remaining measurement limits |
| [TTS candidate results](docs/tts-candidate-test.md) | Local listening page, measured Kokoro and Qwen behavior, and repeatable voice tests |
| [Speech service](docs/speech-service.md) | Independent speech API, durable requests, native model workers, and local development |
| [Conversation demo](docs/conversation-demo.md) | Local text and voice interaction, shared inputs, context snapshots, and retained evidence |
| [Offline development exercise](docs/offline-development.md) | Python task loop, constrained container, live inspection, candidate review, and recovery |
| [Test release prototype](docs/release-demo.md) | Exact human approval, isolated candidate deployment, recovery checks, and current live-test limits |
| [Integrated release](docs/integrated-release.md) | Connected request path, implementation defaults, component contracts, and release gates |
| [Decisions](docs/decisions.md) | Accepted boundaries, proposals, and open choices |
| [Architecture](docs/architecture.md) | Components, ownership, storage, and deployment shapes |
| [Contracts](docs/contracts.md) | Identity references, signed statements, APIs, and data rules |
| [Native protocol](docs/native-protocol.md) | Compiled contract, wire limits, tests, and local development |
| [Deployment](docs/deployment.md) | Scoped Example environment deployment, live checks, and recovery |
| [Events and federation](docs/events.md) | Durable delivery, receipts, subscriptions, and recovery |
| [Standalone event service](docs/event-service.md) | Component API, local inbox/outbox helpers, policy, and recovery tests |
| [Workflows](docs/workflows.md) | Corndogs states, workers, human tasks, and workflow editing |
| [Workflow service](docs/workflow-service.md) | Independent API, initial interpreter, local graph editor, and limits |
| [Local question and answer testing](docs/question-answer.md) | Runnable CLI, small CPU model, retained evidence, and CI checks |
| [Example environment question and answer testing](docs/question-answer-example.md) | Public TLS API, enrolled CLI profile, deployment, and key renewal |
| [Workflow journal](docs/workflow-journal.md) | Durable Corndogs operations, retry bounds, results, and human bindings |
| [Longhouse integration request](docs/longhouse-workflow-contract.md) | Assignment deduplication, atomic privacy, and completion evidence |
| [Execution and knowledge](docs/execution-and-knowledge.md) | Models, isolated work, context, documents, and audit |
| [Execution environments](docs/execution-environments.md) | Magi execution, host selection, credentials, and human control |
| [Evidence archive](docs/evidence-archive.md) | Secret references, agent context, task history, and replay limits |
| [Personas sessions and trust](docs/personas-sessions-and-trust.md) | Stable persona identities, task boundaries, privacy, and evaluation |
| [Knowledge context and reinterpretation](docs/knowledge-and-context.md) | Multiple stores, scoped authority, live queries, evidence layers, and later interpretation |
| [Knowledge records and storage proposal](docs/knowledge-storage.md) | Minimum records and APIs, shared-reply example, storage comparison, and proposed measurements |
| [Knowledge experiment results](docs/knowledge-experiment.md) | Compiled CSIL, passing storage checks, measured recall limits, and remaining integration work |
| [Retrieval comparison](docs/retrieval-comparison.md) | Local keyword, model-assisted, embedding, and linked retrieval results |
| [Evidence support results](docs/evidence-support.md) | Citation checks, current-access guards, and measured model failures |
| [Evidence capture results](docs/capture-experiment.md) | Typed secret substitution, current-input preparation, and private spool recovery |
| [Interaction](docs/interaction.md) | Notifications, voice, calendars, and client sessions |
| [Test environments](docs/test-environments.md) | Two independent test domains and acceptance tests |
| [Implementation plan](docs/implementation-plan.md) | Work packages, dependencies, and completion criteria |
| [Corndogs recovery results](docs/corndogs-recovery-080.md) | Passing 0.8.0 service tests; links to historical failures |
| [Corndogs adapter contract](docs/corndogs-adapter.md) | Protected operations, durable intents, retry deadlines, and remaining tests |
| [Identity and policy experiment](docs/identity-policy.md) | LinkKeys evidence, exact allowance approval, and three-engine policy tests |
| [Authenticated approval results](docs/identity-service.md) | Real LinkKeys login, durable approval, cross-domain verification, and recovery |
| [Evidence](docs/evidence.md) | Inspected ecosystem components and external references |
| [jr2 review](docs/jr2-review.md) | Reference observations only; no code reuse or architecture change |

## Core boundaries

- LinkKeys authenticates identities and supports PKI and signed attributes.
- Each receiving service authorizes access to its own resources.
- A signed delegation allowance expresses intent. It does not force access.
- Corndogs stores task states. An independent workflow service interprets definitions.
- Longhouse owns projects and assignments, not workflows. Its integration is optional.
- Magi owns its execution environments. Workers perform admitted work.
- Reuse selected Reactorcide components without requiring its job coordinator.
- Execution can use native processes, containers, or VMs on approved hosts.
- A human can retain an environment until they explicitly release it.
- Joining is live. Pause and command cancellation are separate requests.
- Personas have stable LinkKeys identities. Workers use their persona's identity.
- Sessions bound task context and child work, not the whole visible conversation.
- Source restrictions apply across a session. Trust policy has scoped human owners.
- Inject admitted tool credentials and evaluate scope outside worker contexts.
- Archive model interactions and task evidence with secret references, not values.
- Historical secret values and temporary task identities are outside scope.
- CSIL defines native APIs and data models. Adapters connect other protocols.
- Each domain can operate independently. Federation preserves local authority.
- Knowledge mirrors accept changes from the authoritative origin.
- Magi arbitrates knowledge across sources; one graph or storage type is not required.
- Knowledge queries can use system authority without a persona or represented user.
- Voice and text can advance several sessions through one response.
- Preserve source interactions, interpretations, and actual inputs for later review.
- Model providers, clients, and execution hosts are replaceable.

Do not add Temporal or Home Assistant to the baseline. Wyoming is an optional
future adapter. The integrated release selects embedded CEL and direct FCM for
the initial implementation. Other services can use different policy engines
and notification adapters.

## Test domains

`catalystsquad.local` is the primary orchestration test domain and the established
LinkKeys domain for Catalyst Community development integrations that use one.
`partner.local` is the independent secondary domain. Each domain has its own
LinkKeys integration and separately installed system. These are two independent
systems for cross-domain tests, not two names for one installation.
Use `primary.opal.test` and
`secondary.opal.test` for disposable automated tests. These are test fixture
names, not public DNS allocations. Production identities can take part in controlled tests
against test resources. Production resource credentials stay outside the lab.

## Next Actions

Close the [demo prototype contracts](docs/demo-prototype.md) for graph records,
sensitive-operation challenges, speech, and the internal Python harness. Use the
[accepted TTS candidates](docs/tts-candidate-test.md#user-review) for the first
CPU speech service test and extend the passing Parakeet smoke test. Then implement the
connected voice, text, task, and self-development scenarios. The wider release
still needs the [integrated tests](docs/integrated-release.md). Routine component
completion is not a user handoff or proof of a complete release.

Keep the [local Q&A path](docs/question-answer.md) and
[Example environment deployment](docs/question-answer-example.md) working during
implementation. They remain public-test-data paths until source policy and
reference-only capture are connected. Device, provider, VM, and independent-domain
acceptance need the specific external setup listed in the release design.
