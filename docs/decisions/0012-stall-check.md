# 0012 A task that makes no progress is found and stopped

Status: accepted

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
