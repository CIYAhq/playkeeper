#!/usr/bin/env bash
# Rehearsal on one supported system (internal/platform) in a fresh KVM guest
# built from its official cloud image, as the OS matrix workflow runs it for
# every supported release (.github/workflows/os-matrix.yml):
#   preflight passes; the one-line install (get.sh from a local mirror,
#   answering its question in a terminal); keyboard-only onboarding in the
#   browser; two protocol bots, a backup with a player online, a same-host
#   restore and its rollback; the network guard, from inside the server
#   (scripts/e2e/guard-check.sh); an update from the dashboard to a newer
#   signed build; a reboot, after which the guard's rules are back; and an
#   uninstall that keeps the world and backups, takes the guard's rules out
#   and leaves the system's packages, users, groups, units and listeners as
#   they were.
# On the RHEL family and Amazon Linux, where Docker comes from dnf and the
# firewall is firewalld, it also checks, with the package sources, IP
# forwarding and firewalld's settings in what the uninstall must leave as it
# found them: a declined plan and an injected failure change nothing,
# podman-docker is refused, a port Docker publishes gets through firewalld,
# and SELinux denies nothing.
# Each step's outcome goes to results.tsv in the evidence folder, and the
# system's facts (kernel, systemd, cgroups, SELinux, Docker, sudo, coreutils,
# memory) to facts.txt.
#
# Usage: [OS_PREP=...] scripts/e2e/vm-os.sh OS RELEASES
#   OS        ubuntu-20.04, ubuntu-22.04, ubuntu-24.04, ubuntu-26.04,
#             debian-12, debian-13, almalinux-9, almalinux-10, rocky-9,
#             rocky-10, oraclelinux-9, centos-stream-9 or amazonlinux-2023
#             (lab_os_url in vm-lab.sh)
#   RELEASES  a folder with a/, the build to install, and b/, a newer build
#             signed with a key a/ trusts, as scripts/e2e/update-releases.sh
#             makes them
#   OS_PREP   what the system has before Playkeeper, as a provider's image or
#             its admin could have it (os_prep): firewalld, podman or
#             docker-selinux
# Remote commands are single-quoted on purpose so they expand in the guest.
# shellcheck disable=SC2016
set -euo pipefail

os=${1:?usage: scripts/e2e/vm-os.sh OS RELEASES}
rel=$(realpath "${2:?usage: scripts/e2e/vm-os.sh OS RELEASES}")
root=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=scripts/e2e/vm-lab.sh
. "$root/scripts/e2e/vm-lab.sh"
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH"
lab_os_url "$os" >/dev/null
# rpm is set on the systems whose packages are RPMs: the RHEL family and
# Amazon Linux.
rpm=""
case $os in ubuntu-* | debian-*) ;; *) rpm=1 ;; esac

