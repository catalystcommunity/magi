# Connected demo prototype design

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. See the
[configuration overlay](../config/README.md).

Updated 2026-10-18. Status: confirmed requirements and a partial prototype.
Conversation graphs are primary. Modes are optional interpretations of graph
segments, not conversation types. Speaker estimates support attribution;
sensitive operations can require stronger authentication through LinkKeys.
The local speech, conversation, and bounded task paths have tests. The isolated
release canary passed live promotion and rollback tests. The wider public demo,
full execution contract, and stateful release recovery remain open.

Use this document for the first useful demo. Use the
[integrated release design](integrated-release.md) for the wider release scope.
A useful demo must let a person develop Magi through Magi. It must also show
non-code tasks, background work, human participation, and evidence inspection.
Q&A alone does not meet this requirement.

## Confirmed requirements

The private operator policy selects the human release approver for the
Example environment self-development demo. Resolve the login through LinkKeys.
Match the configured canonical account and domain
after authentication; do not authorize by login name alone.
Record human approval of the exact candidate and target. Do not treat an
application key as proof of that approval. This permits a cross-domain login
for the test deployment. It does not permit deployment into the approver's domain
or any other production domain. Task review and release approval are separate.
The [test release prototype](release-demo.md) records the approval boundary,
recovery checks, and successful live service checks. The human approved the
system-submitted proposal. The isolated canary reached `healthy` after three
consecutive checks. No approval file transfer was needed. A separate approved
failure test then proved automatic rollback: the controller detected the
protocol failure, restored the exact baseline, and passed three checks.
This test used only the isolated stateless canary.

- Start with Parakeet STT. Compare replacements for Breeze. Provide at least
  eight distinct TTS voices through a replaceable engine contract.
- Design for speaker adaptation per person and capture device. The adaptation
  does not have to work in the prototype.
- Estimate speakers for attribution and cross-talk handling. Use policy-selected
  authentication for sensitive operations, not a confirmation for every request.
- Exclude personal TTS cloning.
- Build an Magi-owned Python harness in this repository. Use the MIT-era lo-agent
  lineage and lessons from OpenCode v2 and Pi. Do not depend on current lo-agent.
- Evaluate forkable open-source microVM and container mechanisms. Microsandbox
  and Lima are candidates. Use mise for environment tools and packages.
- Place the initial STT and TTS system on `192.0.2.20`. The existing
  `lab-speech-engine` is an experiment, not the new service contract.
- Permit separate speech services and additional hosts. Engine-specific CUDA
  requirements do not become requirements for Magi clients or other engines.
- Process retained voice locally with two or three transcription engines.
  Retain their results and agreement assessment so raw audio can expire sooner.
- Detect service failure, use permitted alternatives, and involve a human when
  needed. Support automatic rollback after an unsuccessful Magi deployment.
- Keep Android first, both application modes, local development, independent
  domains, and local calendar adapters. Preserve the earlier authority boundaries.

The requested products are recorded below. A requirement to use a product is
not proof that its current license or runtime meets the deployment requirements.
Do not substitute a different product without recording the choice.

## Source and host review

The review used upstream source and license files. These findings apply to the
checked versions. Recheck and pin all artifacts before integration.

| Component | Finding | Design consequence |
| --- | --- | --- |
| Parakeet | NVIDIA publishes `parakeet-tdt-0.6b-v3` under CC-BY-4.0. The Sherpa-ONNX INT8 CPU smoke test passed on `192.0.2.20`. | Extend the fixture test to real capture and concurrent load. Pin model, conversion, runtime, and attribution. Do not claim native streaming from an offline model. |
| Breeze | BreezeBlue Breeze TTS 2 code is Apache-2.0. Weights, derivatives, and self-hosted outputs have separate research and non-commercial terms. Its documented runtime uses NVIDIA CUDA. | Compare alternatives. CUDA is an engine placement constraint, not a system-wide restriction. |
| lo-agent | Checked version `0.2.24`, commit `9c97580713d51d567f5d8238b37e233d8f984806`. Its current Community License is source-available and restricts organization customization and modified distribution. | The user selected an Magi-owned Python harness based on the MIT-era lineage, not this current release. |
| Microsandbox | The upstream linked by `microsandbox.dev` is `superradcompany/microsandbox`. Its core license is Apache-2.0. It is marked beta. | It is a candidate for a pinned microVM adapter, not a proven production boundary. Verify the selected host, guest, SDK, and network controls. |
| Lima | The core license is Apache-2.0. It manages VMs and can support container runtimes. | Lima is not itself a dev-container definition or runner. Specify the VM, container runtime, and development-environment recipe separately. |
| mise | The core license is MIT. Current documentation includes tool management and OS package bootstrap backends. | Pin tool and package inputs. OS packages still depend on the base system and package sources. Do not install project-selected packages on the coordinator. |

