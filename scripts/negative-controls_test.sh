#!/usr/bin/env bash
# Tests scripts/negative-controls.sh's modes that run no tests, on a copy of
# its helpers with controls of its own in a scratch tree: --check names each
# control whose guard, file, package, test file or test script is gone, with
# its line, and --night DATE --list picks the same share for the same date
# while each cycle of nights names every control once.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
real="$root/scripts/negative-controls.sh"
checks=0
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
ok() {
  checks=$((checks + 1))
  echo "ok: $*"
}

tree=$(mktemp -d)
trap 'rm -rf "$tree"' EXIT
mkdir -p "$tree/scripts" "$tree/internal/x" "$tree/web/src" "$tree/packaging"
echo 'func f() { guarded() }' >"$tree/internal/x/x.go"
echo 'export const a = keep()' >"$tree/web/src/a.tsx"
echo 'test("name", () => {})' >"$tree/web/src/a.test.tsx"
echo 'export default { budget: 1 }' >"$tree/web/vite.config.ts"
printf '#!/bin/sh\nexit 1\n' >"$tree/packaging/a.sh"
echo 'exit 0' >"$tree/packaging/a_test.sh"

# controls writes the script as scripts/negative-controls.sh in the tree: the
# real one's helpers, then the controls given, then its summary.
controls() { # CONTROLS
  {
    sed -n '1,/^control "CSRF token check"/p' "$real" | sed '$d'
    printf '%s\n' "$1"
    echo finish
  } >"$tree/scripts/negative-controls.sh"
}
# lineof is the line the control named starts on.
lineof() { # NAME
  grep -n "^[a-z]*control \"$1\"" "$tree/scripts/negative-controls.sh" | cut -d: -f1
}

good=$(cat <<'EOF'
control "a guard in its file" internal/x/x.go 'guarded()' 'nothing()' ./internal/x '^TestX$'
webcontrol "a web guard" web/src/a.tsx 'keep()' 'drop()' web/src/a.test.tsx 'name'
webcontrol "a web guard, its test file without web/" web/src/a.tsx 'keep()' 'drop()' src/a.test.tsx
buildcontrol "a build guard" web/vite.config.ts 'budget: 1' 'budget: 9'
shcontrol "a script guard" packaging/a.sh 'exit 1' 'exit 0' packaging/a_test.sh
EOF
)
stale=$(cat <<'EOF'
control "a guard that moved" internal/x/x.go 'moved()' 'gone()' ./internal/x '^TestX$'
control "a file that's gone" internal/y/y.go 'guarded()' 'nothing()' ./internal/y '^TestY$'
control "a package that's gone" internal/x/x.go 'guarded()' 'nothing()' ./internal/z '^TestZ$'
webcontrol "a test file that's gone" web/src/a.tsx 'keep()' 'drop()' src/gone.test.tsx 'name'
shcontrol "a test script that's gone" packaging/a.sh 'exit 1' 'exit 0' packaging/gone_test.sh
EOF
)

controls "$good"
out=$(bash "$tree/scripts/negative-controls.sh" --check) || fail "--check failed with every guard in place: $out"
[ "$out" = "all 5 negative controls aim at their guards" ] || fail "--check with every guard in place says: $out"
ok "--check passes when every control aims at its guard"

controls "$good
$stale"
if out=$(bash "$tree/scripts/negative-controls.sh" --check); then fail "--check passed with five controls aiming at nothing: $out"; fi
at="scripts/negative-controls.sh"
for want in \
  "STALE    a guard that moved ($at:$(lineof "a guard that moved")): its guard is no longer in internal/x/x.go" \
  "STALE    a file that's gone ($at:$(lineof "a file that's gone")): internal/y/y.go is gone" \
  "STALE    a package that's gone ($at:$(lineof "a package that's gone")): package ./internal/z is gone" \
  "STALE    a test file that's gone ($at:$(lineof "a test file that's gone")): web/src/gone.test.tsx is gone" \
  "STALE    a test script that's gone ($at:$(lineof "a test script that's gone")): packaging/gone_test.sh is gone"; do
  grep -qxF "$want" <<<"$out" || fail "--check doesn't say \"$want\" in: $out"
done
[ "$(grep -c '^STALE ' <<<"$out")" = 5 ] || fail "--check names other controls too: $out"
grep -qF "5 of the 10 negative controls aim at what is no longer there." <<<"$out" || fail "--check doesn't count them: $out"
ok "--check names each control aiming at nothing, with its line, and only those"

controls "$good
control \"one guard that moved\" internal/x/x.go 'moved()' 'gone()' ./internal/x '^TestX\$'"
out=$(bash "$tree/scripts/negative-controls.sh" --check) && fail "--check passed with a guard that moved"
grep -qF "1 of the 6 negative controls aims at what is no longer there. Aim it at its guard" <<<"$out" || fail "one control aiming at nothing reads: $out"
ok "--check speaks of one control in the singular"

controls "$good
$stale"
nights=$(sed -n 's/^nights=\([0-9]*\)$/\1/p' "$real")
[ -n "$nights" ] || fail "no nights= in $real"
first=$(($(date -u +%s) / 86400 / nights * nights))
named=""
for day in $(seq "$first" $((first + nights - 1))); do
  night=$(date -u -d "@$((day * 86400))" +%F)
  share=$(bash "$tree/scripts/negative-controls.sh" --night "$night" --list)
  [ "$share" = "$(bash "$tree/scripts/negative-controls.sh" --night "$night" --list)" ] || fail "the night of $night picks another share the second time"
  named+=${share:+$share$'\n'}
done
want=$(grep -oE '^[a-z]*control "[^"]*"' "$tree/scripts/negative-controls.sh" | sed -E 's/^[a-z]*control "(.*)"$/\1/' | sort)
[ "$(sort <<<"${named%$'\n'}")" = "$want" ] || fail "a cycle of $nights nights didn't name every control once:
$(sort <<<"$named" | uniq -c)"
ok "the same night picks the same share, and a cycle of $nights nights names every control once"

for args in "--night 2026-13-01" "--night yesterday" "--night" "--night 2026-10-05 --run" "--every"; do
  status=0
  # shellcheck disable=SC2086
  bash "$tree/scripts/negative-controls.sh" $args >/dev/null 2>&1 || status=$?
  [ "$status" = 2 ] || fail "scripts/negative-controls.sh $args exited $status, not 2"
done
ok "a bad date or an unknown flag is refused"

echo "negative-controls.sh: $checks checks passed"
