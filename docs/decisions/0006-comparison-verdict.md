# 0006 A prompt or harness change needs a comparison verdict

Status: accepted

## Decision

A change to a prompt, a tool description, the persona turn, or the coding
harness is kept only with a verdict: pass or fail. One command makes the
verdict. It runs the eval cases on the version before the change (the
baseline) and on the version with the change (the candidate), with the
same local model, and compares them.

- Core cases have a floor. A candidate that fails a core case that the
  baseline passes gets "fail", whatever the total is.
- Assertions on effects come first: which tools ran, what was stored,
  what was posted. A model judge is used only for what an assertion
  cannot test, and a judge cannot change a failed assertion to a pass.
- The verdict states the number of runs. A difference that is smaller
  than the difference between two runs of the baseline is "no change".
- The eval runs use only local models, on an inference server that
  serves no other traffic.

## Reason

Magi has eval suites, but none gives pass or fail for a change. A change
that Magi makes to its own prompts then has no gate, and a person must
judge it. The goal is that a person does not need to.

## Done when

- The command exists and gives a verdict for the persona turn. Done:
  `evals/turn/verdict.py` (`evals/turn/README.md`).
- The command gives a verdict for the coding harness. Not done: the
  coding eval (`evals/coding`) runs through an installation and a
  runner, and a person reads the grades.
- The code-change workflow runs the verdict for a change that touches a
  prompt or the persona turn, and shows the verdict with the diff. Not
  done.
