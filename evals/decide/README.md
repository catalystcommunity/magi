# Decision model

A decision model answers typed questions about a state with probabilities.
It does not write text. The model is an open LLM on llama-server. This is
the method of Jev (TypeSafe AI) and the Gemini Decisions API, but local.

## Method

1. A question has a type: `noul` (yes or no), `choice` (one of up to 26
   options), or `score` (a whole number from 0 to N, N <= 9).
2. The prompt gives the state, the question, and the options with labels
   of one token (`A`, `B`, ... or `0`, `1`, ...). `decide.py` makes sure
   that each label is one token (`/tokenize`).
3. The generation prompt of Gemma 4 ends where the answer starts. The
   model reads the prompt once. llama-server gives the log-probabilities of
   the first token (`n_probs` 200).
4. The probabilities of the labels, normalized over the labels only, are
   the answer. A label that is not in the top 200 gets the lowest value
   minus 1.
5. One temperature for each question makes the probabilities match how
   often the model is right. `eval_notes.py report` fits it on dev (the
   lowest negative log-likelihood).

All questions of one state have the same prompt prefix. llama-server uses
its prompt cache for the next questions only with these settings:

- `--swa-full`. Gemma 4 has layers with a sliding window. Without this
  option, llama-server keeps only the window, and it reuses only the
  system prompt.
- The questions of one state go to the same slot (`id_slot`).
- `--swa-full` uses more memory: `-c 12288 -np 4` fits on a 16 GB GPU
  with Gemma 4 12B; `-c 32768` does not.

## Eval on note-taker messages

`eval_notes.py` asks 6 questions about each case of
`evals/notetaker/data5`. The correct answers come from the specs:

| Question | Type | Correct answer |
| --- | --- | --- |
| `record` | noul | Yes if one part is not a question, request, plan, wish, small talk, or a known fact |
| `asks` | noul | Yes if a part is a question |
| `task` | noul | Yes if a part is a request (not "note this") |
| `count` | score 0-4 | The number of `has`, `ended`, and event checks |
| `about` | choice of 4 | The kind of the subject of the `has` check: the writer, a person, a pet, a thing |
| `kind` | choice of 6 | New fact, change, correction, ended, happening, nothing. A change: the notes have a claim of the same subject and relation |

```sh
evals/decide/eval_notes.py run "evals/notetaker/data5/dev-*.jsonl" --url http://127.0.0.1:18195 --out dev.jsonl
evals/decide/eval_notes.py run "evals/notetaker/data5/test-*.jsonl" --url http://127.0.0.1:18195 --out test.jsonl
evals/decide/eval_notes.py report --dev dev.jsonl --test test.jsonl --temps-out temps.json
```

Do not send these requests to the engine of `magi-inference`. The host
does not see them as its own work, thus it sees a busy GPU and stops the
engine. Stop `magi-inference` and start a separate llama-server.

## Results (2026-11-14, Gemma 4 12B Q4_K_M, data5 test)

| Question | n | T | Accuracy | ECE raw | ECE cal | Brier raw | Brier cal |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `record` | 997 | 1.57 | 94.9% | 0.025 | 0.009 | 0.080 | 0.078 |
| `asks` | 997 | 2.72 | 98.5% | 0.011 | 0.023 | 0.025 | 0.024 |
| `task` | 997 | 2.01 | 97.5% | 0.016 | 0.010 | 0.040 | 0.037 |
| `about` | 494 | 4.06 | 77.1% | 0.217 | 0.061 | 0.439 | 0.358 |
| `kind` | 931 | 2.72 | 76.0% | 0.203 | 0.082 | 0.436 | 0.366 |
| `count` | 997 | 3.86 | 47.6% | 0.448 | 0.153 | 0.947 | 0.674 |

Each question: median 167 ms, p90 299 ms (4 parallel workers). The
first question of a state takes about 200 ms (about 500 tokens); the next
questions take about 80 ms.

- Without calibration, the probabilities are almost 0 or 1. Calibration
  divides the ECE by 3 to 4 on the hard questions.
- The yes or no questions with a clear rule are good (95% to 98.5%).
- `about`, `kind`, and `count` are worse, and some of their answers are
  not clear. "Saw Ingrid at the farmers market" can be a happening or a
  new fact. "Celia's new employer is Quarry Robotics" can be new or a
  change. The model uses the meaning of the words, not the rules of the
  specs. Better option texts, or training on these rules, are necessary.
- An early `task` question ("a reminder, a list item") got 12 of 20,
  because "note this for me" is a request. The question now says that
  notes do not count. The text of the question is the most important part.

`results/` has the scores, the temperatures, and the log-probabilities of
each question (gzip).

## Small models (2026-11-14, data5 test, calibrated on dev)

| Question | Gemma 4 12B | Gemma 4 E4B | Qwen 3.5 4B | Qwen 2.5 1.5B |
| --- | --- | --- | --- | --- |
| `record` | 94.9% | 84.2% | 88.7% | 78.4% |
| `asks` | 98.5% | 98.5% | 98.3% | 83.0% |
| `task` | 97.5% | 90.5% | 86.9% | 76.6% |
| `about` | 77.1% | 82.2% | 74.3% | 42.1% |
| `kind` | 76.0% | 62.1% | 50.6% | 32.4% |
| Median for each question (RTX 4080) | 167 ms | 69 ms | 279 ms | 28 ms |

Qwen 3.5 has recurrent layers: llama.cpp cannot reuse a part of the
prompt, so each question reads the full state again. Gemma 4 E4B reuses
the prompt of the state (`--swa-full`) and is the best small model.

On lab-nas (RX 7600, 8 GB, with 3.6 GB used by the speech engines), E4B
runs with its per-layer embeddings on the CPU:
`--device Vulkan1 -ngl 99 -ot per_layer_token_embd=CPU --swa-full -c 8192 -np 2`.
The GPU then uses 7.2 GB in total. The first question of a state takes
about 430 ms; the next questions take about 190 ms. Through Magi
(`MagiService/decision`, 3 questions): 330 to 430 ms on the host.

## Next

1. Done: `InferenceService/decide` in the inference host, and
   `MagiService/decision` with the model catalog (`docs/models.md`).
2. A LoRA for decisions, trained on the questions of `train5` (about
   28,000 answers of one token). This is the training step of Jev that
   the method above does not have.
