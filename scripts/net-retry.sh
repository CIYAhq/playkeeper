#!/usr/bin/env bash
# Runs a command that downloads Go modules or tools, and runs it again when it
# fails on a network error that clears up by itself, such as the module proxy
# dropping a download ("stream error: ... INTERNAL_ERROR"): up to four tries,
# 5, 10 and 20 seconds apart. Any other failure, such as a checksum mismatch or
# a version that doesn't exist, fails at once with the command's status. The
# go command tries nothing again by itself; curl, npm, Playwright's browser
# download and apt already do. The command's output and errors both go to
# stdout.
#   scripts/net-retry.sh COMMAND [ARG...]
# NET_RETRY_WAIT is the first wait in seconds (the tests use 0).
set -uo pipefail

# transient matches how the go command reports such an error: a stream or
# connection the other end reset or closed, a download cut short, a timeout,
# a name server that failed, or a proxy's 5xx.
transient='stream error|GOAWAY|connection reset|connection lost|broken pipe|unexpected EOF|i/o timeout|TLS handshake timeout|connection timed out|server misbehaving|Temporary failure in name resolution|: 50[0234] (Internal Server Error|Bad Gateway|Service Unavailable|Gateway Timeout)'
tries=4
wait=${NET_RETRY_WAIT:-5}
log=$(mktemp)
trap 'rm -f "$log"' EXIT
for try in $(seq "$tries"); do
  "$@" 2>&1 | tee "$log"
  status=${PIPESTATUS[0]}
  if [ "$status" = 0 ]; then
    exit 0
  fi
  if [ "$try" = "$tries" ] || ! grep -Eq "$transient" "$log"; then
    exit "$status"
  fi
  echo "net-retry: '$*' failed on a network error; trying again in ${wait}s (try $try of $tries)"
  sleep "$wait"
  wait=$((wait * 2))
done
