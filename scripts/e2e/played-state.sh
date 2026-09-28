#!/usr/bin/env bash
# The "played" state the click-through and the views check start from: an
# installed Playkeeper after the keyboard onboarding and the protocol bots'
# scenario, with its admin and a server that has players, sessions, backups
# and an audit log. CI's install-and-play job saves it at its end, to the
# Actions cache, and runners restore it over the build under test instead of
# repeating the onboarding and the bots (.github/actions/played-install).
#
#   install ASSETS             installs the build under test from ASSETS
#                              (playkeeper-linux-amd64.tar.gz and its .sha256)
#                              with the protocol bots' offline harness, and
#                              puts its setup code and certificate in
#                              /tmp/evidence
#   purge                      uninstalls it and deletes its data, to install
#                              again
#   save DIR PASSWORD COMMIT   the server stopped, then /etc/playkeeper/config.json
#                              and /var/lib/playkeeper in DIR/state.tar.gz, the
#                              admin's PASSWORD in DIR/password, the build in
#                              DIR/build and the COMMIT it was made on in
#                              DIR/commit; the services start again after
#   restore DIR                the saved state in place of the installed one,
#                              the services started, and the server started
#                              and online
#
# It changes the machine it runs on, so it only runs in CI.
set -euo pipefail

usage="usage: played-state.sh install ASSETS | purge | save DIR PASSWORD COMMIT | restore DIR"
cmd=${1:?$usage}
root=$(cd "$(dirname "$0")/../.." && pwd)
url=https://127.0.0.1:8443
fail() {
  echo "played state: $*" >&2
  exit 1
}
[ "${CI:-}" = true ] || fail "it changes this machine; run it only in CI (CI=true)"

client() { python3 "$root/test/e2e/pkclient.py" --url "$url" --cacert "$dir/cert.pem" --state "$dir/client.json" "$@"; }
certificate() { sudo install -m 0644 -o "$(id -u)" /var/lib/playkeeper/panel/tls/cert.pem "$1"; }

case $cmd in
  install)
    assets=${2:?$usage}
    mkdir -p /tmp/evidence /tmp/install
    (cd "$assets" && sha256sum -c playkeeper-linux-amd64.tar.gz.sha256)
    rm -rf /tmp/install/playkeeper-*-linux-amd64
    tar -xzf "$assets/playkeeper-linux-amd64.tar.gz" -C /tmp/install
    (cd /tmp/install/playkeeper-*-linux-amd64 && sudo ./install.sh --yes) | tee /tmp/evidence/install.txt
    certificate /tmp/evidence/cert.pem
    grep -o 'setup code: [a-z0-9-]*' /tmp/evidence/install.txt | awk '{print $3}' >/tmp/evidence/code
    sudo mkdir -p /etc/systemd/system/playkeeper-agent.service.d
    printf '[Service]\nEnvironment=PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1\n' | sudo tee /etc/systemd/system/playkeeper-agent.service.d/e2e-offline.conf >/dev/null
    sudo systemctl daemon-reload
    sudo systemctl restart playkeeper-agent
    ;;
  purge)
    sudo playkeeper uninstall --yes || true
    sudo docker ps -aq --filter label=io.playkeeper.managed=true | xargs -r sudo docker rm -f
    sudo rm -rf /var/lib/playkeeper /etc/playkeeper /tmp/evidence/code /tmp/evidence/cert.pem
    ;;
  save)
    dir=${2:?$usage} password=${3:?$usage} commit=${4:?$usage}
    mkdir -p "$dir"
    certificate "$dir/cert.pem"
    client login admin "$password" >/dev/null
    # Stopped, the world is saved and nothing writes to it while it's copied.
    client action stop --wait >/dev/null
    sudo systemctl stop playkeeper-panel playkeeper-agent
    sudo tar -C / -czf "$dir/state.tar.gz" etc/playkeeper/config.json var/lib/playkeeper
    printf '%s\n' "$password" >"$dir/password"
    playkeeper version >"$dir/build"
    printf '%s\n' "$commit" >"$dir/commit"
    rm -f "$dir/client.json" "$dir/cert.pem"
    sudo systemctl start playkeeper-agent playkeeper-panel
    echo "Saved the played state of $(cat "$dir/build") on $commit: $(du -h "$dir/state.tar.gz" | cut -f1)"
    ;;
  restore)
    dir=${2:?$usage}
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
    certificate "$dir/cert.pem"
    cp "$dir/cert.pem" /tmp/evidence/cert.pem
    curl -fsS --retry 30 --retry-delay 2 --retry-all-errors --cacert "$dir/cert.pem" "$url/healthz" >/dev/null ||
      fail "the panel did not come up on the saved state"
    client login admin "$(cat "$dir/password")" >/dev/null
    client action start --wait >/dev/null
    client wait-online --timeout 300 >/dev/null
    echo "Restored the played state of $(cat "$dir/build") under $(playkeeper version); the server is online."
    ;;
  *)
    fail "$usage"
    ;;
esac
