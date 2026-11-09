# Plans and to-dos

This document tells how a persona keeps the to-dos, reminders, and plans of
a person. The code is `magi/compose/plans.go`. The workflow primitive
(`primitives/workflow`) keeps the state.

## Records

| Record | What it is |
| --- | --- |
| To-do | A thing that a person must do. It has a title, an optional due time, an optional reminder time, and an optional to-do that must be done first. |
| Reminder | A notice at a time. It ends when Magi sends it. |
| Plan | A name that groups to-dos. The order of a plan is the "after" of each to-do. |

Each to-do and each reminder is one run of the workflow `todo`. The run
keeps the variables `title`, `who`, `due_ms`, `remind_ms`, `after`, `plan`,
`kind`, `persona`, and `stream`. The owner of the run is the person.

## The workflow `todo`

```
gate ──ready──▶ do ──done/skip──▶ finish
  │              │
  │              └─overdue (due time)──▶ overdue ──▶ late ──(1 day)──▶ overdue
  │                                        └─ after 3 notices ──▶ waiting
  ├─early──▶ early ──remind (reminder time)──▶ remind ──▶ do
  │                                              └─ a reminder ends ──▶ finish
  └─blocked──▶ blocked ──ready (the to-do before it ended)──▶ gate
```

- `gate` (task): when the to-do before it is open, the to-do waits in
  `blocked`. When it has a reminder that Magi did not send, it waits in
  `early`. Else it goes to `do`.
- `early`, `do`, `late`, `waiting`, `blocked` (human): the person marks the
  to-do done or skipped. The deciders are `${who}`; the owner can also
  decide. `early` waits until the reminder time, and `do` waits until the
  due time (`wait_until`).
- `remind` and `overdue` (task): send a notice to the devices of the person.
  A late to-do gets at most 3 notices, one each day.
- `finish` (task): starts the to-dos that wait for this one, and records an
  episode of kind `task` in the knowledge store, with the result.
- `blocked` checks again after 10 minutes. Thus a to-do starts also when
  the finish step of the to-do before it did not reach it.

A reminder time that is in the past when the run starts gives the notice
at once. Magi does not lose it.

## Persona tools

| Tool | Does |
| --- | --- |
| `add_todo` | Adds a to-do: title, due, remind, plan, after. `after: "previous"` is the to-do that the persona added before in the same turn. |
| `set_reminder` | Adds a reminder: text, at. |
| `list_todos` | Lists the open to-dos (optional: one plan, and the to-dos that ended in the last 7 days). |
| `finish_todo` | Marks a to-do done or skipped, or cancels it. |
| `change_todo` | Changes the title, the due time, or the reminder time. |

- Times are local times of the household (`MAGI_TIME_ZONE`), as
  `YYYY-MM-DD HH:MM`. A date alone is 09:00 for a reminder and 17:00 for a
  due time. Magi refuses a time in the past and tells the model the time
  now, so that the model can correct it.
- A run keeps its variables. Thus a change starts a new run and cancels the
  old one. The to-dos that waited for the old run wait for the new one.
- The same utterance and the same title give the same run. A model that
  calls `add_todo` two times makes one to-do.
- The note taker does not record a to-do as a fact.

## Personas

All three personas have these tools. `persona:assistant` (the executive
assistant) has them as its main work. `persona:engineering` uses them for
the plans of software work; code changes stay `start_code_change`.

## Tests

- `TestTodoLifecycle` (`magi/compose/plans_test.go`) runs the workflow
  service with a memory queue and the to-do workers: a reminder, a late
  notice, done, an episode, a plan in order, a change, a reminder that
  ends, and errors that the model can repair. It needs
  `MAGI_TEST_DATABASE_URL`.
- `TestParseWhen` tests the times.

## Not done yet

- A calendar: to-dos are not events on a calendar. An adapter for the
  calendars of the person (CalDAV, Google) is the next step.
- A to-do for another person: `who` can be another subject, but no tool
  sets it yet.
- Steps that Magi does itself (research, a code change) inside a plan.
