#!/usr/bin/env bash
# Tests that scripts/discord-feed.sh posts a release a tag published to the
# Releases webhook with its title, link and bold lead phrases, or "fixes" when
# it only fixes things; a CI run on main or a tag release that failed, timed
# out or couldn't start to the CI webhook with the commit and the jobs that
# failed or timed out; and "main is green again" only for the first passing
# run on main after one of those, whatever was cancelled in between, a passing
# re-run of the failed run included. That pull requests, dry runs, other
# branches and cancelled runs post nothing; that a webhook whose secret isn't
# set is skipped without failing or asking GitHub anything; that a post
# Discord refuses, or GitHub failing to answer, fails the run; that the
# webhook URLs never show in what it prints; and that the workflow runs it
# only for pushes, from the default branch's own files.
# gh and curl are stubs that answer from files.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
feed="$root/scripts/discord-feed.sh"
workflow="$root/.github/workflows/discord.yml"
checks=0
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
ok() {
  checks=$((checks + 1))
  echo "ok: $*"
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
stubs=$tmp/stubs
mkdir -p "$tmp/bin" "$stubs"

cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >>"$STUBS/gh.log"
case "$1 $2" in
  "release view") cat "$STUBS/release.json" ;;
  "run list") cat "$STUBS/runs.json" ;;
  "api "*/attempts/*) cat "$STUBS/attempt-${2##*/}.json" ;;
  "api "*) cat "$STUBS/jobs.json" ;;
  *)
    echo "unexpected: gh $*" >&2
    exit 1
    ;;
esac
EOF
cat >"$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
out= data= url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    --data-binary) data=$2; shift 2 ;;
    -w | -H | --retry) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf '%s' "$data" >"$STUBS/payload.json"
printf '%s' "$url" >"$STUBS/url"
printf '%s' "${ANSWER:-ok}" >"$out"
printf '%s' "${CODE:-200}"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/curl"

releases_url=https://discord.com/api/webhooks/111/releases-token-for-tests
ci_url=https://discord.com/api/webhooks/222/ci-token-for-tests
run_url=https://github.com/CIYAhq/playkeeper/actions/runs/500
# run WORKFLOW EVENT CONCLUSION BRANCH [NAME=VALUE...] runs the script with
# the stubs, leaving what it printed in $tmp/out and its exit status in $status.
run() {
  rm -f "$stubs/payload.json" "$stubs/url" "$stubs/gh.log"
  status=0
  env PATH="$tmp/bin:$PATH" STUBS="$stubs" GH_REPO=CIYAhq/playkeeper GH_TOKEN=test \
    DISCORD_WEBHOOK_RELEASES="$releases_url" DISCORD_WEBHOOK_CI="$ci_url" \
    WORKFLOW="$1" EVENT="$2" CONCLUSION="$3" BRANCH="$4" SHA=0123456789abcdef RUN_ID=500 RUN_ATTEMPT=1 RUN_URL="$run_url" \
    COMMIT_MESSAGE=$'Keep "quotes" & <tags> in the subject\n\nand only the first line' \
    "${@:5}" "$feed" >"$tmp/out" 2>&1 || status=$?
  ! grep -q token-for-tests "$tmp/out" || fail "a webhook URL showed in what it printed for $*"
}
field() { jq -r "$1" "$stubs/payload.json"; }
posted_to() {
  [ "$status" = 0 ] || fail "$2: exited $status: $(cat "$tmp/out")"
  [ -f "$stubs/url" ] && [ "$(cat "$stubs/url")" = "$1?wait=true" ] || fail "$2: posted to '$(cat "$stubs/url" 2>/dev/null)'"
  [ "$(field '.allowed_mentions.parse | length')" = 0 ] || fail "$2: the post could ping someone"
}
nothing_posted() {
  [ "$status" = 0 ] || fail "$1: exited $status: $(cat "$tmp/out")"
  [ ! -f "$stubs/url" ] || fail "$1: posted to $(cat "$stubs/url")"
}
release() { # NAME URL BODY
  jq -n --arg name "$1" --arg url "$2" --arg body "$3" '{name: $name, url: $url, body: $body}' >"$stubs/release.json"
}

