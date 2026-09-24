#!/usr/bin/env bash
# Installs Playkeeper through the one-line installer at URL, checks the
# installed version and that the panel answers over HTTPS with the
# certificate the installer made, then uninstalls and checks nothing is left
# listening. It changes the machine it runs on, so it only runs in CI.
# PLAYKEEPER_BASE_URL and PLAYKEEPER_ALLOW_HTTP are passed on to get.sh when
# set (for a local copy of the release assets).
# Usage: scripts/smoke-install.sh URL VERSION
set -euo pipefail

url=$1
version=$2
fail() {
  echo "smoke install failed: $*" >&2
  exit 1
}
[ "${CI:-}" = true ] || fail "it installs and removes Playkeeper on this machine; run it only in CI (CI=true)"

pass=()
for v in PLAYKEEPER_BASE_URL PLAYKEEPER_ALLOW_HTTP; do
  [ -z "${!v:-}" ] || pass+=("$v=${!v}")
done
echo "+ curl -fsSL $url | sudo ${pass[*]} sh -s -- --yes"
curl -fsSL "$url" | sudo env "${pass[@]}" sh -s -- --yes 2>&1 |
  sed -E 's/(setup code: |#code=)[a-z0-9-]+/\1<redacted>/g'

installed=$(playkeeper version)
[[ $installed == "playkeeper $version ("* ]] || fail "installed '$installed', expected version $version"
cert=$(mktemp)
sudo install -m 0644 -o "$(id -u)" /var/lib/playkeeper/panel/tls/cert.pem "$cert"
health=$(curl -fsS --retry 10 --retry-delay 2 --retry-all-errors --cacert "$cert" https://127.0.0.1:8443/healthz) ||
  fail "the panel does not answer on https://127.0.0.1:8443 with the installer's certificate"
[ "$health" = ok ] || fail "unexpected /healthz answer: $health"
echo "Installed $installed; the panel answers over HTTPS."

sudo playkeeper uninstall --yes
if systemctl list-unit-files | grep -q playkeeper; then fail "systemd units left behind"; fi
if sudo ss -ltnH | grep -qE ':(8443|25565) '; then fail "listeners left behind on 8443 or 25565"; fi
echo "Uninstalled; no Playkeeper services or listeners left."
