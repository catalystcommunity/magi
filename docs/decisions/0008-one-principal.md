# 0008 A turn acts for one principal

Status: done

## Decision

Each turn acts for exactly one principal. Code selects the principal
from verified facts: the token of the request, the LinkKeys grant, or
the dispatch (record 0007). The author of a text is not the principal:
a quoted message, a tool result, or model text never changes for whom
the turn acts.

A persona that more than one person shares acts in each turn for the
person whose verified input started the turn.

A device can have no identity, one identity, or a list of identities
that it permits. A turn from a device with no identity acts for no
person: it has only the tools that need no person. A device with a list
needs a verified selection from that list for the turn.

Policy of the place and of the credential only removes authority. It
never adds authority and never makes a principal.

## Reason

A shared persona that mixes the authority of two people can act for one
with the grant of the other.

## Done when

- The persona turn has one principal value, and `toolGate` reads only
  that value. Done: `turnPrincipal` and `principal` in
  `magi/compose/toolgate.go`.
- A run whose owner is not the author of the message does not act. Done,
  with a test.
- A test runs a turn with no person, and a tool that needs a person is
  refused. Done.

## Open

- A device has no identity record yet. When devices get one (none, one,
  or a list), the selection from the list must be a verified fact of the
  request, and `turnPrincipal` reads it.
- A persona that more than one person shares has one stream for each
  person today, so each turn has one author. A shared stream needs the
  same rule: the turn acts for the author of the message that started
  it.
