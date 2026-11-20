# 0004 A turn that starts again does not repeat its effects

Status: done

## Decision

A persona turn can stop before it completes: the process stops, or the
worker loses its claim. The turn then starts again. The second start
must not repeat an effect of the first.

- The reply has a key made from the utterance that it answers
  (`PostRequest.key`). A second post with the key writes nothing.
- A turn that posted its reply records that on the utterance. A start
  that finds the record does nothing more.
- Each tool call that changes something has a key made from the tool and
  its arguments. `toolGate` keeps the result on the utterance of the turn
  (annotation kind `effect`). The same call in a later start, or a second
  time in the same start, gets the first result and does not run.
- A task that a turn starts has the key of its effect, so that the start
  is safe when Magi stops between the start and the record.
- A turn that starts again tells the model which actions are complete.
- A turn starts at most two times. When the second start fails, the
  persona posts a short message that says that it could not complete the
  request.
- A worker that loses its claim stops its work on the step and reports
  nothing.

## Reason

A turn that starts again from the beginning can post a second reply or
start a second task. A person then sees double work, and a task costs
model time two times.

## Done when

A test stops a turn after each effect, starts it again, and counts each
effect exactly one time. Done: `magi/compose/turn_recovery_test.go`,
`worker_test.go`.

## Open

- When the process stops during the second start, the run fails and the
  person gets no message. The event work (record 0009) closes this: a
  failed run is an event.
- A tool that changes something in an external system (a calendar, a
  Longhouse task) is safe only through the record of the effect. When
  Magi stops between the change and the record, the next start can
  repeat it.
