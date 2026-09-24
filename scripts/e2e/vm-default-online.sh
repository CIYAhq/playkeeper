#!/usr/bin/env bash
# Shipped-defaults check in a fresh KVM guest: install WITHOUT the test-harness
# flag, create a server through the API, and confirm that online mode is on,
# Paper's bStats telemetry is off, and a non-genuine (offline-mode) client is
# refused by Mojang authentication. The bot name is not a Mojang account.
# Usage: scripts/e2e/vm-default-online.sh [path/to/playkeeper-*.tar.gz]
# Remote commands are single-quoted on purpose so they expand in the guest.
# shellcheck disable=SC2016
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=scripts/e2e/vm-lab.sh
. "$root/scripts/e2e/vm-lab.sh"
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH"
OUT=${OUT:-$root/test/e2e/out/vm-default-$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$OUT"
exec > >(tee -a "$OUT/run.log") 2>&1
D=198.51.100.13
password="lab-$(head -c 9 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"
trap 'lab_shutdown d' EXIT

tarball=${1:-$(find "$root/dist" -maxdepth 1 -name 'playkeeper-*-linux-amd64.tar.gz' -printf '%T@ %p\n' | sort -rn | head -1 | cut -d' ' -f2-)}
name=$(basename "$tarball" .tar.gz)
lab_image
lab_network
lab_boot d 13 3072
lab_scp "$tarball" "$tarball.sha256" "pk@$D:"
lab_ssh "$D" "sha256sum -c $name.tar.gz.sha256 && tar -xzf $name.tar.gz && cd $name && sudo ./install.sh --yes" | tee "$OUT/install.txt"
code=$(grep -o 'setup code: [a-z0-9-]*' "$OUT/install.txt" | awk '{print $3}')
lab_ssh "$D" 'sudo cat /var/lib/playkeeper/panel/tls/cert.pem' >"$OUT/cert.pem"
pk() { python3 "$root/test/e2e/pkclient.py" --url "https://$D:8443" --cacert "$OUT/cert.pem" --state "$OUT/state.json" "$@"; }
pk setup "$code" admin "$password" >/dev/null
pk create --version paper-26.1.2 | tail -3
pk wait-online --timeout 600 | grep -E '"phase"|"offlineModeTest"'
echo "## shipped defaults on a fresh install (no test-harness flag)"
lab_ssh "$D" 'sudo grep -E "^(online-mode|white-list|enforce-whitelist|log-ips)=" /var/lib/playkeeper/server/data/server.properties; sudo docker inspect playkeeper-minecraft --format "{{range .Config.Env}}{{println .}}{{end}}" | grep -E "^(ONLINE_MODE|VERSION|SKIP_DOWNLOAD_DEFAULTS)="'
echo "## Paper telemetry (bStats) and third-party config downloads"
lab_ssh "$D" 'sudo grep -E "^enabled:" /var/lib/playkeeper/server/data/plugins/bStats/config.yml'
pk call GET '/api/server/logs?limit=2000' | grep -c 'raw.githubusercontent.com' | sed 's/^/console lines mentioning raw.githubusercontent.com: /' || true
echo "## a non-genuine (offline-mode) client tries to join; Mojang authentication runs before the allowlist check"
if node "$root/test/e2e/bot/bot.js" visit --host "$D" --port 25565 --name PkBotNoAuth --stay 5; then
  echo "UNEXPECTED: the offline-mode client joined"
  exit 1
fi
echo "refused as expected"
lab_ssh "$D" 'sudo docker logs playkeeper-minecraft 2>&1 | grep -E "PkBotNoAuth" | tail -3'
pk call GET '/api/events?limit=10' | grep -c '"kind": "join"' | sed 's/^/join events recorded: /' || true
echo "## root CLI on the installed host"
lab_ssh "$D" 'sudo playkeeper status; sudo playkeeper setup-code || true; playkeeper version'
