# 0012 A task that makes no progress is found and stopped

Status: done

## Decision

A check above the coding harness looks at each task that runs. A task
writes a status line at a regular interval. The check finds a task that
wrote no line, or that repeats the same actions, and stops it or tells
the persona that started it.

The check interval and the loop limit are settings of the task, so that
a task that wastes model time is stopped sooner where capacity is small.

## Reason

A task in a loop holds a runner and the local model for hours. Nothing
above the harness sees it.

## Done when

A test runs a task that repeats one action, and the task stops within
the configured time with a reason that the persona can read.

## Progress

Done (`magi/compose/stall.go`). Each minute (`MAGI_STALL_EVERY`) Magi
looks at each task whose apprentice works. It stops a task when:

- the apprentice wrote no status line for `MAGI_STALL_AFTER` (default
  `30m`); the run variable `stall_ms` of a task replaces the time; or
- the last `MAGI_STALL_REPEAT` status lines (default 6) have the same
  actions; the run variable `stall_repeat` replaces the number.

The default time is longer than one operation of the apprentice (a check
command can run for 20 minutes). The apprentice writes a "working:" line
when the check or the review starts, and when an operation starts after 3
minutes with no line. Before that, the check stopped three tasks of a
slow model (41 tokens/s) while their review ran.

The time of a quiet task starts when the check first sees the step with
a worker, because a step can wait in the queue of a runner. The run is
cancelled with the reason. The person gets a notice, and the persona
that started the task gets the event (record 0009).

Tests: `TestStallCheckStopsATaskThatRepeats`,
`TestStallCheckStopsAQuietTask`.

Open: no request sets `stall_ms` or `stall_repeat` of one task yet. The
settings of the installation apply to all tasks.
