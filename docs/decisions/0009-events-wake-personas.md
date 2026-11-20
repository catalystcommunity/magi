# 0009 Events start persona turns

Status: accepted

## Decision

A persona turn starts from an event, not only from a message of a
person. Each event that a persona must act on is a durable record with a
key that prevents a second copy: a message, a task that completed or
failed, a reminder that is due, a plan step that is ready, a decision
that a person made.

- All turns start from these records. There is no second path.
- Turns for different conversations run at the same time when there is
  model capacity.
- The turn acts with the dispatch of the work that made the event
  (record 0007), or as the person who wrote the message.

A loop is the main risk: a persona acts on an event, the action makes an
event, and so on. Code prevents it:

- An event that a persona turn caused carries the chain length. A chain
  stops at a limit.
- Code counts the events of a persona and a conversation in a time
  window. Above the limit, events wait and one notice goes to the
  person.
- The limit is a rate in a window and also a minimum number of events
  that always pass. Then work that waited a long time (from Friday to
  Monday) is not blocked by an old window.

If code rules are not sufficient to decide if a persona must act on an
event, a decision eval measures that decision before a model makes it.

## Reason

A task that completes only leaves a notice. The persona that started it
does not look at the result, so a person must read it and ask for the
next step. A persona that can continue its own work needs the event.

## Done when

- A task that completes starts a turn of the persona that started it,
  with its dispatch.
- A test makes an event loop, and the loop stops at the limit with one
  notice.
