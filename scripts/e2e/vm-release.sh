#!/usr/bin/env bash
# Release rehearsal of the build in this checkout, in a fresh KVM guest
# (Ubuntu 24.04 cloud image, 2 vCPU, 3 GB RAM, 20 GB disk):
#   fresh  install the release tarball, keyboard-only onboarding in the
#          browser, two protocol bots, backups with a player online and a
#          same-host restore (scenario.py host-a)
#   owner  install the previous release (FROM, default v0.3.1) from its GitHub
#          release, give it a server with settings, a world marker, players
#          and two backups, then update to this build from the dashboard,
#          offered by a release server on the guest; everything must survive,
#          and a player, a backup and a restore of an old backup work after
# Then on both: sign-in and two-factor sign-in, Discord and a free name
# without credentials, every page, and errors in the Playkeeper logs. The
# guest's /etc/hosts sends the names service and Discord to a recorder on
# 127.0.0.1:443 (test/e2e/outbound_recorder.py), so nothing reaches them.
#
# The dashboard's updater only installs a release signed with a key it was
# built with. The owner path signs this build's manifest with a key made for
# the run and, right before the update, swaps the installed FROM binary for
# FROM rebuilt from its tag with that key added to its trusted keys, the only
# change. This build is not changed.
#
# Checks after the install record pass or FAIL and the run carries on; the
# summary at the end sets the exit status. VERSION defaults to the newest
# CHANGELOG section. Usage: scripts/e2e/vm-release.sh fresh|owner
# Remote commands are single-quoted on purpose so they expand in the guest.
# shellcheck disable=SC2016
set -euo pipefail

path=${1:-}
case $path in
  fresh) ip=198.51.100.20 octet=20 ;;
  owner) ip=198.51.100.21 octet=21 ;;
  *)
    echo "Usage: scripts/e2e/vm-release.sh fresh|owner" >&2
    exit 2
    ;;
