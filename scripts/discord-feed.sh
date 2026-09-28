#!/usr/bin/env bash
# Posts the Playkeeper Discord's release and CI feeds when a CI or Release run
# finishes (.github/workflows/discord.yml). A release a tag's Release run
# published goes to #announcements through the Releases webhook
# (DISCORD_WEBHOOK_RELEASES): its title and link, and the bold lead phrases of
# its "What changed" section, or "fixes" when every change is a fix. A CI run
# on main, or a Release run on a tag, that failed, timed out or couldn't start
# goes to #ci through the CI webhook (DISCORD_WEBHOOK_CI) with the commit, the
# jobs that failed or timed out and the run's link, and the first passing CI
# run on main after one of those, a passing re-run included, says main is
# green again. A CI run a newer run on main has overtaken, like a re-run of an
# older one, posts nothing. Nothing else posts either: pull requests, dry runs
# and cancelled runs.
# A webhook whose secret isn't added yet is skipped with a notice, so the
# workflow stays green until it is.
# It reads the finished run from WORKFLOW (its workflow's name), EVENT,
# CONCLUSION, BRANCH (the tag, for a tag push), SHA, RUN_ID, RUN_ATTEMPT,
# RUN_URL and COMMIT_MESSAGE, and asks GitHub about it with gh (GH_TOKEN,
# GH_REPO).
set -euo pipefail
shopt -s inherit_errexit

green=1409085 # 0x15803D, the brand green of Playkeeper's own Discord alerts
red=15680580  # 0xEF4444
answer=$(mktemp)
trap 'rm -f "$answer"' EXIT

# configured SECRET says whether the variable named SECRET holds a webhook
# URL, with a notice when it doesn't.
configured() {
  if [ -n "${!1:-}" ]; then return 0; fi
  echo "::notice::$1 isn't set yet, so nothing was posted."
  return 1
}

# send SECRET BUILDER posts what BUILDER prints to the webhook whose URL is in
# the variable named SECRET. The URL is never printed.
send() {
  local secret=$1 build=$2 url payload code
  configured "$secret" || return 0
  url=${!secret}
  payload=$("$build")
  code=$(curl -sS --retry 3 -o "$answer" -w '%{http_code}' -H 'Content-Type: application/json' \
    --data-binary "$payload" "$url?wait=true") || code=000
  if [ "$code" != 200 ]; then
    echo "::error::Discord answered HTTP $code to the post through $secret: $(head -c 300 "$answer")"
    return 1
  fi
  echo "Posted through $secret."
}

embed() { # TITLE URL DESCRIPTION COLOR
  jq -n --arg title "$1" --arg url "$2" --arg description "$3" --argjson color "$4" \
    '{embeds: [{title: $title, url: $url, description: $description, color: $color}], allowed_mentions: {parse: []}}'
}

commit_line() {
  echo "\`${SHA:0:7}\` $(head -n 1 <<<"${COMMIT_MESSAGE:-}")"
}

# lead_phrases CHANGES prints "- PHRASE" for each change that leads with a bold
# phrase, like "- **Bedrock friends can join:** turn on ...", or "fixes" when
# every change is a fix, or else the first change's first sentence.
lead_phrases() {
  local changes=$1 bullets bold
  bullets=$(grep -E '^- ' <<<"$changes" || true)
  bold=$(sed -nE 's/^- \*\*([^*]+)\*\*.*/\1/p' <<<"$bullets" | sed -E 's/[[:space:]]*:$//')
  if [ -n "$bold" ]; then
    echo "- ${bold//$'\n'/$'\n'- }"
  elif [ -n "$bullets" ] && ! grep -qvE '^- Fixed:' <<<"$bullets"; then
    echo fixes
  elif [ -n "$bullets" ]; then
    head -n 1 <<<"$bullets" | sed -E 's/\. .*//'
  else
    echo "What changed is in the release notes."
  fi
}

release_post() {
  local release changes
  release=$(gh release view "$BRANCH" --json name,url,body)
  changes=$(jq -r '.body' <<<"$release" | tr -d '\r' | awk '/^## What changed/ { on = 1; next } /^## / { on = 0 } on')
  embed "$(jq -r '.name' <<<"$release")" "$(jq -r '.url' <<<"$release")" "$(lead_phrases "$changes")" "$green"
}

