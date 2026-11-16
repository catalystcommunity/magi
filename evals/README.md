# Evals

Each directory here is a benchmark or a method that needs a model or a live installation (see the README in each). They do not run in `tools/check.sh`. The injection fixtures below are the exception.

## Injection fixtures
The injection fixtures are deterministic attack tests that run as Go tests. They need no model and no network. The services are fakes that record which sinks a call reaches. Each fixture names its sink and fails when the sink is reached. They guard the source labels, the confirmations, the memory trust marks and the pull-request sanitizing.
| Area | Test | Sinks |
|---|---|---|
| Persona tools | `TestInjectionPersonaTools` | `knowledge.Assert`, `knowledge.PutEntity` (`remember`), `knowledge.Change` (`change_knowledge`), `workflow.Start` (`start_code_change`) |
| Librarian | `TestInjectionLibrarian` | `conversation.SetVisibility` and `conversation.PutLink` at confidence 1 on an attack message |
| Memory poisoning | `TestInjectionMemoryPoisoning` | a poisoned record returned by `recall` with no untrusted mark |
| Repository text | `TestInjectionRepositoryTextFence`, `TestInjectionPullRequestBody` | the data fence of a turn; the public pull-request body and title |

The files are `magi/compose/injection_test.go` and `magi/runner/injection_test.go`.
`go test ./...` and `tools/check.sh` run the fixtures. `tools/check.sh` fails when a fixture reaches a sink.
To run only the fixtures, go to the `magi` module directory and run:
```
GOWORK=off go test -count=1 -run TestInjection -v ./compose ./runner
```
`TestInjectionHarness` checks that the fakes record the sinks. If it fails, the other fixtures cannot be trusted.
