# Plan

Started 2026-10-27. This plan replaces the earlier implementation. The earlier
code was a method of discovery, not a base to keep.

## Goal

Magi is a composable automation and multi-agent system. The first demo shows
that a person can develop Magi through Magi:

1. The person talks to Magi by text or voice, through any client.
2. Magi interprets the request. It answers, records knowledge, or starts work.
3. For a code change, an apprentice works in an isolated environment on Magi's own
   source. Independent checks run. Progress appears in the conversation.
4. The person reads the change and records a decision through the API.
5. Magi merges the change, builds images, and releases them. The release
   controller checks health and rolls back a failed release.

The API is the product. The web page, the command line client, and the
Android client are replaceable clients of the same API.

## Primitives

Each primitive has its own contract, Go module (or crate or package), store,
and process. A primitive does not import another primitive. Another system
can use one primitive without the rest of Magi.

| Primitive | Contract | Purpose |
| --- | --- | --- |
| artifacts | `contracts/artifacts.csil` | Content-addressed evidence: audio, transcripts, patches, logs |
| knowledge | `contracts/knowledge.csil` | Graph of things, external records, relations, and the evidence that supports them |
| conversation | `contracts/conversation.csil` | Conversation graph: streams, utterances, links, visibility, review queue. Access by identity (row-level security). No interpretation |
| workflow | `contracts/workflow.csil` | Definitions and runs. Task steps for workers, human steps for decisions. Uses Corndogs |
| execution | `contracts/execution.csil` | Environments that run commands and hold files. Container now; VM and host later |
| source | `contracts/source.csil` | Hosted Git repositories, diffs, and merges |
| speech | `contracts/speech.csil` | Speech to text and text to speech behind one contract |
| inference | `contracts/inference.csil` | Model engine on demand on a shared machine. It yields when the machine is busy |
| identity | `contracts/identity.csil` | LinkKeys login and Magi bearer tokens |
| apprentice | none (worker) | Magi-owned Python harness. Claims workflow tasks, uses a model and execution tools |
| release | `contracts/release.csil` | Rust controller. Applies images, checks health, rolls back |

`magi/` is the composition layer and the only code that uses several
primitives. It owns the persona, interpretation, and the human-facing
`contracts/magi.csil` API.

Shared code is limited to `lib/rpc` (transport) and `lib/authn` (tokens).

## Boundaries kept from earlier work

- LinkKeys authenticates. Each service authorizes its own records.
- CSIL defines every API. Generated clients exist for Go, Python, Rust,
  and TypeScript.
- Corndogs holds workflow task state and claims. No Temporal.
- Execution is separate from inference. The environment apprentice works the same
  way in a container, a VM, or on a host.
- Original interactions, interpretations, and evidence are kept.
- Secret values do not go into evidence or model context.

## Deferred

These remain requirements but do not gate the first demo: cross-domain
federation, signed delegation allowances, speaker estimation, multi-engine
transcription, Windows and macOS VMs, and adaptive interaction modes.

## Slices

Each slice must work through the API before the next one starts.

| Slice | Status on 2026-10-28 |
| --- | --- |
| 1. Text conversation with the model | Works through the public API |
| 2. Knowledge: remember and recall external records with evidence | Works; calendars link to system nodes and to the source utterance |
| 3. Code change: environment, apprentice, checks, diff in the conversation | Works; a chat request produced a checked change |
| 4. Decision through the API, then merge | Works |
| 5. Build and release with health check and rollback | Works; Magi released a change to itself; a bad image rolled back |
| 6. Voice in and out on the same conversation | Works with generated test clips |
| 7. Android client | Builds and passes its unit tests; not yet tried on a phone |
| 8. Conversation graph (no chats), librarian, review queue | Works on 2026-10-30; 150 imported utterances classified by the local model |

`tools/demo-check.sh` repeats slices 1, 2, 3, and 6 against an installation.

## Runners

Code changes run on runners: programs on machines that the person owns.
A runner connects to Magi and claims its steps; the cluster never connects
to it. The cluster connects out only to APIs (model providers, the
inference host, speech engines). See `magi/README.md`.

- Runners now: ht0-studiodt (linux/amd64, publishes to GitHub),
  raspberrypi (linux/arm64), and ht0-macbookm5 (darwin/arm64, Seatbelt
  sandbox). A task can name a runner; the publishing runner pushes.
- Magi updates runners (runner releases) and the Android app (app
  releases). A runner tests a new release and goes back to the old one if
  it fails.

## Next Actions

- Push the current work to GitHub. Tasks start from GitHub `main`, and a
  release from an old `main` removes the fleet API and the graph.
- Change the runner unit on ht0-studiodt to `supervise` (see
  `magi/README.md`, "Install a runner").
- Run one code change end to end on the Pi, published by ht0-studiodt.
- Install the Android build with the updater over USB once; later builds
  come from `magi app-release`.
- Knowledge graph next: topics with summaries, episodic and procedural
  records, with evidence links to utterances.
- Production: an admin-only way to make runner tokens at install, or fleet
  tokens.
