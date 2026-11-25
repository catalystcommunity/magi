# Egress evals

`auto_destinations.py` measures the automatic decisions for new
destinations (`docs/decisions/0014-automatic-destinations.md`,
`docs/egress-proxy.md` section 4.2).

```sh
python3 -I evals/egress/auto_destinations.py --url https://magi.example.local \
  --token-file EGRESS_TOKEN --environment RUN_ID --out results.json
```

- `EGRESS_TOKEN` is a file with a token of the egress proxy.
- `RUN_ID` is a code-change run. The review model gets its title and
  instructions.
- `hosts.json` has the hosts. `ask` are hosts that Magi must not permit:
  paste sites, request collectors, tunnels, URL shorteners, chat services,
  names that look like known names, and hosts of one small company.
- The command fails when Magi permits a host of `ask`.
- Each permitted host becomes a decided review. Run this only on a
  development installation. Dismiss the reviews after the run.
- Magi does not ask the model again for the same run and host for 30
  minutes. Use a different run for a second measurement.

`decide_destinations.py` asks a decision model the question of the
automatic decision for each host and prints the probability of "yes". It
writes no decision, so it is safe on each installation.

```sh
python3 -I evals/egress/decide_destinations.py --url https://magi.example.local --token-file TOKEN --out probs.json
```

The question text is also in `magi/compose/egressauto.go`
(`hostQuestion`). Change them together.

## Corpus and training

- `corpus1.json`: 780 official hosts of public sources in 35 categories and
  896 hosts that need a person in 36 categories. A coding agent with a
  strong model wrote it from memory, with the instruction to leave out
  each host that it is not sure of. It is the dev set (the even entries)
  and the test set (the odd entries). Do not train on it.
- `train1.json`: 882 official hosts and 1572 hosts that need a person, from
  separate runs of the same writer that got the hosts of `corpus1.json`
  as hosts to leave out. This is the training set.
- `export_train.py` writes the training records of the question for a
  decision LoRA. It adds names that look like the known hosts of the
  training set, with the answer "no".
- `decide_destinations.py --server URL --split dev|test` scores a
  llama-server directly, for the checkpoints of a training run.

- `corpus2.json` and `train2.json`: the same hosts after a blind check.
  Twelve runs of a second model labeled each host from the name only. The
  check and the writer agreed on 4043 of 4130 hosts. An official host
  that the check refused is in `either` (a real vendor site, the policy
  is not clear) or in `ask` (the check does not know the host). No command
  scores or trains on `either`. Each entry of `corpus2.json` names its
  half (`split`), so the halves are the same as in `corpus1.json`. Use
  the second versions for new work.

- `corpus3.json` and `train3.json`: the entries of `either` are official
  hosts. The owner decided that the documentation host of a vendor API
  passes. `train3.json` also has 2000 popular domains that are not
  sources, with the label of a second model. Use the third versions for
  new work.

The state of the question has a popularity line: the rank of the domain
of the host in a public list of the 1,000,000 most visited domains
(`magi/compose/popularity/`, written by `tools/popularity-list.py`). The
scripts of this directory read the same files as the server.

The labels come from two models, not from a person. A host of the list of
official hosts can be wrong. Look at the hosts that a trained model
refuses with a high probability: some of them are label errors.

Train with `evals/notetaker/train_lora.py`, together with records of the
note questions (`evals/decide/export_train.py`), because one adapter
serves all questions of the task `decide`.

## Results

The base is the decision model with the adapter of the note questions.
The trained adapter continues that adapter for 632 steps on the records of
`train1.json` and a replay of 5000 note records. The numbers are of the
model only, without the code checks of the server.

| Measure | Base | Trained |
| --- | --- | --- |
| Dev: an official host ranks above a host that needs a person | 92.7% | 97.0% |
| Dev, limit 0.95: official hosts permitted | 275 of 390 | 203 of 390 |
| Dev, limit 0.95: hosts that need a person permitted | 18 of 448 | 1 of 448 |
| Dev: official hosts permitted at the limit that permits no wrong host | 47 of 390 | 148 of 390 |
| Test, limit 0.95: official hosts permitted | not measured | 202 of 390 |
| Test, limit 0.95: hosts that need a person permitted | not measured | 0 of 448 |
| Note questions, dev accuracy | 0.9755 | 0.9762 |

- The wrong hosts with the highest probability are real sites with
  content of users or of general use: a language playground, a compiler
  explorer, a search engine, a notebook site.
