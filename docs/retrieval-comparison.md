# Retrieval comparison results

Measured 2026-10-12 in America/Denver. Result filenames use 2026-10-13 UTC.
The comparison uses local CPU models and synthetic evidence. It does not
require a paid API, GPU execution, or a new database service.

Use combined keyword and embedding search with permitted evidence links as
the candidate for the next prototype. In the separate test set, that method
recovered every required record for all 18 questions with supporting evidence.
It did not establish whether Magi could produce a correct final answer.

The subsequent [support experiment](evidence-support.md) confirms that finding
records is not sufficient. Its small local model matches the expected support
mode for only 8 of 20 questions. Search can return related records when the
requested fact is absent. Keep source retrieval and answer validation separate.

## Evaluation scope

The [run instructions](../experiments/knowledge/retrieval/README.md) and
[frozen protocol](../experiments/knowledge/retrieval/protocol.json) define the
comparison. The corpus has 45 authored synthetic records and 300 deterministic
distractors. Access checks exclude four records before embedding. The resulting
index has 341 records. It uses exact vector comparison, not a vector database.

Ten development questions were used before the separate 20-question test run.
The test set has 18 questions with supporting records and two with none.
Supporting records can describe a gap or an outage. They do not necessarily
contain enough data for the requested final answer.

Both sets were prepared during this work. They are not an independent benchmark.
Two questions have a supplied September time window; the others have none.
Models do not receive relevance labels, required record IDs, or expected
response modes. No method or prompt was tuned after the test results.

