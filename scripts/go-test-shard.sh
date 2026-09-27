#!/usr/bin/env bash
# Runs one shard of a Go package's tests, so CI can split a slow package
# between runners, and checks that the shards together ran every test.
#   scripts/go-test-shard.sh PACKAGE K/N [OUT]   runs the Kth of every N tests
#                                                 go test -list names, in its order
#   scripts/go-test-shard.sh --check OUT N        every shard's list is the same and
#                                                 together they ran all of it
# A shard writes the package's tests to OUT/all-K.txt and the ones that ran
# to OUT/ran-K.txt; OUT defaults to a new temporary folder.
set -euo pipefail

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

pkg=${1:?usage: go-test-shard.sh PACKAGE K/N [OUT] | --check OUT N}
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
jq -r 'select(.Action == "run" and .Test != null and (.Test | contains("/") | not)) | .Test' "$json" | sort -u >"$out/ran-$shard.txt"
failed=$(jq -r 'select(.Action == "fail" and .Test != null) | .Test' "$json" | sort -u)
if [ -n "$failed" ]; then
  jq -j --arg failed "$failed" '($failed | split("\n")) as $f | select(.Action == "output" and .Test != null and (.Test as $t | $f | index($t))) | .Output' "$json"
fi
jq -j 'select((.Action == "output" and .Test == null) or .Action == "build-output") | .Output' "$json"
exit "$status"