OUT=${OUT:-$root/test/e2e/out/os-$os-$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
exec > >(tee -a "$OUT/run.log") 2>&1
G=$LAB_NET.20
SITE=http://$LAB_NET.1:8765
UI=$root/test/e2e/ui
BUSYBOX=busybox@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e
export PK_PASSWORD=${PK_PASSWORD:-"lab-$(head -c 9 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"}
export PK_ADMIN_PASSWORD="$PK_PASSWORD"
version() { python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$1/playkeeper-release.json"; }
va=$(version "$rel/a")
vb=$(version "$rel/b")
results=$OUT/results.tsv
: >"$results"
current=""
pids=()

step() {
  current=$1
  printf '\n==== %s: %s  [%s]\n' "$os" "$1" "$(date -u +%H:%M:%S)"
}
ok() {
  printf '%s\tpass\t%s\n' "$current" "${1:-}" >>"$results"
  current=""
}
fail() {
  printf '%s\tFAIL\t%s\n' "$current" "$1" >>"$results"
  echo "FAIL: $1"
  current=""
  exit 1
}
cleanup() {
  local rc=$? p
  if [ "$rc" != 0 ] && [ -n "$current" ]; then
    printf '%s\tFAIL\t%s\n' "$current" "stopped with exit status $rc; see run.log" >>"$results"
  fi
  for p in "${pids[@]}"; do sudo kill "$p" 2>/dev/null || kill "$p" 2>/dev/null || true; done
  if [ "$rc" != 0 ] && [ "${KEEP_ON_FAIL:-0}" = 1 ]; then
    lab_log "failed (exit $rc); the guest keeps running for inspection"
    return
  fi
  lab_shutdown os
}
trap cleanup EXIT

g() { lab_ssh "$G" "$@"; }

pk() { # args... — one API session (sign-ins are rate limited)
  python3 "$root/test/e2e/pkclient.py" --url "https://$G:8443" --cacert "$OUT/cert.pem" --state "$OUT/state.json" "$@"
}

server() { pk call GET '/api/servers/{server}' | sed 1d; }

wait_online() { # signs in again only when the saved session no longer works
  local i st
  for i in $(seq 1 180); do
    if curl -fsS --cacert "$OUT/cert.pem" "https://$G:8443/healthz" >/dev/null 2>&1; then
      st=$(pk call GET '/api/servers/{server}' 2>/dev/null || true)
      if [ "$(head -1 <<<"$st")" = 401 ]; then
        pk login admin "$PK_PASSWORD" >/dev/null 2>&1 || true
      elif grep -q '"phase": "online"' <<<"$st"; then
        return 0
      fi
    fi
    sleep 5
  done
  return 1
}

# Users and groups the system makes by itself, install or not, when one of its
# own timers first runs: fwupd's firmware-refresh timer makes fwupd-refresh
# (AlmaLinux 10).
os_accounts="fwupd-refresh"

# system — what an install must leave as it found it (worlds and backups in
# /var/lib/playkeeper stay by design): packages, users, groups but those the
# system makes itself, units and listeners, and on RPM systems the package
# sources and signing keys, IP forwarding and firewalld's settings and files.
system() {
  local own
  # shellcheck disable=SC2086 # one -e for each name
  own=$(printf ' -e %s' $os_accounts)
  g 'echo "## users"; getent passwd | cut -d: -f1 | grep -vx'"$own"' | sort; echo "## groups"; getent group | cut -d: -f1 | grep -vx'"$own"' | sort; echo "## units"; ls /etc/systemd/system | sort; echo "## listeners"; sudo ss -ltnH | awk "{print \$4}" | sort -u' >"$OUT/$1-system.txt"
  if [ -n "$rpm" ]; then
    g 'set +e; echo "## package sources"; ls /etc/yum.repos.d /etc/pki/rpm-gpg; echo "## forwarding"; sysctl -n net.ipv4.ip_forward; echo "## firewalld"; if sudo firewall-cmd --state >/dev/null 2>&1; then sudo firewall-cmd --list-all-zones; sudo firewall-cmd --get-policies; sudo find /etc/firewalld -type f | sort; else echo "not running"; fi' >>"$OUT/$1-system.txt"
    # Signing keys are each a gpg-pubkey package; the version tells them apart.
    g 'rpm -qa --qf "%{NAME}\n" | grep -vx gpg-pubkey; rpm -qa --qf "%{NAME}-%{VERSION}-%{RELEASE}\n" gpg-pubkey' | LC_ALL=C sort -u >"$OUT/$1-packages.txt"
  else
    g 'dpkg-query -W -f "\${Package}\n"' | LC_ALL=C sort >"$OUT/$1-packages.txt"
  fi
}

# unchanged NAME — the system is as it was before the install; fails with
# what differs.
unchanged() {
  system "$1"
  diff "$OUT/before-system.txt" "$OUT/$1-system.txt" >"$OUT/$1-system.diff" || fail "the system changed: $(head -c 300 "$OUT/$1-system.diff")"
  diff "$OUT/before-packages.txt" "$OUT/$1-packages.txt" >"$OUT/$1-packages.diff" || fail "packages changed: $(grep -E '^[<>]' "$OUT/$1-packages.diff" | head -10 | tr '\n' ' ')"
}

# os_prep leaves the system the way OS_PREP says, as a provider's image or
# its admin could have: firewalld turned on (as Oracle Cloud's images have
# it), Podman running a container that starts again at boot, or Docker
# Engine from Docker's repository running containers under SELinux labels.
os_prep() {
  case ${OS_PREP:-} in
    firewalld)
      g 'sudo dnf -y -q install firewalld && sudo systemctl enable --now firewalld && echo "firewalld $(sudo firewall-cmd --state), zones: $(sudo firewall-cmd --get-active-zones | tr "\n" " ")"'
      ;;
    podman)
      g "sudo dnf -y -q install podman && sudo systemctl enable -q podman-restart.service && sudo podman run -d --restart=always --name existing-podman -p 25590:8080 docker.io/library/$BUSYBOX sh -c 'echo ok >/tmp/index.html && exec httpd -f -p 8080 -h /tmp' >/dev/null && echo \"Podman \$(sudo podman version --format '{{.Version}}') running a container\""
      ;;
    docker-selinux)
      g 'set -e; . /etc/os-release; printf "[docker-ce-stable]\nname=Docker CE Stable\nbaseurl=https://download.docker.com/linux/centos/${VERSION_ID%%.*}/\$basearch/stable\nenabled=1\ngpgcheck=1\ngpgkey=https://download.docker.com/linux/centos/gpg\n" | sudo tee /etc/yum.repos.d/docker-ce.repo >/dev/null; sudo dnf -y -q install docker-ce docker-ce-cli containerd.io; sudo mkdir -p /etc/docker; echo "{\"selinux-enabled\": true}" | sudo tee /etc/docker/daemon.json >/dev/null; sudo systemctl enable --now docker; echo "Docker $(sudo docker version --format "{{.Server.Version}}") from Docker'"'"'s repository, with SELinux labelling"'
      ;;
    *)
      echo "unknown OS_PREP '$OS_PREP'"
      return 1
      ;;
  esac
}

