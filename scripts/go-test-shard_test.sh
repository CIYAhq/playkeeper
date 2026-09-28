#!/usr/bin/env bash
# Tests that scripts/go-test-shard.sh splits a package's tests between
# shards so each runs in exactly one, that a failing test fails its shard
# with its output shown, and that --check passes only when every shard
# listed the same tests and together they ran all of them.
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

func TestA(t *testing.T) {}
func TestB(t *testing.T) {}
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
