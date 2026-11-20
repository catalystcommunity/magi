# 0004 A turn that starts again does not repeat its effects

Status: accepted

## Decision

A persona turn can stop before it completes: the process stops, or the
worker loses its claim. The turn then starts again. The second start
must not repeat an effect of the first.

- Each effect of a turn has a key made from the turn and the position
  of the effect: the reply, each tool call that changes something, each
  task that starts. A second request with the same key returns the first
  result and does nothing more.
- A turn that starts again gets a note that says so and lists the
  effects that are complete.
- A turn starts at most two times. After the second failure the persona
  posts a short message that says that it could not complete the
  request.
- A worker that loses its claim stops its work on the turn.
- A handler error gives the person a message. It does not give silence.

## Reason

A turn that starts again from the beginning can post a second reply or
start a second task. A person then sees double work, and a task costs
model time two times.

## Done when

A test stops a turn after each effect, starts it again, and counts each
effect exactly one time.