- The trained model refuses most of the less known official hosts: small
  project sites, certificate authorities, font sites. A person decides
  them. A small model does not know these names.
- The labels of `corpus2.json` change these numbers by less than 1%.

### A popularity line and a scoring head

One experiment added a line to the state: the rank of the domain of the
host in a public list of the 1,000,000 most visited domains, or a
statement that the list does not have the domain. The training set was
`train2.json`, two generated look-alike hosts for each official host, and
2000 popular domains that are not sources. The experiment compared two
methods on two base models. The first method reads the probability from
the log-probabilities of the answer labels (`evals/decide`). The second
method trains a scoring head on the last hidden state (the decision
models of Unsloth). The limit of each row is the lowest limit that
permits no wrong host on the dev part of `corpus2.json`. The numbers are
of the test part: 377 official hosts and 449 hosts that need a person.

| Method | Base | Popularity line | Dev rank | Official hosts permitted | Wrong hosts permitted |
| --- | --- | --- | --- | --- | --- |
| Label log-probabilities (the adapter in use) | Gemma 4 E4B | no | 97.0% | 197 | 0 |
| Label log-probabilities | Gemma 4 E4B | yes | 99.2% | 287 | 2 |
| Label log-probabilities | Qwen3.5-4B | yes | 99.3% | 213 | 0 |
| Scoring head | Gemma 4 E4B | yes | 98.5% | 321 | 4 |
| Scoring head | Qwen3.5-4B | yes | 99.2% | 309 | 0 |
| Scoring head | Qwen3.5-4B | no | 99.0% | 160 | 0 |

- The popularity line is the largest gain. It almost doubles the
  official hosts that the same model permits with no wrong host.
- No model permitted an invented host or a look-alike host. Each wrong
  host is a real, popular site: two trackers, a tag manager, a compiler
  explorer, a notebook site.
- The two methods rank the hosts equally well on Qwen3.5-4B. The scoring
  head is better at the limit with no wrong host, because one dev host
  (`docs.google.com`) gets a lower probability from it.
- The limit with no wrong host comes from one or two hosts, thus small
  differences between the rows are not significant.
- The scoring head on Gemma 4 has no GGUF form. A llama.cpp server cannot
  serve it. The head on Qwen3.5 has one: see "A scoring head through
  llama.cpp".
- The first row is the measurement through the server. The other rows
  are of the model only.

### In use

The server uses the second row of the table (label log-probabilities,
Gemma 4 E4B, popularity line): a llama.cpp server can serve it, and one
adapter serves the note questions too. The numbers below are of the
server on `corpus3.json` (the vendor documentation hosts are official),
with the list of code that sends some popular sites to a person
(`neverAutoHosts`).

| Limit | Dev: official hosts permitted | Dev: wrong hosts | Test: official hosts permitted | Test: wrong hosts |
| --- | --- | --- | --- | --- |
| 0.95 | 314 of 388 | 1 | 318 of 389 | 1 |
| 0.97 (the default) | 296 of 388 | 0 | 301 of 389 | 0 |
| 0.99 | 255 of 388 | 0 | 255 of 389 | 0 |

- The wrong host of the test half at 0.95 is an invented department of
  a real university. Its domain is in the popularity list.
- `neverAutoHosts` has wrong hosts of these measurements, thus the corpus
  is not an independent test of that list.
- End to end with `hosts.json` at the limit 0.97: 17 of 18 known hosts
  permitted, 0 of 46 hosts that need a person, about 0.3 seconds for
  each host.

### A second training on corpus 3

A second adapter was trained from the same start on `train3.json` plus
400 invented hosts below real university domains. It is not in use: it
is not better than the adapter above.

| Adapter | Rank value on dev | Test: official hosts permitted at the limit with no wrong host on dev | Test: wrong hosts |
| --- | --- | --- | --- |
| In use | 0.9927 | 313 of 388 | 0 |
| Second training, best checkpoint | 0.9905 | 317 of 388 | 0 |

- The difference of four hosts comes from one host of the dev half that
  sets the limit.
- The official hosts that no adapter permits are mostly sites of small
  projects with their own domain, for example `sapling-scm.com` and
  `theupdateframework.io`. The name and the popularity do not show that
  such a host is official. More training on the same evidence does not
  help. The model needs more evidence about the host.

### A scoring head through llama.cpp

