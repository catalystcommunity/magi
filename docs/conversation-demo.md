# Conversation and speech demo

Updated 2026-10-17. The local demo connects independent speech, conversation,
and workflow APIs. It uses Corndogs for task state and PostgreSQL for records.
It uses the pinned small local model, Kokoro, and Parakeet.

This is part of the [connected demo](demo-prototype.md), not the complete
self-development demo. General tools, private sources, speaker authentication,
Android, and deployment approval are not available through this interface.
A separate [offline development exercise](offline-development.md) now adds a
bounded Python task, live inspection, retained candidates, and task review.

## Start the demo

Configure the native backends in the [speech guide](speech-service.md). Then run:

```text
npm run dev:demo
```

Open the local URL that the command prints. Keep its fragment token private.
The browser removes the fragment from the address bar and keeps the token in
tab session storage. This token is not LinkKeys authentication.

The command starts missing speech and conversation services. It does not stop
services that were already running. It starts a separate pinned local model
on an available port. An existing model on port 8088 can remain in use.
Set `MAGI_DEMO_MODEL_ENDPOINT` to use another permitted test endpoint.
No model API token is supported in this path.

| Component | Loopback port |
| --- | --- |
| Speech API | 8094 |
| Conversation API | 8095 |
| Demo workflow API | 8096 |
| Browser | 8097 |
| Demo workflow database | 25436 |
| Demo Corndogs | 25082 |

The existing Q&A configuration and public deployment remain separate.
Private profiles are in `.local/dialogue`, `.local/conversations`, and
`.local/speech`. Do not publish these files. Setup does not overwrite them.
Ctrl-C stops processes started by the command. It retains configuration,
database volumes, and evidence. To stop workflow storage without deletion:

```text
docker compose -f deploy/dialogue.compose.yaml stop
```

## Graph and model context

The standalone Conversations API has append, read, and history operations.
Its schema and generated clients are separate. An external permitted client
does not need a persona or workflow to use it.

Each record has a signed application admission and an immutable receipt.
Current actor and scope grants control each operation. An exact append retry
returns the existing receipt. Changed content at the same reference conflicts.
One record can belong to several sessions.

The demo preserves original text or an audio reference, transcript observations,
interpretations, each session's actual input, the exact model-context snapshot,
assistant output, and browser-reported presentation states.

The browser can split one interaction into two session inputs. This is an
explicit choice, not automatic intent analysis. Both inputs link to the same
source and interpretation. Modes are not conversation types.

The workflow has prepare, answer, present, and finish steps. Model context
contains earlier inputs and assistant outputs from the selected session only.
A descending query uses the admitted input sequence as its upper boundary.
Later concurrent inputs cannot enter that snapshot. The selector keeps recent
text within a 2600-byte budget and scans at most 80 records. It records a notice
when it omits context. The archive retains the original records separately.
Model call evidence includes system instructions and provider requests and replies.

## Voice and presentation

Record up to 58 seconds, or select a mono, 16-bit PCM WAV at 16 kHz.
The browser decodes and resamples a recording before upload. The service retains
the submitted PCM, not the browser's original encoded file. Confirm or correct
the transcript before sending it to a session.

This path does not estimate or authenticate the speaker. A voice does not prove
that a particular person spoke. The authenticated application produces the record.

Kokoro has twelve selectable profiles. Output is a complete utterance, not
streamed synthesis. Speech plays one response at a time. Typing does not pause
playback. Stop playback does not pause an agent or cancel a workflow.
Queued, started, interrupted, and completed reports do not prove human attention.

TTS failure leaves the text answer available with a bounded failure code.
A timeout is not a hardware diagnosis. Audio expires after one hour in this
demo. Text, hashes, admission proofs, and observations remain. Cancellation
is not authorized erasure.

## Access and recovery limits

The browser is loopback-only and uses synthetic local keys. API requests need
its token, expected host, and allowed origin. Do not expose it through a public
proxy. The normal service executables use LinkKeys key resolution.

The graph accepts unrestricted test records in one control scope. It rejects
restriction-bearing records and cross-scope edges. Private processing policy
and authorized deletion are not implemented here. The store has a 10000-record
limit, an 8192-byte append limit, and four records per history page.

A lost response does not authorize another model or tool invocation. Run IDs
remain in tab session storage. Use Read run or the evidence view after a timeout
or reload. The browser does not automatically repeat an interaction. The workflow
engine retains uncertain outcomes for investigation.

## Verification

```text
npm run test:demo
npm run test:speech
npm test
```

The demo integration suite uses disposable PostgreSQL and Corndogs containers.
It tests signed component calls, duplicates, session separation, text fallback,
and retained results after workflow service restart. Deterministic model and
audio fixtures make this suite suitable for CI.

`scripts/test-demo-browser.mjs` uses the actual local model and speech engines.
Set `MAGI_PLAYWRIGHT_MODULE` to an installed Playwright module. Set
`MAGI_DEMO_BROWSER_URL` to the local URL with its token. Do not share that URL.
The result file contains no token.

The first real-model report is
`test-results/2026-10-18T00-01-50.507Z-demo-browser.json`. Firefox 151.0 passed
text-to-speech, shared input to two sessions, and a Kokoro-to-Parakeet round trip.
Browser script errors: zero. This does not test a physical microphone,
household recognition accuracy, Android background operation, or public TLS.

The later `test-results/2026-10-18T00-38-34.427Z-demo-browser.json` report passed
the same paths while a separate real-model development task ran. Its task
report is `test-results/2026-10-18T00-38-35.829Z-task-browser.json`.
The final speech environment change also passed the real browser path in
`test-results/2026-10-18T00-43-49.489Z-demo-browser.json`.

## Next Actions

Extend the isolated task exercise and human participation. Add model health
checks and persistent model paths. Test real LinkKeys calls before public
deployment. Add private-source admission and authorized erasure before personal
use. Keep the original Q&A path available during this work.
