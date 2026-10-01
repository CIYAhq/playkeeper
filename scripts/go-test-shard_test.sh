#!/usr/bin/env bash
# Tests that scripts/go-test-shard.sh splits a package's tests between
# shards so each runs in exactly one, that a failing test fails its shard
# with its output shown, that --check passes only when every shard listed
# the same tests and together they ran all of them, and that runners'
# parts of the shards side by side run each test once and fail when one of
# their shards didn't run all of its tests.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
checks=0
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
ok() {
  checks=$((checks + 1))
  echo "ok: $*"
}

mkdir -p "$t/mod/pkg"
printf 'module example.test/shards\n\ngo 1.24\n' >"$t/mod/go.mod"
cat >"$t/mod/pkg/pkg_test.go" <<'EOF'
package pkg

import (
	"os"
	"testing"
)

// The shard SKIP_SHARD names exits before it runs a test, passing.
func TestMain(m *testing.M) {
	if s := os.Getenv("SKIP_SHARD"); s != "" && s == os.Getenv("PLAYKEEPER_TEST_SHARD") {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestA(t *testing.T) {}
func TestB(t *testing.T) {
	if os.Getenv("PANIC_B") != "" {
		panic("B panicked on purpose")
	}
}
func TestC(t *testing.T) { t.Run("inner", func(t *testing.T) {}) }
func TestD(t *testing.T) {}
func TestE(t *testing.T) {
	if os.Getenv("BREAK_E") != "" {
		t.Fatal("E broke on purpose")
	}
}
EOF
cd "$t/mod"
shard() { "$root/scripts/go-test-shard.sh" ./pkg "$1" "$t/out" >"$t/log-${1%/*}" 2>&1; }
check() { "$root/scripts/go-test-shard.sh" --check "$t/out" 3 >"$t/check" 2>&1; }

for k in 1 2 3; do shard "$k/3" || fail "shard $k of 3 failed: $(cat "$t/log-$k")"; done
[ "$(cat "$t"/out/ran-*.txt | sort | tr '\n' ' ')" = "TestA TestB TestC TestD TestE " ] || fail "the shards didn't run each test once: $(cat "$t"/out/ran-*.txt | tr '\n' ' ')"
[ "$(wc -l <"$t/out/ran-1.txt")" = 2 ] && [ "$(wc -l <"$t/out/ran-2.txt")" = 2 ] && [ "$(wc -l <"$t/out/ran-3.txt")" = 1 ] || fail "the shards ran $(wc -l "$t"/out/ran-*.txt | head -3 | awk '{print $1}' | tr '\n' ' ')tests, not 2, 2 and 1"
ok "three shards run each of five tests once"
check || fail "--check refused shards that ran every test: $(cat "$t/check")"
grep -q 'ran all 5 tests' "$t/check" || fail "--check said: $(cat "$t/check")"
ok "--check passes when the shards ran every test"

cp "$t/out/ran-2.txt" "$t/ran-2.txt"
grep -v TestB "$t/ran-2.txt" >"$t/out/ran-2.txt" || true
! check || fail "--check passed with TestB run by no shard"
grep -q TestB "$t/check" || fail "--check didn't name TestB: $(cat "$t/check")"
ok "--check fails and names a test no shard ran"
rm "$t/out/ran-2.txt"
! check || fail "--check passed without shard 2's report"
grep -q "shard 2 of 3 left no ran-2.txt" "$t/check" || fail "--check said: $(cat "$t/check")"
ok "--check fails when a shard left no report"
cp "$t/ran-2.txt" "$t/out/ran-2.txt"
echo TestF >>"$t/out/all-3.txt"
! check || fail "--check passed with shards that listed other tests"
grep -q "shard 3 of 3 listed other tests" "$t/check" || fail "--check said: $(cat "$t/check")"
ok "--check fails when shards listed other tests"

rm -rf "$t/out"
if BREAK_E=1 shard 2/3; then fail "shard 2 of 3 passed with TestE failing"; fi
grep -q 'E broke on purpose' "$t/log-2" || fail "the failing test's output wasn't shown: $(cat "$t/log-2")"
grep -qx TestE "$t/out/ran-2.txt" || fail "a failing test isn't among the tests its shard ran"
ok "a failing test fails its shard, shows its output and still counts as run"

rm -rf "$t/out"
if PANIC_B=1 shard 1/1; then fail "the shard passed with TestB panicking"; fi
grep -q 'B panicked on purpose' "$t/log-1" || fail "the panic wasn't shown: $(cat "$t/log-1")"
# The testing package reports a test's panic and panics again, so the binary stops there.
first=$(jq -r 'select(.Action == "run" and .Test != null) | .Test' "$t/out/test-1.json.1" | tr '\n' ' ')
[ "$first" = "TestA TestB " ] || fail "the first test binary ran $first, not stopping at TestB's panic"
[ "$(tr '\n' ' ' <"$t/out/ran-1.txt")" = "TestA TestB TestC TestD TestE " ] || fail "after TestB panicked the shard ran $(tr '\n' ' ' <"$t/out/ran-1.txt")"
"$root/scripts/go-test-shard.sh" --check "$t/out" 1 >"$t/check" 2>&1 || fail "--check after the panic: $(cat "$t/check")"
ok "a test that panics fails its shard, and the tests after it still run"

jobs() { "$root/scripts/go-test-shard.sh" --jobs 2 ./pkg "$t/jobs" >"$t/log-jobs" 2>&1; }
jobs || fail "two shards side by side failed: $(cat "$t/log-jobs")"
grep -q 'the 2 shards ran all 5 tests' "$t/log-jobs" || fail "two shards side by side said: $(cat "$t/log-jobs")"
ok "shards side by side run every test"
rm -rf "$t/jobs"
if PANIC_B=1 jobs; then fail "shards side by side passed with TestB panicking"; fi
grep -q 'B panicked on purpose' "$t/log-jobs" || fail "the panic wasn't shown: $(cat "$t/log-jobs")"
grep -q 'the 2 shards ran all 5 tests' "$t/log-jobs" || fail "after TestB panicked, shards side by side said: $(cat "$t/log-jobs")"
ok "shards side by side run every test when one panics, and fail"

# With every test at 5 s, shards 1 and 3 of 3 get TestA, TestD and TestC,
# and shard 2 TestB and TestE.
part() { "$root/scripts/go-test-shard.sh" --jobs 3 ./pkg "$t/part-${1%/*}" /dev/null "$1" >"$t/log-part-${1%/*}" 2>&1; }
part 1/2 || fail "part 1 of 2 failed: $(cat "$t/log-part-1")"
part 2/2 || fail "part 2 of 2 failed: $(cat "$t/log-part-2")"
[ "$(cat "$t"/part-*/ran-*.txt | sort | tr '\n' ' ')" = "TestA TestB TestC TestD TestE " ] || fail "two parts didn't run each test once: $(cat "$t"/part-*/ran-*.txt | tr '\n' ' ')"
[ "$(cd "$t/part-1" && echo ran-*.txt)" = "ran-1.txt ran-3.txt" ] && [ "$(cd "$t/part-2" && echo ran-*.txt)" = "ran-2.txt" ] || fail "part 1 ran $(cd "$t/part-1" && echo ran-*.txt) and part 2 $(cd "$t/part-2" && echo ran-*.txt), not shards 1 and 3, and 2"
grep -q 'part 1 of 2: its shards ran all 3 of their tests' "$t/log-part-1" || fail "part 1 of 2 said: $(cat "$t/log-part-1")"
ok "two runners' parts of three shards run each test once, each only its own shards"
rm -rf "$t"/part-*
if BREAK_E=1 part 2/2; then fail "part 2 of 2 passed with TestE failing"; fi
grep -q 'E broke on purpose' "$t/log-part-2" || fail "the failing test's output wasn't shown: $(cat "$t/log-part-2")"
BREAK_E=1 part 1/2 || fail "part 1 of 2, which hasn't TestE, failed: $(cat "$t/log-part-1")"
ok "a failing test fails the part whose shard ran it, and only that part"
rm -rf "$t"/part-*
if SKIP_SHARD=3 part 1/2; then fail "part 1 of 2 passed with shard 3 running none of its tests"; fi
grep -q 'part 1 of 2: no shard ran these tests' "$t/log-part-1" && grep -qx TestC "$t/log-part-1" || fail "part 1 of 2 said: $(cat "$t/log-part-1")"
ok "a part fails, and names the tests, when one of its shards passes without running them"
for p in 3/2 0/2 1/4 x/2; do
  if "$root/scripts/go-test-shard.sh" --jobs 3 ./pkg "$t/part" /dev/null "$p" >"$t/log" 2>&1; then fail "part $p of 3 shards ran"; fi
  grep -q "not R/M" "$t/log" || fail "part $p said: $(cat "$t/log")"
done
ok "a part out of range is refused"

rm -rf "$t/out"
printf '# PACKAGE TEST WHY\n./pkg TestE   breaks now and then\n./other TestA   in another package\n' >"$t/quarantine.txt"
for k in 1 2 3; do QUARANTINE_LIST="$t/quarantine.txt" BREAK_E=1 shard "$k/3" || fail "shard $k of 3 failed with TestE quarantined: $(cat "$t/log-$k")"; done
[ "$(cat "$t"/out/ran-*.txt | sort | tr '\n' ' ')" = "TestA TestB TestC TestD " ] || fail "with TestE quarantined the shards ran $(cat "$t"/out/ran-*.txt | tr '\n' ' ')"
grep -q "quarantined, so left out: TestE" "$t/log-1" || fail "shard 1 didn't say it left TestE out: $(cat "$t/log-1")"
check || fail "--check refused shards that left out only the quarantined test: $(cat "$t/check")"
ok "a quarantined test is left out of every shard, failing or not, and --check passes without it"

if "$root/scripts/go-test-shard.sh" ./pkg 4/3 "$t/out" >"$t/log" 2>&1; then fail "shard 4 of 3 ran"; fi
grep -q "not K/N" "$t/log" || fail "a shard out of range said: $(cat "$t/log")"
ok "a shard out of range is refused"

echo "go-test-shard.sh: $checks checks passed"
