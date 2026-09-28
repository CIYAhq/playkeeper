#!/usr/bin/env bash
# Tests that test/quarantine.txt still names what it quarantines, and that
# scripts/quarantine.sh reads it as CI needs: each Go test it lists exists in
# its package, go test's skip flag leaves out exactly those tests, and a
# script's checks stay out of it.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
q="$root/scripts/quarantine.sh"
checks=0
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
ok() {
  checks=$((checks + 1))
  echo "ok: $*"
}

skip=$("$q" skip)
tests=()
for pkg in $("$q" packages); do
  for name in $("$q" names "$pkg"); do
    grep -qE "^func $name\(t \*testing\.T\)" "$root/${pkg#./}"/*_test.go || fail "$pkg has no test $name; take it off test/quarantine.txt"
    tests+=("$name")
    ok "$pkg has $name"
  done
done
if [ "${#tests[@]}" -gt 0 ]; then
  want="-skip=^($(IFS='|'; echo "${tests[*]}"))\$"
  [ "$skip" = "$want" ] || fail "the skip flag is '$skip', want '$want'"
  ok "the skip flag leaves out the ${#tests[@]} quarantined Go tests"
else
  [ -z "$skip" ] || fail "no Go test is quarantined, but the skip flag is '$skip'"
  ok "no Go test is quarantined, so there's no skip flag"
fi

while read -r script; do
  [ -f "$root/$script" ] || fail "test/quarantine.txt names $script, which doesn't exist"
  for name in $("$q" names "$script"); do
    grep -q "\"$name\"" "$root/$script" || fail "$script has no check named \"$name\""
    [[ $skip != *"$name"* ]] || fail "$script's check $name is in go test's skip flag"
    ok "$script has its check $name, which go test doesn't skip"
  done
done < <(awk '!/^[[:space:]]*(#|$)/ && $1 !~ /^\.\// { print $1 }' "$root/test/quarantine.txt" | sort -u)

if "$q" nothing >/dev/null 2>&1; then fail "an unknown command ran"; fi
ok "an unknown command is refused"

echo "quarantine.sh: $checks checks passed"
