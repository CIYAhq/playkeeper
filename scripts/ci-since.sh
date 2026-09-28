#!/usr/bin/env bash
# What a pull request's push changes that the pull request's last green run
# of a workflow didn't see. The checks that run for what a pull request
# changes (ci.yml's crawl and end-to-end parts, the OS matrix, ARM64 and the
# release dry run) run for these files alone, so a push that only merges
# main, or changes nothing they cover, such as the docs or CHANGELOG.md,
# doesn't run them again.
#
#   WORKFLOW               in a pull request's run of .github/workflows/WORKFLOW:
#                          finds that workflow's newest green run for the pull
#                          request and prints the files the pull request now
#                          changes differently, one a line. With GITHUB_OUTPUT
#                          set it writes measured=true, and run=false when
#                          none of them is on the workflow's
#                          on.pull_request.paths (or, for a workflow without
#                          that list, when there are none), else run=true.
#                          Without a green run to measure from (none yet, or
#                          its commit can't be fetched) it prints nothing and
#                          writes measured=false and run=true.
#   between BASE OLD NEW   the files NEW changes differently from OLD. A
#                          commit's changes are its diff from where it meets
#                          BASE, and a file counts when its changed lines
#                          differ, wherever BASE's changes moved them. Only
#                          the files NEW still changes are printed.
#   paths WORKFLOW         the patterns WORKFLOW's on.pull_request.paths lists
#
# It needs the history back to where the pull request meets its base
# (actions/checkout with fetch-depth: 0), and GH_TOKEN with actions: read.
set -euo pipefail

usage="usage: ci-since.sh WORKFLOW | between BASE OLD NEW | paths WORKFLOW"

# lines FROM TO FILE: a hash of FILE's changed lines from FROM to TO, without
# the blob names and line numbers that change when BASE's changes move them.
lines() {
  git --literal-pathspecs diff-tree -r -p -U0 --binary --no-renames "$1" "$2" -- "$3" |
    sed -e '/^index /d' -e 's/^@@ .*/@@/' | sha1sum
}

between() {
  local from_old from_new
  from_old=$(git merge-base "$1" "$2") || return
  from_new=$(git merge-base "$1" "$3") || return
  git diff-tree -r -z --name-only --no-renames "$from_new" "$3" | while IFS= read -r -d '' f; do
    was=$(lines "$from_old" "$2" "$f") && now=$(lines "$from_new" "$3" "$f") || exit
    [ "$was" = "$now" ] || printf '%s\n' "$f"
  done
}

paths() {
  awk '
    /^on:/ { on = 1; next }
    on && /^[^ #]/ { exit }
    on && /^  [^ #]/ { pr = ($1 == "pull_request:"); list = 0; next }
    pr && /^    [^ #]/ { list = ($1 == "paths:"); next }
    pr && list && /^      - / { sub(/^      - /, ""); gsub(/"/, ""); print }
  ' ".github/workflows/$1"
}

# covered FILE PATTERN...: whether FILE is on one of the path patterns. Bash's
# * also matches /, where GitHub's doesn't, which only ever runs more.
covered() {
  local f=$1 p
  shift
  for p; do
    # shellcheck disable=SC2053 # the pattern is meant to match as a glob
    [[ $f == $p ]] && return 0
  done
  return 1
}

# everything REASON: the pull request's run can't be measured from a green one.
everything() {
  echo "$1, so everything the pull request changes counts." >&2
  printf 'measured=false\nrun=true\n' >>"${GITHUB_OUTPUT:-/dev/null}"
  exit 0
}

since() {
  local workflow=$1 number head green changed f run=false on=()
  [ -f ".github/workflows/$workflow" ] || {
    echo "ci-since.sh: no workflow .github/workflows/$workflow" >&2
    exit 2
  }
  number=$(jq -r '.pull_request.number // empty' "${GITHUB_EVENT_PATH:-/dev/null}" 2>/dev/null) || number=
  head=$(jq -r '.pull_request.head.sha // empty' "${GITHUB_EVENT_PATH:-/dev/null}" 2>/dev/null) || head=
  if [ -z "$number" ] || [ -z "$head" ]; then everything "This isn't a pull request's run"; fi
  green=$(gh api -X GET "repos/$GITHUB_REPOSITORY/actions/workflows/$workflow/runs" \
    -f event=pull_request -f branch="$GITHUB_HEAD_REF" -f status=success -f per_page=50 |
    jq -r --argjson n "$number" '[.workflow_runs[] | select(any(.pull_requests[]?; .number == $n))][0].head_sha // empty') || green=
  [ -n "$green" ] || everything "$workflow hasn't passed on this pull request yet"
  git cat-file -e "$green^{commit}" 2>/dev/null || git fetch -q --no-tags origin "$green" 2>/dev/null ||
    everything "The commit of $workflow's last green run, ${green:0:8}, can't be fetched"

  changed=$(between "origin/$GITHUB_BASE_REF" "$green" "$head") ||
    everything "Its changes couldn't be compared with those of $workflow's last green run, ${green:0:8}"
  mapfile -t on < <(paths "$workflow")
  while IFS= read -r f; do
    if [ -n "$f" ] && { [ ${#on[@]} -eq 0 ] || covered "$f" "${on[@]}"; }; then run=true; fi
  done <<<"$changed"
  printf 'measured=true\nrun=%s\n' "$run" >>"${GITHUB_OUTPUT:-/dev/null}"

  local what="Since $workflow last passed on this pull request (${green:0:8}),"
  if [ -z "$changed" ]; then
    what="$what the pull request's own changes are the same; merging main doesn't count."
  elif [ "$run" = false ]; then
    what="$what it changed only files $workflow doesn't run for: $(paste -sd' ' <<<"$changed")"
  else
    what="$what it changed: $(paste -sd' ' <<<"$changed")"
  fi
  echo "$what" >&2
  echo "$what" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"
  [ -z "$changed" ] || printf '%s\n' "$changed"
}

case ${1:-} in
  between)
    [ $# -eq 4 ] || {
      echo "$usage" >&2
      exit 2
    }
    between "$2" "$3" "$4"
    ;;
  paths) paths "${2:?$usage}" ;;
  "" | -*)
    echo "$usage" >&2
    exit 2
    ;;
  *) since "$1" ;;
esac
