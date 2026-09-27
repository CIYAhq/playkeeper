#!/usr/bin/env bash
# Saves and restores the "played" state the click-through and the views check
# start from: an installed Playkeeper after the keyboard onboarding and the
# protocol bots' scenario, with its admin and a server that has players,
# sessions, backups and an audit log. CI saves it on main at the end of the
# install-and-play job, and restores it on a fresh runner over the build
# under test, instead of repeating the onboarding and the bots.
#
#   save DIR PASSWORD   the server stopped, then /etc/playkeeper/config.json
#                       and /var/lib/playkeeper in DIR/state.tar.gz, the
#                       admin's PASSWORD in DIR/password, and the build and
#                       commit that made it in DIR/build and DIR/commit; the
#                       services start again after
#   restore DIR         on a fresh runner where the build under test was just
#                       installed: the saved state in place of the new one,
#                       the services started, the server started and online,
#                       and the panel's certificate in /tmp/evidence/cert.pem
#
# It changes the machine it runs on, so it only runs in CI.
set -euo pipefail

usage="usage: played-state.sh save DIR PASSWORD | restore DIR"
cmd=${1:?$usage}
dir=${2:?$usage}
root=$(cd "$(dirname "$0")/../.." && pwd)
url=https://127.0.0.1:8443
fail() {
  echo "played state: $*" >&2
  exit 1
}
[ "${CI:-}" = true ] || fail "it changes this machine; run it only in CI (CI=true)"

client() { python3 "$root/test/e2e/pkclient.py" --url "$url" --cacert "$dir/cert.pem" --state "$dir/client.json" "$@"; }
healthy() { curl -fsS --retry 30 --retry-delay 2 --retry-all-errors --cacert "$dir/cert.pem" "$url/healthz" >/dev/null; }

case $cmd in
  save)
    password=${3:?$usage}
    mkdir -p "$dir"
    sudo install -m 0644 -o "$(id -u)" /var/lib/playkeeper/panel/tls/cert.pem "$dir/cert.pem"
    client login admin "$password" >/dev/null
    # Stopped, the world is saved and nothing writes to it while it's copied.
    client action stop --wait >/dev/null
    sudo systemctl stop playkeeper-panel playkeeper-agent
    sudo tar -C / -czf "$dir/state.tar.gz" etc/playkeeper/config.json var/lib/playkeeper
    printf '%s\n' "$password" >"$dir/password"
    playkeeper version >"$dir/build"
    printf '%s\n' "${GITHUB_SHA:?}" >"$dir/commit"
    rm -f "$dir/client.json" "$dir/cert.pem"
    sudo systemctl start playkeeper-agent playkeeper-panel
    echo "Saved the played state of $(cat "$dir/build"): $(du -h "$dir/state.tar.gz" | cut -f1)"
    ;;
  restore)
    if [ ! -s "$dir/state.tar.gz" ] || [ ! -s "$dir/password" ]; then fail "no saved state in $dir"; fi
    sudo systemctl stop playkeeper-panel playkeeper-agent
    sudo rm -rf /var/lib/playkeeper
    sudo tar -C / -xzpf "$dir/state.tar.gz"
    # The installer made the game's user on this runner, maybe with other ids
    # than on the runner that saved the state: files follow the user's name,
    # and the config gets this runner's ids.
    sudo python3 - "$(id -u playkeeper-mc)" "$(id -g playkeeper-mc)" <<'EOF'
import json, sys
path = "/etc/playkeeper/config.json"
config = json.load(open(path))
config["gameUID"], config["gameGID"] = int(sys.argv[1]), int(sys.argv[2])
with open(path, "w") as f:
    json.dump(config, f, indent=2)
    f.write("\n")
EOF
    sudo systemctl start playkeeper-agent playkeeper-panel
    sudo install -m 0644 -o "$(id -u)" /var/lib/playkeeper/panel/tls/cert.pem "$dir/cert.pem"
    mkdir -p /tmp/evidence
    cp "$dir/cert.pem" /tmp/evidence/cert.pem
    healthy || fail "the panel did not come up healthy on the saved state"
    client login admin "$(cat "$dir/password")" >/dev/null
    client action start --wait >/dev/null
    client wait-online --timeout 300 >/dev/null
    echo "Restored the played state of $(cat "$dir/build") under $(playkeeper version); the server is online."
    ;;
  *)
    fail "$usage"
    ;;
esac
