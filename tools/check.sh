#!/usr/bin/env bash
# Repository checks. The code-change workflow runs this after an apprentice works.
# It needs Go and Python 3. Integration tests skip without their test services.
set -uo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
status=0
step() { echo "== $*"; }
fail() { echo "FAIL: $*"; status=1; }

# The architecture rules (docs/decisions/0001). Fast, so it runs first.
step "architecture rules"
python3 tools/arch-check.py || fail "architecture rules (tools/arch-check.py)"
python3 tools/arch_check_test.py -q || fail "tools/arch_check_test.py"

step "python: evals/turn"
python3 evals/turn/test_verdict.py -q || fail "evals/turn/test_verdict.py"

step "python: evals/coding"
for t in test_run test_dbcheck test_probe; do
  python3 evals/coding/$t.py -q >/dev/null 2>&1 || fail "evals/coding/$t.py"
done

step "gofmt"
unformatted=$(gofmt -l lib primitives magi tools 2>/dev/null | grep -v '\.gen\.go$' || true)
[[ -z $unformatted ]] || fail "gofmt needed: $unformatted"

# The database tests skip without a database, so a change to database code
# can pass its check with its own tests not run. Run them on a throwaway
# PostgreSQL when the runner tools are there. One database serves the whole
# check: the tests write into it directly, so they must use unique ids.
go_tests() {
  for mod in $(find lib primitives magi tools -name go.mod -not -path '*/node_modules/*' -exec dirname {} \; | sort); do
    step "go vet + test: $mod"
    (cd "$mod" && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...) || fail "$mod"
  done
}
if [[ -n ${MAGI_TEST_DATABASE_URL:-} ]]; then
  echo "database tests: run (MAGI_TEST_DATABASE_URL is set)"
  go_tests
elif command -v initdb >/dev/null && command -v pg_ctl >/dev/null; then
  # A sandbox can make PostgreSQL fail to start (for example an unreadable
  # /etc, so initdb cannot look up its user). Probe with a command that
  # only starts and stops the server. A failed probe must not fail the
  # check: run the tests without a database, as before.
  if probe=$(tools/test-db.sh -- true 2>&1); then
    echo "database tests: run (throwaway PostgreSQL 18)"
    # test-db.sh passes the status of its command on. A failed module must
    # not stop the other modules, so the inner loop records the failure and
    # this script reports it.
    tools/test-db.sh -- bash -c "$(declare -f step fail go_tests)
status=0
go_tests
exit \$status" || fail "go modules (database tests)"
  else
    echo "database tests: skipped (PostgreSQL did not start: $(head -1 <<<"$probe"))"
    go_tests
  fi
else
  echo "database tests: skipped (no PostgreSQL; set MAGI_TEST_DATABASE_URL or install the runner tools)"
  go_tests
fi

if [[ -d primitives/apprentice/tests ]]; then
  step "python: primitives/apprentice"
  (cd primitives/apprentice && python3 -m unittest discover -s tests -q) || fail "primitives/apprentice"
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

# The egress proxy keeps secrets in. Its tests build the WASM plugins
# (target wasm32-wasip2) and send real traffic through the proxy with curl.
if [[ -f primitives/egress/Cargo.toml ]]; then
  step "rust: primitives/egress"
  if command -v cargo >/dev/null; then
    (cd primitives/egress && cargo test -q 2>&1 | tail -20; exit "${PIPESTATUS[0]}") || fail "primitives/egress"
  else
    fail "primitives/egress (cargo is not installed)"
  fi
fi

if [[ $status -eq 0 ]]; then echo "ALL CHECKS PASSED"; else echo "CHECKS FAILED"; fi
exit $status
