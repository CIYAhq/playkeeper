#!/usr/bin/env bash
# Tests the VM rehearsal workflow's steps (scripts/e2e/vm-rehearsal.sh)
# without KVM: python3 is a stub on PATH.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
script="$root/scripts/e2e/vm-rehearsal.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() {
  echo "FAIL: $*"
  exit 1
}

mkdir -p "$tmp/bin"
stub() { # BODY: what the stub python3 does
  printf '#!/bin/sh\n%s\n' "$1" >"$tmp/bin/python3"
  chmod +x "$tmp/bin/python3"
}
check() { # runs kvm-check with the stub, at most 15 s; sets st and out
  st=0
  out=$(PATH="$tmp/bin:$PATH" KVM_CHECK_TIMEOUT=1 timeout 15 bash "$script" kvm-check 2>&1) || st=$?
}

stub 'exec sleep 300'
started=$(date +%s)
check
took=$(($(date +%s) - started))
[ "$st" = 1 ] && [ "$took" -lt 12 ] || fail "a KVM that hangs creating a virtual CPU must fail within the check's time limit (status $st after $took s): $out"
case $out in *"within 1 s"*) ;; *) fail "the check must say KVM hung: $out" ;; esac
echo "ok: a hanging KVM check gives up in time"

stub 'echo "KVM can create a virtual CPU"'
check
[ "$st" = 0 ] || fail "a working KVM must pass (status $st): $out"
stub 'exit 1'
check
[ "$st" = 1 ] && case $out in *"exit status 1"*) true ;; *) false ;; esac || fail "a KVM that refuses must fail and say so (status $st): $out"
echo "ok: a working KVM passes and a refusing one fails"

mkdir -p "$tmp/out/vm-1"
echo "no secrets here" >"$tmp/out/vm-1/log.txt"
bash "$script" redact "$tmp/out" || fail "evidence without any setup code must be kept as it is, without an error"
[ "$(cat "$tmp/out/vm-1/log.txt")" = "no secrets here" ] || fail "evidence without setup codes changed"
echo "ok: evidence without setup codes"

printf 'setup code: abcd-1234\nopen https://203.0.113.9:8443/#code=wxyz-5678 now\n' >"$tmp/out/vm-1/setup log.txt"
bash "$script" redact "$tmp/out" || fail "redacting setup codes failed"
got=$(cat "$tmp/out/vm-1/setup log.txt")
[ "$got" = "$(printf 'setup code: <redacted>\nopen https://203.0.113.9:8443/#code=<redacted> now')" ] || fail "setup codes must be redacted: $got"
echo "ok: setup codes are redacted"
