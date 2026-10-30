# magi (composition layer)

This module connects the primitives into one system for people. It is the
only code that uses more than one primitive. It provides the human-facing
API in `contracts/magi.csil`, two clients, and the background workers.

## Parts

| Path | Purpose |
| --- | --- |
| `compose/service.go` | `MagiService`: chats, messages, voice, tasks, decisions, knowledge |
| `compose/persona.go` | Workflow `persona-turn`: interprets one utterance with the model and replies |
| `compose/codechange.go` | Workflow `code-change` and its step workers |
| `compose/release.go` | Release step: build, push, and release through the release controller |
| `compose/worker.go` | Claim loop with heartbeats and progress notes |
| `model/` | OpenAI-compatible Chat Completions client |
| `cmd/magi-server` | The service process (API, web client, workers) |
| `cmd/magi` | Command-line client |
| `web/` | Browser client (TypeScript; `tools/build-web.sh`) |

## How a message flows

1. A client calls `say` with text or audio. Audio is stored as an artifact
   and transcribed by the speech service.
2. Magi posts the utterance to the conversation service with the person's
   token, then starts a `persona-turn` run keyed by the utterance id.
3. A worker claims the step. The model reads the recent conversation and can
   call `recall`, `remember`, `start_code_change`, and `list_tasks`.
4. The reply goes to the conversation as a persona utterance. The tool calls
   are kept as an `interpretation` annotation on the person's utterance.
   With `speak_reply`, the reply is also synthesized and stored as audio.

## Code-change workflow

```text
prepare -> work (agent) -> check --pass--> review (human) --approve--> merge -> release -> cleanup
              ^              |                   | revise                  |
              +-- fail-checks+                   +--> work                 +-- conflict -> cleanup
```

- `prepare` creates an environment, moves the repository in as a Git bundle,
  and makes branch `task/<run>`.
- `work` is done by the Python agent harness (queue `agent`).
- `check` runs `./tools/check.sh`. On success it commits and moves the branch
  out as a bundle (`push-bundle`). Three failures stop the task.
- `review` waits for `decide` from the run owner. A chat message is not a
  decision.
- `release` builds `magi-server` in the environment, pushes an image, and
  asks the release controller to roll it out. The controller rolls back a
  failed version. The step can resume after Magi itself restarts.

Environments never receive Git or registry credentials.

## Runners

A runner is a program on another machine that does the steps of code
changes there. It connects to Magi; Magi never connects to it, and it does
not listen on the network. Set `RUNNER` (`MAGI_RUNNER`) to the runner name
to send code changes to it.

```text
prepare -> work -> check --> review --> merge (and build) -> release -> publish -> cleanup
  runner   runner   runner    person       runner            Magi       runner     runner
```

- The runner claims steps from its own queues (`runner.<name>.<step>`)
  through `RunnerService` with a runner token (subject `runner:<name>`,
  scope `magi:runner`). It can act only on a step that it holds a claim for.
- Each task gets a bubblewrap sandbox (`primitives/execution/sandbox`). The
  sandbox sees its work directory and read-only system directories, not the
  home directory of the runner user. The runner reads and writes sandbox
  files only through envd, inside the sandbox.
- The agent harness runs from a copy (not from a checkout that a task can
  change). Its tools use the sandbox through an execution API on loopback.
  Its model calls go to `/runner/v1/chat/completions` on Magi, which adds
  Magi's provider key. The runner machine holds no provider key.
- The runner keeps a mirror of each repository with an upstream. It syncs
  before prepare and before merge. `merge` merges the reviewed branch onto
  the newest upstream commit in the sandbox and builds `magi-server`; the
  build goes to Magi as an artifact, and Magi releases it. `publish` pushes
  to the upstream with the runner machine's Git credentials, only after the
  release stayed healthy, and only as a fast-forward. If the release rolls
  back, nothing is pushed.
- The review diff is the patch that the runner uploads.
- A runner removes the environments of runs that ended, also after a cancel.

Install one runner for each Magi installation. Names come from the Magi
host, so dev and production runners on one machine do not share files:

```sh
tools/install-runner.sh magi.example.com [RUNNER_NAME]
# token: identity mint --subject runner:RUNNER_NAME --scopes magi:runner ...
#   into ~/.config/magi-runner/magi.example.com.token (mode 600)
cp deploy/runner/magi-runner@.service ~/.config/systemd/user/
systemctl --user enable --now magi-runner@magi.example.com
```

Run `tools/install-runner.sh` again after a change to the runner or the
agent harness, then restart the service.

## Authorization

Calls for a person's own records use that person's token. Background work
uses the `service:magi` token with act-as scopes (see `deploy/README.md`).

## Command-line client

```sh
go build -o magi ./cmd/magi
magi --server https://magi.example.local login you@example.local
magi chat                      # interactive
magi say -speak -audio question.wav
magi tasks; magi task RUN; magi diff RUN; magi decide RUN approve
magi cancel RUN -m "not needed"   # stop a task; asks first at a risky step
magi know calendar
magi usage -days 7             # your Magi replies by model
```

`login` prints a sign-in URL. Open it in a browser on any device. On a
desktop, the program also opens it. After the sign in, the browser shows a
one-time code. Paste it into the terminal, and the client gets its token.
The client does not listen on a local address, so the browser does not have
to be on the same machine.

## Configuration

`MAGI_LISTEN`, `MAGI_AUTH_KEYS`, `MAGI_SERVICE_TOKEN_FILE`, the `MAGI_*_URL`
endpoints of each primitive, `MAGI_MODEL_BASE_URL`, `MAGI_MODEL_NAME`,
`MAGI_MODEL_API_KEY_FILE`, `MAGI_REPO`, `MAGI_ENV_IMAGE`, `MAGI_CHECK_COMMAND`,
`MAGI_RELEASE_URL`, `MAGI_IMAGE_REGISTRY`, `MAGI_IMAGE_PULL_REGISTRY`,
`MAGI_IMAGE_CREDENTIALS_FILE`, `MAGI_IMAGE_BASE`, `MAGI_RELEASE_TARGET`.

## Model check

`TestLivePersonaTools` sends scripted messages to a model with the persona
prompt and tools. It checks the first tool call: `recall` before an answer
about the person's things, `remember` for new facts, `start_code_change`,
`list_tasks`, and no tool for small talk. Use it before you put a model in
`MAGI_PERSONA_MODELS`:

```sh
MAGI_TEST_PERSONA_URL=http://inference.example.local:8095/v1 \
MAGI_TEST_PERSONA_MODEL=qwen3.5-9b \
MAGI_TEST_PERSONA_KEY_FILE=/path/to/token \
go test -count=1 -v -run LivePersona ./compose/
```

On 2026-10-28, Qwen3.5 9B (Q4_K_M, RTX 4080) passed all 7 cases in 3 of 3
runs, with a mean of 0.6 s for each call.

## Limits

- One replica. The persona and step workers are safe with more replicas,
  because claims are exclusive, but this is not tested.
- The release step builds and releases only `magi-server`.
- Task listings read the person's runs; there is no paging.
