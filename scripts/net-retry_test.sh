#!/usr/bin/env bash
# Tests scripts/net-retry.sh with a stand-in command that fails, printing what
# it's told, a given number of times before it works, and a stand-in sleep.
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

mkdir -p "$t/bin"
# flaky FAILURES MESSAGE [STATUS]: its first FAILURES runs print MESSAGE to
# stderr and exit with STATUS (1 by default); after that it prints "done".
cat >"$t/flaky" <<'EOF'
#!/usr/bin/env bash
n=$(($(cat "$T/runs" 2>/dev/null || echo 0) + 1))
echo "$n" >"$T/runs"
if [ "$n" -le "$1" ]; then
  echo "$2" >&2
  exit "${3:-1}"
fi
echo done
EOF
cat >"$t/bin/sleep" <<'EOF'
#!/bin/sh
echo "$1" >>"$T/slept"
EOF
chmod +x "$t/flaky" "$t/bin/sleep"
status=0 out=''
run() { # [VAR=value...] -- FAILURES MESSAGE [STATUS]
  local vars=()
  while [ "$1" != -- ]; do
    vars+=("$1")
    shift
  done
  shift
  rm -f "$t/runs" "$t/slept"
  status=0
  out=$(env PATH="$t/bin:$PATH" T="$t" NET_RETRY_WAIT=0 "${vars[@]}" "$root/scripts/net-retry.sh" "$t/flaky" "$@" 2>&1) || status=$?
}
runs() { cat "$t/runs"; }

dropped='go: golang.org/x/text@v0.42.0: read "https://proxy.golang.org/golang.org/x/text/@v/v0.42.0.zip": stream error: stream ID 117; INTERNAL_ERROR; received from peer'
run -- 2 "$dropped"
[ "$status" = 0 ] && [ "$(runs)" = 3 ] && [[ $out == *done* ]] || fail "a download the proxy dropped should be tried until it works ($status after $(runs) runs): $out"
ok "a download the module proxy dropped, as in the 0.4.11 Release check, is tried again until it works"

while read -r msg; do
  run -- 1 "$msg"
  [ "$status" = 0 ] && [ "$(runs)" = 2 ] || fail "'$msg' should be tried again ($status after $(runs) runs)"
done <<'EOF'
go: example.com/m@v1.2.3: Get "https://proxy.golang.org/example.com/m/@v/v1.2.3.zip": net/http: TLS handshake timeout
go: example.com/m@v1.2.3: Get "https://proxy.golang.org/example.com/m/@v/v1.2.3.info": dial tcp: lookup proxy.golang.org on 127.0.0.53:53: server misbehaving
go: example.com/m@v1.2.3: Get "https://proxy.golang.org/example.com/m/@v/v1.2.3.zip": dial tcp 142.250.1.1:443: i/o timeout
go: example.com/m@v1.2.3: read "https://proxy.golang.org/example.com/m/@v/v1.2.3.zip": read tcp 10.1.0.4:40022->142.250.1.1:443: read: connection reset by peer
go: example.com/m@v1.2.3: read "https://proxy.golang.org/example.com/m/@v/v1.2.3.zip": unexpected EOF
go: example.com/m@v1.2.3: Get "https://proxy.golang.org/example.com/m/@v/v1.2.3.mod": http2: server sent GOAWAY and closed the connection; LastStreamID=1999, ErrCode=NO_ERROR, debug=""
go: example.com/m@v1.2.3: reading https://proxy.golang.org/example.com/m/@v/v1.2.3.zip: 502 Bad Gateway
go: example.com/m@v1.2.3: reading https://proxy.golang.org/example.com/m/@v/v1.2.3.info: 503 Service Unavailable
EOF
ok "a TLS handshake or connection that timed out, a failed name server, a connection reset or closed, a download cut short and a proxy's 5xx are tried again"

while read -r msg; do
  run -- 1 "$msg" 3
  [ "$status" = 3 ] && [ "$(runs)" = 1 ] || fail "'$msg' should fail at once with the command's status ($status after $(runs) runs)"
done <<'EOF'
verifying golang.org/x/text@v0.42.0: checksum mismatch
go: example.com/m@v9.9.9: reading https://proxy.golang.org/example.com/m/@v/v9.9.9.info: 404 Not Found
go: example.com/m@v1.0.0: invalid version: unknown revision v1.0.0
go: updates to go.mod needed; to update it: go mod tidy
EOF
ok "a checksum mismatch, a missing version and any other failure fail at once, with the command's status"

run -- 9 "$dropped" 2
[ "$status" = 2 ] && [ "$(runs)" = 4 ] && [[ $out == *INTERNAL_ERROR* ]] || fail "a network error that doesn't clear up should fail after four tries with its status ($status after $(runs) runs): $out"
ok "a network error that doesn't clear up fails after four tries, with the command's status and output"

run NET_RETRY_WAIT=5 -- 9 "$dropped"
[ "$(paste -sd' ' "$t/slept")" = "5 10 20" ] || fail "the waits between tries should be 5, 10 and 20 seconds, not: $(cat "$t/slept")"
ok "the tries are 5, 10 and 20 seconds apart"

run -- 0 ''
[ "$status" = 0 ] && [ "$(runs)" = 1 ] && [ "$out" = "done" ] || fail "a command that works should run once, its output passed through ($status after $(runs) runs): $out"
ok "a command that works runs once, and its output is passed through"

echo "net-retry.sh: $checks checks passed"
