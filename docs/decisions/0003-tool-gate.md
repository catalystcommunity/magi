# 0003 Each tool call of a persona goes through `toolGate`

Status: done

## Decision

One function, `toolGate`, runs each tool call of a persona turn. No
other code calls a tool handler. `toolGate` does these steps in this
order:

1. Find the tool. Refuse a name that the persona does not have.
2. Parse the arguments. Refuse arguments that are not valid.
3. Check the authority: the principal of the turn, the grants, and the
   limits of the place.
4. Run the handler.
5. Limit the size of the result.
6. Put the result in the fence for untrusted text. This includes error
   text.
7. Record the call: tool, principal, result or refusal, duration.

A refusal has three parts (record 0005).

## Reason

Checks that each tool does for itself are different from tool to tool,
and a new tool can omit one. One function makes a check impossible to
omit. A test can then prove a property for all tools.

## Done when

- The persona turn calls tools only through `toolGate`.
- A test fails when a tool handler is called from a different place.
- A test gives each tool a result that is too large and an argument
  that is not valid, and sees the limit and the fence.
