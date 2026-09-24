#!/usr/bin/env bash
# Copies the text evidence of a rehearsal run into docs/evidence/<name>/,
# redacting one-time setup codes, lab passwords and private addresses, and
# dropping session state, secret hashes and binaries. Logs are renamed to .txt
# because the repository ignores *.log.
# Usage: scripts/e2e/collect-evidence.sh test/e2e/out/vm-runN docs/evidence/vm-rehearsal
set -euo pipefail
src=$1
dest=$2
mkdir -p "$dest"
find "$src" -maxdepth 2 -type f \( -name '*.txt' -o -name '*.diff' -o -name '*results.json' -o -name 'sessions.json' -o -name 'events.json' \
  -o -name 'restore-preview.json' -o -name 'status-two-players.json' -o -name 'marker.json' -o -name 'summary-a.json' -o -name 'audit-a.json' \
  -o -name '*install-manifest.json' -o -name 'artifact.sha256' -o -name 'run.log' \) \
  ! -name '*state*' ! -name 'join-address.txt' ! -name 'refused.out' -print0 |
  while IFS= read -r -d '' f; do
    rel=${f#"$src"/}
    rel=${rel%.log}
    [ "$rel" = "${f#"$src"/}" ] || rel="$rel-log.txt"
    out="$dest/${rel//\//-}"
    sed -E 's/(setup code: |#code=|--code )[a-z0-9]{6}(-[a-z0-9]{6}){3}/\1<redacted>/g; s/lab-[A-Za-z0-9]{8,}/<lab-password>/g;
      s/\b10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\b/<private-ip>/g; s/\b172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3}\b/<private-ip>/g;
      s/\b192\.168\.[0-9]{1,3}\.[0-9]{1,3}\b/<private-ip>/g' "$f" >"$out"
  done
echo "copied $(find "$dest" -type f | wc -l) files to $dest"
