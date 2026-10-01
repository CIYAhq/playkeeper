#!/usr/bin/env bash
# Tests lab_dnf (scripts/e2e/vm-lab.sh) without a guest: ssh is a stub on PATH
# that answers each call as dnf would, from a list of outcomes. dnf is tried
# again, with fresh metadata, when it couldn't get something from any mirror
# (as AlmaLinux 9's extras repository failed on 1 Oct 2026), and not for any
# other failure; and the OS rehearsal runs dnf in the guest only through it.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
checks=0
fail() {
  echo "FAIL: $*"
  exit 1
}

mkdir -p "$tmp/bin"
cat >"$tmp/bin/ssh" <<'EOF'
#!/usr/bin/env bash
# The guest's command is everything after pk@IP; the Nth call answers with
# the Nth line of $T/outcomes.
while [ $# -gt 0 ] && [[ $1 != pk@* ]]; do shift; done
shift
echo "$*" >>"$T/calls"
case $(sed -n "$(wc -l <"$T/calls")p" "$T/outcomes") in
  ok) exit 0 ;;
  mirrors) echo "Error: Failed to download metadata for repo 'extras': Yum repo downloading error: Downloading error(s): repodata/4a21bf8eeb47d833caaebca30efd66e0a793131b21da17553e03b677347a9635-comps-extras.x86_64.xml - Cannot download, all mirrors were already tried without success" ;;
  package) printf 'Error: Error downloading packages:\n  Curl error (28): Timeout was reached for https://mirror.example/9/AppStream/x86_64/os/Packages/podman-5.4.0-1.el9.x86_64.rpm\n' ;;
  missing) echo "Error: Unable to find a match: podman" ;;
  *) echo "stub ssh: no answer for call $(wc -l <"$T/calls")" ;;
esac
exit 1
EOF
chmod +x "$tmp/bin/ssh"

# shellcheck source=scripts/e2e/vm-lab.sh
. "$root/scripts/e2e/vm-lab.sh"

run() { # OUTCOME... — lab_dnf 198.51.100.20 -q install podman against those answers; sets st, out, calls and tries
  printf '%s\n' "$@" >"$tmp/outcomes"
  : >"$tmp/calls"
  st=0
  out=$(
    export PATH="$tmp/bin:$PATH" T="$tmp" LAB_DNF_WAIT=0
    lab_dnf 198.51.100.20 -q install podman 2>&1
  ) || st=$?
  calls=$(cat "$tmp/calls")
  tries=$(wc -l <"$tmp/calls")
}
ok() {
  checks=$((checks + 1))
  echo "ok: $*"
}

run ok
[ "$st" = 0 ] && [ "$tries" = 1 ] && [ "$calls" = "sudo dnf -y -q install podman" ] || fail "an install that works must run once, as sudo dnf -y (status $st, calls: $calls)"
ok "an install that works runs once"

run mirrors ok
[ "$st" = 0 ] || fail "an install whose mirrors failed once must succeed on the next try (status $st): $out"
[ "$tries" = 2 ] || fail "an install whose mirrors failed once must be tried twice, not $tries times: $calls"
[ "$(sed -n 2p <<<"$calls")" = "sudo dnf -y --refresh -q install podman" ] || fail "the next try must read the metadata afresh: $calls"
case $out in *"all mirrors were already tried without success"*"trying again"*) ;; *) fail "it must show dnf's error, then say it tries again: $out" ;; esac
ok "metadata no mirror had is asked for again, afresh"

run package ok
[ "$st" = 0 ] && [ "$tries" = 2 ] || fail "a package download that timed out must be tried again (status $st, $tries tries): $out"
ok "a package no mirror sent is asked for again"

run missing
[ "$st" = 1 ] && [ "$tries" = 1 ] || fail "a package that doesn't exist must fail at once with dnf's status (status $st, $tries tries): $out"
case $out in *"Unable to find a match: podman"*) ;; *) fail "it must show why dnf failed: $out" ;; esac
ok "a package that doesn't exist fails at once"

run mirrors mirrors mirrors mirrors mirrors
[ "$st" = 1 ] && [ "$tries" = 4 ] || fail "mirrors that keep failing must be given four tries, then fail (status $st, $tries tries)"
ok "mirrors that keep failing get four tries"

# The OS rehearsal runs dnf in the guest only through lab_dnf.
direct=$(grep -n 'sudo dnf' "$root/scripts/e2e/vm-os.sh" || true)
[ -z "$direct" ] || fail "vm-os.sh runs dnf in the guest without lab_dnf: $direct"
ok "vm-os.sh runs dnf in the guest only through lab_dnf"

echo "vm-lab.sh: $checks checks passed"
