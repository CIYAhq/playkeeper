#!/usr/bin/env bash
# The quarantined tests test/quarantine.txt lists, for the commands that
# leave them out of the checks that block a merge and the workflow that runs
# them on their own:
#   scripts/quarantine.sh packages          the Go packages with a quarantined test
#   scripts/quarantine.sh names PACKAGE     PACKAGE's quarantined Go tests, or a script's checks
#   scripts/quarantine.sh skip              go test's -skip flag for every quarantined Go test, or nothing
# QUARANTINE_LIST names another list, for tests.
set -euo pipefail

list=${QUARANTINE_LIST:-"$(cd "$(dirname "$0")/.." && pwd)/test/quarantine.txt"}
entries() { awk '!/^[[:space:]]*(#|$)/ { print $1, $2 }' "$list"; }

case ${1:-} in
  packages) entries | awk '$1 ~ /^\.\// { print $1 }' | sort -u ;;
  names) entries | awk -v p="${2:?usage: quarantine.sh names PACKAGE}" '$1 == p { print $2 }' ;;
  skip)
    tests=$(entries | awk '$1 ~ /^\.\// { print $2 }' | paste -sd'|' -)
    if [ -n "$tests" ]; then echo "-skip=^($tests)\$"; fi
    ;;
  *)
    echo "usage: quarantine.sh packages | names PACKAGE | skip" >&2
    exit 2
    ;;
esac