release "Playkeeper v0.4.4 (early release)" https://github.com/CIYAhq/playkeeper/releases/tag/v0.4.4 "$(
  cat <<'EOF'
**Early release.** Keep your own copies of any backup you care about.

## What changed

- Fixed: modpacks that ship a `default-server.properties` switched off the console.
- **Bedrock friends can join:** turn on **Bedrock players** in a server's Settings.
- **A server's own address:** under your own domain, each server can have one.
- **New server › A template** lists Playkeeper's own templates.
- Templates can carry CurseForge modpacks.

## Install or update

- **Not a change:** this line is in another section.
EOF
)"
run Release push success v0.4.4
posted_to "$releases_url" "a release"
[ "$(field '.embeds[0].title')" = "Playkeeper v0.4.4 (early release)" ] || fail "release title: $(field '.embeds[0].title')"
[ "$(field '.embeds[0].url')" = https://github.com/CIYAhq/playkeeper/releases/tag/v0.4.4 ] || fail "release link: $(field '.embeds[0].url')"
want=$'- Bedrock friends can join\n- A server\'s own address\n- New server › A template'
[ "$(field '.embeds[0].description')" = "$want" ] || fail "release lead phrases: $(field '.embeds[0].description')"
[ "$(field '.embeds[0].color')" = 1409085 ] || fail "release colour: $(field '.embeds[0].color')"
grep -q '^gh release view v0.4.4 ' "$stubs/gh.log" || fail "release: asked GitHub $(cat "$stubs/gh.log")"
ok "a release posts its title, link and bold lead phrases to the Releases webhook"

release "Playkeeper v0.4.5 (early release)" https://github.com/CIYAhq/playkeeper/releases/tag/v0.4.5 \
  $'## What changed\r\n\r\n- Fixed: records were found late.\r\n- Fixed: another thing.\r\n\r\n## Install or update\r\n\r\n- **Not a change:** elsewhere.\r\n'
run Release push success v0.4.5
posted_to "$releases_url" "a fix-only release"
[ "$(field '.embeds[0].description')" = fixes ] || fail "fix-only release: $(field '.embeds[0].description')"
ok "a release that only fixes things says fixes, whatever its line ends"

release "Playkeeper v0.4.2 (early release)" https://github.com/CIYAhq/playkeeper/releases/tag/v0.4.2 \
  $'## What changed\n\n- A **Files** tab on each server, for admins. Browse its folder.\n- Fixed: one thing.\n\n## Install or update\n'
run Release push success v0.4.2
posted_to "$releases_url" "a release without lead phrases"
[ "$(field '.embeds[0].description')" = "- A **Files** tab on each server, for admins" ] || fail "release without lead phrases: $(field '.embeds[0].description')"
ok "a release without bold lead phrases shows its first change's first sentence"

cat >"$stubs/jobs.json" <<'EOF'
{"jobs": [
  {"name": "Plan — the pages to click through", "conclusion": "success"},
  {"name": "Lint — gofmt, go vet, ESLint, TypeScript, THIRD_PARTY_NOTICES, shellcheck", "conclusion": "failure"},
  {"name": "Go unit tests, every package but the agent's", "conclusion": "failure"},
  {"name": "Package", "conclusion": "skipped"}
]}
EOF
failed_jobs=$'- Lint — gofmt, go vet, ESLint, TypeScript, THIRD_PARTY_NOTICES, shellcheck\n- Go unit tests, every package but the agent\'s'
commit="\`0123456\` Keep \"quotes\" & <tags> in the subject"
run Release push failure v0.4.5
posted_to "$ci_url" "a failed release"
[ "$(field '.embeds[0].title')" = "Release v0.4.5 failed" ] || fail "failed release title: $(field '.embeds[0].title')"
[ "$(field '.embeds[0].url')" = "$run_url" ] || fail "failed release link: $(field '.embeds[0].url')"
[ "$(field '.embeds[0].description')" = "$commit"$'\n'"$failed_jobs" ] || fail "failed release: $(field '.embeds[0].description')"
[ "$(field '.embeds[0].color')" = 15680580 ] || fail "failed release colour: $(field '.embeds[0].color')"
grep -q '^gh api repos/CIYAhq/playkeeper/actions/runs/500/jobs' "$stubs/gh.log" || fail "failed release: asked GitHub $(cat "$stubs/gh.log")"
ok "a failed tag release posts the commit, the failed jobs and the run to the CI webhook"

run CI push failure main
posted_to "$ci_url" "a failed CI run on main"
[ "$(field '.embeds[0].title')" = "CI failed on main" ] || fail "failed CI title: $(field '.embeds[0].title')"
[ "$(field '.embeds[0].description')" = "$commit"$'\n'"$failed_jobs" ] || fail "failed CI: $(field '.embeds[0].description')"
ok "a failed CI run on main posts the commit, the failed jobs and the run to the CI webhook"

echo '[{"databaseId": 501, "conclusion": "failure"}, {"databaseId": 500, "conclusion": "success"},
  {"databaseId": 499, "conclusion": "cancelled"}, {"databaseId": 498, "conclusion": "failure"},
  {"databaseId": 497, "conclusion": "success"}]' >"$stubs/runs.json"
