# Evidence support results

Measured 2026-10-12 in America/Denver. Result filenames use 2026-10-13 UTC.
The local support experiment selects source passages after retrieval. It does
not generate final answers or execute tools.

Do not select Qwen2.5 1.5B Q4_K_M as the default support evaluator from these
results. It often found text but assigned the wrong meaning or response mode.
Deterministic checks caught some failures, but a valid citation did not prove
that the assessment was correct.

## Measured results

The [test summary](evidence/experiments.md#experiment-022)
and [per-question evidence](evidence/experiments.md#experiment-021)
retain each request, raw output, validation result, and selected source.

| Measure | Result |
| --- | --- |
| Structurally valid responses with exact citations | 10 / 20 |
| Valid responses with the expected support mode | 8 / 20 |
| Valid responses that cite every required source | 7 / 18 supported questions |
| Complete source selection and expected mode together | 6 / 18 supported questions |
| Inference runtime failures | 0 |
| p95 inference time | 19.85 seconds |
| Maximum inference time | 20.66 seconds |

The run used 23,935 prompt tokens and generated 1,135 tokens. Inference used
two CPU threads, with GPU offload disabled. These are shared-host measurements,
not latency guarantees. The p95 alone uses almost all of the longer recall
target before final answer generation or speech.

The [repeat run](evidence/experiments.md#experiment-023)
adds the empty-query authorization guard. Model inputs, prompt, and raw outputs
are identical to the first test. Scores are unchanged. Its p95 is 20.24 seconds.
Both test runs have no source drift. No prompt tuning followed the test results.

Some failures were output-contract failures. For example, the model put a
summary in the `gap` field while it selected `answer`. One otherwise useful
bicycle quote changed the source's lowercase word to uppercase. The exact
quote check rejected that response. This is stricter than factual similarity.

Other failures were semantic. The model confused missing garage decision
evidence with a lawn-camera decision. It mixed campaign data into the family
spending question. It treated the absence of historical secret values as an
availability problem. A valid protocol citation still received an incorrect
clarification request. Structural validation cannot correct these errors.

The bank-PIN query correctly returned `no_evidence`. The denied journal was
not in the model input. The current-calendar query correctly reported an
unavailable live source. These successes do not offset the other failures.

## Scope and method

The [protocol](../experiments/knowledge/sufficiency/protocol.json) and
[run instructions](../experiments/knowledge/sufficiency/README.md) define the
experiment. It reuses frozen candidate lists from the earlier combined search
with evidence links. Retrieval already found all required sources for the
18 supported questions. The new stage must choose useful evidence from them.

The runner reconstructs the synthetic source store and checks current access
before inference. The model receives source text, source times, the question,
fixture time, and opaque citation aliases. Expected modes and relevance labels
stay outside model input. The known questions are not an independent benchmark.

The second development version moved mode selection after evidence and added
two unrelated examples. It produced six valid responses and one expected mode
out of ten questions. The test used that prompt without further tuning.
Its [development evidence](evidence/experiments.md#experiment-020)
is retained, including the poor scores.

The initial harness failed to join split Go test-output events. The repaired
collector has regression tests. Another fix moved worker cancellation after
normal process cleanup. Neither repair changes the model's quality score.

## Access and citation checks

The internal support boundary now performs these checks:

- Authorize the question's control scope, even when retrieval returns no records.
- Check caller authority, represented identity, purpose, and processor trust.
- Admit only currently permitted source text. Do not expose denied source IDs.
- Bound candidate count, text size, model context, and output size.
- Reject duplicate JSON keys, unknown fields, invalid modes, and invented quotes.
- Check every input again before result release, including uncited inputs.
- Reject changed source bytes or changed access after inference.

Each access boundary uses one database snapshot. No transaction stays open
during inference. A later denial cannot withdraw bytes already sent to the
processor. Atomic policy revocation across network delivery remains open.

The checks have executable tests, but the query context still comes from
fixtures. W02 authentication is not replaced. The model has no tools; this
does not prove resistance to tool misuse caused by imported instructions.

## Next Actions

Use a new evaluation version for a stronger model or a different support method.
Keep source availability and freshness as explicit adapter data where possible.
Test answer correctness separately from citation integrity and support modes.
Do not let this model's assessment authorize an action or relax source policy.
