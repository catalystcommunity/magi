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
magi know calendar
```

## Configuration

`MAGI_LISTEN`, `MAGI_AUTH_KEYS`, `MAGI_SERVICE_TOKEN_FILE`, the `MAGI_*_URL`
endpoints of each primitive, `MAGI_MODEL_BASE_URL`, `MAGI_MODEL_NAME`,
`MAGI_MODEL_API_KEY_FILE`, `MAGI_REPO`, `MAGI_ENV_IMAGE`, `MAGI_CHECK_COMMAND`,
`MAGI_RELEASE_URL`, `MAGI_IMAGE_REGISTRY`, `MAGI_IMAGE_PULL_REGISTRY`,
`MAGI_IMAGE_CREDENTIALS_FILE`, `MAGI_IMAGE_BASE`, `MAGI_RELEASE_TARGET`.

## Limits

- One replica. The persona and step workers are safe with more replicas,
  because claims are exclusive, but this is not tested.
- The release step builds and releases only `magi-server`.
- Task listings read the person's runs; there is no paging.
