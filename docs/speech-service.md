# Speech service

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. See the
[configuration overlay](../config/README.md).

The first speech component provides Parakeet transcription and Kokoro synthesis.
It has its own CSIL entry point, generated clients, signed requests, authorization,
and PostgreSQL records. It does not require a persona, task, or workflow.
Use public test data only. Private-source policy and secret-reference capture
are not connected to this component yet.

## API boundary

The standalone source is [speech.csil](../csil/speech.csil). The aggregate Magi
client includes the same records and operations. LinkKeys verifies application
identity. The speech service grants access to an exact application and owner
scope. An authenticated application cannot read another application's requests,
even when both applications have a grant for the same scope.

`Speech/capabilities` takes a signed owner scope. The other operations take
signed request records. Signatures bind the operation, schema, recipient, key,
time, and original request bytes. Current key status and local grants are checked
on each API call. Model code receives no database or LinkKeys credential.

| Operation | Purpose |
| --- | --- |
| `submit` | Admit text for TTS, or an STT upload with its expected size and hash. |
| `upload` | Add a fixed-size PCM chunk. Exact repeated chunks are accepted. |
| `read` | Read request state and a completed transcript. |
| `read-audio` | Read one bounded output chunk before audio retention expires. |
| `cancel` | Withdraw pending work or a completed result. |
| `read-evidence` | Read the signed admission and recorded state changes. |

The request ID is unique within the caller and owner scope. An exact submission
retry returns the retained state. Changed content at that ID is refused.
The generation is part of the immutable request. Use a new request ID for new
work. A retry does not reset the deadline or start inference again.

## Capacity and recovery

The first host profile permits one active TTS call and one active STT call.
Their queues and workers are separate. Several API instances can share the
store. Worker claims use database locks and a token that must match at completion.
An expired worker claim becomes a failure. It is not automatically replayed.

Input audio must be mono, signed 16-bit little-endian PCM at 16 kHz. The maximum
duration is 60 seconds. Output uses the same sample format at 24 kHz and is
limited to 120 seconds. Upload chunks contain 48,000 bytes, except the final
chunk. The full input hash must match before transcription starts.

TTS text is limited to 2,000 UTF-8 bytes. Deadlines are at most two minutes.
An application can have 16 unfinished requests. The initial store stops new
admissions at 10,000 retained requests. This is a development bound, not a
storage lifecycle policy for a long-running service.

Audio retention is between two minutes and one day. The development client
requests one hour. Cleanup removes raw input and output audio. Request metadata,
text, transcripts, hashes, and state history remain. Cancellation removes
available audio immediately. Deletion from the active database is not secure
erasure of database backups or disk pages.

Cancellation fences late results. A worker checks its claim each second and
stops its Python process when the claim is no longer valid. Remote SSH execution
can take longer to stop computation on the host. No late result can restore a
cancelled request. Audio already delivered to a client needs separate playback
cancellation. Speech request completion does not mean playback or human attention.

The first backend returns a complete utterance. Bounded chunk reads are not
incremental model streaming. Service readiness currently checks database access,
not model readiness. Model failures appear in request state with bounded error
codes. Library error messages are not stored because they can contain input text.

## Model execution

[Model pins](../speech/model-pins.json) contain the tested model and voice-file
hashes. Python checks them before loading a model. Kokoro and Parakeet use separate
Python environments. One worker keeps its model loaded between requests.
Model ports are not exposed.

An operator can select local execution or a fixed SSH host. SSH uses existing
host verification and non-interactive authentication. The Python runtime and
model directory are operator configuration, not request fields. Native execution
is not an operating system sandbox.
Both local and remote Python start with a cleared environment and a fixed
set of runtime variables. The SSH account's other environment values do not
enter the model process.

## Local development

Set these variables for the first start:

```sh
export MAGI_SPEECH_TTS_PYTHON=/absolute/path/to/tts/bin/python
export MAGI_SPEECH_TTS_MODEL=/absolute/path/to/models/kokoro
export MAGI_SPEECH_STT_PYTHON=/absolute/path/to/stt/bin/python
export MAGI_SPEECH_STT_MODEL=/absolute/path/to/parakeet
# Optional: paths above are then on this host.
export MAGI_SPEECH_SSH_HOST=operator@speech-host
npm run dev:speech
```

The launcher creates private profiles under `~/.config/magi/profiles/speech/`. It starts a
dedicated `opal-speech` PostgreSQL Compose project on loopback port 25434.
The API uses loopback port 8094. It does not overwrite existing profiles.
Change the retained server configuration to change backend paths later.

This path uses synthetic local keys, not LinkKeys. The executable refuses local
keys with a public listener, public origin, or real identity domain. Normal
service mode uses the LinkKeys resolver. A public deployment still needs its
own credentials, TLS route, runtime database roles, and integration checks.

In another terminal:

```sh
.build/magi-speech-client voices
printf '%s' 'The answer is four.' | .build/magi-speech-client tts \
  -id first-speech -voice af_heart -output /tmp/opal-first-speech.wav
.build/magi-speech-client evidence -id first-speech
.build/magi-speech-client stt -id first-transcript < public-fixture-16khz.wav
```

The client never overwrites an output file. Use `read`, `audio`, or `evidence`
to inspect an uncertain request. Do not submit it again with a changed deadline.
Ctrl-C stops the development processes and retains the database. To stop the
dedicated database without deleting it:

```sh
docker compose -p opal-speech -f deploy/speech.compose.yaml stop
```

## Validation

Run `npm run test:speech`. The suite uses disposable PostgreSQL fixtures and
real signatures. It checks duplicate admission, chunk retries, audio hashes,
separate capacity, cancellation, lost-worker fencing, revocation, cross-caller
denial, retained history, and raw-audio expiry. It also tests Python process
cancellation during model load and inference with deterministic fixtures.

The first three-pass run passed 23 checks per pass with race detection and no
skips. The full repository suite passed 187 Go and 528 Node checks per pass.
These reports precede the later client and local launcher additions.

The new Python adapters also ran on `192.0.2.20`. All twelve Kokoro profiles
produced valid audio. Parakeet transcribed the generated test phrase as
"The answer is 4." This is one integration fixture, not an accuracy benchmark.
The final host report is
`test-results/2026-10-18T00-44-34.685Z-speech-host.json`.
The final repeated speech suite passed 25 checks per run. Its report is
`test-results/2026-10-18T00-42-55.714Z-speech.summary.json`.

## Next Actions

The [conversation demo](conversation-demo.md) now connects graph records,
explicit capture, playback reports, and text fallback. Add authenticated public
deployment, persistent model paths, and model readiness. Keep release approval
and rollback separate from task review. Do not call this component the completed
demo.
