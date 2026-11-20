# Instructions for coding agents

Magi is a personal assistant system: primitives (Go services with CSIL
contracts), a composition layer (`magi/`), a Python coding harness
(`primitives/apprentice`), Rust services (`primitives/release`,
`primitives/egress`), and a web app. Read `docs/conventions.md` before
you change a primitive. `docs/plan.md` has the goal and the parts.

## Commands

| Purpose | Command |
| --- | --- |
| Test one Go module (fast) | `cd <module> && GOWORK=off go test -count=1 ./...` |
| Run database tests of one module | `tools/test-db.sh -- sh -c 'cd <module> && GOWORK=off go test -count=1 ./...'` |
| Vet one Go module | `cd <module> && GOWORK=off go vet ./...` |
| Format Go | `gofmt -w <files>` (`tools/check.sh` fails on unformatted files) |
| Test the Python harness | `cd primitives/apprentice && python3 -m unittest discover -s tests -q` |
| Regenerate code after a contract change | `tools/gen.sh` |
| All checks (slow: all modules, Python, Rust) | `tools/check.sh` |

A Go module is a directory with `go.mod`: `lib/rpc`, `lib/authn`, `lib/revconn`,
`primitives/<name>`, `magi`, `tools/<name>`. Test the modules that you
change first. Run `tools/check.sh` last.

Integration tests skip when their test services are not there (for
example `MAGI_TEST_DATABASE_URL`). A skipped test is not a failure. A test
that skips for a missing database is not a pass of that test. Run it with
`tools/test-db.sh`.

## Rules

- Do not edit generated files: `*.gen.go`, `primitives/*/api/`,
  `clients/python/magi_*/`, `clients/typescript/*/*.gen.ts`. Change the
  contract in `contracts/<name>.csil` and run `tools/gen.sh`.
- A primitive imports only its own packages, `lib/`, and third-party
  libraries. A `cmd/` package can also import the generated `api` client
  of a different primitive. Only `magi/` composes primitives.
  `tools/arch-check.py` enforces this rule and the next three.
- Do not put private values in the repository: host names, addresses,
  accounts, keys, tokens. Use `example.local` in examples and tests.
- Do not name anything "opal". There is no backwards compatibility: change
  the callers instead of keeping an old form.
- Do not add Node.js to a service. The web app is the only JavaScript.
- Each new behavior gets a test. Test an RPC operation through the real
  HTTP transport with a generated client at least once.
- Write documentation in ASD-STE100 Simplified Technical English: short
  sentences, active voice, one instruction in each sentence.
- Read `docs/decisions/README.md`: the decisions in force and who
  decides what (code, the model, a person).
- Read `magi/compose/baseknowledge/review-lessons.md`: lessons from earlier
  code reviews.
- Comments tell why, not what. Keep the style of the code around you.

## Where things are

| Area | Path |
| --- | --- |
| Persona turns, tools, note taker | `magi/compose/persona.go`, `personas.go`, `notetaker.go` |
| Code-change workflow | `magi/compose/codechange.go`, `magi/runner/steps.go` |
| Coding harness (this agent loop) | `primitives/apprentice/magi_apprentice/` |
| Model selection | `primitives/models`, `magi/compose/models.go`, `docs/models.md` |
| Knowledge graph | `primitives/knowledge`, `docs/knowledge.md` |
| Deploy manifests | `deploy/render.py`, `deploy/base/` |
| Evals | `evals/notetaker`, `evals/decide` |
