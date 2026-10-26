# Local question and answer testing

Use this setup to send one question through Magi and receive a model answer.
The CLI signs a CSIL request. The workflow service records it in PostgreSQL.
Corndogs owns the task state. A Python worker calls the configured model API.
The CLI reads the completed answer and can retrieve the model evidence.

This is a test setup. Use synthetic questions and public facts only. It has no
knowledge retrieval, conversation memory, tools, voice, or private-data policy.
Each question has its own run. A new question receives no earlier questions.
The native Python process is not an operating system sandbox.

For real LinkKeys authentication through public TLS, use the separate
[Example environment setup](question-answer-example.md). Do not expose this local
test-key executable to the internet.

## Start the local model

Install Go, Node.js, Docker with Compose, and Python 3. The current repository
uses Go 1.27 and Node 26. The Python worker uses only the standard library.

The small test model is Qwen2.5 1.5B Instruct Q4_K_M. The runner checks its
SHA-256 digest and the llama.cpp build version against
[model pins](../question/model-pin.json). It uses two CPU threads, no GPU
offload, one inference slot, and a 4096-token context. This choice proves the
integration path. It is not a recommendation for knowledge assessment.

On this workstation, the required files already exist:

- `~/models/qwen2.5-1.5b-instruct-q4_k_m.gguf`.
- `~/llamacpp/llama.cpp/build/bin/llama-server`.

