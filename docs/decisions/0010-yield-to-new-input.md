# 0010 A turn yields to new input

Status: accepted

## Decision

When a person sends a new message to a conversation while a persona turn
runs there, the turn stops at the next safe point: between two model
rounds, and never in the middle of an effect. The next turn gets the old
and the new input together.

## Reason

A person who corrects a request waits for an answer to the old request,
and then gets a second answer. On local models each round is slow, so
the wait is long.

## Done when

A test sends a second message during a turn, and sees one reply that
answers both.