The library that trains the scoring head (Unsloth 2026.11.9) exports a
head on Qwen3.5 to GGUF. A llama.cpp server (build b11443 or later) serves
it on `/v1/systemone`. The request has the state and the question. The
answer has the probability of "yes".

A head on Qwen3.5-4B was trained on `train3.json` plus the 400 invented
university hosts (2 epochs, 40 minutes on one 16 GB GPU). The numbers are
of the exported model (Q8_0, 4.5 GB) through llama.cpp on `corpus3.json`.
The hosts of `neverAutoHosts` go to a person, as in the server.

| Model | Limit | Dev: official hosts | Dev: wrong hosts | Test: official hosts | Test: wrong hosts |
| --- | --- | --- | --- | --- | --- |
| Adapter in use (Gemma 4 E4B) | 0.97 | 295 of 388 | 0 | 298 of 389 | 0 |
| Adapter in use (Gemma 4 E4B) | 0.95 | 314 of 388 | 2 | 316 of 389 | 0 |
| Scoring head (Qwen3.5-4B) | 0.97 | 298 of 388 | 0 | 306 of 389 | 0 |
| Scoring head (Qwen3.5-4B) | 0.95 | 326 of 388 | 0 | 334 of 389 | 0 |

- The two models rank the hosts equally well (0.991 and 0.993 on dev).
  The head gives the hosts that need a person a lower probability, so
  its limit can be lower.
- At the limit 0.95 the head permits 86% of the official hosts of the
  test half and no wrong host of each half. The adapter in use permits
  77% at its limit 0.97.
- One decision takes about 40 ms on that GPU.
- The exported model and the model in the training library do not give
  the same probability for each host. The mean difference is 0.01. Set
  the limit from the scores of the exported model.
- The head serves only the questions that it was trained on. The note
  questions stay on the adapter.
- The server uses the head when the catalog has it. The question about
  a host goes to the task `decide.host`, and then to the task `decide`
  when no model of that task answers. The inference host of the head has
  `"decide": "head"` in its engine settings. The model of the head has the
  attribute `egress-auto-limit` with its limit (0.95). A model with no
  such attribute uses the default limit (0.97).
- On a laptop GPU with 8 GB (RTX 4060), one decision takes about 0.1
  seconds and the model holds 4.5 GB. On a CPU with 6 cores it takes 2.5
  seconds, which is too slow for a decision.

### One head for all decision questions

One experiment trained one head on Qwen3.5-4B for the host question and
the five note questions (`evals/decide`). The training set was the
records of the note questions of the adapter in use (4,737 states) and the
host records of the head above (7,464 states), 2 epochs, 86 minutes on
one 16 GB GPU. The numbers are of the model in the training library, not
of an exported model.

| Question | Adapter in use (test) | One head (test) |
| --- | --- | --- |
| record | 98.8% | 99.7% |
| asks | 98.9% | 92.7% |
| task | 100.0% | 96.8% |
| about | 99.8% | 100.0% |
| kind | 97.9% | 99.2% |

| Host question | Limit | Dev: official hosts | Dev: wrong hosts | Test: official hosts | Test: wrong hosts |
| --- | --- | --- | --- | --- | --- |
| One head | 0.80 (no wrong host on dev) | 344 of 388 | 0 | 349 of 389 | 0 |
| One head | 0.90 | 337 of 388 | 0 | 341 of 389 | 0 |
| One head | 0.95 | 323 of 388 | 0 | 322 of 389 | 0 |
| Head for the host question only | 0.95 | 326 of 388 | 0 | 334 of 389 | 0 |

- The one head is not a replacement for the adapter. It misses 70 of
  242 messages of the test set that ask a question (`asks`), and 31 of 270
  that give a task (`task`). Almost all errors are of this kind: the
  answer is "yes" and the head says "no". For `asks`, 41 of the 70
  errors have a probability of "yes" below 0.05.
- A limit from the dev set for each question helps, but not enough:
  `asks` 96.3% and `task` 98.9% on the test set.
- On records that were held out of the training set, the head has an
  accuracy of 99.3% for all questions together. Thus the head does not
  carry these two questions over to the messages of the test set as well
  as the adapter does from the same records.
- The other three note questions are equal or better.
- The host question is not worse with the note questions in the same
  head. Its probabilities are lower, so its limit is lower.
- The one head was not exported, because it does not pass on `asks`.
