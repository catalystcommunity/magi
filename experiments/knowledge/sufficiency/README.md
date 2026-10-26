# Evidence support experiment

This experiment tests whether a local model can select supporting evidence
after retrieval. It does not generate final answers or execute tools.
The model proposes a support mode and exact source passages. Code checks
the response structure, citations, and current source access.

Use only the supplied synthetic data. This is not an authenticated API.

## Run

From `experiments/knowledge/`:

```sh
node --test sufficiency/test-events.test.mjs
node sufficiency/run.mjs dev
node sufficiency/run.mjs test
```

The runner uses the existing local Qwen2.5 1.5B Q4_K_M model and llama.cpp.
It starts a private loopback listener with two CPU threads and no GPU offload.
It creates a labelled PostgreSQL test container and removes it after the test.
No paid API, public domain, or production credential is used.

`OPAL_PLANNER_MODEL` and `OPAL_LLAMA_SERVER` can select other local files.
The result records both file hashes. Python 3.14 uses only its standard library.
The runner retains requests, raw responses, validation results, timing, token
counts, source hashes, and test logs in a new directory under `results/`.
It sets result files to mode `0600` and the directory to `0700`.

## Evaluation boundary

`protocol.json` defines the first experiment. Candidate rankings come from
the frozen `hybrid_links` retrieval runs. The runner rebuilds the synthetic
source store and checks current access before it sends text to the model.
It does not run retrieval again or use a relevance label to choose model input.

The model receives the question, fixture time, source times, source text,
and local citation aliases such as `e01`. It receives no expected support
mode, relevant-record list, required-record list, or denied-record metadata.

The response can propose `answer`, `partial`, `clarify`, `unavailable`, or
`no_evidence`. These are assessments, not authorization decisions. Mode
agreement and source coverage are separate measures. The fixture labels
can be debatable. These known synthetic questions are not an independent
answer-quality benchmark.

The first development run exposed response-shape failures. The next version
moved mode selection after evidence and added two unrelated examples.
The test run uses that version without further prompt changes.
Keep failures. Do not tune against the test set and retain its old score.

## Deterministic checks

- Reject malformed JSON, duplicate fields, unknown fields, and extra objects.
- Require citations to name supplied aliases and quote exact contiguous text.
- Reject unknown modes, oversized output, and inconsistent gap fields.
- Check all source inputs again after inference, including uncited inputs.
- Check the query control scope even when no source is available.
- Reject a changed actor, processor, purpose, source payload, or access grant.
- Refuse oversized model context instead of silently truncating it.
- Treat runtime failure separately from absent evidence or a poor assessment.

Current access checks use one database snapshot at each boundary. No database
transaction remains open during inference. The later check cannot withdraw
text already sent to the processor. It is not an atomic network-send protocol.

An exact quote can still be irrelevant. A valid JSON object can still have
the wrong mode. Do not treat a passing structural check as proof of a correct
answer. The model has no tools, so this does not prove tool-level resistance
to instructions in imported source text.

## Next Actions

Use a new evaluation version to test stronger local models or a different
assessment method. Add live source-status inputs and a reviewed answer stage.
Complete W02 before exposing this internal store through an API.
