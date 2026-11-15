# Evals

Each directory here is a benchmark or a method that needs a model or a live installation (see the README in each). They do not run in `tools/check.sh`.

## Injection baseline fixtures

Deterministic attack fixtures run as Go tests. They need no model and no network: the services are fakes that record which sinks a call reaches. Each fixture names its sink and fails when the sink is reached. The risk-remediation spec (requirement 5) uses them as the failing baseline for the controls of tasks 1.2 to 1.4.

| Area | Test | Sinks |
|---|---|---|
| Persona tools | `TestInjectionPersonaTools` | `knowledge.Assert`, `knowledge.PutEntity` (`remember`), `knowledge.Change` (`change_knowledge`), `workflow.Start` (`start_code_change`) |
| Librarian | `TestInjectionLibrarian` | `conversation.SetVisibility` and `conversation.PutLink` at confidence 1 on an attack message |
| Memory poisoning | `TestInjectionMemoryPoisoning` | a poisoned record returned by `recall` with no untrusted mark |
| Repository text | `TestInjectionRepositoryTextFence`, `TestInjectionPullRequestBody` | the data fence of a turn; the public pull-request body and title |

The files are `magi/compose/injection_baseline_test.go` and `magi/runner/injection_baseline_test.go`.

The fixtures are expected to fail on the current code. They are skipped unless `MAGI_INJECTION_BASELINE=1`, so `tools/check.sh` (and the apprentice finish gate) stay green. Run them from the `magi` module directory:

```
MAGI_INJECTION_BASELINE=1 GOWORK=off go test -count=1 -run 'TestInjection' -v ./compose/ ./runner/
```

`TestInjectionHarness` is not gated. It checks that the fakes record the sinks. `TestInjectionRepositoryTextFence` passes today; it is a control that the fence keeps holding.

When tasks 1.2 to 1.4 are done, task 1.5 removes the gate so that the fixtures run in `tools/check.sh`. There is no hosted CI in this repository; `tools/check.sh` is the only check.
