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
# to OUT/ran-K.txt; OUT defaults to a new temporary folder.
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

# ran JSON: the top-level tests that ran.
ran() {
  jq -r 'select(.Action == "run" and .Test != null and (.Test | contains("/") | not)) | .Test' "$1" | sort -u
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
  dir=$(go list -f '{{.Dir}}' "$pkg")
  path=$(go list -f '{{.ImportPath}}' "$pkg")
  go test -c -o "$out/pkg.test" "$pkg"
  (cd "$dir" && "$out/pkg.test" -test.list '.*') | grep -E '^(Test|Example|Fuzz)' >"$out/all.txt"
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
        (cd "$dir" && go tool test2json -t -p "$path" "$out/pkg.test" -test.v=test2json -test.paniconexit0 -test.count=1 -test.timeout=30m \
          -test.run "^($(paste -sd'|' "$out/mine-$k.txt"))\$") >"$out/test-$k.json" 2>&1 || status=$?
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
go test -list '.*' "$pkg" | grep -E '^(Test|Example|Fuzz)' >"$out/all-$shard.txt"
awk -v k="$shard" -v n="$of" 'NR % n == k % n' "$out/all-$shard.txt" >"$out/mine-$shard.txt"
echo "shard $shard of $of: $(wc -l <"$out/mine-$shard.txt") of the $(wc -l <"$out/all-$shard.txt") tests in $pkg"
json="$out/test-$shard.json"
status=0
go test -count=1 -timeout 30m -json -run "^($(paste -sd'|' "$out/mine-$shard.txt"))\$" "$pkg" >"$json" || status=$?
ran "$json" >"$out/ran-$shard.txt"
report "$json"
exit "$status"
