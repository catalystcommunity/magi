# Decisions: a decision model with a scoring head answers the host question

The question about a new host of a task environment (the automatic destination decision, docs/egress-proxy.md 4.2) now goes to the adapter of the task `decide`. A better model exists: a model with a scoring head in place of its language head (evals/egress/README.md, "A scoring head through llama.cpp"). Make an inference host serve such a model, and make Magi ask it first, with the limit that was measured for it.

Files: primitives/inference/host/ (host.go, decide.go, the tests), primitives/inference/README.md, magi/compose/ (egressauto.go, decision.go, the tests), docs/egress-proxy.md, evals/egress/README.md. No contract changes.

1. A new engine setting of the inference host: `"decide": "head"` in `engine` (next to `command`, `port`, `model`). It tells that the engine is a decision model. With no `decide` key, decide works as before.

2. `InferenceService/decide` on a host with `"decide": "head"`. The engine is a llama-server that serves the model on `/v1/systemone`. The host sends the state and all questions in one call: POST /v1/systemone with the JSON body

   {"model": "<the model of the engine>", "state": "<the state>", "questions": {"<id>": {"type": "noul", "instructions": "<the question text>"}}}

   The engine answers with the probability p of "yes" for each question:

   {"answers": {"<id>": {"type": "noul", "noul": 0.96}}}

   The host answers each question with probs [p, 1 - p]. The answer is "yes" when p is 0.5 or more, else "no", and the confidence is the larger probability. The answers are in the sequence of the questions. The temperature of a question is not used: a head has its calibration from its training. The checks of the caller, the limits of the request, the model name of the answer, and the count of the request as work of the host stay as they are for decide now.
   - A decision model answers only "noul" questions: a question of a different type is not valid (status 400).
   - Each question needs its own id, and the id is not empty: else the request is not valid (status 400).
   - When the engine gives no answer for a question, decide fails. It is an error of the host (not status 400), and it is not the answer "no". A number that is not from 0 to 1 is not a probability: decide fails in the same way.

3. Magi (magi/compose). The question about a new host goes to the models of the task `decide.host` of the model catalog. When the catalog has no task `decide.host`, or the task has no model, the models of the task `decide` answer, as before. Magi asks the models of the task in their sequence. The first model that answers decides. A model that fails, or that gives no probability, does not answer. The question, its id, and the state do not change.

4. The limit. A model of the catalog can have the attribute `egress-auto-limit`: the least probability of "yes" that permits a host, as it was measured for that model. The limit of a decision is `limit` of the `auto` setting of the egress rules, when it is set. With no limit in the setting, the limit is the attribute `egress-auto-limit` of the model that answered. A model with no such attribute has the limit 0.97, as now. An attribute value that is not a number above 0 and at most 1 counts as no attribute. The note of the decision and the log line have the limit that Magi used, as now.

5. Tests. In primitives/inference/host: decide on a host with a decision model, with the fake engine of the tests (yes, no, a question of a different type, an answer that the engine does not give). In magi/compose: the host question with and without the task `decide.host`, a first model that is down, and the three sources of the limit.

6. Docs (ASD-STE100):
   - primitives/inference/README.md, section "Decide": `"decide": "head"`, what such a model is, the one call to `/v1/systemone`, and its limits: only `noul` questions, only the questions that it was trained on, no chat. The `question` text of a request is the instruction of the training, so it must be the same text.
   - docs/egress-proxy.md 4.2, the row `"model": true`: the task `decide.host`, else the task `decide`; and the limit: `limit` of the setting, else the attribute `egress-auto-limit` of the model that answers, else 0.97.
   - evals/egress/README.md says "The server does not use the head yet." Replace that item: the server uses the head when the catalog has it, the two tasks, the engine setting, and the attribute (the limit of the head is 0.95). Add the measured speed: on a laptop GPU with 8 GB (RTX 4060), one decision takes about 0.1 seconds and the model holds 4.5 GB; on a CPU with 6 cores it takes 2.5 seconds, which is too slow for a decision.

Verify: cd primitives/inference && GOWORK=off go test -count=1 ./... ; cd magi && GOWORK=off go test -count=1 ./compose/ ; then ./tools/check.sh.