# podman_serves WHEN — the container os_prep started still runs and answers.
podman_serves() {
  g "set -e; served=\$(curl -fsS --max-time 10 http://127.0.0.1:25590/); echo \"Podman's container $1: \$(sudo podman ps --filter name=existing-podman --format '{{.Status}}'), serving: \$served\""
}

# denials — the SELinux denials logged since the install started.
denials() {
  g "sudo sh -c 'grep \"avc: *denied\" /var/log/audit/audit.log 2>/dev/null | tail -n +$((avc + 1))'"
}

world_sums() {
  g 'sudo sh -c "find /var/lib/playkeeper/servers/*/data/world /var/lib/playkeeper/backups -type f -exec sha256sum {} +" | sort -k2'
}

step "boot a fresh $os guest from its official cloud image"
lab_key
lab_image_named "$os" "$(lab_os_url "$os")" "$(lab_os_sums "$os")"
lab_network
LAB_BASE_IMAGE="$LAB_DIR/$os.img" lab_boot os 20 3072
g 'set +e
. /etc/os-release
echo "system: $PRETTY_NAME"
echo "kernel: $(uname -r)"
echo "systemd: $(systemctl --version | head -1)"
case $(stat -fc %T /sys/fs/cgroup) in cgroup2fs) echo "cgroups: v2" ;; *) echo "cgroups: v1 (hybrid)" ;; esac
echo "sudo: $(sudo --version | head -1)"
echo "coreutils: $(ls --version | head -1)"
echo "memory: $(awk "/MemTotal/ {print int(\$2 / 1024)}" /proc/meminfo) MB reported"
echo "firewall: ufw $(command -v ufw >/dev/null && echo installed || echo absent), nft $(command -v nft >/dev/null && echo installed || echo absent), firewalld $(systemctl is-active firewalld 2>/dev/null || true)"
echo "selinux: $(getenforce 2>/dev/null || echo off)"
echo "docker before the install: $(command -v docker || echo none)"' | tee "$OUT/facts.txt"
ok "$(sed -n 's/^system: //p' "$OUT/facts.txt"), kernel $(sed -n 's/^kernel: //p' "$OUT/facts.txt"), $(sed -n 's/^systemd: \([a-z]* [0-9]*\).*/\1/p' "$OUT/facts.txt"), cgroups $(sed -n 's/^cgroups: //p' "$OUT/facts.txt"), SELinux $(sed -n 's/^selinux: //p' "$OUT/facts.txt")"

