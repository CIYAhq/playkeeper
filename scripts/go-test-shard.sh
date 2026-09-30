#!/usr/bin/env bash
# Runs a Go package's tests in shards, and checks that the shards together
# ran every test. The agent's tests mostly wait on timers, so CI runs all
# their shards side by side on one runner.
#   scripts/go-test-shard.sh --jobs N PACKAGE [OUT] [TIMES]
#                                                 all N shards at once, each test in the shard
#                                                 with the least time so far, the slowest first,
#                                                 by TIMES (lines "Test seconds"), then --check
#   scripts/go-test-shard.sh PACKAGE K/N [OUT]   runs the Kth of every N tests
#                                                 go test -list names, in its order
#   scripts/go-test-shard.sh --check OUT N        every shard's list is the same and
#                                                 together they ran all of it
# A shard writes the package's tests to OUT/all-K.txt and the ones that ran
# to OUT/ran-K.txt; OUT defaults to a new temporary folder. A test that
# crashes its shard's test binary (a panic, a timeout) fails the shard, and
# the tests it kept from starting run in a binary of their own (resume), so
# every test still passes or fails.
set -euo pipefail

# report JSON: prints the output of the tests that failed and the package's own lines.
report() {
  local json=$1 failed
  failed=$(jq -r 'select(.Action == "fail" and .Test != null) | .Test' "$json" | sort -u)
  if [ -n "$failed" ]; then
    jq -j --arg failed "$failed" '($failed | split("\n")) as $f | select(.Action == "output" and .Test != null and (.Test as $t | $f | index($t))) | .Output' "$json"
  fi
  jq -j 'select((.Action == "output" and .Test == null) or .Action == "build-output") | .Output' "$json"
}

# listed: the tests on stdin but the ones test/quarantine.txt quarantines in
# $pkg, which the Quarantined tests workflow runs on their own.
listed() {
  local q
  q=$("$(dirname "$0")/quarantine.sh" names "$pkg")
  if [ -z "$q" ]; then
    cat
    return
  fi
  echo "quarantined, so left out: $(paste -sd' ' - <<<"$q")" >&2
  grep -vxF -e "$q" || true
}

# ran JSON: the top-level tests that ran.
ran() {
  jq -r 'select(.Action == "run" and .Test != null and (.Test | contains("/") | not)) | .Test' "$1" | sort -u
}

# resume LIST JSON COMMAND...: runs the tests in LIST with COMMAND, given the
# pattern that picks them as its last argument, into JSON. A test binary
# stops at a test that panics or runs out of time, so the tests it didn't
# start run again with COMMAND, until each has started or a run started
# none. Fails when any run failed.
resume() {
  local list=$1 json=$2 todo started status=0 round=0
  shift 2
  todo=$(cat "$list")
  : >"$json"
  while [ -n "$todo" ]; do
    round=$((round + 1))
    "$@" "^($(paste -sd'|' <<<"$todo"))\$" >"$json.$round" 2>&1 || status=1
    cat "$json.$round" >>"$json"
    started=$(ran "$json.$round")
    if [ -z "$started" ]; then break; fi
    todo=$(grep -vxF -e "$started" <<<"$todo" || true)
  done
  return "$status"
}

if [ "${1:-}" = --check ]; then
  dir=$2 n=$3
  for k in $(seq "$n"); do
    for f in all ran; do
      if [ ! -f "$dir/$f-$k.txt" ]; then
        echo "shard $k of $n left no $f-$k.txt: it didn't finish" >&2
        exit 1
      fi
    done
    if ! cmp -s "$dir/all-1.txt" "$dir/all-$k.txt"; then
      echo "shard $k of $n listed other tests than shard 1:" >&2
      diff "$dir/all-1.txt" "$dir/all-$k.txt" >&2 || true
      exit 1
    fi
  done
  missed=$(comm -23 <(sort -u "$dir/all-1.txt") <(sort -u "$dir"/ran-*.txt))
  if [ -n "$missed" ]; then
    echo "no shard ran these tests:" >&2
    echo "$missed" >&2
    if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::error title=Tests no shard ran::$(paste -sd' ' <<<"$missed")"; fi
    exit 1
  fi
  echo "the $n shards ran all $(wc -l <"$dir/all-1.txt") tests"
  exit 0
