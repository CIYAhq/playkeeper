#!/usr/bin/env bash
# Full Playkeeper rehearsal in fresh KVM guests (Ubuntu cloud images):
#   host A: decline + injected-failure rollback, install from the release
#           tarball, keyboard-only browser onboarding, two protocol bots,
#           backup with a player online, same-host restore and rollback,
#           browser-only operation, exposure checks, error states, reboot,
#           uninstall/reinstall
#   host B: existing Minecraft service, Crafty-style directory, a Docker
#           container and ufw; low disk; collisions refused; coexistence;
#           blocked egress and recovery; purge
#   host C: one-line install (get.sh) from a local HTTP mirror, including a
#           bad-checksum refusal; browser restore of host A's archive;
#           damaged archives refused while a world is live
#   lowmem: 2 GB guest, preflight refuses; jammy: Ubuntu 22.04, preflight refuses
#   containers: preflight in containers without systemd (Ubuntu 24.04, Debian 12)
# Guests run one at a time. The archive moves between hosts only as a file.
# DNS queries on the lab bridge are recorded to show which services the guests
# contacted. Usage: scripts/e2e/vm-e2e.sh [path/to/playkeeper-*.tar.gz]
# Remote commands are single-quoted on purpose so they expand in the guest.
# shellcheck disable=SC2016
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=scripts/e2e/vm-lab.sh
. "$root/scripts/e2e/vm-lab.sh"
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH"

stamp=$(date -u +%Y%m%dT%H%M%SZ)
OUT=${OUT:-$root/test/e2e/out/vm-$stamp}
mkdir -p "$OUT"
exec > >(tee -a "$OUT/run.log") 2>&1
A=198.51.100.10
B=198.51.100.11
C=198.51.100.12
LOWMEM=198.51.100.14
JAMMY=198.51.100.15
LAB_HOST=$LAB_NET.1
SITE="http://$LAB_HOST:8765"
export PK_PASSWORD=${PK_PASSWORD:-"lab-$(head -c 9 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"}
export PK_ADMIN_PASSWORD="$PK_PASSWORD"
UI="$root/test/e2e/ui"
OFFLINE_DROPIN='[Service]
Environment=PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1'
pids=()

phase() { printf '\n==== %s  [%s]\n' "$*" "$(date -u +%H:%M:%S)"; }
cleanup() {
  local rc=$? p
  for p in "${pids[@]}"; do sudo kill "$p" 2>/dev/null || kill "$p" 2>/dev/null || true; done
  if [ "$rc" -ne 0 ] && [ "${KEEP_ON_FAIL:-0}" = 1 ]; then
    lab_log "failed (exit $rc); guests kept running for inspection"
    return
  fi
  for g in a b c lowmem jammy; do lab_shutdown "$g"; done
}
trap cleanup EXIT

tarball=${1:-}
if [ -z "$tarball" ]; then
  (cd "$root" && make package >/dev/null)
  tarball=$(find "$root/dist" -maxdepth 1 -name 'playkeeper-*-linux-amd64.tar.gz' -printf '%T@ %p\n' | sort -rn | head -1 | cut -d' ' -f2-)
fi
name=$(basename "$tarball" .tar.gz)
dist=$(dirname "$tarball")
phase "Artifact: $name"
sha256sum "$tarball" | tee "$OUT/artifact.sha256"
tar -tzvf "$tarball" | tee "$OUT/artifact-contents.txt"
if tar -tzf "$tarball" | grep -Ei '\.(jar|pem|key|env|db|sqlite)$|/\.git|node_modules'; then
  echo "unexpected file in the artifact" && exit 1
fi
echo "artifact contains no jars, keys, databases, .git or node_modules" | tee -a "$OUT/artifact-contents.txt"

for d in "$root/test/e2e/bot" "$UI"; do
  [ -d "$d/node_modules" ] || (cd "$d" && npm ci --no-audit --no-fund >/dev/null)
done
for tool in nmap tcpdump; do
  command -v "$tool" >/dev/null || sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$tool" >/dev/null
done
lab_image
lab_network
sudo sh -c "exec tcpdump -i $LAB_BRIDGE -n -l 'udp dst port 53' >'$OUT/dns-capture.raw' 2>/dev/null" &
pids+=($!)

# install_artifact IP — copies only the release tarball into the guest.
install_artifact() {
  local ip=$1
  lab_scp "$tarball" "$tarball.sha256" "pk@$ip:"
  lab_ssh "$ip" "sha256sum -c $name.tar.gz.sha256 && tar -xzf $name.tar.gz"
}

snapshot() { # IP LABEL — state that an install must not leave behind after rollback/decline
  lab_ssh "$1" 'set +e; echo "## packages"; dpkg-query -W -f "\${Package}\n" | sort; echo "## users"; getent passwd | cut -d: -f1 | sort; echo "## groups"; getent group | cut -d: -f1 | sort; echo "## units"; ls /etc/systemd/system; echo "## paths"; ls -d /etc/playkeeper /var/lib/playkeeper /usr/local/bin/playkeeper /var/lib/docker /var/lib/containerd /etc/docker 2>/dev/null; echo "## containers"; (sudo docker ps -a --format "{{.Names}}" 2>/dev/null || echo "no docker"); echo "## listeners"; sudo ss -ltnH | awk "{print \$4}" | sort' >"$OUT/$2.txt"
}

unchanged() { # IP BEFORE AFTER FILE — snapshot diff, appended to FILE
  snapshot "$1" "$3"
  if diff "$OUT/$2.txt" "$OUT/$3.txt" >"$OUT/$3.diff"; then
    echo "host unchanged (snapshot identical: packages, users, groups, units, paths, containers, listeners)" | tee -a "$OUT/$4"
  else
    echo "HOST CHANGED:" | tee -a "$OUT/$4"
    tee -a "$OUT/$4" <"$OUT/$3.diff"
    return 1
  fi
}