if [ -n "${OS_PREP:-}" ]; then
  step "before Playkeeper: $OS_PREP"
  prep=$(os_prep | tail -1) || fail "could not prepare $OS_PREP"
  echo "$prep" | tee "$OUT/prep.txt"
  ok "$prep"
fi

step "preflight passes"
lab_scp "$rel/a/playkeeper-linux-amd64.tar.gz" "$rel/a/playkeeper-linux-amd64.tar.gz.sha256" "pk@$G:"
g 'sha256sum -c playkeeper-linux-amd64.tar.gz.sha256 && mkdir -p pk && tar -xzf playkeeper-linux-amd64.tar.gz -C pk'
g 'sudo pk/playkeeper-*/playkeeper preflight' | tee "$OUT/preflight.txt" || fail "preflight refused this system"
system before
avc=0
if [ -n "$rpm" ]; then
  avc=$(g 'sudo grep -c "avc: *denied" /var/log/audit/audit.log 2>/dev/null || true')
  avc=${avc:-0}
fi
ok "$(grep -m1 'Operating system' "$OUT/preflight.txt" | sed -E 's/^ *\[(ok  |WARN)\] *Operating system *//')"

if [ "${OS_PREP:-}" = podman ]; then
  step "podman-docker is refused, with the command that removes it"
  g 'sudo dnf -y -q install podman-docker'
  g 'sudo pk/playkeeper-*/playkeeper preflight' >"$OUT/podman-docker.txt" 2>&1 || true
  cat "$OUT/podman-docker.txt"
  grep -q 'FAIL.*podman-docker is installed' "$OUT/podman-docker.txt" || fail "preflight did not refuse podman-docker"
  g 'sudo dnf -y -q remove podman-docker'
  unchanged after-podman-docker
  ok "$(grep -m1 -o 'Fix: .*' "$OUT/podman-docker.txt")"
fi

if [ -n "$rpm" ]; then
  step "a declined plan and an injected failure change nothing"
  g 'cd pk/playkeeper-* && echo n | sudo DO_NOT_TRACK=1 ./install.sh' >"$OUT/decline.txt" 2>&1 || true
  unchanged after-decline
  g "cd pk/playkeeper-* && sudo DO_NOT_TRACK=1 PLAYKEEPER_TEST_FAIL_INSTALL_STEP='install and start systemd services' ./install.sh --yes" >"$OUT/rollback.txt" 2>&1 || true
  grep -q 'Rollback complete' "$OUT/rollback.txt" || fail "the injected failure did not roll back: $(tail -3 "$OUT/rollback.txt" | tr '\n' ' ')"
  unchanged after-rollback
  ok "the rollback took back $(grep -c '↺ undo' "$OUT/rollback.txt") steps, Docker and where it came from included; packages, users, groups, units, listeners, package sources, forwarding and firewalld as before"
fi

