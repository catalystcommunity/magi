# Apprentice: batch independent tool calls; cost of each task in the result

Lower the model cost of apprentice tasks: fewer model calls, and the cost of each task in its result. This is a small task; keep it small.

1. SYSTEM_PROMPT in primitives/apprentice/magi_apprentice/apprentice.py: add one rule after the rule "Use the tools to inspect...": "Each model call costs money. When tool calls do not depend on each other, make them in one reply: for example, read three files, or read a file and search, in one reply. Read the files that the task names before you search."
2. Cost in the result. The provider gives a "cost" field (US dollars, float) in the usage of each answer. In Apprentice.run, add the cost of each answer (also of the last call at a limit) to usage["cost"] as a float, next to prompt_tokens and completion_tokens. Add the cached prompt tokens too, as usage["cached_tokens"], from usage.prompt_tokens_details.cached_tokens. A missing field counts as 0.
3. primitives/apprentice/magi_apprentice/__main__.py: the JSON result line also has "usage" (the dict above).
4. magi/runner/steps.go (work step): read usage from that JSON line. Add the outputs apprentice.cost_usd (format %.3f) and apprentice.prompt_tokens, and add " (cost $X.XXX)" to the note "apprentice ... after N steps". When there is no cost, leave the outputs and the text out.
5. Tests: a FakeModel test that two answers with cost 0.01 and 0.02 give usage["cost"] 0.03 and the cached tokens; a test in magi/runner for the note and the outputs with and without a cost (use the existing tests of the work step as the pattern).
6. README: one line in primitives/apprentice/README.md where the result is described.

Verify: cd primitives/apprentice && python3 -m unittest discover -s tests -q; cd magi && GOWORK=off go test -count=1 ./runner/; then ./tools/check.sh.