host_facts() { # IP FILE
  lab_ssh "$1" 'echo "os: $(. /etc/os-release; echo $PRETTY_NAME)"; echo "kernel: $(uname -r)"; echo "cpus: $(nproc)"; free -m | awk "/Mem:/ {print \"memory_mb: \" \$2}"; df -h / | awk "NR==2 {print \"disk: \" \$2 \" total, \" \$4 \" free\"}"; echo "init: $(ps -p 1 -o comm=) ($(systemctl is-system-running))"' | tee "$OUT/$2"
}

enable_offline_harness() {
  lab_ssh "$1" "sudo mkdir -p /etc/systemd/system/playkeeper-agent.service.d && printf '%s\n' '$OFFLINE_DROPIN' | sudo tee /etc/systemd/system/playkeeper-agent.service.d/e2e-offline.conf >/dev/null && sudo systemctl daemon-reload && sudo systemctl restart playkeeper-agent"
}

fetch_cert() { # IP DEST
  lab_ssh "$1" 'sudo cat /var/lib/playkeeper/panel/tls/cert.pem' >"$2"
}

pk() { # IP args... — one API session per host (sign-ins are rate limited)
  local ip=$1
  shift
  python3 "$root/test/e2e/pkclient.py" --url "https://$ip:8443" --cacert "$OUT/cert-$ip.pem" --state "$OUT/state-$ip.json" "$@"
}

wait_online() { # IP — signs in only when the saved session is no longer valid
  local ip=$1 i st
  for i in $(seq 1 180); do
    if curl -fsS --cacert "$OUT/cert-$ip.pem" "https://$ip:8443/healthz" >/dev/null 2>&1; then
      st=$(pk "$ip" call GET /api/server 2>/dev/null || true)
      if [ "$(head -1 <<<"$st")" = 401 ]; then
        pk "$ip" login admin "$PK_PASSWORD" >/dev/null 2>&1 || true
      elif grep -q '"phase": "online"' <<<"$st"; then
        return 0
      fi
    fi
    sleep 5
  done
  return 1
}