step "one-line install in a terminal (get.sh from a local mirror)"
mkdir -p "$OUT/site/a"
cp "$rel/a/get.sh" "$rel/a/playkeeper-linux-amd64.tar.gz" "$rel/a/playkeeper-linux-amd64.tar.gz.sha256" "$OUT/site/a/"
python3 -m http.server 8765 --bind "$LAB_NET.1" --directory "$OUT/site" >"$OUT/mirror.log" 2>&1 &
pids+=($!)
# The dashboard looks for updates at a release location on the guest itself:
# plain HTTP is allowed only there.
g 'mkdir -p latest'
lab_scp "$rel/b/"* "pk@$G:latest/"
g 'nohup python3 -m http.server 8765 --bind 127.0.0.1 --directory latest </dev/null >/dev/null 2>&1 & sleep 1; echo "release location on the guest: http://127.0.0.1:8765"'
oneliner="curl -fsSL $SITE/a/get.sh | sudo DO_NOT_TRACK=1 PLAYKEEPER_BASE_URL=$SITE/a PLAYKEEPER_ALLOW_HTTP=1 sh -s -- --release-url http://127.0.0.1:8765"
echo "\$ $oneliner   # in a terminal; the installer's question is answered with y" | tee "$OUT/install.txt"
start=$(date +%s)
python3 "$root/test/e2e/tty_run.py" --answer 'Proceed? [y/N]=y' -- ssh -tt -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  -i "$LAB_KEY" "pk@$G" "$oneliner" | tee -a "$OUT/install.txt"
took=$(($(date +%s) - start))
code=$(grep -o 'setup code: [a-z0-9-]*' "$OUT/install.txt" | awk '{print $3}')
[ -n "$code" ] || fail "the installer printed no setup code"
[ "$(g 'playkeeper version' | awk '{print $2}')" = "$va" ] || fail "playkeeper version is not $va"
g 'systemctl is-active docker playkeeper-agent playkeeper-panel playkeeper-update.path' | tee -a "$OUT/install.txt"
docker=$(g 'sudo docker version --format "{{.Server.Version}}"') || fail "the docker command doesn't work"
g 'sudo cat /var/lib/playkeeper/install-manifest.json' >"$OUT/install-manifest.json"
# systemd warns about settings it doesn't know, and then ignores them.
g 'sudo systemd-analyze verify /etc/systemd/system/playkeeper-agent.service /etc/systemd/system/playkeeper-panel.service /etc/systemd/system/playkeeper-update.service /etc/systemd/system/playkeeper-update.path 2>&1 || true' | tee "$OUT/units-verify.txt"
if grep -iE 'playkeeper.*(unknown|ignoring)' "$OUT/units-verify.txt"; then
  fail "this systemd ignores settings in Playkeeper's units"
fi
g 'sudo cat /var/lib/playkeeper/panel/tls/cert.pem' >"$OUT/cert.pem"
g "sudo mkdir -p /etc/systemd/system/playkeeper-agent.service.d && printf '[Service]\nEnvironment=PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1\n' | sudo tee /etc/systemd/system/playkeeper-agent.service.d/e2e-offline.conf >/dev/null && sudo systemctl daemon-reload && sudo systemctl restart playkeeper-agent"
ok "Playkeeper $va in $took s, with Docker $docker ($(python3 -c 'import json, sys; print(" ".join(p for p in json.load(open(sys.argv[1]))["packagesInstalled"] if p.startswith("docker")))' "$OUT/install-manifest.json")); systemd knows every setting in its units"

step "keyboard-only onboarding in the browser"
(cd "$UI" && PK_URL="https://$G:8443" PK_SETUP_CODE="$code" PK_SHOTS="$OUT/screenshots" PK_OUT="$OUT/ui" npx playwright test onboarding.spec.ts --reporter=list) | tee "$OUT/onboarding.txt"
join=$(cat "$OUT/ui/join-address.txt")
ok "a Paper server made in the browser, joinable at $join"

step "two protocol bots, a backup with a player online, a same-host restore and its rollback"
python3 "$root/test/e2e/scenario.py" host-a --existing --url "https://$G:8443" --cacert "$OUT/cert.pem" --code "$code" \
  --game-host "$join" --out "$OUT/host" | tee "$OUT/scenario.txt"
ok "$(python3 -c 'import json, sys; c = json.load(open(sys.argv[1]))["checks"]; n = sum(x["ok"] for x in c); print(f"{n} of {len(c)} checks passed")' "$OUT/host/host-a-results.json")"

step "from inside the server: this machine and link-local addresses are out of reach, the internet isn't"
g 'sudo bash -s' <"$root/scripts/e2e/guard-check.sh" | tee "$OUT/guard.txt"
ok "$(grep -c '^  ok: ' "$OUT/guard.txt") checks passed, the last: $(grep '^  ok: ' "$OUT/guard.txt" | tail -1 | sed 's/^  ok: //')"

