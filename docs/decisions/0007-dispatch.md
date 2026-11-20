# 0007 Work that continues later keeps its authority in a dispatch

Status: accepted

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
