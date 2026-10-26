# Retrieval comparison

This experiment compares evidence retrieval, not final answer generation.
It uses synthetic records, local CPU models, and a disposable PostgreSQL
container. No paid model endpoint or production account is required.

Read [protocol.json](protocol.json) before changing the methods or labels.
The data has 10 development questions and 20 separate test questions.
The same author prepared both sets. This is not an independent benchmark.
Do not tune methods after inspecting the test results. Make a new evaluation
version for later changes.

## Methods

Each method returns at most ten records under the same fixture authority.

| Method | Operation |
| --- | --- |
| text_all | Match all normalized words from the original question. This retains the previous baseline. |
| text_any | Match any normalized word, then rank the results with PostgreSQL text ranking. |
| model_text | Ask the local Qwen model for up to three search strings. Use any-word matching and combine the result rankings. |
| embedding | Encode the original question. Compare its vector with authorized record vectors by exact cosine similarity. |
| hybrid | Combine text_any and embedding rankings with reciprocal rank fusion, with constant 60. |
| hybrid_links | Start with the first three hybrid results. Add their permitted one-hop links, then fill the remaining positions from hybrid results. |

The first development run used all-word matching for model-generated strings.
It exposed poor recall when the model added absent words. The second development
run changed that method to any-word matching. The planner prompt did not change.
Keep the first result as evidence of that change; do not mix the two versions.

## Data and scoring

[corpus.json](corpus.json) contains 45 source records. The test adds 300
deterministic distractors. Four source records must not enter the embedding
processor: another user's private record, a foreign record, a deleted record,
and a derivative of the deleted record. The permitted index has 341 records.

[questions.json](questions.json) contains questions and evaluator labels.
Models receive question text or permitted record text, not record IDs,
relevance labels, required record lists, or expected response modes.
Two questions receive a September time window. All other questions have no
time filter. Time-window interpretation is not performed by the model.

The primary score is the number of questions for which the first ten candidates
contain every required supporting record. The report also includes any-source
hits, average source recall, precision, reciprocal rank, failures, and latency.
Questions with no supporting evidence have a separate count of returned
candidates. That count is not a refusal or hallucination score.

Expected modes include answer, partial, clarify, unavailable, and no_evidence.
They describe the intended later answer behavior. The retrieval methods do not
predict those modes. A retrieved outage record can support a truthful failure
response; it does not make the unavailable source available.

Queries run sequentially in fixed method order. Timing includes fresh fixture
permission checks and live query inference. It excludes initial model loading,
record indexing, public RPC, final answers, and speech. Per-record permission
checks are deliberately simple and not a production query plan.

## Local dependencies

The embedding model is the quantized AVX2 ONNX export of all-MiniLM-L6-v2.
[model.json](model.json) pins its revision, size, model hash, and tokenizer blob.
The weights require about 23 MB. The worker applies attention-mask mean pooling
and normalizes each vector. It rejects inputs beyond 256 tokens instead of
silently truncating them. Only the CPU execution provider is enabled.

The planner uses an existing Qwen2.5 1.5B Instruct Q4_K_M GGUF file and an
existing llama.cpp server binary. Default paths are under the current home
directory:

- `models/qwen2.5-1.5b-instruct-q4_k_m.gguf`.
- `llamacpp/llama.cpp/build/bin/llama-server`.

Set `OPAL_PLANNER_MODEL` and `OPAL_LLAMA_SERVER` to use other local paths.
The runner records their hashes. A different model or binary is a different
configuration, not a repeat of the same model comparison.

The runner starts only its own loopback server. It disables GPU offload, uses
two CPU threads, and stops that process when the run ends. It does not reuse
or stop other inference services. The PostgreSQL harness removes only its
labelled synthetic container and volume.

## Run the comparison

Use the requirements from the [parent experiment](../README.md). Run these
commands from `experiments/knowledge/`.

Create an isolated Python environment and install the locked binary packages:

```sh
uv venv --python /usr/bin/python3.14 .cache/retrieval-venv
uv pip sync --python .cache/retrieval-venv/bin/python --require-hashes --only-binary :all: retrieval/requirements.lock
```

Download and verify the two pinned model files:

```sh
node retrieval/prepare.mjs
```

This command downloads public model files, not inference requests. Model
processing during the comparison is local. Existing valid cache files are
reused. An unexpected file size or hash stops preparation.

Run development questions first:

```sh
node retrieval/compare.mjs dev
```

After freezing the settings, run the separate test questions:

```sh
node retrieval/compare.mjs test
```

Each run creates a unique directory under `results/`. It contains the summary,
per-question candidates and plans, raw Go test events, and local model logs.
The summary records model hashes and source hashes before execution. It checks
for source changes at the end. Do not edit measured sources during a run.

The original 200,000-segment experiment remains separate:

```sh
node run.mjs --corpus
```

That run does not execute model comparisons. Its explicit comparison skip is
expected. The small quality corpus is not a replacement for scale tests.

## Limits

This is an internal test harness, not an authenticated API. It uses fixture
grants. The model worker has no source tools or resource credentials. Its
planner output is text, not SQL or policy. Denied sources are excluded before
embedding, and current permissions are checked before candidate selection.
Concurrent policy revocation still needs the separate service contract.

Vectors use an exact in-memory scan. There is no vector database or approximate
index. The corpus has short records and hand-authored links. It does not prove
large-scale performance, automatic relationship extraction, prompt-injection
resistance in an answer agent, or evidence sufficiency decisions.

## Next Actions

1. Review complete-evidence results and individual misses, not only source hits.
2. Test an evidence sufficiency step before generating answers from candidates.
3. Test more varied records and scale after selecting a candidate retrieval path.
4. Keep authentication and real-data capture behind the existing W02 and W13 gates.
