#!/usr/bin/env bash
# The parts of the end-to-end workflow (.github/workflows/e2e.yml) a pull
# request runs besides the fast checks and the crawl of the pages it changes:
# the ones that check what it touched. Reads the changed files, one a line,
# and prints the parts, comma-separated, in e2e.yml's order (nothing when it
# touched none). The Release check runs every part.
# Usage: git diff --name-only BASE HEAD | scripts/ci-parts.sh
set -euo pipefail

files=$(cat)
parts=(install-and-play restore views core-flows update site names stats certs fake-panel)

# checks PART: the changed files that part checks, as an extended regex.
checks() {
  case $1 in
    install-and-play) echo '^(test/e2e/(scenario|pkclient)\.py$|test/e2e/bot/|test/e2e/ui/onboarding\.spec\.ts$|scripts/e2e/played-state\.sh$|\.github/actions/played-install/)' ;;
    restore) echo '^(test/e2e/(scenario|pkclient)\.py$|test/e2e/bot/)' ;;
    views) echo '^(test/e2e/ui/views\.spec\.ts$|scripts/e2e/played-state\.sh$|\.github/actions/played-install/)' ;;
    core-flows) echo '^(test/e2e/(pkclient|stats_recorder)\.py$|test/e2e/ui/smoke\.spec\.ts$|internal/usage/)' ;;
    update) echo '^(test/e2e/(update|pkclient)\.py$|scripts/e2e/update-releases\.sh$|internal/(install|update)/|packaging/)' ;;
    site) echo '^(site/|cmd/site/|internal/site/|scripts/site-check\.sh$|web/src/demo/|web/(package|package-lock)\.json$|web/vite\.config\.ts$|test/e2e/ui/(demo|site)[^/]*\.spec\.ts$)' ;;
    names) echo '^(services/names/|internal/names/|scripts/names-check\.sh$)' ;;
    stats) echo '^(services/stats/|cmd/playkeeper-stats/|internal/usage/|scripts/stats-check\.sh$)' ;;
    certs) echo '^internal/certs/' ;;
    fake-panel) echo '^(test/e2e/ui/|site/|cmd/site/|internal/site/|cmd/playkeeper-stats/|internal/usage/)' ;;
  esac
}

# The specs under test/e2e/ui other parts run, which the fake-panel job doesn't.
others='^test/e2e/ui/(onboarding|views|smoke|demo|demo-iphone)\.spec\.ts$'

out=()
for part in "${parts[@]}"; do
  mine=$files
  if [ "$part" = fake-panel ]; then mine=$(grep -vE "$others" <<<"$files" || true); fi
  if grep -qx '\.github/workflows/e2e\.yml' <<<"$files" || grep -qE "$(checks "$part")" <<<"$mine"; then
    out+=("$part")
  fi
done
# The second runner restores the first one's backup.
if [[ " ${out[*]-} " == *" restore "* && " ${out[*]} " != *" install-and-play "* ]]; then
  out=(install-and-play "${out[@]}")
fi
(
  IFS=,
  echo "${out[*]-}"
)
