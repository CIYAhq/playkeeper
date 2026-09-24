#!/usr/bin/env bash
# Copies the text evidence of a rehearsal run into docs/evidence/<name>/,
# redacting one-time setup codes and dropping session state and binaries.
# Usage: scripts/e2e/collect-evidence.sh test/e2e/out/vm-runN docs/evidence/vm-rehearsal
set -euo pipefail
src=$1
dest=$2
mkdir -p "$dest"
find "$src" -maxdepth 2 -type f \( -name '*.txt' -o -name '*.diff' -o -name '*results.json' -o -name 'sessions.json' -o -name 'events.json' -o -name 'restore-preview.json' -o -name 'status-two-players.json' -o -name 'marker.json' -o -name 'artifact.sha256' -o -name 'run.log' \) \
  ! -name '*state*' ! -name 'join-address.txt' -print0 |
  while IFS= read -r -d '' f; do
    rel=${f#"$src"/}
    out="$dest/${rel//\//-}"
    sed -E 's/(setup code: |#code=|--code )[a-z0-9]{6}(-[a-z0-9]{6}){3}/\1<redacted>/g; s/lab-[A-Za-z0-9]{8,}/<lab-password>/g' "$f" >"$out"
  done
echo "copied $(find "$dest" -type f | wc -l) files to $dest"
