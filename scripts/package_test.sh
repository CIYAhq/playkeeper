#!/usr/bin/env bash
# Tests that scripts/package.sh builds CURSEFORGE_API_KEY into the binary,
# and nothing without it, and never shows the key: not in the build's output,
# nor in the build info `go version -m` reads from the binary.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
export PATH="$root/.tools/go/bin:$PATH"
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

# Shaped like a CurseForge key, with the dollar signs, dots and slashes one has.
# shellcheck disable=SC2016
key='$2a$10$pk.dummy/CurseForge.key.0123456789abcdefghijklmnopqrstu'

build() { # OUT: scripts/package.sh --binary, its output in OUT.log
  "$root/scripts/package.sh" --binary "$1" >"$1.log" 2>&1
}

CURSEFORGE_API_KEY=$key build "$t/with" || { cat "$t/with.log" >&2; fail "the build with CURSEFORGE_API_KEY failed"; }
grep -qaF -- "$key" "$t/with" || fail "the binary built with CURSEFORGE_API_KEY doesn't carry the key"
ok "a build with CURSEFORGE_API_KEY carries it"
! grep -qF -- "$key" "$t/with.log" || fail "the build printed the key"
go version -m "$t/with" >"$t/info" || fail "go version -m can't read the binary"
! grep -qF -- "$key" "$t/info" || fail "the binary's build info shows the key"
ok "the key is neither printed nor in the build info"

(unset CURSEFORGE_API_KEY && build "$t/without") || { cat "$t/without.log" >&2; fail "the build without CURSEFORGE_API_KEY failed"; }
! grep -qaF -- "${key:0:20}" "$t/without" || fail "a build without CURSEFORGE_API_KEY carries a key"
ok "a build without CURSEFORGE_API_KEY carries none"

bad='two words-0123456789abcdefghij'
! CURSEFORGE_API_KEY=$bad build "$t/bad" || fail "a key with a space was built in"
grep -q "spaces, quotes or backslashes" "$t/bad.log" && ! grep -qF -- "$bad" "$t/bad.log" || fail "a key that can't be one is refused, without printing it"
ok "a key that can't be one is refused, without printing it"

echo "package.sh: $checks checks passed"
