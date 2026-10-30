#!/usr/bin/env bash
# Repository checks. The code-change workflow runs this after an agent works.
# It needs Go and Python 3. Integration tests skip without their test services.
set -uo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
status=0
step() { echo "== $*"; }
fail() { echo "FAIL: $*"; status=1; }

step "gofmt"
unformatted=$(gofmt -l lib primitives magi tools 2>/dev/null | grep -v '\.gen\.go$' || true)
[[ -z $unformatted ]] || fail "gofmt needed: $unformatted"

for mod in $(find lib primitives magi tools -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; | sort); do
  step "go vet + test: $mod"
  (cd "$mod" && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...) || fail "$mod"
done

if [[ -d primitives/agent/tests ]]; then
  step "python: primitives/agent"
  (cd primitives/agent && python3 -m unittest discover -s tests -q) || fail "primitives/agent"
fi

# The release controller keeps Magi up when it releases itself; its tests
# (fake cluster: rollout failure, health failure, restore, restart) must pass.
if [[ -f primitives/release/Cargo.toml ]]; then
  step "rust: primitives/release"
  if command -v cargo >/dev/null; then
    (cd primitives/release && cargo test -q 2>&1 | tail -20; exit "${PIPESTATUS[0]}") || fail "primitives/release"
  else
    fail "primitives/release (cargo is not installed)"
  fi
fi

if [[ $status -eq 0 ]]; then echo "ALL CHECKS PASSED"; else echo "CHECKS FAILED"; fi
exit $status
