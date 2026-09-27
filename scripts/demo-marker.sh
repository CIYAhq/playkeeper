#!/usr/bin/env bash
# Finds the live demo's marker in the code a /demo/ page loads: in its entry
# script, or in any chunk reachable from it. A chunk names each chunk it loads
# as a whole string, "./x.js" (a static or dynamic import) or "assets/x.js"
# (the preload list), in any quotes, so the walk follows those, breadth-first,
# fetching each chunk once, and stops at the first chunk with the marker, which
# it prints. It fails, saying so, when no chunk it reaches has the marker, or
# none of the first 200.
# Usage: scripts/demo-marker.sh BASE ENTRY
#   BASE   where the site answers, http://host:port (or file:///dir for tests)
#   ENTRY  the entry script's path, /demo/assets/index-….js
set -euo pipefail

base=$1 entry=$2
marker=playkeeper-live-demo
limit=200
dir=${entry%/*}
q="[\"'\`]"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

queue=("$entry")
seen=" $entry "
next=0
while [ "$next" -lt ${#queue[@]} ] && [ "$next" -lt "$limit" ]; do
  chunk=${queue[next]}
  next=$((next + 1))
  if ! curl -fsS -o "$work/chunk.js" "$base$chunk" 2>/dev/null; then
    echo "$chunk does not download" >&2
    exit 1
  fi
  if grep -qF "$marker" "$work/chunk.js"; then
    echo "$chunk"
    exit 0
  fi
  # A chunk that loads no other chunk finds nothing here, which is fine.
  names=$(grep -oE "$q(\./|assets/)[A-Za-z0-9_.-]+\.js$q" "$work/chunk.js" | sed -E "s#^$q(\./|assets/)##; s#$q\$##" | sort -u) || true
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    case $seen in *" $dir/$name "*) continue ;; esac
    seen+="$dir/$name "
    queue+=("$dir/$name")
  done <<<"$names"
done
if [ "$next" -lt ${#queue[@]} ]; then
  echo "$entry is not the demo build (vite build --mode demo): none of the first $limit chunks it loads has the demo's marker" >&2
else
  echo "$entry is not the demo build (vite build --mode demo): no chunk it loads has the demo's marker" >&2
fi
exit 1
