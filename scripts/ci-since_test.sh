#!/usr/bin/env bash
# Tests that scripts/ci-since.sh measures a pull request's push from its last
# green run: nothing for a push that only merges main or rebases onto it, the
# docs alone for a docs change, and a file whose own changed lines changed
# (whitespace, a binary file, a conflict with main resolved), but not one the
# pull request no longer changes; that a workflow with paths runs only when a
# file on them changed; that without a green run of its own for the pull
# request, or outside a pull request, everything runs; and that each workflow
# that uses it names itself and has paths it can read.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
since="$root/scripts/ci-since.sh"
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

cd "$tmp"
git init -q -b main repo
cd repo
git config user.email ci@example.com
git config user.name CI
commit() {
  git add -A
  git commit -qm "$1"
  git rev-parse HEAD
}
between() { "$since" between main "$@" | paste -sd' ' -; }
expect() { # WANT OLD NEW WHAT
  local got
  got=$(between "$2" "$3")
  [ "$got" = "$1" ] || fail "$4: got '$got', want '$1'"
  ok "$4"
}

seq 1 40 >web.ts
echo docs >README.md
echo '# Changelog' >CHANGELOG.md
mkdir -p internal/install .github/workflows
seq 1 20 >internal/install/install.go
printf '\000\001\002' >logo.png
cat >.github/workflows/matrix.yml <<'EOF'
name: Matrix
on:
  pull_request:
    paths:
      - internal/install/**
      - .github/workflows/matrix.yml
  workflow_dispatch:
jobs: {}
EOF
cat >.github/workflows/ci.yml <<'EOF'
name: CI
on:
  pull_request:
  push:
    branches: [main]
jobs: {}
EOF
commit base >/dev/null

git checkout -qb pr
sed -i 's/^5$/five/' web.ts
sed -i 's/^3$/three/' internal/install/install.go
green=$(commit "the pull request")

git checkout -q main
sed -i -e '1i // a line above the pull request'"'"'s' -e 's/^30$/thirty/' web.ts
echo other >other.go
commit "main changes the same file, above and below, and another" >/dev/null

git checkout -q pr
git merge -q --no-edit main >/dev/null
merged=$(git rev-parse HEAD)
expect "" "$green" "$merged" "a push that only merges main changes nothing"

rebased=$(git checkout -q --detach "$green" && git rebase -q main && git rev-parse HEAD)
git checkout -q pr
expect "" "$green" "$rebased" "the pull request rebased onto main changes nothing"

echo '- a line' >>CHANGELOG.md
echo 'more docs' >>README.md
docs=$(commit docs)
expect "CHANGELOG.md README.md" "$merged" "$docs" "a docs change is the docs alone"

sed -i 's/^three$/THREE/' internal/install/install.go
code=$(commit code)
expect "internal/install/install.go" "$docs" "$code" "a change to code already changed counts"

sed -i 's/^THREE$/  THREE/' internal/install/install.go
spaces=$(commit spaces)
expect "internal/install/install.go" "$code" "$spaces" "a change of whitespace alone counts"

printf '\000\001\003' >logo.png
binary=$(commit binary)
expect "logo.png" "$spaces" "$binary" "a change to a binary file counts"

git show main:web.ts >web.ts
reverted=$(commit "web.ts as main has it")
expect "" "$binary" "$reverted" "a file the pull request no longer changes doesn't count"

git checkout -q main
sed -i 's/^3$/tres/' internal/install/install.go
commit "main changes the line the pull request changes" >/dev/null
git checkout -q pr
if git merge -q --no-edit main >/dev/null 2>&1; then fail "the merge was meant to conflict"; fi
git checkout -q --ours internal/install/install.go
resolved=$(commit "merge main, keeping the pull request's line")
expect "internal/install/install.go" "$reverted" "$resolved" "a conflict with main, resolved, counts"

# The CI mode: a fake gh answers the runs API, the repository is its own
# origin, and the event is a pull request's.
git remote add origin "$tmp/repo"
git fetch -q origin
mkdir -p "$tmp/bin"
cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
cat "$RUNS"
EOF
chmod +x "$tmp/bin/gh"
runs() { # RUNS for the fake gh: head commit and pull request number pairs, newest first
  local out='{"workflow_runs":[' sep=
  while [ $# -gt 1 ]; do
    out+="$sep{\"head_sha\":\"$1\",\"pull_requests\":[{\"number\":$2}]}"
    sep=,
    shift 2
  done
  echo "$out]}" >"$tmp/runs.json"
}
event() { # HEAD: a pull request event for pull request 7 at HEAD
  echo "{\"pull_request\":{\"number\":7,\"head\":{\"sha\":\"$1\"}}}" >"$tmp/event.json"
}
ci() { # WORKFLOW: runs it as CI would, with its output in $out and what it printed in $printed
  : >"$tmp/output"
  printed=$(PATH="$tmp/bin:$PATH" RUNS="$tmp/runs.json" GITHUB_EVENT_PATH="$tmp/event.json" GITHUB_OUTPUT="$tmp/output" \
    GITHUB_REPOSITORY=o/r GITHUB_HEAD_REF=pr GITHUB_BASE_REF=main "$since" "$1" 2>/dev/null | paste -sd' ' -)
  out=$(paste -sd' ' "$tmp/output")
}

event "$docs"
runs "$merged" 7
ci matrix.yml
[ "$out" = "measured=true run=false" ] && [ "$printed" = "CHANGELOG.md README.md" ] || fail "docs on matrix.yml: '$out', '$printed'"
ok "a workflow with paths doesn't run again for a docs change, and says what changed"

event "$code"
runs "$docs" 7
ci matrix.yml
[ "$out" = "measured=true run=true" ] && [ "$printed" = "internal/install/install.go" ] || fail "code on matrix.yml: '$out', '$printed'"
ok "a workflow with paths runs again for a change on them"

event "$merged"
runs "$green" 7
ci ci.yml
[ "$out" = "measured=true run=false" ] && [ -z "$printed" ] || fail "a merge of main on ci.yml: '$out', '$printed'"
ok "a workflow without paths measures nothing for a push that only merges main"

event "$docs"
runs "$merged" 7
ci ci.yml
[ "$out" = "measured=true run=true" ] && [ "$printed" = "CHANGELOG.md README.md" ] || fail "docs on ci.yml: '$out', '$printed'"
ok "a workflow without paths gets the files for a docs change"

runs "$merged" 8
ci matrix.yml
[ "$out" = "measured=false run=true" ] && [ -z "$printed" ] || fail "another pull request's green run: '$out', '$printed'"
ok "another pull request's green run isn't measured from"

runs
ci matrix.yml
[ "$out" = "measured=false run=true" ] || fail "no green run: '$out'"
ok "without a green run everything runs"

runs 0123456789abcdef0123456789abcdef01234567 7
ci matrix.yml
[ "$out" = "measured=false run=true" ] || fail "a green run whose commit can't be fetched: '$out'"
ok "a green run whose commit can't be fetched means everything runs"

echo '{"push":{}}' >"$tmp/event.json"
ci matrix.yml
[ "$out" = "measured=false run=true" ] || fail "outside a pull request: '$out'"
ok "outside a pull request everything runs"

if "$since" nothing.yml >/dev/null 2>&1; then fail "a workflow that doesn't exist was measured"; fi
ok "a workflow that doesn't exist is refused"

# The real workflows that use it name themselves, and those that run only for
# some paths have paths it reads.
cd "$root"
for wf in .github/workflows/*.yml; do
  name=$(basename "$wf")
  grep -q 'scripts/ci-since\.sh' "$wf" || continue
  grep -q "scripts/ci-since\.sh $name" "$wf" || fail "$name runs ci-since.sh for another workflow"
  if grep -q '^    paths:' "$wf"; then
    [ -n "$("$since" paths "$name")" ] || fail "$name has paths, but ci-since.sh reads none"
  fi
  ok "$name measures itself"
done
got=$("$since" paths os-matrix.yml)
grep -qx 'internal/install/\*\*' <<<"$got" && grep -qx '\.github/workflows/os-matrix\.yml' <<<"$got" || fail "os-matrix.yml's paths: $got"
[ -z "$("$since" paths ci.yml)" ] || fail "ci.yml has no paths, but ci-since.sh read some"
ok "the paths are read from on.pull_request.paths and no other list"

echo "ci-since.sh: $checks checks passed"
