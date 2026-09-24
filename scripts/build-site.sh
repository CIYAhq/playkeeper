#!/usr/bin/env bash
# Assembles the playkeeper.io site in OUT: the files in site/ plus /install,
# a copy of the one-line installer given as GET_SH (a release's get.sh).
# Usage: scripts/build-site.sh GET_SH OUT
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
get=$1
out=$2
fail() {
  echo "site build failed: $*" >&2
  exit 1
}

sh -n "$get" || fail "$get is not a valid sh script"
grep -Eq '^default_base=https://github\.com/[^/]+/[^/]+/releases/latest/download$' "$get" ||
  fail "$get does not look like Playkeeper's get.sh"
grep -qF 'curl -fsSL https://playkeeper.io/install | sudo sh' "$root/site/index.html" ||
  fail "site/index.html must show the install command"

rm -rf "$out"
mkdir -p "$out"
cp -R "$root/site/." "$out/"
install -m 0644 "$get" "$out/install"
echo "Built the site in $out: $(cd "$out" && find . -type f | sort | tr '\n' ' ')"
