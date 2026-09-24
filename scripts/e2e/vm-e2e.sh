#!/usr/bin/env bash
# Full Playkeeper rehearsal in fresh KVM guests (Ubuntu 24.04 cloud image):
#   host A: decline + injected-failure rollback, install from the release
#           tarball, keyboard-only browser onboarding, two protocol bots,
#           exposure checks, error states, reboot, backup, uninstall/reinstall
#   host B: collision fixtures (port listener, minecraft.service, Crafty dir)
#   host C: clean second host, browser restore of host A's archive, marker check
# Guests run one at a time. The archive moves between hosts only as a file.
# Usage: scripts/e2e/vm-e2e.sh [path/to/playkeeper-*.tar.gz]
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
export PK_PASSWORD=${PK_PASSWORD:-"lab-$(head -c 9 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"}
export PK_ADMIN_PASSWORD="$PK_PASSWORD"
UI="$root/test/e2e/ui"
OFFLINE_DROPIN='[Service]
Environment=PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1'

phase() { printf '\n==== %s  [%s]\n' "$*" "$(date -u +%H:%M:%S)"; }
cleanup() {
  local rc=$?
  if [ "$rc" -ne 0 ] && [ "${KEEP_ON_FAIL:-0}" = 1 ]; then
    lab_log "failed (exit $rc); guests kept running for inspection"
    return
  fi
  lab_shutdown a
  lab_shutdown b
  lab_shutdown c
}
trap cleanup EXIT

tarball=${1:-}
if [ -z "$tarball" ]; then
  (cd "$root" && make package >/dev/null)
  tarball=$(find "$root/dist" -maxdepth 1 -name 'playkeeper-*-linux-amd64.tar.gz' -printf '%T@ %p\n' | sort -rn | head -1 | cut -d' ' -f2-)
fi
name=$(basename "$tarball" .tar.gz)
phase "Artifact: $name"
sha256sum "$tarball" | tee "$OUT/artifact.sha256"

for d in "$root/test/e2e/bot" "$UI"; do
  [ -d "$d/node_modules" ] || (cd "$d" && npm ci --no-audit --no-fund >/dev/null)
done
command -v nmap >/dev/null || sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nmap >/dev/null
lab_image
lab_network

# install_artifact IP — copies only the release tarball into the guest.
install_artifact() {
  local ip=$1
  lab_scp "$tarball" "$tarball.sha256" "pk@$ip:"
  lab_ssh "$ip" "sha256sum -c $name.tar.gz.sha256 && tar -xzf $name.tar.gz"
}