if [ -n "$rpm" ] && g 'sudo firewall-cmd --state' >/dev/null 2>&1; then
  step "a port Docker publishes gets through firewalld without a rule"
  g "sudo docker run -d --rm --name pk-e2e-published -p 25599:8080 $BUSYBOX sh -c 'echo ok >/tmp/index.html && exec httpd -f -p 8080 -h /tmp' >/dev/null && sleep 2"
  answer=$(curl -fsS --max-time 10 "http://$G:25599/") || fail "port 25599, published by Docker, doesn't answer through firewalld"
  zone=$(g 'sudo firewall-cmd --get-default-zone')
  g "sudo docker rm -f pk-e2e-published >/dev/null && sudo docker image rm -f $BUSYBOX >/dev/null"
  ok "port 25599 answers \"$answer\"; firewalld's $zone zone allows $(g "sudo firewall-cmd --zone=$zone --list-ports")"
fi
if [ "${OS_PREP:-}" = podman ]; then
  step "Podman's container keeps running next to the Docker Engine Playkeeper installed"
  ok "$(podman_serves "with Playkeeper running" | tee -a "$OUT/podman.txt")"
fi

step "memory the system, Docker and Playkeeper use while the server runs"
g 'sudo python3 -' <<'PY' | tee -a "$OUT/facts.txt"
import http.client, json, socket