For another host, obtain the GGUF file from the
[official model repository](https://huggingface.co/Qwen/Qwen2.5-1.5B-Instruct-GGUF/blob/main/qwen2.5-1.5b-instruct-q4_k_m.gguf).
The expected digest is in the model pins. Build `llama-server` from llama.cpp
commit `c96f608d9861f7e8466bc1b6ac2ff4e3c6f96641`. The runner accepts that build
version and records the executable digest in each model test report.
Different compilers can produce different executable digests.
Set `MAGI_QA_GGUF` and `MAGI_QA_LLAMA_SERVER` to change the file paths.
The runner does not download models or install software.

In terminal one, from the repository root:

```sh
npm run dev:qa:model
```

Wait for the ready message. The model API uses
`http://127.0.0.1:8088/v1/chat/completions`. It has no API key and accepts only
local connections. The model name is `opal-test-small`. Ctrl-C stops only the
model process started by this command. It does not stop an existing model.

The adapter uses the model server's chat-completions API. The local request
format is documented in the pinned llama.cpp checkout under
`tools/server/README.md`. This adapter does not make that API the Magi model
contract. Other model protocols need their own adapters.

## Start Magi

In terminal two:

```sh
npm run dev:qa
```

This command starts the dedicated `opal-qa` Compose project. It builds the CLI,
creates private configuration files on first use, and starts the local API,
coordinator, timeout evaluator, and question worker. It does not change the
Kubernetes cluster or the public Example environment service.

The first start can download the two pinned container images. Ports are bound
to loopback only:

| Component | Address |
| --- | --- |
| Model | `127.0.0.1:8088` |
| Workflow API | `127.0.0.1:8093` |
| PostgreSQL | `127.0.0.1:25432` |
| Corndogs | `127.0.0.1:25080` |

A one-time initialization container gives the dedicated Corndogs volume to UID
65532. It has no network access and mounts only that volume. PostgreSQL readiness
uses TCP. A Unix-socket readiness check can report the temporary initialization
server as ready before the final server accepts connections.

The launcher does not overwrite an existing configuration. Files are under
`.local/qa/`, which Git ignores. The directory uses mode `0700`; files use
`0600`. `client.json` contains the local signing key. Do not share it.
`server.json` contains the public key, test database connection, model settings,
and the admitted actor and scope.

This executable uses **local test keys, not LinkKeys authentication**. The
identity domain is `local.opal.test`. It refuses public listen addresses and
real identity domains. The normal `cmd/workflows` service still uses LinkKeys.
Do not expose this test API, database, Corndogs, or model through a router.

The local server uses the test database administrator role to migrate and
restore its fixed grants on startup. This is not the deployment privilege model.
Use the separate migration and runtime roles in the
[workflow service guide](workflow-service.md) for deployment work.

## Ask and inspect

In terminal three:

```sh
printf '%s\n' 'What is 2 + 2? Reply with only the single digit.' |
  .build/magi-qa ask -run arithmetic-001
```

The CLI prints the run ID to standard error before submission. It prints the
completed answer to standard output. The default wait is three minutes.
The model call has a two-minute limit. A wait timeout or Ctrl-C on the CLI does
not cancel the admitted run.

Inspect the same run without another model call:

```sh
.build/magi-qa read -run arithmetic-001
.build/magi-qa evidence -run arithmetic-001
```

Evidence includes the exact model request bytes, the configured destination,
adapter revision, available response bytes, timestamps, and a bounded error
code. The display expands JSON for inspection and retains the original bytes
as base64 in the metadata. It does not execute or resolve anything.
Signed publication and admission records remain in the workflow database.

Use a new run ID for a new question. To recover an uncertain submission reply,
repeat the **same run ID and identical question**. Changed input at that ID
fails. An exact retry does not repeat a dispatched model call.

## Failure and storage behavior

The worker stores request evidence before starting Python. It records a response
before storing the workflow result. A process failure between these steps can
leave prepared evidence or a returned response without a completed run.
Neither condition permits automatic model replay. Inspect the evidence first.
Authorized result reconciliation is not implemented yet.

A dispatch observation can say `outcome_unknown` while the worker is active.
That phase alone does not prove a failure. The CLI waits for completion or a
retained failure. Provider HTTP errors, malformed replies, truncated answers,
and tool-call replies do not become successful answers.

Questions are limited to 3000 UTF-8 bytes. Accepted answers are limited to
3500 bytes. The worker retains at most 64 KiB of response bytes. An oversized
response retains that prefix with an explicit error. It does not return a
partial answer as complete. Requests are limited to 16 KiB.

No API key or other credential is accepted by this model adapter. It does not
inherit workstation environment variables or proxy settings. URLs cannot contain
credentials, query parameters, or fragments. HTTP is limited to literal
loopback; HTTPS uses normal certificate verification. Redirects and automatic
request retries are disabled. Arbitrary secrets in question text are not detected.

Ctrl-C in terminal two stops the API and its worker. It retains the database,
Corndogs volume, configuration, and evidence. To stop the containers without
deleting their data:

```sh
docker compose -f deploy/qa.compose.yaml stop
```

Start the same setup with `npm run dev:qa`. Do not remove one store while keeping
the other and assume that runs will recover. Cross-store restore reconciliation
is still a separate requirement. There is no automatic data deletion command.

## Automated checks

Run the deterministic fault and boundary suite:

```sh
npm run test:qa
```

Run the actual small-model path:

```sh
npm run test:qa:model
```

Both commands require Docker and the pinned container images in the local cache.
They create isolated, labelled containers with random loopback ports. Cleanup
removes only those test containers and their volumes. They do not use the manual
Compose project or its stored questions.

The model command starts its own hash-checked CPU model on a free local port.
It builds and invokes the actual CLI. The CLI must receive `4` for the fixed
arithmetic question, and the evidence must contain a real model response.
Missing model files, a wrong build, or a wrong answer fail the check. They do
not select the deterministic responder or skip inference.

Each suite runs three times with Go race detection and vet. Reports retain
raw output, source hashes, and model metadata where applicable. This checks
integration and a small answer case, not broad answer quality.
CI can cache the pinned model and container images, then run these commands.
The runner does not require a paid provider, GPU, or production identity.

Test the Compose setup on a host with ports 25432 and 25080 free:

```sh
node scripts/test-qa-compose.mjs
```

This check uses the deterministic responder. It starts a unique Compose project,
asks through the CLI, recreates the containers, and checks the retained answer
and evidence. It then removes only that project and its test volumes. It does
not use or remove an existing `opal-qa` project. An occupied port fails setup.

For a transport-only manual test, initialize a new setup with
`npm run dev:qa -- -model-url fixture`. The response clearly says that it came
from a test responder. Use this only when actual inference is not under test.

## Verified results

The final checks pass on the current source:

- [Core regression](evidence/index.md#evidence-013):
  171 Go checks and 482 Node checks in each of three passes.
- [Workflow recovery](evidence/index.md#evidence-010):
  178 leaf checks in each of three passes.
- [Q&A boundary and failure tests](evidence/index.md#evidence-012):
  34 leaf checks in each of three passes.
- [Actual model and CLI](evidence/index.md#evidence-011):
  the real arithmetic answer and retained exchange pass three times.
- [Compose recreation](evidence/index.md#evidence-014):
  the answer and evidence remain available after container recreation.

The Go suites pass race detection and vet. Each report records its tested
source hashes.
Earlier failed setup records remain available. They identified non-root volume
permissions and PostgreSQL initialization readiness. Disposable test containers
and their volumes were removed. No public deployment was changed.

## Next Actions

1. Test ordinary public questions with the local model and inspect their evidence.
2. Test the [public LinkKeys deployment](question-answer-example.md) with
   its separate profile. Do not deploy the local test-key executable.
3. Add credential delivery and reference-only capture before a remote provider
   that needs an API key. Do not place a token in a URL or question.
4. Add source policy and knowledge retrieval as separate stages. A model answer
   without source evidence is not an accepted knowledge record.
