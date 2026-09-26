#!/usr/bin/env bash
# Tests that scripts/demo-marker.sh finds the live demo's marker in any chunk
# the entry script reaches, however deep, through imports or the preload
# list, fetching each chunk once and stopping at the marker, and that it fails
# with its message when no chunk has the marker (the entry naming no chunk at
# all included), when a chunk doesn't download and after 200 chunks.
# The sites are folders read through file:// URLs.
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

# A curl that notes every address it's asked for, to count the fetches.
real_curl=$(command -v curl)
mkdir -p "$t/bin"
cat >"$t/bin/curl" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\${!#}" >>"$t/fetched"
exec "$real_curl" "\$@"
EOF
chmod +x "$t/bin/curl"

chunk() { # SITE FILE CONTENT
  mkdir -p "$t/$1/demo/assets"
  printf '%s\n' "$3" >"$t/$1/demo/assets/$2"
}
find_marker() { # SITE ENTRY: $out, $err and $status of demo-marker.sh
  : >"$t/fetched"
  out=$(PATH="$t/bin:$PATH" "$root/scripts/demo-marker.sh" "file://$t/$1" "/demo/assets/$2" 2>"$t/err") && status=0 || status=$?
  err=$(cat "$t/err")
}
fetches() { wc -l <"$t/fetched" | tr -d ' '; }
not_demo="is not the demo build (vite build --mode demo)"

# The entry imports the app statically, and the app imports the engine that
# holds the marker dynamically, the way rolldown writes it: two chunks deep.
# The engine loads one more chunk, which the walk never needs.
chunk deep index-E1.js 'import{a}from"./app-A1.js";a()'
# shellcheck disable=SC2016 # rolldown's quotes, not a command
chunk deep app-A1.js 'export function a(){return import(`./engine-B2.js`)}'
chunk deep engine-B2.js 'import"./after-C3.js";const k="playkeeper-live-demo";export{k}'
chunk deep after-C3.js 'export const z=1'
find_marker deep index-E1.js
[ "$status" = 0 ] && [ "$out" = /demo/assets/engine-B2.js ] || fail "the marker two chunks deep: status $status, found '$out', said '$err'"
[ "$(fetches)" = 3 ] || fail "the marker two chunks deep: fetched $(fetches) chunks, not the 3 up to the marker"
ok "finds the marker two chunks deep, and stops there"

# The preload list names chunks as 'assets/x.js', and the app and a vendor
# chunk import each other and the entry: each chunk is fetched once.
chunk preload index-E1.js "const d=['assets/app-A1.js','assets/vendor-V1.js'];import(\"./app-A1.js\")"
chunk preload app-A1.js 'import"./index-E1.js";import"./vendor-V1.js";const m=["assets/lazy-C3.js"]'
chunk preload vendor-V1.js 'import"./index-E1.js";import"./app-A1.js"'
chunk preload lazy-C3.js 'sessionStorage.getItem("playkeeper-live-demo")'
find_marker preload index-E1.js
[ "$status" = 0 ] && [ "$out" = /demo/assets/lazy-C3.js ] || fail "the marker in the preload list's chunk: status $status, found '$out', said '$err'"
twice=$(sort "$t/fetched" | uniq -d)
[ -z "$twice" ] || fail "fetched more than once: $twice"
ok "follows the preload list, and fetches each chunk once"

chunk whole index-E1.js 'const k="playkeeper-live-demo"'
find_marker whole index-E1.js
[ "$status" = 0 ] && [ "$out" = /demo/assets/index-E1.js ] || fail "the marker in the entry: status $status, found '$out', said '$err'"
ok "finds the marker in the entry itself"

# The dashboard's own build: no chunk has the marker. Paths that are only part
# of a string, like an address ending in assets/x.js, are not chunks it loads.
chunk none index-E1.js 'import"./app-A1.js"'
chunk none app-A1.js 'import("./page-B2.js")'
chunk none page-B2.js 'const u="https://example.com/assets/cdn-Z9.js",v="../up-Z8.js",w="x./in-Z7.js"'
find_marker none index-E1.js
[ "$status" = 1 ] && [ -z "$out" ] && [ "$err" = "/demo/assets/index-E1.js $not_demo: no chunk it loads has the demo's marker" ] ||
  fail "no chunk with the marker: status $status, found '$out', said '$err'"
[ "$(fetches)" = 3 ] || fail "no chunk with the marker: fetched $(fetches) chunks, not the 3 there are"
ok "fails, saying so, when no chunk has the marker"

chunk single index-E1.js 'document.title="Playkeeper"'
find_marker single index-E1.js
[ "$status" = 1 ] && [ "$err" = "/demo/assets/index-E1.js $not_demo: no chunk it loads has the demo's marker" ] ||
  fail "an entry that loads no chunk: status $status, said '$err'"
ok "fails, saying so, when the entry loads no chunk"

chunk missing index-E1.js 'import"./gone-A1.js"'
find_marker missing index-E1.js
[ "$status" = 1 ] && [ "$err" = "/demo/assets/gone-A1.js does not download" ] || fail "a missing chunk: status $status, said '$err'"
ok "names a chunk that doesn't download"

# A chain of 250 chunks with the marker at its end is followed for 200.
for i in $(seq 1 250); do chunk long "c-$i.js" "import\"./c-$((i + 1)).js\""; done
chunk long c-251.js 'playkeeper-live-demo'
find_marker long c-1.js
[ "$status" = 1 ] && [ "$err" = "/demo/assets/c-1.js $not_demo: none of the first 200 chunks it loads has the demo's marker" ] ||
  fail "a chain longer than 200 chunks: status $status, found '$out', said '$err'"
[ "$(fetches)" = 200 ] || fail "a chain longer than 200 chunks: fetched $(fetches)"
ok "stops after 200 chunks"

echo "demo-marker.sh: $checks checks passed"