# outcome CONCLUSION prints success or failure, counting a run that timed out
# or couldn't start as failed, and nothing for a cancelled or skipped run.
outcome() {
  case "$1" in
    success) echo success ;;
    failure | timed_out | startup_failure) echo failure ;;
  esac
}

failure_post() {
  local what title jobs description
  case "$CONCLUSION" in
    timed_out) what="timed out" ;;
    startup_failure) what="couldn't start" ;;
    *) what=failed ;;
  esac
  title="CI $what on main"
  if [ "$WORKFLOW" = Release ]; then title="Release $BRANCH $what"; fi
  jobs=$(gh api "repos/$GH_REPO/actions/runs/$RUN_ID/jobs?per_page=100" |
    jq -r '.jobs[] | select(.conclusion == "failure" or .conclusion == "timed_out")
      | "- " + .name + (if .conclusion == "timed_out" then " (timed out)" else "" end)')
  description=$(commit_line)
  if [ -n "$jobs" ]; then description+=$'\n'"$jobs"; fi
  embed "$title" "$RUN_URL" "$description" "$red"
}

green_post() {
  embed "main is green again" "$RUN_URL" "$(commit_line)" "$green"
}

# main_runs prints main's last 20 CI push runs, newest first, as JSON.
main_runs() {
  gh run list --workflow CI --branch main --event push --limit 20 --json databaseId,status,conclusion
}

# newer_result RUNS prints the id of a CI run on main newer than this one that
# has finished with a result, if there is one: then this run, a re-run of an
# older one, no longer says how main is. Newer runs still going, or
# cancelled, don't count.
newer_result() {
  jq -r --argjson run "$RUN_ID" '[.[] | select(.databaseId > $run and .status == "completed"
    and (.conclusion | IN("success", "failure", "timed_out", "startup_failure")))] | max_by(.databaseId) | .databaseId // ""' <<<"$1"
}

# previous_outcome RUNS prints how CI on main went before this run: its own
# earlier attempts first, since a re-run keeps the run's id, then the last CI
# push run on main before it. Cancelled runs, like those a newer push
# replaced, don't count.
previous_outcome() {
  local attempt=${RUN_ATTEMPT:-1} earlier result
  while [ "$attempt" -gt 1 ]; do
    attempt=$((attempt - 1))
    earlier=$(gh api "repos/$GH_REPO/actions/runs/$RUN_ID/attempts/$attempt" | jq -r '.conclusion // ""')
    result=$(outcome "$earlier")
    if [ -n "$result" ]; then
      echo "$result"
      return
    fi
  done
  jq -r --argjson run "$RUN_ID" '[.[] | select(.databaseId < $run and .status == "completed")
    | .conclusion |= (if . == "timed_out" or . == "startup_failure" then "failure" else . end)
    | select(.conclusion == "success" or .conclusion == "failure")] | max_by(.databaseId) | .conclusion // ""' <<<"$1"
}

case "${WORKFLOW:-}:${EVENT:-}:$(outcome "${CONCLUSION:-}")" in
  Release:push:success) send DISCORD_WEBHOOK_RELEASES release_post ;;
  Release:push:failure) send DISCORD_WEBHOOK_CI failure_post ;;
  CI:push:failure | CI:push:success)
    if [ "${BRANCH:-}" = main ] && configured DISCORD_WEBHOOK_CI; then
      runs=$(main_runs)
      newer=$(newer_result "$runs")
      if [ -n "$newer" ]; then
        echo "A newer CI run on main, $newer, has finished since, so this one's result isn't news."
      elif [ "$(outcome "$CONCLUSION")" = failure ]; then
        send DISCORD_WEBHOOK_CI failure_post
      else
        previous=$(previous_outcome "$runs")
        if [ "$previous" = failure ]; then
          send DISCORD_WEBHOOK_CI green_post
        else
          echo "main was already green, so nothing was posted."
        fi
      fi
    fi
    ;;
  *) echo "Nothing to post for a ${CONCLUSION:-} ${WORKFLOW:-} run (${EVENT:-})." ;;
esac
