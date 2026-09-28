#!/usr/bin/env bash
# The network guard (internal/netguard), checked from inside a running
# Playkeeper server's container, as a plugin would try it.
#
# With Keep servers away from this machine on (Machine settings):
#
#   - the server can't open connections to the machine: the dashboard's port,
#     on the bridge's gateway and on the machine's own address;
#   - nor to a link-local address, where clouds answer with the machine's
#     metadata;
#   - it still looks up and reaches the internet;
#   - with the guard's rules taken out, both connections get through, so the
#     checks can tell;
#   - and the agent puts the rules back, first in their chains, by itself.
#
# With it off, as a machine starts (guard-check.sh default), the server
# reaches the machine, as a plugin with a database there needs, but still no
# link-local address, and the internet.
#
# The link-local address is 169.254.77.77, answered by a web server in a
# network namespace of its own: not every machine has a metadata service, and
# the real one stays untouched.
#
# It runs as root on the machine the server runs on and changes its firewall,
# so it only runs in CI: sudo bash guard-check.sh [default], or bash -s over
# ssh.
set -euo pipefail
mode=${1:-kept}

fail() {
  echo "  FAIL: network guard: $*" >&2
  exit 1
}
ok() { echo "  ok: $*"; }
[ "$(id -u)" = 0 ] || fail "it needs root"

c=$(docker ps -q --filter label=io.playkeeper.server | head -1)
[ -n "$c" ] || fail "no server's container is running"
gw=$(docker network inspect playkeeper -f '{{range .IPAM.Config}}{{.Gateway}} {{end}}' | awk '{print $1}')
self=$(ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "src") {print $(i + 1); exit}}')
port=$(python3 -c 'import json; print(json.load(open("/etc/playkeeper/config.json")).get("panelPort") or 8443)')
meta=169.254.77.77
if [ -z "$gw" ] || [ -z "$self" ]; then fail "no gateway ($gw) or machine address ($self)"; fi

ip netns add pk-guard
cleanup() {
  ip netns pids pk-guard 2>/dev/null | xargs -r kill 2>/dev/null || true
  ip link delete pk-guard0 2>/dev/null || true
  ip netns delete pk-guard 2>/dev/null || true
}
trap cleanup EXIT
ip link add pk-guard0 type veth peer name pk-guard1 netns pk-guard
ip addr add 169.254.77.1/32 dev pk-guard0
ip link set pk-guard0 up
ip route add "$meta/32" dev pk-guard0
ip -n pk-guard addr add "$meta/32" dev pk-guard1
ip -n pk-guard link set lo up
ip -n pk-guard link set pk-guard1 up
ip -n pk-guard route add 169.254.77.1/32 dev pk-guard1
ip netns exec pk-guard python3 -m http.server 80 --bind "$meta" >/dev/null 2>&1 &
for _ in $(seq 50); do
  if (exec 3<>"/dev/tcp/$meta/80") 2>/dev/null; then break; fi
  sleep 0.1
done
(exec 3<>"/dev/tcp/$meta/80") 2>/dev/null || fail "the web server at $meta doesn't answer this machine"

# reach HOST PORT: the server's container opens a TCP connection to HOST:PORT.
reach() { docker exec "$c" timeout 5 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; }
# rules: the guard's rules, in both families.
rules() { { iptables -S; ip6tables -S 2>/dev/null || true; } | grep -e '--comment "\?playkeeper-guard' || true; }
# first CHAIN N: whether the first N rules of CHAIN are the guard's.
first() { [ "$(iptables -S "$1" | grep '^-A' | head -n "$2" | grep -c playkeeper-guard)" = "$2" ]; }
meta_chain=FORWARD
if iptables -S DOCKER-USER >/dev/null 2>&1; then meta_chain=DOCKER-USER; fi

blocked() { # WHEN
  first INPUT 2 || fail "$1: the guard's rules aren't the first two in INPUT: $(iptables -S INPUT | head -5 | tr '\n' ' ')"
  first "$meta_chain" 1 || fail "$1: the guard's rule isn't the first in $meta_chain: $(iptables -S "$meta_chain" | head -3 | tr '\n' ' ')"
  if reach "$gw" "$port"; then fail "$1: the server reaches the dashboard's port $port on the bridge's gateway $gw"; fi
  if reach "$self" "$port"; then fail "$1: the server reaches the dashboard's port $port on the machine's address $self"; fi
  if reach "$meta" 80; then fail "$1: the server reaches the link-local address $meta"; fi
}

if [ "$mode" = default ]; then
  if iptables -S INPUT | grep -q playkeeper-guard; then fail "with servers free to reach the machine, INPUT has the guard's rules: $(iptables -S INPUT | grep playkeeper-guard | tr '\n' ' ')"; fi
  first "$meta_chain" 1 || fail "the guard's rule isn't the first in $meta_chain: $(iptables -S "$meta_chain" | head -3 | tr '\n' ' ')"
  reach "$gw" "$port" || fail "with servers free to reach the machine, the server can't reach port $port on the gateway $gw"
  if reach "$meta" 80; then fail "the server reaches the link-local address $meta"; fi
  reach sessionserver.mojang.com 443 || fail "the server can't reach sessionserver.mojang.com:443, which online mode needs"
  ok "with Keep servers away from this machine off: port $port reached on the gateway $gw, $meta:80 refused, sessionserver.mojang.com:443 reached"
  exit 0
fi

blocked "with the guard on"
reach sessionserver.mojang.com 443 || fail "the server can't reach sessionserver.mojang.com:443, which online mode needs"
ok "from inside the server: port $port refused on the gateway $gw and on $self, $meta:80 refused, sessionserver.mojang.com:443 reached"

# take_out takes the guard's rules out of both families. The agent may put
# them back at any moment, so what follows it tries again when they are.
take_out() {
  rules | sed 's/^-A/-D/' >/tmp/pk-guard-rules
  while read -r r; do
    eval "iptables $r" 2>/dev/null || eval "ip6tables $r" 2>/dev/null || true
  done </tmp/pk-guard-rules
  [ -z "$(rules)" ]
}
tries=0
until take_out && reach "$gw" "$port" && reach "$meta" 80; do
  tries=$((tries + 1))
  [ -z "$(rules)" ] && fail "without the guard's rules the server still can't reach port $port on $gw or $meta:80, so the check can't tell"
  [ "$tries" -lt 3 ] || fail "the guard's rules kept coming back while they were taken out: $(rules)"
done
ok "with the guard's $(wc -l </tmp/pk-guard-rules) rules taken out, the server reaches both"

start=$SECONDS
until first INPUT 2 && first "$meta_chain" 1; do
  [ $((SECONDS - start)) -lt 90 ] || fail "the agent didn't put the rules back in 90 s: $(rules)"
  sleep 1
done
blocked "once the agent put the rules back"
ok "the agent put the rules back in $((SECONDS - start)) s, first in INPUT and $meta_chain, and the server is kept out again"