Sources: [Parakeet model](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3),
[Sherpa-ONNX Parakeet](https://k2-fsa.github.io/sherpa/onnx/pretrained_models/offline-transducer/nemo-transducer-models.html),
[Breeze runtime](https://github.com/breezeblue-ai/breeze-tts),
[Breeze model terms](https://huggingface.co/BreezeBlue/breeze-tts-2/blob/main/LICENSE),
[lo-agent license](https://github.com/IMJONEZZ/lo-agent/blob/9c97580713d51d567f5d8238b37e233d8f984806/LICENSE),
[Microsandbox](https://github.com/superradcompany/microsandbox),
[Lima license](https://github.com/lima-vm/lima/blob/master/LICENSE),
[mise license](https://github.com/jdx/mise/blob/main/LICENSE), and
[mise packages](https://mise.jdx.dev/bootstrap/packages/).

The lo-agent license change is commit
`a9bc6efeb8ea6ecc2ad973645631606e81cf9b47`, dated 2026-07-14. Its parent,
`9e1c6d7d0fc30d816a250c0d66c0fb3d7d6c323e`, declares MIT in package metadata.
That tree has no top-level MIT license file. The change message says it replaces
MIT. Use this revision as the source-review boundary for the requested internal
fork. Record file origins, dependencies, notices, and modifications before code
import. Do not copy later restricted changes into that fork.
[License change](https://github.com/IMJONEZZ/lo-agent/commit/a9bc6efeb8ea6ecc2ad973645631606e81cf9b47)

A read-only SSH check found Debian Linux, AMD Navi 33 graphics, and about 91 GiB
of system RAM on `192.0.2.20`. No NVIDIA GPU appeared in the PCI listing.
The experimental speech service reported inactive. This review did not stop it.
The old inventory identifies the discrete card as an RX 7600. Actual usable
GPU memory and supported inference paths still need measurement.

Breeze documents about 7.7 GiB GPU use in eager mode and recommends at least
12 GB GPU memory. Its fast mode has higher requirements. These NVIDIA figures
are not evidence of AMD compatibility or host latency. The previous Whisper
Vulkan result proves neither. Do not change drivers or buy hardware as an
implicit implementation step.

The [Parakeet CPU smoke test](parakeet-cpu-test.md) processed a 3.845-second
public fixture in 0.123 to 0.126 seconds with two inference threads. It does not
measure household accuracy, streaming latency, or combined speech-service load.

## TTS alternatives

These are candidates for listening and deployment tests, not measured quality
rankings against Breeze. Keep the engine, model, and voice-asset terms separate.

| Candidate | Fit | Important limit |
| --- | --- | --- |
| Qwen3-TTS 1.7B CustomVoice | Apache-2.0 model; nine stock speakers; instruction-based style control. Strong candidate for expressive persona output on a suitable model host. | Stock speakers have different native languages. Test at least eight in the intended language. Verify actual streaming support in the chosen server. |
| Kokoro 82M | Apache-2.0 weights; a stock catalogue with more than eight English voices. Candidate for CPU deployment and a small local fallback. | Voice quality varies. Its voice guide identifies weaknesses on very short utterances and rushing on long ones. Test conversation-sized chunks. |
| Pocket TTS | CPU-oriented streaming engine. MIT code; the current model card labels weights CC-BY-4.0. Many supplied voice profiles. | Model access is gated with additional use conditions. Voice assets have mixed terms. Select CC0 or CC-BY assets; exclude non-commercial profiles from the general baseline. |
| Chatterbox Turbo | MIT model; expressive non-speech tags and reference-conditioned voices. Candidate for a later expressive engine comparison. | It needs a separately licensed reference catalogue for eight voices. Personal voice enrollment is not part of this project. |

Sources: [Qwen CustomVoice](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice),
[Kokoro model](https://huggingface.co/hexgrad/Kokoro-82M),
[Kokoro voices](https://huggingface.co/hexgrad/Kokoro-82M/blob/main/VOICES.md),
[Pocket code](https://github.com/kyutai-labs/pocket-tts),
[Pocket model](https://huggingface.co/kyutai/pocket-tts),
[Pocket voice terms](https://huggingface.co/kyutai/tts-voices), and
[Chatterbox Turbo model](https://huggingface.co/ResembleAI/chatterbox-turbo).

Use Kokoro for the first CPU service test. Keep Qwen CustomVoice as an optional
candidate on an admitted compatible host. Pocket remains excluded at the user's
request. Do not assume any engine can
reproduce another engine's persona voice. Register permitted fallback mappings
per voice; report a changed voice or use text when no mapping is accepted.

Use the same short acknowledgements, names, dates, numbers, technical terms,
questions, and long explanations for each candidate. Compare intelligibility,
naturalness, voice consistency, pronunciation, first audio, sustained generation,
memory, concurrency, and cancellation. Use blind listening where practical.
ASR agreement on generated audio cannot substitute for human quality judgment.

The [local TTS audition](tts-candidate-test.md) now provides pinned
Kokoro and Qwen adapters, common prompts, a listening page, and evidence checks.
The user excluded Pocket from this audition on 2026-10-17. This does not remove
the replaceable-engine requirement. The user's introduction review kept all twelve
Kokoro candidates and seven Qwen candidates. It rejected Qwen Aiden and Uncle_Fu.
Persona assignments, fallback mappings, and two listening flags remain open.
Kokoro's measured CPU path can generate speech faster than playback. Qwen's
tested float32 CPU path needs about five seconds per second of audio. This does
not establish Qwen latency on a GPU or with another runtime.

## Speech service boundaries

Keep five capabilities separate: capture and wake detection, speech boundaries,
STT, speaker assessment, and TTS. They can share audio and compute resources.
None owns user authority or task state.

Use separate CSIL entry points for speech and speaker-profile operations.
Expose authenticated admission, capability discovery, readiness, cancellation,
and bounded streaming. Keep the control protocol separate from audio transport.
An external permitted client can use STT or TTS without creating an Magi persona.

The initial contract needs these records:

- Capture: device identity, capture ID, sequence, sample format, time estimate,
  input route, bounded audio reference, and gaps.
- Transcript: capture reference, time range, language, model revision, text,
  revision number, and provisional or final status.
- Speaker assessment: candidate identity or unknown, profile revision, device
  match, assessment method, uncertainty, and any replay or overlap warning.
- Speech output: text reference, voice-profile revision, language, format,
  destination, audience restrictions, deadline, and cancellation generation.
- Presentation: queued, started, interrupted, completed, or failed; report
  playback position when available. Playback does not prove human attention.

Use local wake detection and a bounded pre-roll buffer. Do not upload continuous
room audio by default. The first real-device path can use push-to-talk while
the background-listening path is tested. Show that limit in the demo.

For an offline Parakeet backend, use bounded speech segments and final results.
Optional partial transcripts need explicit replacement semantics. A partial
transcript must not commit a protected operation. Cancellation invalidates late
results and queued speech. Provider cancellation can be best-effort; Magi must
still reject stale output. Do not promise that cancelled compute stops instantly.

Run STT and TTS with separate capacity limits on the host. Long speech output
must not block incoming transcription. Define bounded queues, overload responses,
timeouts, and a text fallback. Measure cold start, end-of-speech to transcript,
first audio, cancellation, and concurrent use. Record p50 and p95 results.

STT and TTS are separate logical services. Their replicas can share hardware or
use different hosts. Register engine revision, voices or languages, capacity,
health, processing trust, and placement requirements. CUDA and driver versions
belong to a backend's deployment profile, not the public speech request.

After failure, select another admitted instance with the required capabilities.
Do not send private text or audio to a processor merely because it is healthy.
Use bounded retries, deadlines, and failure backoff. Do not restart already
played speech from the beginning without a presentation decision. A fallback
must preserve the requested voice or use an accepted replacement.

If no TTS path works, send a permitted text notice with the observed failure
and any known cause. Distinguish an observed timeout from a diagnosed hardware
fault. Link an investigation task and ask for human help when needed. Quiet
hours do not suppress a failure that blocks the current interaction. Keep this
fallback usable without the failed speech engine or a working reasoning model.

Deploy new named services and state paths. Do not overwrite the experiment's
service, model files, tokens, or router entry. Keep model ports private behind
the authenticated service. Select the new test endpoint through the deployment
overlay. Keep the experiment's existing endpoint unchanged.

## Eight voices and personal adaptation

A persona voice and a human speaker profile are different records.

Provide eight selectable, stable TTS profiles. A profile pins its backend,
model revision, speaker setting or reference audio, generation settings,
language, provenance, and permitted use. One model can serve several profiles;
eight voices do not require eight loaded models. Different quality settings for
one voice do not count as distinct voices.

Breeze supports voice design and reference-based generation. It does not remove
the need to establish and test a stable Magi voice catalogue. Verify that each
profile remains distinct and usable across sentences, restarts, and streaming
requests. Do not silently change a persona's voice when a model changes.

Person-and-device tuning covers input speaker estimation and optional
recognition adaptation. Adaptation can include vocabulary and acoustic settings;
it does not require a full model
fine-tune. Personal TTS cloning is excluded.

Enroll a speaker only after authenticated approval. Bind enrollment to a LinkKeys
identity, device, capture route, and profile revision. Require a new check after
a microphone or audio route changes. Keep a person profile and device-specific
calibration separate. Do not train from unidentified room speech automatically.
Support withdrawal, replacement, and deletion. Treat samples and embeddings as
private biometric data, not general knowledge enrichment.

## Speaker identity and authority

An authenticated microphone proves which device submitted audio, not who spoke.
A wake word proves neither. Parakeet transcribes speech; it does not authenticate
the speaker. A voice match is probabilistic and can fail on another person,
recorded audio, generated speech, noise, or a short utterance.

Use speaker assessment to propose identity and routing. Do not make it a LinkKeys
authentication assertion. Resource services still decide authority. A persona
voice selection also grants no authority.

The user selected risk-based authentication on 2026-10-16:

1. Retain candidate speaker attribution, uncertainty, and cross-talk evidence.
   An unknown or overlapping speaker can remain unresolved in the graph.
2. Let simple requests proceed under the receiving service's policy without
   a new authentication challenge for every utterance.
3. For a sensitive operation, emit a durable authentication or approval request
   to the intended user. LinkKeys supplies the required authentication evidence.
   Magi records exact-operation approval when needed. The resource service
   checks both that evidence and its own current authorization policy.
4. Bind the request to the operation, resource, graph inputs, destination session,
   expiry, and single-use challenge. Provider delivery is not human approval.
   Recheck policy and operation revision before continuing after a reply.
5. If the required authentication method or assurance is unavailable, leave the
   operation pending or deny it. Do not claim LinkKeys supports an unverified
   method, and do not silently lower the requirement.

Sensitivity is an editable resource-policy decision, not an inferred conversation
mode. A prior valid authentication can be sufficient when policy permits it.
Speaker confidence is attribution evidence, not an authentication assurance level.
Another household member uses their own identity and applicable shared grants.

Tests must include the other household member, guests, phone replay, Magi's own
TTS, short replies, device handoff, and overlapping speech. Check identity before
private retrieval, not only before a tool write. Check the output audience too:
an authorized request does not permit a private answer on a shared room speaker.

## Conversation graph

The graph records interactions and their relationships. Do not give a conversation
one required mode or put it through a fixed mode-transition state machine.
Planning, discussion, instruction, correction, approval, and explanation can
overlap within one segment. They are examples for analysis, not an enum for
conversation creation or an authorization input.

Keep source interactions, speaker observations, transcripts, interpretations,
presentations, and actual session inputs as separate linked records. Edges can
express reply, reference, correction, derivation, or delivery. Preserve observed
sequence and causal references separately. Speech and text can overlap in time.

A segment selects graph nodes or source ranges. Segments can overlap and need
not be contiguous. Optional analysis attaches labels, method, input revisions,
uncertainty, and supporting evidence to a segment. An interpretation is not a
new fact about what a person meant. Corrections add records without changing
the historical inputs used for an action.

Each model call receives an ordered, permitted projection of the graph plus
relevant external evidence. It does not receive the whole graph by default.
Task sessions still bound execution and privacy. One interaction can advance
several sessions without merging their access rights. Graph links themselves
need access checks; a hidden node's title or existence can disclose information.

Knowledge comes from this graph and other sources, including searches, documents,
and live services. Retain source revisions, authorship, acceptance, and derivation
links. A search result is evidence, not automatic ground truth. Communication
does not become authored knowledge merely because it has a graph node.

Keep separate records for task state, human wait, agent control, environment
holds, and output presentation. A muted speaker is not a paused agent. A paused
agent is not a cancelled command. A disconnected client is not a released hold.
For an ambiguous "stop", stop local playback first and ask what other work to
stop. It must not silently cancel unrelated sessions.

One utterance can advance several sessions. Retain one source interaction,
the interpretation, and separately admitted destination inputs. A typed answer
can satisfy a spoken prompt. An unrelated typed message need not take over
the voice channel. Use explicit reply links when available; clarify material
ambiguity before an effect. Do not serialize background work behind a voice wait.

A revised plan changes future admitted work; it does not undo commands that
already ran. Keep receipts for concurrent human and agent changes. Neither
segment labels nor later graph analysis can expand resource grants.

## Harness and execution boundary

Magi owns context selection, policy, task state, environment lifetime, and the
evidence archive. Build the harness in Python within this repository. Use the
MIT-era lo-agent lineage as a bounded source for reuse and ideas. Learn from
OpenCode v2's API-oriented design and Pi's small composable loop. Do not replace
the current Go services or make an external harness the authority.

OpenCode v2 exposes session input, context inspection, forms, permissions,
events, shell operations, and terminals through APIs. Pi separates application
messages from provider context and has context preparation, tool hooks, and
queued input. These are useful patterns, not requirements to copy their schemas
or session model. OpenCode v2 and Pi inspected license files are MIT.
[OpenCode v2 API](https://opencode.ai/v2/docs/api),
[OpenCode v2 license](https://github.com/anomalyco/opencode/blob/v2.0.29/LICENSE),
[Pi agent core](https://github.com/earendil-works/pi/tree/main/packages/agent), and
[Pi license](https://github.com/earendil-works/pi/blob/main/LICENSE).

The first harness contract needs graph-context admission, provider adapters,
streamed output, proposed tools, independent evaluation, execution receipts,
steering input, pending human requests, cancellation, and resumable attempts.
Logit and local-model controls are negotiated optional capabilities. A provider
without them must still support ordinary admitted tasks. Transport reconnection
must not repeat tool effects. Context compaction retains its source projection.

The inspected lo-agent code has model and tool events, context-compaction events,
tool registration, and sandbox hooks. These are useful integration points.
Its event listeners run after SQLite commit. Thus an export listener is too late
for secret substitution. The integration needs a pre-write capture boundary,
including initial tasks, model requests, results, summaries, and error paths.
Disable or isolate local memory per admitted scope. Do not share `lo.db` or a
home memory directory across users by accident. Recheck hooks in the selected
licensed revision. The 2026-10-16 inspection confirms the post-commit listener
pattern in the MIT-era revision too. Its resume mechanism also needs Magi receipt
reconciliation before repeating a tool whose result is missing.

Every tool proposal needs exact operation admission outside the worker. Route
execution through the admitted environment. Tool-name approval alone does not
approve every later shell command. A broad shell still permits many operations
inside its actual filesystem, network, and credential limits. Do not claim that
model evaluation prevents every misuse of an injected credential.

Compose an environment from these independent fields:

- Placement: a named host or required capabilities, with no Kubernetes assumption.
- Isolation: native, container, or VM backend and required controls.
- Recipe: OS or OCI image digest, development-container configuration, mise
  configuration and lock data, and verified package sources.
- Bindings: named directories, devices, network destinations, and secret references.
- Limits: CPU, memory, disk, execution time, process count, and evidence capacity.
- Control: command receipts, attachments, human holds, expiry, and cleanup policy.

Use an isolated Linux backend for the first coding environment. Native execution
is a distinct, explicitly admitted exact-host capability. A container can run in
a Lima VM; Lima and containers are not mutually exclusive alternatives. Disable
broad host mounts, forwarded SSH agents, and runtime sockets unless specifically
admitted. Inspect actual Lima security settings, not only the VM label.
[Lima security](https://lima-vm.io/docs/security/)

Microsandbox host-platform support does not prove native Windows or macOS guest
application support. Keep the existing Reactorcide VM mechanisms as separate
backend candidates. Keep these platforms in the contracts; report untested
backends as unavailable. Do not silently fall back from VM isolation to native.

Treat repository setup hooks and mise tasks as code execution. Provision in the
admitted environment without unrelated credentials. Pin base images as well as
tools. Cache clean artifacts by digest and trust scope. Do not save or clone live
VM memory containing credentials as an ordinary reusable environment image.
A new attempt gets new control receipts and current credentials.

Coordinator restart must not start a second command merely because a worker
reply was lost. Reconcile with the host receipt. Preserve uncertain outcomes
when an external service cannot prove the result. Keep live attachment and
evidence upload independent of the model process lifetime.

## Evidence and audio

Retain the available model inputs and outputs, tool requests and results,
context revisions, human input, approvals, and presentation receipts. Do not
claim access to model reasoning that a provider does not expose. Preserve the
link from audio through transcript and interpretation to actual session input.
Later transcription or interpretation creates new records, not a new execution.

Use one admitted STT engine for the live response. Run two or three local engines
on retained recordings as background work. This work has no per-request model
API fee, but it still needs compute, storage, and queue limits. Do not put three
full transcriptions on the live-response latency path by default.

Keep each transcript, engine revision, audio ranges, and uncertainty. An alignment
and agreement assessment produces a preferred transcript with disputed spans.
Agreement is evidence, not proof: engines can share systematic errors. Preserve
names, numbers, negation, and cross-talk disagreements. Request human review
when an unresolved span affects a sensitive operation or an important claim.
Do not use transcript agreement as proof of who spoke.

Retain the live transcript and exact context used at the time. A later correction
can support new analysis, but cannot silently rerun an operation or rewrite its
approval. Knowledge derived from corrected evidence needs a new assessment.

Put raw audio in a restricted short-retention store. After durable derivative
records and the retention decision are confirmed, delete audio according to
policy. A processing outage must not retain it indefinitely. Bound any review
extension and record expiry, unresolved spans, and unavailable source ranges.
Preserve permitted timing and speaker observations before deletion. Later tone
or acoustic analysis cannot recover information that was discarded. Exact
retention periods remain an operator setting, not an invented fixed duration.

Apply secret-reference substitution before durable evidence writes, including
harness databases, debug output, and upload spools. Disable unfiltered crash
dumps for credential-bearing processes. Raw audio introduces a
new limit: a sanitized transcript does not sanitize a spoken secret in the
recording. Do not archive known credential-bearing audio unchanged. Omit or
remove affected audio ranges and record the gap. Use bounded transient capture
while deciding retention. Do not claim perfect discovery of unknown secrets.

Prototype recording uses explicit capture and test data. Before ambient use,
define household recording notice, bystander treatment, raw-audio retention,
and deletion for derived transcripts and speaker profiles. Keep enrollment
samples separate from ordinary task evidence. Content storage and access
restrictions still apply when an artifact is not text.

## Connected demo

The user accepted this demonstration direction on 2026-10-16. It needs a
repeatable test run, not only a successful live conversation.
Use public fixtures and synthetic private data until admission tests pass.

1. Use a persona through Android voice and web text. Select each of eight voice
   profiles. Edit and run a reusable non-code calendar or reminder workflow.
   Author a scoped project note, retrieve it, and show a denied cross-user query.
2. Ask the Engineering Manager to implement a small Magi feature. Produce a
   plan, an admitted task, and an isolated workspace with pinned tools.
3. Continue a separate conversation while the agent works. Give one mixed reply
   to two pending prompts. Inspect the separate destination contexts.
4. Join the workspace live. Send an explicit update through the persona. Pause
   the agent separately, then resume. Retain the environment under a human hold.
5. Complete tests and produce a patch, test evidence, and a candidate artifact.
   A separate permitted release step installs the candidate in the test system.
6. Receive a completion notice. Ask what ran, why it ran, and why another action
   was denied. Follow the evidence through the task, tools, and speech records.
7. Restart a coordinator and disconnect a worker. Recover pending work without
   repeating an uncertain effect. Release the hold explicitly.
8. Send an event or workflow request from the independent domain. Show one
   permitted operation and one receiver-policy refusal without changing identity.
9. Fail the selected TTS instance. Try an admitted alternative or send text with
   the observed failure. Fail a candidate release and show automatic rollback.

Self-development must not require the candidate code to approve itself or be
the only means of recovery. Keep a known-good deployment and an independent
administrative recovery path. Test in disposable data and a separate candidate
deployment. Promotion needs configured release authority; task credentials
must not grant policy editing, secret administration, or unrestricted cluster
access. Test rollback and database compatibility before promotion.

A release controller outside the candidate's failure boundary checks readiness
and synthetic end-to-end behavior within a bounded deadline. It can restore the
known-good artifact and route without asking the failing model. Keep thresholds,
retry limits, and a stop condition to prevent rollback loops. Fence or drain old
workers so rollback does not produce duplicate effects. Keep schema changes
backward-compatible during the rollback window. Binary rollback cannot undo
data loss or external effects; incompatible changes need a separate recovery plan.

Small-model CI verifies contracts, failures, and bounded tasks. It does not prove
the quality of a general engineering agent. Keep deterministic fault fixtures,
real small-model tests, and human usability tests separate. A stronger admitted
model can later use the same APIs.

## Remaining design gates

| Gate | Proposed first choice | Evidence or decision still needed |
| --- | --- | --- |
| G01: source reuse | The first Magi-owned Python proposal loop is implemented. No upstream source was copied. | Extend the harness contract and source review for later capabilities. Keep current restricted lo-agent changes out. |
| G02: speech engines | Parakeet and all twelve accepted Kokoro profiles pass native model tests. The signed speech service and browser path have local tests. | Select persona voices and fallback mappings. Add model readiness, persistent paths, and public service tests. |
| G03: sensitive operations | Policy-triggered authentication events with LinkKeys evidence and service-local authorization. | Define assurance, freshness, exact approval binding, timeout, and unsupported-method behavior. Simple requests need no blanket MFA. |
| G04: conversation graph | Linked source records, overlapping segments, interpretations, and admitted context projections. | Test mixed replies, cross-talk attribution, corrections, private edges, and knowledge derivation. No required mode enum. |
| G05: execution profile | A bounded offline Linux container exercise has tool receipts and exact checked-source hashes. Speech workers use fixed native backends. | Add the standalone execution API, captured shells, separate controls, and secret-reference capture. Container tests do not prove a microVM boundary. |
| G06: audio processing | Local two- or three-engine processing with bounded raw-audio retention. | Define alignment, disputed spans, deletion receipts, backlog limits, and policy-selected retention periods. |
| G07: development and release | The isolated stateless canary passed approval, automatic rollback, controller-crash recovery, and Corndogs task completion. Task source can be exported and staged separately. | Connect staged source to a fixed build recipe and exact release proposal. Complete compatible-schema recovery, worker fencing, and manual reconciliation before wider deployment. |

These gates close the demo design. They do not require solving automatic mode
learning, model fine-tuning, full reflection, every VM backend, or a new calendar
standard first. Preserve extension points and report each deferred capability.
The demo must not be described as the full integrated release.

## Next Actions

Extend the tested graph, speech, and task paths. Implement the release plan,
authentication requests, private processing, and separate execution controls.
Select persona mappings. Add Android and independent-domain integration.
Keep local and public Q&A working during this work.