run CI push success main
posted_to "$ci_url" "main passing after a failure"
[ "$(field '.embeds[0].title')" = "main is green again" ] || fail "green again title: $(field '.embeds[0].title')"
[ "$(field '.embeds[0].description')" = "$commit" ] || fail "green again: $(field '.embeds[0].description')"
[ "$(field '.embeds[0].color')" = 1409085 ] || fail "green again colour: $(field '.embeds[0].color')"
grep -q -- '--workflow CI --branch main --event push' "$stubs/gh.log" || fail "green again: asked GitHub $(cat "$stubs/gh.log")"
ok "the first passing run on main after a failed one says main is green again, past cancelled runs"

echo '[{"databaseId": 500, "conclusion": "success"}, {"databaseId": 499, "conclusion": "success"},
  {"databaseId": 498, "conclusion": "failure"}]' >"$stubs/runs.json"
run CI push success main
nothing_posted "main passing after a pass"
echo '[{"databaseId": 500, "conclusion": "success"}]' >"$stubs/runs.json"
run CI push success main
nothing_posted "main's first run"
ok "a passing run after a passing one, or with none before it, posts nothing"

cat >"$stubs/jobs.json" <<'EOF'
{"jobs": [
  {"name": "Go unit tests, every package but the agent's", "conclusion": "success"},
  {"name": "Agent unit tests — 16 shards side by side, every test run", "conclusion": "timed_out"},
  {"name": "Lint, typecheck and unit tests — every job passed", "conclusion": "failure"}
]}
EOF
run CI push timed_out main
posted_to "$ci_url" "a CI run on main that timed out"
[ "$(field '.embeds[0].title')" = "CI timed out on main" ] || fail "timed-out CI title: $(field '.embeds[0].title')"
want="$commit"$'\n- Agent unit tests — 16 shards side by side, every test run (timed out)\n- Lint, typecheck and unit tests — every job passed'
[ "$(field '.embeds[0].description')" = "$want" ] || fail "timed-out CI: $(field '.embeds[0].description')"
echo '{"jobs": []}' >"$stubs/jobs.json"
run Release push startup_failure v0.4.5
posted_to "$ci_url" "a release that couldn't start"
[ "$(field '.embeds[0].title')" = "Release v0.4.5 couldn't start" ] || fail "release that couldn't start: $(field '.embeds[0].title')"
[ "$(field '.embeds[0].description')" = "$commit" ] || fail "release that couldn't start: $(field '.embeds[0].description')"
echo '[{"databaseId": 500, "conclusion": "success"}, {"databaseId": 499, "conclusion": "timed_out"}]' >"$stubs/runs.json"
run CI push success main
posted_to "$ci_url" "main passing after a timeout"
[ "$(field '.embeds[0].title')" = "main is green again" ] || fail "green after a timeout: $(field '.embeds[0].title')"
ok "a run that timed out or couldn't start counts as failed, with its timed-out jobs listed"

