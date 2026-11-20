# 0007 Work that continues later keeps its authority in a dispatch

Status: done

## Decision

When a turn starts work that continues after the turn (a task, a
reminder, a plan step, a wait for an event), Magi stores a dispatch with
the work. The dispatch records:

- the principal for whom the work acts, and the LinkKeys grant when a
  persona acts as a person;
- the persona and the conversation;
- the limits that applied when the work started.

When the work continues, code restores the dispatch. It does not derive
the authority again from the event that woke it. The authority that
applies is the dispatch narrowed by the policy now: a grant that was
revoked, or a limit that is stricter now, applies to work that started
before. Work with no dispatch, or with a policy that cannot be read,
does not run. There is no fallback identity.

## Reason

Work that continues later must not gain authority from the thing that
woke it (a webhook, a model text, a different person's message), and it
must lose authority that was taken away while it waited.

## Done when

- Each kind of continued work stores and restores a dispatch.
- A test revokes a grant while a task runs, and the next step of the
  task is refused.
- A test continues work with no dispatch, and the work does not run.

## Progress

Done (`magi/compose/dispatch.go`):

- A persona turn, a task, a to-do, and a reminder each start with a
  dispatch in the run variable `dispatch`. The workflow service refuses a
  step output that changes a variable that the run started with, so a
  runner cannot change it.
- Each step that a worker of Magi or a runner claims goes through one
  check (`permitted`). A run with no dispatch, with the dispatch of a
  different principal or persona, or with a revoked grant is cancelled.
  The owner gets one notice.
- The tools of a turn are the tools of its dispatch that the persona
  still has. The secrets of a task (the variables of its environment, and
  what the egress proxy puts in) are the grants of its dispatch that the
  policy still gives. A grant that the policy added later does not come
  to work that started before.
- Work that a turn starts gets a dispatch from the dispatch of the turn:
  the same principal and persona, and no more than the turn has.
- Tests: `TestRevokedGrantStopsTheNextStep`,
  `TestWorkWithNoDispatchDoesNotRun`, `TestRestoreNarrowsTheDispatch`,
  `TestSecretRulesFollowTheDispatch`, `TestTurnUsesAndPassesItsDispatch`.

Not in the dispatch: the LinkKeys grant of a calendar connection. Magi
gets that token for the principal at each use, so a revoked connection
already stops at the next use.

A run that started before this change has no dispatch. It stops at its
next step, and its owner gets the notice.