The [first development run](evidence/experiments.md#experiment-014)
showed that all-word matching was too strict for many model-generated queries.
The model sometimes added words such as "archive" that were absent from the
records. The method changed to any-word matching for its generated strings.
The planner prompt stayed unchanged. The
[second development run](evidence/experiments.md#experiment-015)
then used the same settings as the separate test run.

## Test results

The primary measure is complete evidence within the first ten candidates.
This means that every required supporting record is present. It does not mean
that the system selected the correct conclusion from those records.

| Method | At least one supporting record | All required records | p95 ms |
| --- | --- | --- | --- |
| Original all-word search | 2 / 18 | 2 / 18 | 244 |
| Any-word keyword search | 18 / 18 | 16 / 18 | 230 |
| Model-assisted keyword search | 17 / 18 | 16 / 18 | 3506 |
| Embedding search | 18 / 18 | 17 / 18 | 223 |
| Combined keyword and embedding search | 18 / 18 | 17 / 18 | 229 |
| Combined search with evidence links | 18 / 18 | 18 / 18 | 256 |

These values come from the
[first separate test summary](evidence/experiments.md#experiment-017).
The [per-question results](evidence/experiments.md#experiment-016)
retain candidates, generated search strings, errors, and inference timing.
All 120 method/question evaluations completed without retrieval errors or
unauthorized results in this fixture set.

The [repeat test run](evidence/experiments.md#experiment-018)
used unchanged sources and settings. Its coverage scores were identical.
Combined search with links had p95 of 238 ms in that run, but one request took
2685 ms. The cause of that delay was not measured. Do not hide it behind the
p95 result or treat this shared host as a controlled latency environment.

Both test runs returned identical candidate lists and generated search strings.
The 240 method/question evaluations had no recorded errors. The
[original regression run](evidence/experiments.md#experiment-019)
also passed: 56 leaf-test passes over two runs, with no failures or race-detector
findings. Its two comparison skips are deliberate; the model comparison runs
separately. Generated Go/Rust checks, `go vet`, Python compilation, and Node
syntax checks passed. The original large-corpus recall remains 50 percent.

Queries ran sequentially in a fixed method order. Every method performs a
fresh, simple permission check for each corpus record. That work dominates
the roughly 0.2-second non-planner times. The measurements include query model
inference but exclude model startup, initial indexing, public RPC, final answer
generation, and speech. They are not production latency guarantees.

The embedding model took about 1.0 second to index the 341 permitted records.
Its p95 query encoding time was about 3 ms. The planner processed 2795 prompt
tokens and generated 571 tokens over the 20 questions. No paid inference
service was called. CPU and memory use still have a resource cost.

## What changed the result

The question about the mistaken calendar interpretation illustrates the value
of explicit links. Keyword search and embedding search both found the mistaken
record but missed the later correction. Following the interpretation link
recovered the correction and preserved both records for later explanation.

The question about the mass of a dead star also exposed a keyword limit.
Any-word search found the earlier discussion but not its supporting reference.
Embedding search recovered both within the supplied month window.

The small planner did not improve complete-evidence coverage over any-word
search. It also missed the record that explained why current calendar data
was unavailable. Its generated terms focused on "free" and the date, which
matched unrelated records. Keep query planning available as a separate tool;
this result does not support making it mandatory for every lookup.

The difference between the original baseline and any-word search matters.
PostgreSQL `plainto_tsquery` joins surviving terms with AND. The new control
uses an OR of safely quoted normalized terms. It does not pass model text
through as SQL. See [PostgreSQL 17 query parsing](https://www.postgresql.org/docs/17/textsearch-controls.html#TEXTSEARCH-PARSING-QUERIES).

Combined search uses reciprocal rank fusion with constant 60. Link expansion
then visits permitted one-hop links from its first three candidates, within
the same ten-record limit. The combination is an experiment setting, not a
claim that these constants are optimal. See the
[reciprocal rank fusion paper](https://cormack.uwaterloo.ca/cormacksigir09-rrf.pdf).

## Missing evidence and irrelevant candidates

The two questions without supporting evidence ask for another person's private
bank PIN and tomorrow's dinner guests. The bank PIN is a fictional denial
canary, not a real credential. Its source never enters the embedding processor
or the returned candidates.

Embedding search and both combined methods still return ten permitted
candidates for each question. For example, a general note about a bank source
is not evidence of a PIN. A calendar discussion is not a dinner guest list.
Any-word search and model-assisted search return candidates for one of these
two questions. Original all-word search returns none for either question,
but also misses most questions that have evidence.

Even the best complete-evidence result has only 16.7 percent average precision
at ten on the 18 supported cases. There are few relevant records per question,
and the methods often fill the remaining positions with unrelated material.
An answer process must not treat every returned item as relevant or true.

Expected response modes are labels only in this experiment. No mode classifier,
answer model, or abstention rule was evaluated. In particular, the test does
not prove correct treatment of an ambiguous call, incomplete financial sources,
or a stale calendar snapshot. It tests access to the supporting records for
those decisions.

## Models and safety boundaries

The embedding baseline is the approximately 23 MB AVX2 quantized ONNX export
of all-MiniLM-L6-v2. Its revision and hashes are in
[model.json](../experiments/knowledge/retrieval/model.json). The worker uses
attention-mask mean pooling and normalized 384-dimensional vectors. It rejects
oversized input instead of silently truncating it. This is a compact baseline,
not a production model selection. See the
[model card](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2).

The planner uses the existing Qwen2.5 1.5B Instruct Q4_K_M file with llama.cpp
build 8265. Both model processes use two CPU threads. The server runs only on
loopback with GPU offload disabled. Each result summary records model and
binary hashes. The runner stops only the server process it created.
The local server uses constrained JSON output through its
[completion API](https://github.com/ggml-org/llama.cpp/tree/master/tools/server).

Test database containers and their synthetic volumes were removed. The local
planner processes were stopped. The small model cache, isolated Python
environment, source fixtures, and result logs remain for repeat runs.

The access tests cover denied sources before embedding, permission filtering
before the candidate limit, and a deletion barrier applied after vectors were
built. They also check link filtering and invalid vectors. Original fixture
policies remain service-local rules, not LinkKeys authorization.

This remains an internal experiment. It does not implement W02 authentication,
SQL row security, concurrent policy revocation, or separate-domain federation.
The stored instruction-like distractor is source data. There is no answer agent
with tools in this test, so no prompt-injection-resistance claim follows.

## Recommendation and limits

Keep PostgreSQL records and keyword search. Add embedding retrieval behind a
replaceable interface. Preserve source, interpretation, and correction links
as first-class evidence. Select candidates from permitted data, then assess
whether those candidates actually support the request.

The first practical test for that assessment is the correction question:
explain the mistaken admission from its retained record, then cite the later
correction without rewriting what happened. The negative cases must return a
truthful evidence gap rather than turn related calendar or banking text into
an unsupported answer.

Do not select a separate vector service from this result. An exact scan of
341 short records says little about index behavior at 200,000 records or more.
The original large corpus and its failed paraphrase baseline remain unchanged.
The next scale test must use the selected retrieval path and a more varied
corpus. Hand-authored links also do not prove automatic relationship extraction.

## Next Actions

1. Improve the tested evidence sufficiency stage with a new evaluation version.
   The small model does not reliably select the correct sources and support mode.
2. Test more varied documents, longer histories, and larger indexes. Keep the
   current test set frozen and use new questions for further tuning.
3. Keep the small planner optional until another measured use case needs it.
4. Complete W02 and evidence capture safeguards before exposing APIs or using
   real private data. No user account or model endpoint is needed for the next
   local synthetic step.