echo '[{"databaseId": 500, "conclusion": "success"}, {"databaseId": 499, "conclusion": "success"}]' >"$stubs/runs.json"
echo '{"conclusion": "failure"}' >"$stubs/attempt-1.json"
run CI push success main RUN_ATTEMPT=2
posted_to "$ci_url" "a passing re-run of a failed run"
[ "$(field '.embeds[0].title')" = "main is green again" ] || fail "green re-run: $(field '.embeds[0].title')"
grep -q '^gh api repos/CIYAhq/playkeeper/actions/runs/500/attempts/1$' "$stubs/gh.log" || fail "green re-run: asked GitHub $(cat "$stubs/gh.log")"
echo '{"conclusion": "cancelled"}' >"$stubs/attempt-2.json"
run CI push success main RUN_ATTEMPT=3
posted_to "$ci_url" "a passing re-run after a cancelled attempt of a failed run"
echo '{"conclusion": "success"}' >"$stubs/attempt-1.json"
echo '[{"databaseId": 500, "conclusion": "success"}, {"databaseId": 499, "conclusion": "failure"}]' >"$stubs/runs.json"
run CI push success main RUN_ATTEMPT=2
nothing_posted "a passing re-run of a passing run"
echo '{"conclusion": "cancelled"}' >"$stubs/attempt-1.json"
run CI push success main RUN_ATTEMPT=2
posted_to "$ci_url" "a passing re-run of a cancelled run after a failure"
ok "a re-run looks at the run's own earlier attempts first, past cancelled ones"

echo 'not json' >"$stubs/runs.json"
run CI push success main
[ "$status" != 0 ] && [ ! -f "$stubs/url" ] || fail "a GitHub answer it can't read passed as main already green: $(cat "$tmp/out")"
ok "GitHub failing to answer fails the run instead of posting nothing"

run CI pull_request failure siya/some-branch
nothing_posted "a pull request's failed CI"
run CI push failure some-branch
nothing_posted "a failed push to another branch"
run Release pull_request success siya/some-branch
nothing_posted "a pull request's release dry run"
run Release workflow_dispatch failure main
nothing_posted "a dry run started by hand"
run CI push cancelled main
nothing_posted "a cancelled run"
[ ! -f "$stubs/gh.log" ] || fail "a run with nothing to post asked GitHub $(cat "$stubs/gh.log")"
ok "pull requests, dry runs, other branches and cancelled runs post nothing"

run Release push success v0.4.4 DISCORD_WEBHOOK_RELEASES=
nothing_posted "a release before its secret is set"
grep -q "DISCORD_WEBHOOK_RELEASES isn't set yet" "$tmp/out" || fail "no notice for the missing secret: $(cat "$tmp/out")"
[ ! -f "$stubs/gh.log" ] || fail "a skipped post asked GitHub $(cat "$stubs/gh.log")"
run CI push failure main DISCORD_WEBHOOK_CI=
nothing_posted "a failure before its secret is set"
ok "a webhook whose secret isn't set yet is skipped with a notice, and the run passes"

run CI push failure main CODE=400 'ANSWER={"message": "Invalid Webhook Token", "code": 50027}'
[ "$status" != 0 ] || fail "a refused post passed: $(cat "$tmp/out")"
grep -q 'HTTP 400' "$tmp/out" && grep -q 'Invalid Webhook Token' "$tmp/out" || fail "a refused post didn't say why: $(cat "$tmp/out")"
ok "a post Discord refuses fails the run and says why, without the webhook's URL"

grep -q '^  workflow_run:$' "$workflow" && grep -q '^    workflows: \[CI, Release\]$' "$workflow" || fail "discord.yml doesn't follow CI and Release"
grep -q "github.event.workflow_run.event == 'push'" "$workflow" || fail "discord.yml doesn't run only for pushes"
! grep -qE '^\s+ref:' "$workflow" || fail "discord.yml checks out something other than the default branch"
grep -q '^name: CI$' "$root/.github/workflows/ci.yml" && grep -q '^name: Release$' "$root/.github/workflows/release.yml" ||
  fail "the workflows discord.yml follows are named differently"
[ "$(grep -cF '{{ secrets.' "$workflow")" = 2 ] || fail "discord.yml uses secrets other than the two webhooks"
ok "the workflow follows CI and Release pushes and runs the default branch's own files"

echo "all $checks checks passed"