esac
root=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=scripts/e2e/vm-lab.sh
. "$root/scripts/e2e/vm-lab.sh"
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH"
FROM=${FROM:-v0.3.1}
version=${VERSION:-$(awk '$1 == "##" && $2 ~ /^[0-9]+\.[0-9]+\.[0-9]+$/ {print $2; exit}' "$root/CHANGELOG.md")}
OUT=${OUT:-$root/test/e2e/out/release-$path-$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$OUT"
exec > >(tee -a "$OUT/run.log") 2>&1
work=$(mktemp -d) # the run's signing key stays out of the evidence
UI="$root/test/e2e/ui"
url="https://$ip:8443"
cert="$OUT/cert.pem"
export PK_PASSWORD=${PK_PASSWORD:-"lab-$(head -c 9 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"}
export PK_ADMIN_PASSWORD="$PK_PASSWORD"
OFFLINE_DROPIN='[Service]
Environment=PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1'
BLOCKED="names.playkeeper.io discord.com discordapp.com canary.discord.com ptb.discord.com"
results=()
since=""

phase() { printf '\n==== %s  [%s]\n' "$*" "$(date -u +%H:%M:%S)"; }

journal() {
  lab_ssh "$ip" 'sudo journalctl -u playkeeper-agent -u playkeeper-panel -u playkeeper-update --no-pager -o short-iso' >"$OUT/$path-journal.txt" 2>&1 || true
}

cleanup() {
  local rc=$?
  if [ -f "$LAB_DIR/$path/qemu.pid" ]; then journal; fi
  rm -rf "$work"
  git -C "$root" worktree prune
  if [ "$rc" -ne 0 ] && [ "${KEEP_ON_FAIL:-0}" = 1 ]; then
    lab_log "failed (exit $rc); guest kept running for inspection"
    return
  fi
  lab_shutdown "$path"
}
trap cleanup EXIT

# run_check NAME FILE COMMAND... — records pass or FAIL for COMMAND and carries on.
run_check() {
  local name=$1 file=$2
  shift 2
  if "$@" 2>&1 | tee "$OUT/$file"; then results+=("pass  $name"); else results+=("FAIL  $name"); fi
}

checks() { python3 "$root/test/e2e/release_checks.py" "$@" --url "$url" --cacert "$cert" --out "$OUT/$path"; }

build() {
  (cd "$root" && VERSION="$version" ./scripts/package.sh >/dev/null)
  name=playkeeper-$version-linux-amd64
  tarball=$root/dist/$name.tar.gz
  sha256sum "$tarball" | tee "$OUT/artifact.sha256"
}

boot_guest() {
  for d in "$root/test/e2e/bot" "$UI"; do
    [ -d "$d/node_modules" ] || (cd "$d" && npm ci --no-audit --no-fund >/dev/null)
  done
  lab_image
  lab_network
  lab_boot "$path" "$octet" 3072
  lab_scp "$root/test/e2e/outbound_recorder.py" "pk@$ip:"
  lab_ssh "$ip" "echo '127.0.0.1 $BLOCKED' | sudo tee -a /etc/hosts >/dev/null"
  lab_ssh "$ip" 'sudo sh -c "nohup python3 outbound_recorder.py 127.0.0.1 443 /var/log/pk-outbound.log </dev/null >/dev/null 2>&1 &"; sleep 1'
  lab_ssh "$ip" 'echo "## /etc/hosts"; grep "^127.0.0.1" /etc/hosts; echo "## recorder"; sudo ss -ltnH "sport = :443"' | tee "$OUT/$path-outbound-guard.txt"
}

offline_harness() {
  lab_ssh "$ip" "sudo mkdir -p /etc/systemd/system/playkeeper-agent.service.d && printf '%s\n' '$OFFLINE_DROPIN' | sudo tee /etc/systemd/system/playkeeper-agent.service.d/e2e-offline.conf >/dev/null && sudo systemctl daemon-reload && sudo systemctl restart playkeeper-agent"
}

wait_panel() {
  local i
  for i in $(seq 1 90); do
    if curl -fsS --cacert "$cert" "$url/healthz" >/dev/null 2>&1; then
      sleep 3
      return 0
    fi
    sleep 2
  done
  echo "the dashboard did not answer at $url" >&2
  return 1
}

installed() { # FILE — the setup code and certificate of the install whose output is in FILE
  code=$(grep -o 'setup code: [a-z0-9-]*' "$OUT/$1" | awk '{print $3}')
  lab_ssh "$ip" 'sudo cat /var/lib/playkeeper/panel/tls/cert.pem' >"$cert"
}

# sign_release: this build's release assets, signed with a key made for this
# run, in $work/releases/latest.
sign_release() {
  (cd "$root" && go run ./cmd/release-sign keygen --public-key-file "$work/rehearsal.pub" >"$work/signing.key")
  mkdir -p "$work/releases/latest"
  cp "$root/dist/get.sh" "$root/dist/playkeeper-linux-amd64.tar.gz" "$root/dist/playkeeper-linux-amd64.tar.gz.sha256" "$root/dist/playkeeper-release.json" "$work/releases/latest/"
  (cd "$root" && PLAYKEEPER_RELEASE_SIGNING_KEY=$(cat "$work/signing.key") go run ./cmd/release-sign sign "$work/releases/latest/playkeeper-release.json")
  (cd "$root" && go run ./cmd/release-sign verify --public-key-file "$work/rehearsal.pub" "$work/releases/latest/playkeeper-release.json") | tee "$OUT/release-signature.txt"
  cp "$work/releases/latest/playkeeper-release.json" "$OUT/release-manifest.json"
}

# from_rekeyed: FROM rebuilt from its tag with this run's key added to its
# trusted keys, in $work/from-playkeeper.
from_rekeyed() {
  local v=${FROM#v}
  git -C "$root" rev-parse -q --verify "refs/tags/$FROM" >/dev/null || git -C "$root" fetch -q --depth=1 origin tag "$FROM"
  git -C "$root" worktree add -q --detach "$work/from" "$FROM"
  cat "$work/rehearsal.pub" >>"$work/from/internal/update/release.pub"
  if [ -d "$root/.tools" ]; then cp -a "$root/.tools" "$work/from/.tools"; fi
  (cd "$work/from" && ./scripts/setup.sh >/dev/null && VERSION=$v ./scripts/package.sh >/dev/null)
  mkdir -p "$work/rekeyed"
  tar -xzf "$work/from/dist/playkeeper-$v-linux-amd64.tar.gz" -C "$work/rekeyed"
  mv "$work/rekeyed/playkeeper-$v-linux-amd64/playkeeper" "$work/from-playkeeper"
  { echo "## $FROM rebuilt from its tag; the change:"; git -C "$work/from" diff --stat; "$work/from-playkeeper" version; } | tee "$OUT/from-rekeyed.txt"
  git -C "$root" worktree remove --force "$work/from"
}

common() {
  local label=${path^^}
  phase "$label: sign-in and two-factor sign-in"
  run_check "$path: sign-in and two-factor sign-in" "$path-signin.txt" checks signin
  run_check "$path: reset-2fa turns two-factor sign-in off" "$path-reset-2fa.txt" lab_ssh "$ip" 'sudo playkeeper reset-2fa admin'
  run_check "$path: the password alone signs in after reset-2fa" "$path-after-reset.txt" checks after-reset
  phase "$label: Discord and a free name without credentials"
  mkdir -p "$OUT/ui-pages"
  run_check "$path: Discord and a free name degrade cleanly" "$path-degrade.txt" checks degrade --browser-session "$OUT/ui-pages/browser-session.json"
  lab_ssh "$ip" 'echo "TLS connections the recorder took on 127.0.0.1:443, by host name asked for:"; sudo cat /var/log/pk-outbound.log 2>/dev/null || echo "none"' | tee "$OUT/$path-outbound.txt"
  phase "$label: every page loads"
  run_check "$path: every page loads" "$path-pages.txt" \
    env -C "$UI" PK_URL="$url" PK_SHOTS="$OUT/screenshots/pages" PK_OUT="$OUT/ui-pages" npx playwright test pages-load.spec.ts --reporter=list
  phase "$label: errors in the Playkeeper logs"
  journal
  local args=(--journal "$OUT/$path-journal.txt")
  if [ -n "$since" ]; then args+=(--since "$since"); fi
  run_check "$path: no errors in the agent, panel and updater logs${since:+ since the update}" "$path-log-errors.txt" checks logs "${args[@]}"
}

fresh() {
  phase "FRESH: build $version and boot a fresh Ubuntu 24.04 guest"
  build
  boot_guest
  phase "FRESH: install $name from its release tarball"
  lab_scp "$tarball" "$tarball.sha256" "pk@$ip:"
  lab_ssh "$ip" "sha256sum -c $name.tar.gz.sha256 && tar -xzf $name.tar.gz && cd $name && sudo ./install.sh --yes" | tee "$OUT/fresh-install.txt"
  installed fresh-install.txt
  offline_harness
  wait_panel
  phase "FRESH: keyboard-only onboarding in the browser"
  (cd "$UI" && PK_URL="$url" PK_SETUP_CODE="$code" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui" npx playwright test onboarding.spec.ts --reporter=list) | tee "$OUT/fresh-onboarding.txt"
  phase "FRESH: two protocol bots, sessions, console, backups with a player online, a same-host restore"
  run_check "fresh: players, backups and a same-host restore (scenario.py host-a)" fresh-scenario.txt \
    python3 "$root/test/e2e/scenario.py" host-a --existing --url "$url" --cacert "$cert" --code "$code" --game-host "$(cat "$OUT/ui/join-address.txt")" --out "$OUT/fresh"
  common
}

owner() {
  local base=https://github.com/CIYAhq/playkeeper/releases/download/$FROM
  phase "OWNER: build $version, sign it with a key made for this run, rebuild $FROM with that key"
  build
  sign_release
  from_rekeyed
  boot_guest
  phase "OWNER: install $FROM from its GitHub release"
  lab_ssh "$ip" "curl -fsSL $base/get.sh | sudo PLAYKEEPER_BASE_URL=$base sh -s -- --yes --release-url http://127.0.0.1:8765/latest" | tee "$OUT/owner-install.txt"
  installed owner-install.txt
  curl -fsSL -o "$work/from-release.tar.gz" "$base/playkeeper-linux-amd64.tar.gz"
  mkdir -p "$work/release" && tar -xzf "$work/from-release.tar.gz" -C "$work/release"
  want=$(sha256sum "$work"/release/*/playkeeper | cut -d' ' -f1)
  got=$(lab_ssh "$ip" 'sha256sum /usr/local/bin/playkeeper' | cut -d' ' -f1)
  echo "installed binary $got; $FROM's release tarball has $want" | tee "$OUT/owner-installed-binary.txt"
  [ "$got" = "$want" ]
  lab_ssh "$ip" 'playkeeper version' | tee -a "$OUT/owner-installed-binary.txt"
  offline_harness
  wait_panel
  phase "OWNER: a server with settings, a world marker and a backup, on $FROM"
  checks owner-prepare --code "$code" | tee "$OUT/owner-prepare.txt"
  phase "OWNER: two players join, then a backup with a player online, on $FROM"
  checks owner-players --game-host "$ip" | tee "$OUT/owner-players.txt"
  phase "OWNER: the $FROM binary that installs the update trusts this run's signing key"
  lab_scp "$work/from-playkeeper" "pk@$ip:playkeeper-rekeyed"
  lab_ssh "$ip" 'sha256sum /usr/local/bin/playkeeper playkeeper-rekeyed && sudo install -m 0755 playkeeper-rekeyed /usr/local/bin/playkeeper && sudo systemctl restart playkeeper-agent playkeeper-panel && playkeeper version' | tee "$OUT/owner-swap.txt"
  wait_panel
  checks still-running | tee -a "$OUT/owner-swap.txt"
  phase "OWNER: a release server on the guest offers $version"
  lab_scp -r "$work/releases" "pk@$ip:"
  lab_ssh "$ip" 'nohup python3 -m http.server 8765 --bind 127.0.0.1 --directory releases </dev/null >releases.log 2>&1 & sleep 1; curl -fsS http://127.0.0.1:8765/latest/playkeeper-release.json' | tee "$OUT/owner-release-server.txt"
  phase "OWNER: update to $version from the dashboard"
  since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  python3 "$root/test/e2e/update.py" update --url "$url" --cacert "$cert" --out "$OUT/owner" --to "$version" | tee "$OUT/owner-update.txt"
  phase "OWNER: $version runs and kept everything"
  run_check "owner: the server, its settings, world, backups and history survive the update" owner-verify.txt \
    python3 "$root/test/e2e/update.py" verify --url "$url" --cacert "$cert" --out "$OUT/owner" --expect-version "$version"
  phase "OWNER: players, a backup with a player online and a restore of a $FROM backup, on $version"
  run_check "owner: players kept, a backup with a player online, a $FROM backup restored" owner-after.txt checks owner-after --game-host "$ip"
  common
}

"$path"
phase "SUMMARY: $path, Playkeeper $version$([ "$path" = owner ] && echo " from $FROM")"
printf '%s\n' "${results[@]}" | tee "$OUT/summary.txt"
! grep -q '^FAIL' "$OUT/summary.txt"
