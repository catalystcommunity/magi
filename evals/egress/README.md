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

The labels come from a model, not from a person. A host of the list of
official hosts can be wrong. Look at the hosts that a trained model
refuses with a high probability: some of them are label errors.

Train with `evals/notetaker/train_lora.py`, together with records of the
note questions (`evals/decide/export_train.py`), because one adapter
serves all questions of the task `decide`.
