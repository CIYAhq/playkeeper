#!/usr/bin/env bash
# Tests that scripts/ci-parts.sh gives a pull request the end-to-end parts
# that check what it changed and no others: none for a change to the
# dashboard's pages or the agent alone, each part for its own files, every
# part when e2e.yml itself changes, and the first runner whenever the second
# one, which restores its backup, runs.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
checks=0
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
parts() { printf '%s\n' "$@" | "$root/scripts/ci-parts.sh"; }
expect() { # WANT FILE...: ci-parts.sh prints WANT for those changed files
  local want=$1 got
  shift
  got=$(parts "$@")
  [ "$got" = "$want" ] || fail "$* gave '$got', want '$want'"
  checks=$((checks + 1))
  echo "ok: $* -> ${want:-nothing}"
}

got=$(printf '' | "$root/scripts/ci-parts.sh")
[ -z "$got" ] || fail "no changed files gave '$got'"
checks=$((checks + 1))

expect "" web/src/pages/server/console.tsx internal/agent/pregen.go CHANGELOG.md docs/ARCHITECTURE.md
expect "" .github/workflows/ci.yml web/src/i18n/en.ts
expect "install-and-play,restore,views,core-flows,update,site,names,stats,certs,fake-panel" .github/workflows/e2e.yml
expect "install-and-play,restore,core-flows,update" test/e2e/pkclient.py
expect "install-and-play,restore" test/e2e/scenario.py test/e2e/bot/bot.js
expect "install-and-play" test/e2e/ui/onboarding.spec.ts
expect "install-and-play,views" scripts/e2e/played-state.sh test/e2e/ui/views.spec.ts
expect "install-and-play" scripts/e2e/guard-check.sh
expect "install-and-play,views" .github/actions/played-install/action.yml
expect "core-flows" test/e2e/ui/smoke.spec.ts
expect "core-flows" test/e2e/stats_recorder.py
expect "stats" scripts/stats-check.sh services/stats/Dockerfile
expect "stats,fake-panel" cmd/playkeeper-stats/main.go
expect "core-flows,stats,fake-panel" internal/usage/usage.go
expect "update" internal/install/install.go
expect "update" internal/update/apply.go packaging/get.sh scripts/e2e/update-releases.sh
expect "site,fake-panel" site/pages/index.html
expect "site,fake-panel" cmd/site/main.go web/src/demo/data.ts
expect "site,fake-panel" internal/site/build.go
expect "site" web/src/demo/data.ts
expect "site" web/package-lock.json
expect "site" test/e2e/ui/demo-iphone.spec.ts
expect "site,fake-panel" test/e2e/ui/site-tools-motd.spec.ts
expect "names" services/names/Dockerfile internal/names/server.go scripts/names-check.sh
expect "certs" internal/certs/acme.go
expect "fake-panel" test/e2e/ui/clickthrough-plan.ts
expect "" web/src/pages/server/files/index.tsx scripts/site-check.shx internal/certsx/a.go

echo "all $checks checks passed"
