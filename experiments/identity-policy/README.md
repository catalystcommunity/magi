# Identity and policy experiment

This is a local W02 experiment. It verifies synthetic LinkKeys application
evidence and compares CEL, Cedar, and Cerbos against the same receiver rules.
It is not a production protocol, approval service, or LinkKeys deployment.

## Requirements

- Linux amd64 and Go 1.27.0 for the recorded run.
- The sibling LinkKeys checkout at commit
  `a1ddbbf67fe542ee04476d1178f8618e2147d52d`.
- Cedar CLI 4.13.0 and Cerbos 0.55.0 for the full comparison.
- Permission to bind loopback ports for temporary Cerbos processes.

The module pins CEL 0.30.0. The LinkKeys Go module uses a local replacement.
Check the SDK without changing another session's checkout:

```sh
git -C ../../../linkkeys diff --exit-code a1ddbbf67fe542ee04476d1178f8618e2147d52d -- sdks/regular-rp/go
```

Download the tools from their official releases. Verify each archive against
its published checksum before extraction or execution.
[Cedar release](https://github.com/cedar-policy/cedar/releases/tag/cedar-policy-cli-v4.13.0),
[Cerbos release](https://github.com/cerbos/cerbos/releases/tag/v0.55.0).

| Linux amd64 archive | SHA-256 |
| --- | --- |
| cedar-policy-cli-x86_64-unknown-linux-gnu.tar.xz | `23d358ef38edad8ade629a09f6a7359ecbd1761c9ff82e9fd0a5c5bce36b8c79` |
| cerbos_0.55.0_Linux_x86_64.tar.gz | `01cb5ae0b888393219846c1bc43d4ed1a4701a51fb8ae38523d3a378e685efca` |

The tests check tool versions. They do not download or install tools.

## Run

From this directory, set `OPAL_W02_CEDAR` and `OPAL_W02_CERBOS` to the two
verified executable paths. Then run:

```sh
GOCACHE=/tmp/opal-recovery-go-cache go test -race -json -count=3 -timeout=3m .
GOCACHE=/tmp/opal-recovery-go-cache go vet ./...
GOCACHE=/tmp/opal-recovery-go-cache go test -run '^$' -bench . -benchmem -benchtime=200ms -count=3
```

Without those two variables, external-engine tests skip. That is a valid
SDK/CEL check, but not proof of the full comparison. The recorded final run
had no skips.

The [report](../../docs/identity-policy.md) identifies the final result file.
Count leaf outcomes without counting parent tests twice:

```sh
node ../corndogs-recovery/summarize.mjs results/2026-09-30-complete-fixtures.jsonl
```

## Test boundaries

- `evidence.go` verifies application keys through the LinkKeys SDK. It uses
  generated CSIL codecs for LinkKeys attestations. Its signed statement
  format is strict JSON for this experiment only, not a proposed wire standard.
- The fixture has independent domain keys and reverses the domain roles.
  Trust anchors and resolver results are supplied locally. No live DNS,
  TLS, login, database, or remote approval exchange is tested.
- `recordApproval` tests exact-byte and human-identity binding after assumed
  authentication. It is not an authentication function or durable transaction.
- Approved records retain verified allowance data. New requests still need
  current keys. The retained-approval case advances the clock by two days,
  renews requester evidence, and tests issuer-key expiry and withdrawal.
- Decimal and timestamp comparisons use exact conversion before policy
  evaluation. This does not prove native decimal support in all engines.
- Missing-deny-fact tests deliberately expose unsafe engine defaults. The
  selected wrappers deny on errors. Cedar CLI parsing is test code only.

Temporary Cerbos processes have loopback HTTP, gRPC, and debug listeners.
Their environment excludes inherited Hub credentials. They use a temporary
working directory, disabled telemetry, and disabled policy file watching.
Cleanup stops only the process started by that test. It removes synthetic
temporary files. The downloaded executables remain in `/tmp` for reuse.

## Next Actions

Use the [service experiment](../identity-service/README.md) for real login,
durable approval, and cross-domain verification. Keep these policy fixtures
separate. They retain the exact decimal and nanosecond comparison tests.
