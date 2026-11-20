# 0009 Events start persona turns

Status: done for tasks

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

- The dispatch of an event turn has the chain length: the number of
  events between a message of a person and this turn. It counts the
  earlier events of the same work, and it goes to the work that the turn
  starts. A chain stops at a limit: the next event starts no turn, and
  the person gets one notice.
- Code counts the event turns of a persona in a conversation that
  started in a time window. The limit is a minimum number of events in
  the window: fewer events always pass. More events wait until the window
  has room. They are not dropped and they do not wait for a person, so
  work that waited a long time (from Friday to Monday) is not blocked by
  an old window, and the person gets one notice in a window.
- Both numbers and the window are settings (`MAGI_EVENT_CHAIN`,
  `MAGI_EVENT_RATE`, `MAGI_EVENT_WINDOW`).

An event turn cannot approve, reject, or merge a task. Only a message of
the person can.

The text of an event is written by code: the id and the title of the
task, and what occurred. It does not have the text of the report, which
can have text that a task made.

If code rules are not sufficient to decide if a persona must act on an
event, a decision eval measures that decision before a model makes it.

## Reason

A task that completes only leaves a notice. The persona that started it
does not look at the result, so a person must read it and ask for the
next step. A persona that can continue its own work needs the event.

## Done when

- A task that completes starts a turn of the persona that started it,
  with its dispatch. Done for each report of a task that is not `low`
  (`TestReportWakesThePersona`, `TestEventTurn`).
- A test makes an event loop, and the loop stops at the limit with one
  notice. Done (`TestEventLoopStopsAtTheChainLimit`,
  `TestEventsAboveTheRateWait`).

## Open

- A reminder that is due and a plan step that is ready send a notice
  only. They do not start a turn yet.
- The reply of an event turn is a message in the conversation. It is not
  a notice on the devices of the person; the report of the task is.
- The report of a task is in the conversation as a notice of the
  persona, so an event turn reads it as history. It can have text that a
  task made. The turn cannot decide a task, but it can start one.
- Eval: `evals/turn/core-v2.json`, cases `v01` to `v03`. On the local
  model the persona reads the task and tells the person in 11 of 12 runs.
  In each run of `v03` the model tried to approve, and code refused.