class Docker(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.connect("/var/run/docker.sock")

def get(path):
    c = Docker("docker")
    c.request("GET", path)
    return json.loads(c.getresponse().read())

mem = {l.split(":")[0]: int(l.split()[1]) // 1024 for l in open("/proc/meminfo")}
servers = 0
for c in get("/containers/json"):
    s = get(f"/containers/{c['Id']}/stats?stream=false&one-shot=true")["memory_stats"]
    stats = s.get("stats", {})
    servers += (s.get("usage", 0) - stats.get("total_inactive_file", stats.get("inactive_file", 0))) // 2**20
used = mem["MemTotal"] - mem["MemAvailable"]
print(f"in use with the server running: {used} MB, of which the Minecraft server {servers} MB and the system, Docker and Playkeeper {used - servers} MB")
PY
ok "$(sed -n 's/^in use with the server running: //p' "$OUT/facts.txt")"

step "update from the dashboard to a newer signed build"
pk login admin "$PK_PASSWORD" >/dev/null
started=$(server | python3 -c 'import json, sys; print(json.load(sys.stdin)["startedAt"])')
mkdir -p "$OUT/update"
cp "$OUT/state.json" "$OUT/update/state.json"
python3 "$root/test/e2e/update.py" update --url "https://$G:8443" --cacert "$OUT/cert.pem" --out "$OUT/update" --to "$vb" | tee "$OUT/update.txt"
[ "$(g 'playkeeper version' | awk '{print $2}')" = "$vb" ] || fail "playkeeper version is not $vb after the update"
cp "$OUT/update/state.json" "$OUT/state.json"
wait_online || fail "the server is not online after the update"
[ "$(server | python3 -c 'import json, sys; print(json.load(sys.stdin)["startedAt"])')" = "$started" ] || fail "the Minecraft server restarted during the update"
ok "$va → $vb through playkeeper-update.path; the Minecraft server kept running"

step "reboot: Playkeeper and the server come back by themselves"
g 'sudo systemctl reboot' || true
sleep 20
lab_wait_ssh "$G"
wait_online || fail "the dashboard did not come back after the reboot"
pk wait-online --timeout 600 >/dev/null || fail "the server did not come back online and reachable after the reboot"
g 'systemctl is-active docker playkeeper-agent playkeeper-panel' | tee "$OUT/reboot.txt"
booted=$(g 'awk "/^btime/ {print \$2}" /proc/stat')
since=$(server | python3 -c '
import datetime, json, re, sys
s = re.sub(r"(\.\d{6})\d*", r"\1", json.load(sys.stdin).get("startedAt", "")).replace("Z", "+00:00")
started = datetime.datetime.fromisoformat(s)
if started.timestamp() < int(sys.argv[1]):
    raise SystemExit(f"the server last started at {s}, before this boot")
print(s[:19].replace("T", " "))' "$booted") || fail "the server was not started again after the reboot: $since"
guard=$(g 'sudo iptables -S | grep -c playkeeper-guard' || true)
[ "${guard:-0}" -ge 3 ] || fail "the network guard's rules are not back after the reboot ($guard of 3)"
ok "online and reachable again, started at $since UTC after the reboot, with the network guard's $guard rules back"

if [ -n "$rpm" ]; then
  step "SELinux: what the server and Playkeeper run as, and nothing denied"
  g "set +e; echo \"mode: \$(getenforce)\"; c=\$(sudo docker ps -q --filter label=io.playkeeper.server | head -1); echo \"server container binds: \$(sudo docker inspect \$c --format '{{json .HostConfig.Binds}}')\"; echo \"server process: \$(ps -eo label,args | grep '[p]aper-' | head -1 | cut -d' ' -f1)\"; echo \"world folder: \$(sudo sh -c 'ls -dZ /var/lib/playkeeper/servers/*/data' | head -1 | cut -d' ' -f1)\"; echo \"Playkeeper: \$(ps -eo label,comm | grep '[p]laykeeper' | awk '{print \$1}' | sort -u | tr '\n' ' ')\"; echo \"binary: \$(ls -Z /usr/local/bin/playkeeper | cut -d' ' -f1)\"" | tee "$OUT/selinux.txt"
  denials | tee "$OUT/denials.txt"
  [ ! -s "$OUT/denials.txt" ] || fail "SELinux denied $(grep -c . "$OUT/denials.txt") things since the install began"
  ok "$(sed -n 's/^mode: //p' "$OUT/selinux.txt"); the server runs as $(sed -n 's/^server process: //p' "$OUT/selinux.txt" | cut -d: -f3), its world is $(sed -n 's/^world folder: //p' "$OUT/selinux.txt" | cut -d: -f3); 0 denials through the update and the reboot"
fi

step "uninstall keeps the world and backups and leaves the system as it was"
world_sums >"$OUT/world-before-uninstall.txt"
[ -s "$OUT/world-before-uninstall.txt" ] || fail "no world or backup files to compare"
g 'sudo playkeeper uninstall --yes' | tee "$OUT/uninstall.txt"
g 'sudo rm -rf /etc/systemd/system/playkeeper-agent.service.d && sudo systemctl daemon-reload'
world_sums >"$OUT/world-after-uninstall.txt"
diff "$OUT/world-before-uninstall.txt" "$OUT/world-after-uninstall.txt" || fail "world or backup files changed"
guard=$(g 'sudo sh -c "{ iptables -S; ip6tables -S; } 2>/dev/null | grep -c playkeeper-guard"' || true)
[ "${guard:-0}" = 0 ] || fail "the network guard's $guard rules are still there after the uninstall"
system after
diff "$OUT/before-system.txt" "$OUT/after-system.txt" || fail "the system differs from before the install: $(diff "$OUT/before-system.txt" "$OUT/after-system.txt" | grep -E '^[<>]' | head -5 | tr '\n' ' ')"
removed=$(LC_ALL=C comm -23 "$OUT/before-packages.txt" "$OUT/after-packages.txt" | paste -sd' ')
[ -z "$removed" ] || fail "uninstall removed packages that were there before: $removed"
left=$(python3 -c 'import json, sys; print("\n".join(json.load(open(sys.argv[1]))["packagesInstalled"]))' "$OUT/install-manifest.json" | grep -xFf "$OUT/after-packages.txt" | paste -sd' ' || true)
[ -z "$left" ] || fail "packages the install added are still there: $left"
added=$(LC_ALL=C comm -13 "$OUT/before-packages.txt" "$OUT/after-packages.txt" | paste -sd' ')
[ -z "$added" ] || echo "packages the system's own updates installed meanwhile: $added" | tee -a "$OUT/uninstall.txt"
if [ -n "$rpm" ]; then
  denials | tee "$OUT/denials.txt"
  [ ! -s "$OUT/denials.txt" ] || fail "SELinux denied $(grep -c . "$OUT/denials.txt") things by the end of the uninstall"
fi
if [ "${OS_PREP:-}" = podman ]; then
  podman_serves "after the uninstall" | tee -a "$OUT/podman.txt" || fail "Podman's container stopped serving after the uninstall"
fi
ok "$(wc -l <"$OUT/world-after-uninstall.txt") world and backup files unchanged; Docker and every package the install added removed"

touch "$OUT/passed"
echo
echo "==== $os: every step passed"
column -t -s "$(printf '\t')" "$results"