snapshot() { # IP LABEL — state that an install must not leave behind after rollback/decline
  lab_ssh "$1" 'set +e; echo "## packages"; dpkg-query -W -f "\${Package}\n" | sort; echo "## users"; getent passwd | cut -d: -f1 | sort; echo "## groups"; getent group | cut -d: -f1 | sort; echo "## units"; ls /etc/systemd/system; echo "## paths"; ls -d /etc/playkeeper /var/lib/playkeeper /usr/local/bin/playkeeper /var/lib/docker /var/lib/containerd /etc/docker 2>/dev/null; echo "## containers"; (sudo docker ps -a --format "{{.Names}}" 2>/dev/null || echo "no docker"); echo "## listeners"; sudo ss -ltnH | awk "{print \$4}" | sort' >"$OUT/$2.txt"
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

pk() { # IP STATE args...
  local ip=$1 state=$2
  shift 2
  python3 "$root/test/e2e/pkclient.py" --url "https://$ip:8443" --cacert "$OUT/cert-$ip.pem" --state "$state" "$@"
}

wait_online() { # IP
  local ip=$1 i
  for i in $(seq 1 180); do
    if curl -fsS --cacert "$OUT/cert-$ip.pem" "https://$ip:8443/healthz" >/dev/null 2>&1 &&
      pk "$ip" "$OUT/state-probe-$ip.json" login admin "$PK_PASSWORD" >/dev/null 2>&1 &&
      pk "$ip" "$OUT/state-probe-$ip.json" call GET /api/server | grep -q '"phase": "online"'; then
      return 0
    fi
    sleep 5
  done
  return 1
}

shoot() { # IP NAME routes...
  local ip=$1 n=$2
  shift 2
  (cd "$UI" && PK_URL="https://$ip:8443" PK_SHOTS="$OUT/screenshots" node shoot.mjs "$n" "$@" >/dev/null)
}

########################################################################
phase "HOST A: boot a fresh Ubuntu 24.04 guest"
lab_boot a 10 3072
host_facts "$A" host-a-facts.txt
install_artifact "$A"
snapshot "$A" host-a-snapshot-0-before

phase "HOST A: preflight (read-only)"
lab_ssh "$A" "cd $name && sudo ./playkeeper preflight" | tee "$OUT/host-a-preflight.txt" || true

phase "HOST A: declining the plan changes nothing"
lab_ssh "$A" "cd $name && echo n | sudo ./install.sh" | tee "$OUT/host-a-decline.txt" || true
snapshot "$A" host-a-snapshot-1-after-decline
diff "$OUT/host-a-snapshot-0-before.txt" "$OUT/host-a-snapshot-1-after-decline.txt" && echo "decline: no changes" | tee -a "$OUT/host-a-decline.txt"

phase "HOST A: injected mid-install failure rolls back"
lab_ssh "$A" "cd $name && sudo PLAYKEEPER_TEST_FAIL_INSTALL_STEP='install and start systemd services' ./install.sh --yes" >"$OUT/host-a-rollback.txt" 2>&1 || true
cat "$OUT/host-a-rollback.txt"
snapshot "$A" host-a-snapshot-2-after-rollback
if diff "$OUT/host-a-snapshot-0-before.txt" "$OUT/host-a-snapshot-2-after-rollback.txt" >"$OUT/host-a-rollback.diff"; then
  echo "rollback: snapshot identical to before" | tee -a "$OUT/host-a-rollback.txt"
else
  echo "rollback: DIFFERENCES (see host-a-rollback.diff)" | tee -a "$OUT/host-a-rollback.txt"
  cat "$OUT/host-a-rollback.diff"
fi

phase "HOST A: install from the release tarball"
start=$(date +%s)
lab_ssh "$A" "cd $name && sudo ./install.sh --yes" | tee "$OUT/host-a-install.txt"
echo "install wall time: $(($(date +%s) - start)) s" | tee -a "$OUT/host-a-install.txt"
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
(cd "$UI" && PK_URL="https://$A:8443" PK_SETUP_CODE="$code" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui-a" npx playwright test onboarding.spec.ts --reporter=list) | tee "$OUT/host-a-onboarding.txt"
join=$(cat "$OUT/ui-a/join-address.txt")
echo "join address copied from the UI: $join" | tee -a "$OUT/host-a-onboarding.txt"
PK_PASSWORD="$PK_PASSWORD" shoot "$A" empty-before-play /players /world

phase "HOST A: protocol bots, sessions, console, backup and download"
python3 "$root/test/e2e/scenario.py" host-a --existing --url "https://$A:8443" --cacert "$OUT/cert-$A.pem" --code "$code" \
  --game-host "$join" --out "$OUT/host-a" | tee "$OUT/host-a-scenario.txt"

phase "HOST A: screenshots of every view, accessibility and keyboard checks"
(cd "$UI" && PK_URL="https://$A:8443" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui-a" npx playwright test views.spec.ts --reporter=list) | tee "$OUT/host-a-views.txt"

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
sudo docker inspect playkeeper-minecraft --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E '^(MEMORY|ONLINE_MODE|ENABLE_WHITELIST|LOG_IPS|VERSION|PAPER_BUILD)='
echo "## JVM heap flags"; sudo tr '\0' ' ' </proc/"$(pgrep -f 'paper-26.1.2' | head -1)"/cmdline | grep -oE -- '-Xm[sx][0-9]+[MG]' | sort -u
echo "## server.properties"; sudo grep -E '^(online-mode|white-list|enforce-whitelist|log-ips|enable-rcon|server-port)=' /var/lib/playkeeper/server/data/server.properties
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
echo "## outbound connections right now"; sudo ss -tnp state established | grep -v ':22 ' | head -20
EOF
lab_ssh "$A" "sudo journalctl -u playkeeper-agent -u playkeeper-panel --no-pager | grep -c '$PK_PASSWORD' || true" | sed 's/^/admin password occurrences in journal: /' | tee -a "$OUT/host-a-privileges.txt"

phase "HOST A: overview numbers against ground truth"
pk "$A" "$OUT/state-gt.json" login admin "$PK_PASSWORD" >/dev/null
{
  pk "$A" "$OUT/state-gt.json" call GET /api/server
  lab_ssh "$A" 'sudo docker stats --no-stream --format "docker stats: cpu={{.CPUPerc}} mem={{.MemUsage}}" playkeeper-minecraft; sudo docker inspect -f "StartedAt={{.State.StartedAt}}" playkeeper-minecraft; df -B1 /var/lib/playkeeper | tail -1; sudo docker logs playkeeper-minecraft 2>&1 | grep -m1 "Starting minecraft server version"'
  pk "$A" "$OUT/state-gt.json" command list
} | tee "$OUT/host-a-ground-truth.txt"
PK_PASSWORD="$PK_PASSWORD" shoot "$A" ground-truth-overview /

phase "HOST A: agent unavailable"
lab_ssh "$A" 'sudo systemctl stop playkeeper-agent'
pk "$A" "$OUT/state-gt.json" call GET /api/server | tee "$OUT/host-a-agent-down.txt"
PK_PASSWORD="$PK_PASSWORD" shoot "$A" state-agent-down / /console
lab_ssh "$A" 'sudo systemctl start playkeeper-agent'
wait_online "$A"

phase "HOST A: crash (kill -9 java) and recovery policy"
lab_ssh "$A" "sudo pkill -9 -f 'paper-26.1.2'" || true
sleep 4
pk "$A" "$OUT/state-gt.json" call GET /api/server | tee "$OUT/host-a-crash.txt"
PK_PASSWORD="$PK_PASSWORD" PK_WAIT=500 shoot "$A" state-crashed /
wait_online "$A"
pk "$A" "$OUT/state-gt.json" call GET '/api/events?limit=20' | tee -a "$OUT/host-a-crash.txt"

phase "HOST A: port collision gives an actionable error"
pk "$A" "$OUT/state-gt.json" action stop --wait >/dev/null
lab_ssh "$A" 'nohup python3 -m http.server 25565 </dev/null >/dev/null 2>&1 & echo started-listener'
sleep 2
pk "$A" "$OUT/state-gt.json" action start --wait | tee "$OUT/host-a-port-collision.txt" || true
PK_PASSWORD="$PK_PASSWORD" shoot "$A" state-port-collision /
lab_ssh "$A" "pkill -f 'http.server 25565'" || true
pk "$A" "$OUT/state-gt.json" action start --wait >/dev/null
wait_online "$A"

phase "HOST A: low disk space"
lab_ssh "$A" 'free=$(df -B1 --output=avail /var/lib/playkeeper | tail -1); sudo fallocate -l $((free - 300*1024*1024)) /var/tmp/pk-fill && df -h /var/lib/playkeeper | tail -1'
pk "$A" "$OUT/state-gt.json" call POST /api/backups '{"note":"low disk test"}' | tee "$OUT/host-a-low-disk.txt"
sleep 3
pk "$A" "$OUT/state-gt.json" call GET /api/server | grep -E '"lastOperation"|"error"|"hint"' | tee -a "$OUT/host-a-low-disk.txt"
PK_PASSWORD="$PK_PASSWORD" shoot "$A" state-low-disk / /world
lab_ssh "$A" 'sudo rm -f /var/tmp/pk-fill'

phase "HOST A: server offline for 3 minutes (chart shows a gap, not zeros)"
pk "$A" "$OUT/state-gt.json" action stop --wait >/dev/null
sleep 180
pk "$A" "$OUT/state-gt.json" action start --wait >/dev/null
wait_online "$A"

phase "HOST A: reboot; the desired state comes back and analytics show the outage"
lab_ssh "$A" 'sudo systemctl reboot' || true
sleep 20
lab_wait_ssh "$A"
wait_online "$A"
pk "$A" "$OUT/state-gt.json" login admin "$PK_PASSWORD" >/dev/null
pk "$A" "$OUT/state-gt.json" call GET /api/server | grep -E '"phase"|"desired"|"startedAt"' | tee "$OUT/host-a-reboot.txt"
pk "$A" "$OUT/state-gt.json" call GET '/api/metrics?range=1h' >"$OUT/host-a-metrics-after-reboot.json"
python3 - "$OUT/host-a-metrics-after-reboot.json" <<'PY' | tee -a "$OUT/host-a-reboot.txt"
import json, sys
m = json.loads(open(sys.argv[1]).read().split("\n", 1)[1])
states = [b["state"] for b in m["buckets"]]
print("1h buckets by state:", {s: states.count(s) for s in set(states)})
print("gaps:", [(g["kind"], g["from"][11:19], g["to"][11:19]) for g in m["gaps"]])
PY
PK_PASSWORD="$PK_PASSWORD" shoot "$A" state-after-reboot / /players

phase "HOST A: uninstall keeps worlds and backups; reinstall picks them up"
lab_ssh "$A" 'sudo find /var/lib/playkeeper/server/data/world /var/lib/playkeeper/backups -type f -exec sha256sum {} + | sort -k2' >"$OUT/host-a-data-before-uninstall.txt"
start=$(date +%s)
lab_ssh "$A" 'sudo playkeeper uninstall --yes' | tee "$OUT/host-a-uninstall.txt"
echo "uninstall wall time: $(($(date +%s) - start)) s" | tee -a "$OUT/host-a-uninstall.txt"
lab_ssh "$A" 'sudo find /var/lib/playkeeper/server/data/world /var/lib/playkeeper/backups -type f -exec sha256sum {} + | sort -k2' >"$OUT/host-a-data-after-uninstall.txt"
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
c = Client(f"https://{sys.argv[2]}:8443", f"{sys.argv[3]}/cert-{sys.argv[2]}.pem", None)
c.login("admin", sys.argv[4])
g = m["gold"]
c.ok("POST", "/api/server/command", {"command": f"forceload add {g[0]} {g[2]}"}); time.sleep(2)
print("after reinstall, marker check:", c.ok("POST", "/api/server/command", {"command": f"data get block {m['sign'][0]} {m['sign'][1]} {m['sign'][2]} front_text.messages"})["output"])
c.ok("POST", "/api/server/command", {"command": f"forceload remove {g[0]} {g[2]}"})
PY
lab_shutdown a

########################################################################
phase "HOST B: collision fixtures refuse, coexistence leaves them untouched"
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
nohup python3 -m http.server 25565 </dev/null >/dev/null 2>&1 &
sleep 1
sudo sha256sum /etc/systemd/system/minecraft.service /srv/minecraft/world/level.dat /var/opt/minecraft/crafty/app/config/config.json | tee /tmp/fixtures.sha256
EOF
lab_ssh "$B" "cd $name && sudo ./playkeeper preflight" | tee "$OUT/host-b-preflight-conflicts.txt" || true
lab_ssh "$B" "cd $name && sudo ./install.sh --yes" | tee "$OUT/host-b-install-refused.txt" || true
lab_ssh "$B" "pkill -f 'http.server 25565'; cd $name && sudo ./install.sh --yes --allow-existing-minecraft" | tee "$OUT/host-b-install-coexist.txt"
lab_ssh "$B" 'sudo playkeeper uninstall --yes' | tee "$OUT/host-b-uninstall.txt"
lab_ssh "$B" 'sudo sha256sum -c /tmp/fixtures.sha256 && systemctl is-active minecraft.service && curl -s -o /dev/null -w "fixture still serving on 25570: %{http_code}\n" http://127.0.0.1:25570/' | tee -a "$OUT/host-b-fixtures.txt"
lab_shutdown b

########################################################################
phase "HOST C: clean second host restores host A's archive in the browser"
lab_boot c 12 3072
host_facts "$C" host-c-facts.txt
install_artifact "$C"
start=$(date +%s)
lab_ssh "$C" "cd $name && sudo ./install.sh --yes" | tee "$OUT/host-c-install.txt"
echo "install wall time: $(($(date +%s) - start)) s" | tee -a "$OUT/host-c-install.txt"
code_c=$(grep -o 'setup code: [a-z0-9-]*' "$OUT/host-c-install.txt" | awk '{print $3}')
fetch_cert "$C" "$OUT/cert-$C.pem"
enable_offline_harness "$C"
archive="$OUT/host-a/world-backup.tar.gz"
sha=$(python3 -c "import json; print(json.load(open('$OUT/host-a/host-a-results.json'))['backup']['sha256'])")
python3 "$root/test/e2e/scenario.py" host-b --steps setup,refusals --url "https://$C:8443" --cacert "$OUT/cert-$C.pem" --code "$code_c" \
  --game-host "$C" --archive "$archive" --marker "$OUT/host-a/marker.json" --expect-sha256 "$sha" --out "$OUT/host-c" | tee "$OUT/host-c-refusals.txt"
(cd "$UI" && PK_URL="https://$C:8443" PK_ARCHIVE="$archive" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui-c" npx playwright test restore.spec.ts --reporter=list) | tee "$OUT/host-c-restore-ui.txt"
python3 "$root/test/e2e/scenario.py" host-b --steps verify --url "https://$C:8443" --cacert "$OUT/cert-$C.pem" --code "$code_c" \
  --game-host "$C" --archive "$archive" --marker "$OUT/host-a/marker.json" --expect-sha256 "$sha" --out "$OUT/host-c" | tee "$OUT/host-c-verify.txt"
pk "$C" "$OUT/state-c.json" login admin "$PK_PASSWORD" >/dev/null
pk "$C" "$OUT/state-c.json" call GET /api/audit >"$OUT/host-c-audit.json"
PK_PASSWORD="$PK_PASSWORD" shoot "$C" host-c-after-restore / /world
lab_shutdown c

phase "DONE: evidence in $OUT"
