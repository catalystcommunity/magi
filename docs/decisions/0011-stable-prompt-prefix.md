# 0011 The prompt prefix is stable in a session

Status: accepted

## Decision

The first part of the prompt of a persona does not change during a
session: the system prompt, the tool descriptions, and the key of the
fence for untrusted text. All that changes in each turn (the time, the
recalled knowledge, the place) is at the end.

The fence key is random at the start of each session and stays the same
in that session. A new session gets a new key.

## Reason

The local inference server keeps the computed state of a prompt prefix.
A prefix that changes in each turn makes it compute the whole prompt
again. The gain is small when the server is busy with many prompts and
larger when there is spare capacity. A key for each session keeps the
fence hard to guess and lets the prefix stay the same.

## Done when

- A test builds the prompt of two turns of one session and finds the
  same prefix up to the part for the turn.
- The time to the first token of the second turn is measured before and
  after the change.