shoot() { # IP NAME routes...
  local ip=$1 n=$2 ui=$OUT/ui-${1##*.}
  shift 2
  (cd "$UI" && PK_URL="https://$ip:8443" PK_SHOTS="$OUT/screenshots" PK_OUT="$ui" node shoot.mjs "$n" "$@" >/dev/null) ||
    lab_log "screenshot $n on $ip failed"
}

players_online() { # IP COUNT — waits until the dashboard reports COUNT players
  local i
  for i in $(seq 1 40); do
    pk "$1" call GET /api/server | grep -q "\"online\": $2," && return 0
    sleep 2
  done
  return 1
}

world_sums() { # IP — checksums of the live world and backups
  lab_ssh "$1" 'sudo find /var/lib/playkeeper/server/data/world /var/lib/playkeeper/backups -type f -exec sha256sum {} + | sort -k2'
}

host_a() {
phase "HOST A: boot a fresh Ubuntu 24.04 guest"
lab_boot a 10 3072
host_facts "$A" host-a-facts.txt
install_artifact "$A"
snapshot "$A" host-a-snapshot-0-before

phase "HOST A: preflight (read-only)"
lab_ssh "$A" "cd $name && sudo ./playkeeper preflight" | tee "$OUT/host-a-preflight.txt" || true

phase "HOST A: declining the plan changes nothing"
lab_ssh "$A" "cd $name && echo n | sudo ./install.sh" | tee "$OUT/host-a-decline.txt" || true
unchanged "$A" host-a-snapshot-0-before host-a-snapshot-1-after-decline host-a-decline.txt

phase "HOST A: injected mid-install failure rolls back"
lab_ssh "$A" "cd $name && sudo PLAYKEEPER_TEST_FAIL_INSTALL_STEP='install and start systemd services' ./install.sh --yes" >"$OUT/host-a-rollback.txt" 2>&1 || true
cat "$OUT/host-a-rollback.txt"
unchanged "$A" host-a-snapshot-0-before host-a-snapshot-2-after-rollback host-a-rollback.txt

phase "HOST A: install from the release tarball"
start=$(date +%s)
lab_ssh "$A" "cd $name && sudo ./install.sh --yes" | tee "$OUT/host-a-install.txt"
echo "install wall time: $(($(date +%s) - start)) s" | tee -a "$OUT/host-a-install.txt"
lab_ssh "$A" 'sudo cat /var/lib/playkeeper/install-manifest.json' | tee "$OUT/host-a-install-manifest.json"
code=$(grep -o 'setup code: [a-z0-9-]*' "$OUT/host-a-install.txt" | awk '{print $3}')
fingerprint=$(grep -A1 'SHA-256 fingerprint' "$OUT/host-a-install.txt" | tail -1 | tr -d ' ')
fetch_cert "$A" "$OUT/cert-$A.pem"
got=$(openssl x509 -in "$OUT/cert-$A.pem" -noout -fingerprint -sha256 | cut -d= -f2)
[ "$got" = "$fingerprint" ] && echo "certificate fingerprint matches the installer output: $got" | tee "$OUT/host-a-tls.txt"
curl -sS -v --cacert "$OUT/cert-$A.pem" "https://$A:8443/healthz" 2>&1 | grep -E '^\* (SSL connection|Server certificate|subject|issuer)|^< HTTP|^ok' | tee -a "$OUT/host-a-tls.txt"
echo "plain HTTP on the panel port:" | tee -a "$OUT/host-a-tls.txt"
curl -sS -i "http://$A:8443/" 2>&1 | head -3 | tee -a "$OUT/host-a-tls.txt" || true
enable_offline_harness "$A"

phase "HOST A: fresh-install empty state, then keyboard-only onboarding in the browser"
PK_PASSWORD='' shoot "$A" empty-setup /setup
(cd "$UI" && PK_URL="https://$A:8443" PK_SETUP_CODE="$code" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui-10" npx playwright test onboarding.spec.ts --reporter=list) | tee "$OUT/host-a-onboarding.txt"
join=$(cat "$OUT/ui-10/join-address.txt")
echo "join address copied from the UI: $join" | tee -a "$OUT/host-a-onboarding.txt"
shoot "$A" empty-before-play /players /world

phase "HOST A: session cookie flags on a real sign-in"
curl -sS -D - -o /dev/null --cacert "$OUT/cert-$A.pem" -H "Origin: https://$A:8443" -H 'X-Requested-With: playkeeper' \
  -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$PK_PASSWORD\"}" "https://$A:8443/api/auth/login" |
  grep -iE '^(HTTP|set-cookie|strict-transport|content-security|x-frame|x-content-type|referrer)' | sed -E 's/(__Host-playkeeper=)[^;]+/\1<redacted>/' | tee "$OUT/host-a-cookie.txt"

phase "HOST A: protocol bots, sessions, console, backup with a player online, same-host restore, controls"
python3 "$root/test/e2e/scenario.py" host-a --existing --url "https://$A:8443" --cacert "$OUT/cert-$A.pem" --code "$code" \
  --game-host "$join" --out "$OUT/host-a" | tee "$OUT/host-a-scenario.txt"
lab_ssh "$A" 'sudo journalctl -u playkeeper-agent --no-pager -o short-iso | grep -E "world saved before stop"' | tee "$OUT/host-a-save-before-stop.txt"
echo "## containers managed by Playkeeper after repeated start/stop/restore (docker ps -a)" | tee "$OUT/host-a-containers.txt"
lab_ssh "$A" 'sudo docker ps -a --filter label=io.playkeeper.managed=true --format "{{.Names}}: {{.Status}}"' | tee -a "$OUT/host-a-containers.txt"

phase "HOST A: browser-only operation (console command, backup, download)"
(cd "$UI" && PK_URL="https://$A:8443" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui-10" npx playwright test operate.spec.ts --reporter=list) | tee "$OUT/host-a-operate.txt"
tee -a "$OUT/host-a-operate.txt" <"$OUT/ui-10/browser-download.txt"

phase "HOST A: screenshots of every view, accessibility and keyboard checks"
(cd "$UI" && PK_URL="https://$A:8443" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui-10" npx playwright test views.spec.ts --reporter=list) | tee "$OUT/host-a-views.txt"

phase "HOST A: exposure and privilege checks"
nmap -p- -sT -Pn -T4 "$A" | tee "$OUT/host-a-nmap.txt"
lab_ssh "$A" 'bash -s' <<'EOF' | tee "$OUT/host-a-privileges.txt"
set +e
echo "## listeners (ss -ltnup)"; sudo ss -ltnup
echo "## agent socket"; sudo ss -xlp | grep playkeeper; stat -c '%A %U:%G %n' /run/playkeeper/agent.sock
panel=$(pgrep -f 'playkeeper panel'); agent=$(pgrep -f 'playkeeper agent')
echo "## processes"; ps -o user=,pid=,cmd= -p "$panel","$agent"
echo "## panel user"; id playkeeper
echo "## docker.sock among panel fds: $(sudo ls -l /proc/$panel/fd | grep -c docker.sock)"
echo "## DOCKER_HOST in panel environment: $(sudo tr '\0' '\n' </proc/$panel/environ | grep -c DOCKER_HOST)"
echo "## panel user reaching the Docker socket:"; sudo -u playkeeper curl -sS --unix-socket /var/run/docker.sock http://d/version 2>&1 | head -1
echo "## unprivileged user reaching the agent socket:"; curl -sS --unix-socket /run/playkeeper/agent.sock http://a/v1/health 2>&1 | head -1
echo "## agent allowlist probes (as the panel user)"
sudo -u playkeeper curl -sS -o /dev/null -w "POST /v1/rm -> %{http_code}\n" -X POST --unix-socket /run/playkeeper/agent.sock http://a/v1/rm
sudo -u playkeeper curl -sS -w " <- extra argument\n" -X POST --unix-socket /run/playkeeper/agent.sock http://a/v1/server/start -d '{"actor":"probe","cmd":"id"}'
sudo -u playkeeper curl -sS -w " <- traversal id\n" --unix-socket /run/playkeeper/agent.sock 'http://a/v1/backups/..%2F..%2Fetc%2Fpasswd/download'
echo "## minecraft container"; sudo docker inspect playkeeper-minecraft --format 'Privileged={{.HostConfig.Privileged}} NetworkMode={{.HostConfig.NetworkMode}} PidMode={{.HostConfig.PidMode}} CapAdd={{.HostConfig.CapAdd}} CapDrop={{.HostConfig.CapDrop}} Memory={{.HostConfig.Memory}} MemorySwap={{.HostConfig.MemorySwap}} User={{.Config.User}} Ports={{json .HostConfig.PortBindings}} Mounts={{range .Mounts}}{{.Source}}->{{.Destination}}({{.RW}}) {{end}}'
sudo docker inspect playkeeper-minecraft --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E '^(TYPE|VERSION|CUSTOM_SERVER|SKIP_DOWNLOAD_DEFAULTS|MEMORY|ONLINE_MODE|ENABLE_WHITELIST|LOG_IPS)='
echo "## image"; sudo docker inspect playkeeper-minecraft --format '{{.Config.Image}}'
echo "## JVM heap flags"; sudo tr '\0' ' ' </proc/"$(pgrep -f 'paper-26.1.2' | head -1)"/cmdline | grep -oE -- '-Xm[sx][0-9]+[MG]' | sort -u
echo "## server.properties"; sudo grep -E '^(online-mode|white-list|enforce-whitelist|log-ips|enable-rcon|server-port)=' /var/lib/playkeeper/server/data/server.properties
echo "## Paper bStats telemetry"; sudo grep -E '^enabled:' /var/lib/playkeeper/server/data/plugins/bStats/config.yml
echo "## secret files"; sudo stat -c '%a %U %n' /var/lib/playkeeper/agent/rcon.secret /var/lib/playkeeper/panel/tls/key.pem /var/lib/playkeeper/panel/panel.db /var/lib/playkeeper/agent/agent.db
echo "## IP addresses in databases"
sudo python3 - <<'PY'
import re, sqlite3
for db in ("/var/lib/playkeeper/agent/agent.db", "/var/lib/playkeeper/panel/panel.db"):
    con = sqlite3.connect(db)
    dump = "\n".join(con.iterdump())
    ips = sorted(set(re.findall(r"\b(?:\d{1,3}\.){3}\d{1,3}\b", dump)))
    print(db, "IPv4-like strings:", ips or "none")
PY
echo "## RCON password in logs or databases"
pw=$(sudo cat /var/lib/playkeeper/agent/rcon.secret)
echo "journal: $(sudo journalctl -u playkeeper-agent -u playkeeper-panel --no-pager | grep -c "$pw")"
echo "container log: $(sudo docker logs playkeeper-minecraft 2>&1 | grep -c "$pw")"
echo "databases: $(sudo cat /var/lib/playkeeper/agent/agent.db* /var/lib/playkeeper/panel/panel.db* | grep -ac "$pw")"
echo "## established connections right now (the agent's RCON link to the container on the Docker bridge)"; sudo ss -tnp state established | grep -v ':22 ' | head -20
EOF
lab_ssh "$A" "sudo journalctl -u playkeeper-agent -u playkeeper-panel --no-pager | grep -c '$PK_PASSWORD' || true" | sed 's/^/admin password occurrences in journal: /' | tee -a "$OUT/host-a-privileges.txt"
pk "$A" call GET '/api/server/logs?limit=2000' | grep -c 'raw.githubusercontent.com' | sed 's/^/console lines mentioning raw.githubusercontent.com: /' | tee -a "$OUT/host-a-privileges.txt" || true
lab_ssh "$A" 'sudo sha256sum /var/lib/playkeeper/agent/rcon.secret /var/lib/playkeeper/panel/tls/key.pem' | awk '{print $1}' >"$OUT/host-a-secret-hashes"

phase "HOST A: overview numbers against ground truth"
wait_online "$A"
{
  pk "$A" call GET /api/server
  lab_ssh "$A" 'sudo docker stats --no-stream --format "docker stats: cpu={{.CPUPerc}} mem={{.MemUsage}}" playkeeper-minecraft; sudo docker inspect -f "StartedAt={{.State.StartedAt}}" playkeeper-minecraft; df -B1 /var/lib/playkeeper | tail -1; sudo docker logs playkeeper-minecraft 2>&1 | grep -m1 "Starting minecraft server version"'
  pk "$A" command list
} | tee "$OUT/host-a-ground-truth.txt"
shoot "$A" ground-truth-overview /

phase "HOST A: restarting the agent does not duplicate events or sessions"
counts() { pk "$A" call GET '/api/events?limit=1000' | grep -c '"kind"'; pk "$A" call GET '/api/players/sessions?range=24h' | grep -c '"player"'; }
before=$(counts | paste -sd' ')
lab_ssh "$A" 'sudo systemctl restart playkeeper-agent'
wait_online "$A"
sleep 20
after=$(counts | paste -sd' ')
echo "events and sessions before the agent restart: $before; after: $after" | tee "$OUT/host-a-agent-restart.txt"
[ "$before" = "$after" ] && echo "no duplicates after replaying the server log" | tee -a "$OUT/host-a-agent-restart.txt"

phase "HOST A: agent unavailable"
lab_ssh "$A" 'sudo systemctl stop playkeeper-agent'
pk "$A" call GET /api/server | tee "$OUT/host-a-agent-down.txt"
shoot "$A" state-agent-down / /console
lab_ssh "$A" 'sudo systemctl start playkeeper-agent'
wait_online "$A"

phase "HOST A: crash (kill -9 java) with a player online, and the recovery policy"
node "$root/test/e2e/bot/bot.js" visit --host "$join" --port 25565 --name PkBotFriend --stay 150 >"$OUT/host-a-crash-bot.log" 2>&1 &
botpid=$!
players_online "$A" 1
lab_ssh "$A" "sudo pkill -9 -f 'paper-26.1.2'" || true
sleep 4
pk "$A" call GET /api/server | tee "$OUT/host-a-crash.txt"
shoot "$A" state-crashed /
wait_online "$A"
wait "$botpid" || true
tee -a "$OUT/host-a-crash.txt" <"$OUT/host-a-crash-bot.log"
pk "$A" call GET '/api/events?limit=20' | tee -a "$OUT/host-a-crash.txt"
pk "$A" call GET '/api/players/sessions?range=1h' | python3 -c '
import json, sys
s = [x for x in json.loads(sys.stdin.read().split("\n", 1)[1])["sessions"] if x["player"] == "PkBotFriend"][0]
print("session of the player online during the crash:", {k: s.get(k) for k in ("start", "end", "endReason", "endUncertain")})
assert s["endReason"] == "server_crashed" and s["endUncertain"], "crash must end the session as uncertain"
print("marked as ended by a crash, end time uncertain; no leave event invented")' | tee -a "$OUT/host-a-crash.txt"

phase "HOST A: port collision gives an actionable error"
pk "$A" action stop --wait >/dev/null
lab_ssh "$A" 'nohup python3 -m http.server 25565 </dev/null >/dev/null 2>&1 & echo started-listener'
sleep 2
pk "$A" action start --wait | tee "$OUT/host-a-port-collision.txt" || true
shoot "$A" state-port-collision /
lab_ssh "$A" "pkill -f 'http[.]server 25565'" || true
pk "$A" action start --wait >/dev/null
wait_online "$A"

phase "HOST A: low disk space"
lab_ssh "$A" 'free=$(df -B1 --output=avail /var/lib/playkeeper | tail -1); sudo fallocate -l $((free - 300*1024*1024)) /var/tmp/pk-fill && df -h /var/lib/playkeeper | tail -1'
pk "$A" call POST /api/backups '{"note":"low disk test"}' | tee "$OUT/host-a-low-disk.txt"
sleep 3
pk "$A" call GET /api/server | grep -E '"lastOperation"|"error"|"hint"' | tee -a "$OUT/host-a-low-disk.txt"
shoot "$A" state-low-disk / /world
lab_ssh "$A" 'sudo rm -f /var/tmp/pk-fill'

phase "HOST A: server offline for 3 minutes (chart shows a gap, not zeros)"
pk "$A" action stop --wait >/dev/null
shoot "$A" state-offline / /console
sleep 180
pk "$A" action start --wait >/dev/null
wait_online "$A"

phase "HOST A: reboot; the desired state comes back and analytics show the outage"
lab_ssh "$A" 'sudo systemctl reboot' || true
sleep 20
lab_wait_ssh "$A"
wait_online "$A"
pk "$A" call GET /api/server | grep -E '"phase"|"desired"|"startedAt"' | tee "$OUT/host-a-reboot.txt"
pk "$A" call GET '/api/metrics?range=1h' >"$OUT/host-a-metrics-after-reboot.json"
pk "$A" call GET '/api/players/summary?days=1&tz=UTC' >"$OUT/host-a-summary-after-reboot.json"
python3 - "$OUT/host-a-metrics-after-reboot.json" "$OUT/host-a-summary-after-reboot.json" <<'PY' | tee -a "$OUT/host-a-reboot.txt"
import json, sys
m = json.loads(open(sys.argv[1]).read().split("\n", 1)[1])
states = [b["state"] for b in m["buckets"]]
print("1h buckets by state:", {s: states.count(s) for s in set(states)})
print("gaps:", [(g["kind"], g["from"][11:19], g["to"][11:19]) for g in m["gaps"]])
d = json.loads(open(sys.argv[2]).read().split("\n", 1)[1])["days"][-1]
print(f"today: {d['uniquePlayers']} players, {d['sessions']} sessions, {d['playtimeSeconds']} s observed playtime, data collected {d['coverage']:.1%}")
PY
shoot "$A" state-after-reboot / /players

phase "HOST A: uninstall keeps worlds and backups; reinstall picks them up"
world_sums "$A" >"$OUT/host-a-data-before-uninstall.txt"
start=$(date +%s)
lab_ssh "$A" 'sudo playkeeper uninstall --yes' | tee "$OUT/host-a-uninstall.txt"
echo "uninstall wall time: $(($(date +%s) - start)) s" | tee -a "$OUT/host-a-uninstall.txt"
world_sums "$A" >"$OUT/host-a-data-after-uninstall.txt"
diff "$OUT/host-a-data-before-uninstall.txt" "$OUT/host-a-data-after-uninstall.txt" && echo "world and backup checksums unchanged ($(wc -l <"$OUT/host-a-data-after-uninstall.txt") files)" | tee -a "$OUT/host-a-uninstall.txt"
lab_ssh "$A" 'set +e; echo "## after uninstall"; systemctl list-unit-files | grep -c playkeeper; id playkeeper; ls /usr/local/bin/playkeeper /etc/playkeeper; command -v docker; sudo ss -ltnH' 2>&1 | tee -a "$OUT/host-a-uninstall.txt"
lab_ssh "$A" "cd $name && sudo ./install.sh --yes" | tee "$OUT/host-a-reinstall.txt"
fetch_cert "$A" "$OUT/cert-$A.pem"
enable_offline_harness "$A"
wait_online "$A"
python3 - "$OUT/host-a/marker.json" "$A" "$OUT" "$PK_PASSWORD" <<'PY' | tee -a "$OUT/host-a-reinstall.txt"
import json, sys, time
sys.path.insert(0, sys.argv[3].rsplit("/test/e2e/out", 1)[0] + "/test/e2e")
from pkclient import Client
m = json.load(open(sys.argv[1]))
c = Client(f"https://{sys.argv[2]}:8443", f"{sys.argv[3]}/cert-{sys.argv[2]}.pem", f"{sys.argv[3]}/state-{sys.argv[2]}.json")
g = m["gold"]
c.ok("POST", "/api/server/command", {"command": f"forceload add {g[0]} {g[2]}"}); time.sleep(2)
print("after reinstall, marker check:", c.ok("POST", "/api/server/command", {"command": f"data get block {m['sign'][0]} {m['sign'][1]} {m['sign'][2]} front_text.messages"})["output"])
c.ok("POST", "/api/server/command", {"command": f"forceload remove {g[0]} {g[2]}"})
PY
lab_shutdown a
}

host_b() {
phase "HOST B: an existing Minecraft service, Crafty-style directory, Docker container and ufw firewall"
lab_boot b 11 3072
host_facts "$B" host-b-facts.txt
install_artifact "$B"
lab_ssh "$B" 'bash -s' <<'EOF' | tee "$OUT/host-b-fixtures.txt"
set -e
sudo mkdir -p /srv/minecraft/world /var/opt/minecraft/crafty/app/config
echo "their world" | sudo tee /srv/minecraft/world/level.dat >/dev/null
echo '{"crafty":true}' | sudo tee /var/opt/minecraft/crafty/app/config/config.json >/dev/null
printf '[Unit]\nDescription=Existing Minecraft server (fixture)\n[Service]\nWorkingDirectory=/srv/minecraft\nExecStart=/usr/bin/python3 -m http.server 25570\n[Install]\nWantedBy=multi-user.target\n' | sudo tee /etc/systemd/system/minecraft.service >/dev/null
sudo systemctl daemon-reload && sudo systemctl enable --now minecraft.service
sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io >/dev/null
sudo docker pull -q busybox@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e >/dev/null
sudo docker tag busybox@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e local/minecraft-fixture:1
sudo docker run -d --name existing-minecraft --restart unless-stopped -p 25580:8080 local/minecraft-fixture:1 sh -c 'echo ok >/tmp/index.html && exec httpd -f -p 8080 -h /tmp' >/dev/null
sudo ufw allow 22/tcp >/dev/null && sudo ufw --force enable >/dev/null
nohup python3 -m http.server 25565 </dev/null >/dev/null 2>&1 &
sleep 2
sudo sha256sum /etc/systemd/system/minecraft.service /srv/minecraft/world/level.dat /var/opt/minecraft/crafty/app/config/config.json | tee /tmp/fixtures.sha256
echo "existing container: $(sudo docker ps --filter name=existing-minecraft --format '{{.Names}} {{.Image}} {{.Status}}')"
sudo ufw status | sed 's/^/ufw: /'
EOF

phase "HOST B: low disk space is refused with a fix (read-only)"
lab_ssh "$B" 'free=$(df -B1 --output=avail /var/lib | tail -1); sudo fallocate -l $((free - 2500*1024*1024)) /var/tmp/pk-fill && df -h /var/lib | tail -1'
lab_ssh "$B" "cd $name && sudo ./playkeeper preflight" | tee "$OUT/host-b-preflight-low-disk.txt" || true
lab_ssh "$B" 'sudo rm -f /var/tmp/pk-fill'

phase "HOST B: collisions refused; coexistence with --allow-existing-minecraft"
lab_ssh "$B" "cd $name && sudo ./playkeeper preflight" | tee "$OUT/host-b-preflight-conflicts.txt" || true
lab_ssh "$B" "cd $name && sudo ./install.sh --yes" | tee "$OUT/host-b-install-refused.txt" || true
lab_ssh "$B" "pkill -f 'http[.]server 25565'; cd $name && sudo ./install.sh --yes --allow-existing-minecraft" | tee "$OUT/host-b-install-coexist.txt"
code_b=$(grep -o 'setup code: [a-z0-9-]*' "$OUT/host-b-install-coexist.txt" | awk '{print $3}')
lab_ssh "$B" 'sudo ufw status' | tee "$OUT/host-b-ufw.txt"
fetch_cert "$B" "$OUT/cert-$B.pem"
curl -fsS --cacert "$OUT/cert-$B.pem" "https://$B:8443/healthz" >/dev/null && echo "panel reachable from another host through ufw" | tee -a "$OUT/host-b-ufw.txt"

phase "HOST B: blocked outbound HTTPS gives actionable errors; after unblocking, Start recovers"
pk "$B" setup "$code_b" admin "$PK_PASSWORD" >/dev/null
lab_ssh "$B" 'for t in iptables ip6tables; do sudo $t -I OUTPUT 1 -p tcp -m multiport --dports 80,443 -m conntrack --ctstate NEW -j REJECT --reject-with tcp-reset; done'
pk "$B" call GET /api/preflight | python3 -c '
import json, sys
for c in json.loads(sys.stdin.read().split("\n", 1)[1])["checks"]:
    print(f"[{c[\"status\"]}] {c[\"label\"]}: {c[\"detail\"]}" + (f" -> Fix: {c[\"fix\"]}" if c.get("fix") else ""))' | tee "$OUT/host-b-blocked-egress.txt"
shoot "$B" state-blocked-egress-check /
pk "$B" create --version paper-26.1.2 | grep -E '"status"|"error"|"hint"' | tee -a "$OUT/host-b-blocked-egress.txt" || true
shoot "$B" state-blocked-egress-failed /
lab_ssh "$B" 'for t in iptables ip6tables; do sudo $t -D OUTPUT -p tcp -m multiport --dports 80,443 -m conntrack --ctstate NEW -j REJECT --reject-with tcp-reset; done'
echo "## outbound HTTPS allowed again; pressing Start as the hint says" | tee -a "$OUT/host-b-blocked-egress.txt"
pk "$B" action start --wait | grep -E '"kind"|"status"|"error"' | tee -a "$OUT/host-b-blocked-egress.txt"
wait_online "$B" && echo "server online after the fix" | tee -a "$OUT/host-b-blocked-egress.txt"

phase "HOST B: purge needs the typed phrase; existing services, container and firewall rules survive"
lab_ssh "$B" "echo 'keep my worlds' | sudo playkeeper uninstall --yes --purge" 2>&1 | tail -2 | tee "$OUT/host-b-purge.txt" || true
lab_ssh "$B" 'echo "after the refused purge: $(systemctl is-active playkeeper-agent playkeeper-panel | paste -sd" ") ; data dir present: $(sudo test -d /var/lib/playkeeper/server/data && echo yes)"' | tee -a "$OUT/host-b-purge.txt"
lab_ssh "$B" "echo 'delete my worlds' | sudo playkeeper uninstall --yes --purge" 2>&1 | tee -a "$OUT/host-b-purge.txt"
lab_ssh "$B" 'bash -s' <<'EOF' | tee -a "$OUT/host-b-fixtures.txt"
set +e
echo "## after install, blocked-egress test and purge"
sudo sha256sum -c /tmp/fixtures.sha256
echo "minecraft.service: $(systemctl is-active minecraft.service)"
curl -s -o /dev/null -w "fixture still serving on 25570: %{http_code}\n" http://127.0.0.1:25570/
echo "existing container: $(sudo docker ps --filter name=existing-minecraft --format '{{.Names}} {{.Image}} {{.Status}}')"
curl -s -o /dev/null -w "existing container still serving on 25580: %{http_code}\n" http://127.0.0.1:25580/
echo "docker still installed: $(command -v docker)"
echo "/var/lib/playkeeper after purge: $(sudo test -e /var/lib/playkeeper && echo present || echo removed)"
sudo ufw status | sed 's/^/ufw: /'
EOF
lab_shutdown b
}

host_c() {
phase "HOST C: one-line install (get.sh) from a local HTTP mirror of the release assets"
site="$OUT/site"
mkdir -p "$site/bad"
cmp "$tarball" "$dist/playkeeper-linux-amd64.tar.gz"
cp "$dist/get.sh" "$dist/playkeeper-linux-amd64.tar.gz" "$dist/playkeeper-linux-amd64.tar.gz.sha256" "$site/"
cp "$tarball" "$site/bad/playkeeper-linux-amd64.tar.gz"
printf '%064d  playkeeper-linux-amd64.tar.gz\n' 0 >"$site/bad/playkeeper-linux-amd64.tar.gz.sha256"
{
  echo "mirror at $SITE serves:"
  (cd "$site" && sha256sum get.sh playkeeper-linux-amd64.tar.gz && cat playkeeper-linux-amd64.tar.gz.sha256)
  echo "the served tarball is byte-identical to the tested artifact $name.tar.gz"
  echo "$SITE/bad serves the same tarball with a wrong .sha256"
} | tee "$OUT/host-c-oneliner-mirror.txt"
python3 -m http.server 8765 --bind "$LAB_HOST" --directory "$site" >"$OUT/site-server.log" 2>&1 &
pids+=($!)
sleep 1
lab_boot c 12 3072
host_facts "$C" host-c-facts.txt
snapshot "$C" host-c-snapshot-0-before
refused() { # FILE COMMAND — runs COMMAND in guest C, which must fail
  local st=0
  echo "\$ $2" | tee -a "$OUT/$1"
  lab_ssh "$C" "$2" >"$OUT/refused.out" 2>&1 || st=$?
  tee -a "$OUT/$1" <"$OUT/refused.out"
  echo "exit status: $st" | tee -a "$OUT/$1"
  [ "$st" != 0 ]
}
refused host-c-oneliner-refusals.txt "curl -fsSL $SITE/get.sh | sudo PLAYKEEPER_BASE_URL=$SITE/bad PLAYKEEPER_ALLOW_HTTP=1 sh -s -- --yes"
refused host-c-oneliner-refusals.txt "curl -fsSL $SITE/get.sh | sudo PLAYKEEPER_BASE_URL=$SITE PLAYKEEPER_ALLOW_HTTP=1 sh"
unchanged "$C" host-c-snapshot-0-before host-c-snapshot-1-after-refusals host-c-oneliner-refusals.txt
oneliner="curl -fsSL $SITE/get.sh | sudo PLAYKEEPER_BASE_URL=$SITE PLAYKEEPER_ALLOW_HTTP=1 sh"
echo "\$ $oneliner   # in a terminal; the installer's question is answered with y" | tee "$OUT/host-c-install.txt"
start=$(date +%s)
python3 "$root/test/e2e/tty_run.py" --answer 'Proceed? [y/N]=y' -- ssh -tt -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -i "$LAB_KEY" "pk@$C" "$oneliner" | tee -a "$OUT/host-c-install.txt"
echo "one-line install wall time (download, checksum, install): $(($(date +%s) - start)) s" | tee -a "$OUT/host-c-install.txt"
code_c=$(grep -o 'setup code: [a-z0-9-]*' "$OUT/host-c-install.txt" | awk '{print $3}')
fetch_cert "$C" "$OUT/cert-$C.pem"
enable_offline_harness "$C"
lab_ssh "$C" 'sudo sha256sum /var/lib/playkeeper/agent/rcon.secret /var/lib/playkeeper/panel/tls/key.pem' | awk '{print $1}' >"$OUT/host-c-secret-hashes"
if grep -qxFf "$OUT/host-a-secret-hashes" "$OUT/host-c-secret-hashes"; then
  echo "SECRETS REPEATED: host C shares an RCON password or TLS key with host A" | tee "$OUT/host-c-secrets.txt"
  exit 1
fi
echo "host C's RCON password and TLS key differ from host A's (compared by SHA-256; values not recorded)" | tee "$OUT/host-c-secrets.txt"

phase "HOST C: restore host A's archive in the browser"
archive="$OUT/host-a/world-backup.tar.gz"
sha=$(python3 -c "import json; print(json.load(open('$OUT/host-a/host-a-results.json'))['backup']['sha256'])")
scen() { python3 "$root/test/e2e/scenario.py" host-b --url "https://$C:8443" --cacert "$OUT/cert-$C.pem" --code "$code_c" \
  --game-host "$C" --archive "$archive" --marker "$OUT/host-a/marker.json" --expect-sha256 "$sha" --out "$OUT/host-c" "$@"; }
scen --steps setup,refusals | tee "$OUT/host-c-refusals.txt"
(cd "$UI" && PK_URL="https://$C:8443" PK_ARCHIVE="$archive" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui-12" npx playwright test restore.spec.ts --reporter=list) | tee "$OUT/host-c-restore-ui.txt"
scen --steps verify | tee "$OUT/host-c-verify.txt"

phase "HOST C: damaged archives are refused and the live world stays byte-identical"
pk "$C" login admin "$PK_PASSWORD" >/dev/null
pk "$C" action stop --wait >/dev/null
world_sums "$C" >"$OUT/host-c-world-before-tamper.sums"
scen --steps tamper | tee "$OUT/host-c-tamper.txt"
world_sums "$C" >"$OUT/host-c-world-after-tamper.sums"
diff "$OUT/host-c-world-before-tamper.sums" "$OUT/host-c-world-after-tamper.sums" &&
  echo "live world and backups unchanged after the refusals ($(wc -l <"$OUT/host-c-world-after-tamper.sums") files compared by SHA-256)" | tee -a "$OUT/host-c-tamper.txt"
pk "$C" action start --wait >/dev/null
wait_online "$C"
pk "$C" call GET /api/audit >"$OUT/host-c-audit.json"
shoot "$C" host-c-after-restore / /world
lab_shutdown c
}

host_lowmem() {
phase "HOST LOWMEM: a 2 GB guest; preflight and the installer refuse and change nothing"
lab_boot lowmem 14 2048
host_facts "$LOWMEM" host-lowmem-facts.txt
install_artifact "$LOWMEM"
snapshot "$LOWMEM" host-lowmem-snapshot-0-before
lab_ssh "$LOWMEM" "cd $name && sudo ./playkeeper preflight" | tee "$OUT/host-lowmem-preflight.txt" || true
lab_ssh "$LOWMEM" "cd $name && sudo ./install.sh --yes" 2>&1 | tail -4 | tee -a "$OUT/host-lowmem-preflight.txt" || true
unchanged "$LOWMEM" host-lowmem-snapshot-0-before host-lowmem-snapshot-1-after host-lowmem-preflight.txt
lab_shutdown lowmem
}

host_jammy() {
phase "HOST JAMMY: Ubuntu 22.04; preflight and the installer refuse an untested OS and change nothing"
lab_image_named jammy "$LAB_JAMMY_URL"
LAB_BASE_IMAGE="$LAB_DIR/jammy.img" lab_boot jammy 15 3072
host_facts "$JAMMY" host-jammy-facts.txt
install_artifact "$JAMMY"
snapshot "$JAMMY" host-jammy-snapshot-0-before
lab_ssh "$JAMMY" "cd $name && sudo ./playkeeper preflight" | tee "$OUT/host-jammy-preflight.txt" || true
lab_ssh "$JAMMY" "cd $name && sudo ./install.sh --yes" 2>&1 | tail -4 | tee -a "$OUT/host-jammy-preflight.txt" || true
unchanged "$JAMMY" host-jammy-snapshot-0-before host-jammy-snapshot-1-after host-jammy-preflight.txt
lab_shutdown jammy
}

host_containers() {
phase "CONTAINERS: preflight where systemd is not running (Ubuntu 24.04 and Debian 12 containers)"
for image in ubuntu:24.04 debian:12; do
  echo "## $image container (root, no systemd)" | tee -a "$OUT/containers-preflight.txt"
  sudo docker run --rm -v "$tarball:/pk.tgz:ro" "$image" sh -c 'tar -xzf /pk.tgz -C /tmp && /tmp/playkeeper-*/playkeeper preflight' 2>&1 | tee -a "$OUT/containers-preflight.txt" || true
done
}

dns_summary() {
phase "DNS: names the guests looked up during the run"
python3 - "$OUT/dns-capture.raw" <<'PY' | tee "$OUT/dns-queries.txt"
import collections, re, sys
hosts = {"10": "A", "11": "B", "12": "C", "14": "lowmem", "15": "jammy"}
seen = collections.defaultdict(collections.Counter)
for line in open(sys.argv[1], errors="replace"):
    src = re.search(r"IP 198\.51\.100\.(\d+)\.\d+ > ", line)
    q = re.search(r" (?:A|AAAA)\? (\S+?)\.? \(", line)
    if src and q:
        seen[hosts.get(src.group(1), src.group(1))][q.group(1).lower()] += 1
print("Queries (A/AAAA) captured on the lab bridge; counts are lookups, not connections.")
for h in sorted(seen):
    print(f"\nhost {h}:")
    for name, n in sorted(seen[h].items()):
        print(f"  {name}  ({n})")
PY
}

# HOSTS selects which guests to run; host C reuses host A's archive, marker
# and secret hashes from $OUT, so "HOSTS='b c' OUT=<earlier run>" resumes a run.
for h in ${HOSTS:-a b c lowmem jammy containers}; do
  "host_$h"
done
sudo kill "${pids[0]}" 2>/dev/null || true
sleep 1
dns_summary
phase "DONE: evidence in $OUT"
