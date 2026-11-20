# 0005 A refusal explains itself; the prompt does not list the rules

Status: accepted

## Decision

When code refuses an action of the model, the refusal text has three
parts:

1. What did not occur.
2. The rule, in one sentence.
3. What the model can do next, with tools that it has in this turn.

The system prompt does not list rules that code enforces. It tells the
persona who it is and how it speaks.

## Reason

A local model follows a short prompt better than a long one. A rule in
the prompt costs tokens in each turn and the model can ignore it. A
refusal costs tokens only when the rule applies, and code enforces it.

## Done when

- Each refusal of `toolGate` has the three parts. A test checks this.
- Each rule that code enforces is gone from the persona prompts, and the
  evals do not get worse (record 0006).
