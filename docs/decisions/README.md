# Decision records

A decision record states one decision about how Magi works, and why. Read
the records before you change the area that a record names. A change that
goes against a record must change the record in the same commit.

Each record has the same parts: the status, the decision, the reason, and
the condition that shows the work is complete ("Done when").

Status values: `accepted` (decided, work not complete), `done` (the code
obeys the record), `replaced` (a later record replaces it).

| Record | Decision | Status |
| --- | --- | --- |
| [0001](0001-architecture-rules-are-scripts.md) | Architecture rules are scripts that fail the check | done |
| [0002](0002-decision-records.md) | Decisions are records in this directory | done |
| [0003](0003-tool-gate.md) | Each tool call of a persona goes through `toolGate` | done |
| [0004](0004-turn-recovery.md) | A turn that starts again does not repeat its effects | done |
| [0005](0005-refusals-explain-themselves.md) | A refusal explains itself; the prompt does not list the rules | accepted |
| [0006](0006-comparison-verdict.md) | A prompt or harness change needs a comparison verdict | accepted |
| [0007](0007-dispatch.md) | Work that continues later keeps its authority in a dispatch | done |
| [0008](0008-one-principal.md) | A turn acts for one principal | done |
| [0009](0009-events-wake-personas.md) | Events start persona turns | done for tasks |
| [0010](0010-yield-to-new-input.md) | A turn yields to new input | accepted |
| [0011](0011-stable-prompt-prefix.md) | The prompt prefix is stable in a session | accepted |
| [0012](0012-stall-check.md) | A task that makes no progress is found and stopped | done |
| [0013](0013-not-done.md) | Things that Magi does not do | accepted |
| [0014](0014-automatic-destinations.md) | Magi permits known public destinations with no person | accepted |

## Who decides

Code decides everything that has a correct answer. The model decides
content. A person decides what cannot be undone.

| Question | Who decides | Where |
| --- | --- | --- |
| For whom does a turn act? | Code, from the verified token and the grant | `lib/authn`, the identity primitive |
| Can this tool run in this turn? | Code | `toolGate` (record 0003) |
| What does the reply say? Which tool is useful? | The model | the persona turn |
| Is text from a tool or a document an instruction? | Code: never. It is data in a fence | `docs/prompt-injection.md` |
| Does a secret go into the model context? | Code: never | the egress proxy, the secret references |
| When does a turn end? | Code: the round limit, the false-claim check, new input | the persona turn (record 0010) |
| Is a code change good enough to show? | Code: the checks and the review gate. The model writes the review | the code-change workflow |
| Is a code change merged? | A person | the decision step |
| Is a prompt or harness change kept? | Code: the comparison verdict. A person for a change that fails it | record 0006 |
| Which model serves a role? | Code, from measured results | `docs/models.md` |
| Does a persona start work with no message from a person? | Code: the event rules and the loop limit | record 0009 |
| Is a task stopped because it makes no progress? | Code | record 0012 |
| Can a task environment connect to a new host? | Code for known software sources and for private names; the review model for a host that it knows with high confidence; a person for the rest | record 0014 |
| Is a remembered fact changed or retracted? | The note taker proposes; code applies the change rules; a person reviews the queue | `docs/knowledge.md` |