fi

if [ "${1:-}" = --jobs ]; then
  jobs=${2:?usage: go-test-shard.sh --jobs N PACKAGE [OUT] [TIMES]} pkg=${3:?usage: go-test-shard.sh --jobs N PACKAGE [OUT] [TIMES]}
  out=${4:-$(mktemp -d)} times=${5:-/dev/null}
  if ! [[ $jobs =~ ^[0-9]+$ ]] || [ "$jobs" -lt 1 ]; then
    echo "--jobs takes a number of shards, not '$jobs'" >&2
    exit 2
  fi
  mkdir -p "$out"
  out=$(cd "$out" && pwd)
  dir=$(go list -f '{{.Dir}}' "$pkg")
  path=$(go list -f '{{.ImportPath}}' "$pkg")
  go test -c -o "$out/pkg.test" "$pkg"
  (cd "$dir" && "$out/pkg.test" -test.list '.*') | grep -E '^(Test|Example|Fuzz)' | listed >"$out/all.txt"
  # A test TIMES doesn't know counts as 5 s.
  awk 'FILENAME == ARGV[1] { t[$1] = $2; next } { print $1, ($1 in t ? t[$1] : 5) }' "$times" "$out/all.txt" | sort -k2,2nr -k1,1 |
    awk -v n="$jobs" -v out="$out" '{ k = 1; for (i = 2; i <= n; i++) if (load[i] < load[k]) k = i; load[k] += $2; print $1 > (out "/mine-" k ".txt") }'
  started=$(date +%s)
  for k in $(seq "$jobs"); do
    cp "$out/all.txt" "$out/all-$k.txt"
    touch "$out/mine-$k.txt"
    (
      status=0
      if [ -s "$out/mine-$k.txt" ]; then
        cd "$dir"
        export PLAYKEEPER_TEST_SHARD=$k PLAYKEEPER_TEST_SHARDS=$jobs
        resume "$out/mine-$k.txt" "$out/test-$k.json" go tool test2json -t -p "$path" "$out/pkg.test" -test.v=test2json -test.paniconexit0 \
          -test.count=1 -test.timeout=30m -test.run || status=$?
      else
        : >"$out/test-$k.json"
      fi
      echo "$status" >"$out/status-$k"
    ) &
  done
  wait
  failed=0
  for k in $(seq "$jobs"); do
    ran "$out/test-$k.json" >"$out/ran-$k.txt"
    status=$(cat "$out/status-$k")
    echo "shard $k of $jobs: $(wc -l <"$out/ran-$k.txt") of the $(wc -l <"$out/all.txt") tests, exit $status"
    if [ "$status" != 0 ]; then
      failed=1
      report "$out/test-$k.json"
    fi
  done
  echo "all $jobs shards took $(($(date +%s) - started)) s"
  "$0" --check "$out" "$jobs"
  exit "$failed"
fi

pkg=${1:?usage: go-test-shard.sh PACKAGE K/N [OUT] | --jobs N PACKAGE [OUT] [TIMES] | --check OUT N}
shard=${2%/*} of=${2#*/}
out=${3:-$(mktemp -d)}
if ! [[ $shard =~ ^[0-9]+$ && $of =~ ^[0-9]+$ ]] || [ "$shard" -lt 1 ] || [ "$shard" -gt "$of" ]; then
  echo "shard is '$2', not K/N with 1 <= K <= N" >&2
  exit 2
fi
mkdir -p "$out"
go test -list '.*' "$pkg" | grep -E '^(Test|Example|Fuzz)' | listed >"$out/all-$shard.txt"
awk -v k="$shard" -v n="$of" 'NR % n == k % n' "$out/all-$shard.txt" >"$out/mine-$shard.txt"
echo "shard $shard of $of: $(wc -l <"$out/mine-$shard.txt") of the $(wc -l <"$out/all-$shard.txt") tests in $pkg"
json="$out/test-$shard.json"
status=0
resume "$out/mine-$shard.txt" "$json" go test -count=1 -timeout 30m -json "$pkg" -run || status=$?
ran "$json" >"$out/ran-$shard.txt"
report "$json"
exit "$status"
